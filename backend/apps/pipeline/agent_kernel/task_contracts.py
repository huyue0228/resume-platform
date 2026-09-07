"""候选人级任务协议和冻结快照；数据库主键只保存在控制面映射中。"""
import hashlib
import hmac
import json
import time
import uuid
from datetime import datetime
from copy import deepcopy
from pathlib import Path
from typing import Literal, Optional

from django.conf import settings
from pydantic import Field

from apps.core import models as m
from apps.pipeline import ai_config
from apps.pipeline.errors import AIServiceError
from apps.ingestion.sources import RESUME_SUBDIR
from resume_contracts.models import (PROTOCOL, TOOLSET, RESULT, POLICY, INSTRUCTIONS, TaskPinV1, TaskBudgetV1,
    EvidenceV1, ClaimDetailsV1, ClaimV1, CandidateProfileV1, JobMatchV1, TaskSafeTraceV1,
    TaskManifestV1 as PublicManifest, AnalysisResponseV1, StrictModel)
from .contracts import KernelModelConfigV1, _pin_id

class TaskEnvelopeV1(StrictModel):
    prepare_only: bool = False
    protocol_version: Literal[PROTOCOL] = PROTOCOL
    task_kind: Literal["candidate.resume_job_match"] = "candidate.resume_job_match"
    task_id: str
    idempotency_key: str
    trigger: str
    pin: TaskPinV1
    snapshot: dict
    model: KernelModelConfigV1
    budget: TaskBudgetV1 = Field(default_factory=TaskBudgetV1)


class DeterministicResultV1(StrictModel):
    first_degree_tag_ref: str = ""
    highest_degree_tag_ref: str = ""
    volunteer_order: list[str]
    current_volunteer_ref: str
    current_rank: int
    admission_passed: bool
    admission_rule_ref: str
    job_refs: list[str]
    status: str


class LocalManifest(PublicManifest):
    terminal_state: Literal["DONE", "FAILED", "BLOCKED", "PREPARED"]


class TaskResultV1(AnalysisResponseV1):
    """平台内部持久化结构；deterministic 不属于引擎响应协议。"""
    deterministic: DeterministicResultV1
    manifest: LocalManifest


def runtime_pin():
    payload = dict(kernel_build=settings.AGENT_KERNEL_BUILD, protocol_version=PROTOCOL,
                   toolset_version=TOOLSET, result_schema_version=RESULT,
                   policy_version=POLICY, instruction_version=INSTRUCTIONS,
                   model_config_revision=ai_config.current_ai_connection_fingerprint())
    return TaskPinV1(pin_id=_pin_id(payload), **payload)


def job_content(job):
    """岗位内容指纹不含实时 HC。"""
    return dict(entity=job.entity, public_name=job.public_name,
                position_name=job.position_name, category=job.category,
                job_family=job.job_family, location=job.location,
                education=job.education, required_majors=sorted(item.major for item in job.majors.all()),
                responsibilities=job.responsibilities, department_name=job.department.name if job.department else "",
                department_level=job.department.level if job.department else 0,
                department_identity=job.department_id, is_active=job.is_active)


def job_hash(job):
    return _pin_id(job_content(job))


