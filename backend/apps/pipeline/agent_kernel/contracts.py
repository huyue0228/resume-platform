"""Agent Kernel v1 协议；这里只描述不可变输入和无写权限建议。"""

from __future__ import annotations

import hashlib
import json
from datetime import datetime
from typing import Literal

from django.conf import settings
from django.core.exceptions import ObjectDoesNotExist
from pydantic import BaseModel, ConfigDict, Field

from apps.pipeline import ai_config
from apps.pipeline.screening_types import ResumeScreeningOutput


PROTOCOL_VERSION = "resume-agent/v2"
PROPOSAL_VERSION = "agent-action-proposal/v1"
TOOLSET_VERSION = "resume-tool-providers/v2"
RESULT_SCHEMA_VERSION = "resume-screening/v1"
POLICY_VERSION = "django-policy-gate/v1"
INSTRUCTION_VERSION = "resume-screening-kernel/v1"


class StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid")


class KernelPinV2(StrictModel):
    pin_id: str
    kernel_build: str
    protocol_version: Literal[PROTOCOL_VERSION] = PROTOCOL_VERSION
    toolset_version: Literal[TOOLSET_VERSION] = TOOLSET_VERSION
    result_schema_version: Literal[RESULT_SCHEMA_VERSION] = RESULT_SCHEMA_VERSION
    policy_version: Literal[POLICY_VERSION] = POLICY_VERSION
    instruction_version: Literal[INSTRUCTION_VERSION] = INSTRUCTION_VERSION
    model_config_revision: str


class CaseConstraintsV1(StrictModel):
    workflow_revision: int = Field(ge=0)
    volunteer_rank: int = Field(ge=1, le=4)
    policies: list[str]


class CandidateReferenceV1(StrictModel):
    highest_major: str = ""


class CurrentVolunteerV1(StrictModel):
    position_name: str


class FixedJobContextV1(StrictModel):
    entity: str = ""
    public_name: str = ""
    position_name: str = ""
    category: str = ""
    job_family: str = ""
    location: str = ""
    required_majors: list[str] = Field(default_factory=list)
    responsibilities: str = Field(min_length=1, max_length=12_000)
    department_name: str = ""


class ResumeContentV1(StrictModel):
    checksum: str = Field(min_length=64, max_length=64)
    text: str = Field(min_length=1, max_length=60_000)


class KernelModelConfigV1(StrictModel):
    api_style: Literal["chat_json", "responses"]
    base_url: str
    model_name: str
    structured_output_mode: str
    timeout_seconds: float = Field(gt=0, le=1800)
    retry_count: int = Field(ge=0, le=5)
    insecure_skip_verify: bool = False


class KernelBudgetV1(StrictModel):
    max_turns: int = Field(default=6, ge=1, le=8)
    max_tool_calls: int = Field(default=12, ge=1, le=16)
    max_duration_seconds: int = Field(default=600, ge=1, le=1800)


class CaseEnvelopeV2(StrictModel):
    protocol_version: Literal[PROTOCOL_VERSION] = PROTOCOL_VERSION
    task_id: str
    idempotency_key: str
    pin: KernelPinV2
    constraints: CaseConstraintsV1
    candidate_reference: CandidateReferenceV1
    current_volunteer: CurrentVolunteerV1
    current_job: FixedJobContextV1
    resume: ResumeContentV1
    model: KernelModelConfigV1
    budget: KernelBudgetV1 = Field(default_factory=KernelBudgetV1)


class ToolTraceV1(StrictModel):
    name: str
    status: Literal["success", "rejected"]
    duration_ms: int = Field(ge=0)
    item_count: int = Field(ge=0)


class SafeTraceV1(StrictModel):
    trace_id: str
    kernel_build: str
    started_at: datetime
    finished_at: datetime
    turns: int = Field(ge=1, le=8)
    tool_call_count: int = Field(ge=0, le=16)
    tool_calls: list[ToolTraceV1]
    input_tokens: int = Field(ge=0)
    output_tokens: int = Field(ge=0)
    status: Literal["completed"]


class AgentActionProposalV1(StrictModel):
    proposal_version: Literal[PROPOSAL_VERSION]
    task_id: str
    pin_id: str
    action: Literal["dispatch", "review", "archive"]
    evaluation: ResumeScreeningOutput
    safe_trace: SafeTraceV1


def _pin_id(payload):
    encoded = json.dumps(
        payload,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def build_runtime_pin(model_config):
    pin_payload = {
        "kernel_build": getattr(settings, "AGENT_KERNEL_BUILD", "dev"),
        "protocol_version": PROTOCOL_VERSION,
        "toolset_version": TOOLSET_VERSION,
        "result_schema_version": RESULT_SCHEMA_VERSION,
        "policy_version": POLICY_VERSION,
        "instruction_version": INSTRUCTION_VERSION,
        "model_config_revision": ai_config.current_ai_connection_fingerprint(),
    }
    return KernelPinV2(pin_id=_pin_id(pin_payload), **pin_payload)


def build_envelope(prepared, *, processing_run_id=None, pin=None):
    """从 ORM 对象构造无数据库 ID 的不可变 Kernel 输入。"""

    try:
        workflow = prepared.resume.candidate.workflow
    except ObjectDoesNotExist:
        workflow = None
    workflow_revision = max(0, int(getattr(workflow, "revision", 0) or 0))
    volunteer_rank = int(prepared.resume.volunteer_rank or 1)
    pin = pin or build_runtime_pin(prepared.model_config)
    task_id = (
        f"run-{processing_run_id}-{prepared.checksum[:12]}"
        if processing_run_id is not None
        else f"adhoc-{prepared.checksum[:16]}"
    )
    idempotency_key = _pin_id(
        {
            "task_id": task_id,
            "resume_checksum": prepared.checksum,
            "workflow_revision": workflow_revision,
            "pin_id": pin.pin_id,
        }
    )
    current_job = dict(prepared.job_context)
    current_job["department_name"] = (
        prepared.department.name if prepared.department else ""
    )
    return CaseEnvelopeV2(
        task_id=task_id,
        idempotency_key=idempotency_key,
        pin=pin,
        constraints=CaseConstraintsV1(
            workflow_revision=workflow_revision,
            volunteer_rank=volunteer_rank,
            policies=[
                "只处理当前有效志愿",
                "岗位与二级部门引用已经固定，不得替换",
                "学历与院校准入结果已经由 Django Policy Gate 确认",
                "最终业务写入必须由 Django Policy/Executor 完成",
            ],
        ),
        candidate_reference=CandidateReferenceV1(
            highest_major=prepared.resume.candidate.highest_major or ""
        ),
        current_volunteer=CurrentVolunteerV1(
            position_name=prepared.resume.position_name or ""
        ),
        current_job=FixedJobContextV1.model_validate(current_job),
        resume=ResumeContentV1(
            checksum=prepared.checksum,
            text=prepared.text[:60_000],
        ),
        model=KernelModelConfigV1(
            api_style=prepared.model_config.api_style,
            base_url=prepared.model_config.base_url,
            model_name=prepared.model_config.model_name,
            structured_output_mode=ai_config.get_structured_output_mode(
                api_style=prepared.model_config.api_style
            ),
            timeout_seconds=ai_config.get_ai_runtime_config().timeout_seconds,
            retry_count=ai_config.get_ai_runtime_config().retry_count,
            insecure_skip_verify=getattr(
                settings, "AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY", False
            ),
        ),
    )