def freeze_case(candidate, run=None):
    from apps.pipeline.services import classify_school as schools, school_admission
    ref = lambda: uuid.uuid4().hex
    stable_ref = lambda kind, pk: hmac.new(settings.SECRET_KEY.encode(), f"{kind}:{pk}".encode(), hashlib.sha256).hexdigest()
    school_map = {schools.normalize_school_name(s.name): s for s in m.School.objects.select_related("school_tag")}
    non_target = schools._non_target_school_tag()
    default = schools._default_school_tag(non_target)
    first = school_map.get(schools.normalize_school_name(candidate.first_degree_school))
    highest = school_map.get(schools.normalize_school_name(candidate.highest_degree_school))
    tags = {tag.pk: ref() for tag in m.SchoolTag.objects.all()}
    tag_ref = lambda s: tags[schools._school_tag(s, default, non_target).pk]
    try:
        workflow = candidate.workflow
    except m.CandidateWorkflow.DoesNotExist:
        workflow = None
    rejected = set(m.AssignmentAttempt.objects.filter(workflow=workflow, feedback_result="rejected").values_list("resume_id", flat=True)) if workflow else set()
    volunteer_ids, job_ids, volunteers, jobs, rules, rule_ids = {}, {}, [], [], [], {}
    for resume in candidate.resumes.order_by("id"):
        key = stable_ref("volunteer", resume.pk); volunteer_ids[key] = resume.pk
        artifact = dict(path="", checksum="", media_type="application/pdf", size_bytes=0)
        try:
            path = Path(settings.MEDIA_ROOT) / RESUME_SUBDIR / Path(resume.resume_file or "").name
            artifact.update(path=str(path.relative_to(settings.MEDIA_ROOT)), checksum=hashlib.sha256(path.read_bytes()).hexdigest(), size_bytes=path.stat().st_size)
        except (AIServiceError, OSError, ValueError):
            pass  # 缺失文件保留为可审计失败，不阻止创建整个批次。
        volunteers.append(dict(ref=key, position_name=resume.position_name, entity=resume.entity,
                               apply_date=resume.apply_date.isoformat() if resume.apply_date else "",
                               rejected=resume.pk in rejected, artifact=artifact))
    departments = {}
    capacities = {c.job_id: c for c in run.job_capacities.all()} if run else {}
    for job in m.Job.objects.filter(is_active=True).select_related("department").prefetch_related("majors").order_by("id"):
        key = stable_ref("job", job.pk); job_ids[key] = job.pk
        content = job_content(job); content.pop("department_identity"); content.pop("is_active")
        capacity = capacities.get(job.pk)
        jobs.append(dict(ref=key, content_hash=job_hash(job), **content,
                         department_ref=departments.setdefault(job.department_id, ref()) if job.department_id else "",
                         capacity=capacity.capacity if capacity else job.headcount,
                         used_count=capacity.used_count if capacity else 0))
    for rule in school_admission.active_rules():
        rule_ref = ref(); rule_ids[rule_ref] = rule.pk
        rules.append(dict(ref=rule_ref, priority=rule.priority,
                          first_tag_refs=[tags[l.school_tag_id] for l in rule.tag_links.all() if l.degree_type == "first"],
                          highest_tag_refs=[tags[l.school_tag_id] for l in rule.tag_links.all() if l.degree_type == "highest"],
                          educations=[l.education for l in rule.education_links.all()]))
    snapshot = dict(candidate=dict(ref=ref(), highest_major=candidate.highest_major,
                    highest_education=candidate.highest_education, household_province=candidate.household_province,
                    first_degree_school=candidate.first_degree_school, first_degree_province=first.province if first else "",
                    first_degree_tag_ref=tag_ref(first), highest_degree_school=candidate.highest_degree_school,
                    highest_degree_province=highest.province if highest else "", highest_degree_tag_ref=tag_ref(highest)),
                    workflow=dict(revision=workflow.revision if workflow else 0, current_rank=workflow.current_rank if workflow else 0, retry_volunteer_ref=""),
                    volunteers=volunteers, admission_rules=rules, jobs=jobs,
                    schools=[dict(name=s.name, province=s.province, tag_ref=tags.get(s.school_tag_id, "")) for s in school_map.values() if s in (first, highest)],
                    default_school_tag_ref=tags[default.pk], non_target_school_tag_ref=tags[non_target.pk],
                    taxonomy=[dict(name=a.name, category=a.category.name, match_type=a.match_type) for a in m.MajorAlias.objects.filter(is_active=True, category__is_active=True).select_related("category")])
    return dict(snapshot=snapshot, volunteer_ids=volunteer_ids, job_ids=job_ids,
                task_id=uuid.uuid4().hex,
                rule_ids=rule_ids, tag_ids={v:k for k,v in tags.items()},
                lane=settings.AGENT_KERNEL_ROLLOUT, pin=runtime_pin().model_dump(),
                thresholds=dict(dispatch=ai_config.get_ai_runtime_config().dispatch_threshold,
                                review=ai_config.get_ai_runtime_config().review_threshold))


def build_task(frozen, model_config, workflow_revision, *, task_id, retry_resume_id=None, prepare_only=False):
    snapshot = deepcopy(frozen["snapshot"])
    snapshot["workflow"]["revision"] = workflow_revision
    if retry_resume_id:
        snapshot["workflow"]["retry_volunteer_ref"] = next((k for k, v in frozen["volunteer_ids"].items() if v == retry_resume_id), "")
        if not snapshot["workflow"]["retry_volunteer_ref"]:
            raise AIServiceError("ai_reference_invalidated", "重试志愿不在任务冻结范围内")
    expiry = int(time.time()) + 900
    for v in snapshot["volunteers"]:
        a = v["artifact"]; a["expires_at"] = expiry
        value = f'{a["path"]}\n{a["checksum"]}\n{a["size_bytes"]}\n{expiry}'
        a["signature"] = hmac.new(settings.AGENT_KERNEL_DOCUMENT_SIGNING_KEY.encode(), value.encode(), hashlib.sha256).hexdigest()
    model = KernelModelConfigV1(api_style=model_config.api_style, base_url=model_config.base_url,
        model_name=model_config.model_name, structured_output_mode=ai_config.get_structured_output_mode(api_style=model_config.api_style),
        timeout_seconds=ai_config.get_ai_runtime_config().timeout_seconds, retry_count=ai_config.get_ai_runtime_config().retry_count,
        insecure_skip_verify=settings.AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY)
    return TaskEnvelopeV1(prepare_only=prepare_only, task_id=task_id, idempotency_key=_pin_id(dict(task_id=task_id, snapshot=snapshot, pin=frozen["pin"], prepare_only=prepare_only)),
                          trigger="processing_run", pin=frozen["pin"], snapshot=snapshot, model=model)
