"""简历分析公开协议。这里不定义招聘准入或业务动作。"""
from datetime import datetime
from typing import Literal, Optional

from pydantic import BaseModel, ConfigDict, Field, model_validator

PROTOCOL = "resume-analysis/v1"
TOOLSET = "resume-job-match-tools/v1"
RESULT = "resume-job-match/v1"
POLICY = "django-policy-gate/v2"
INSTRUCTIONS = "resume-job-match-kernel/v1"
SCORE_WEIGHTS = {"major_match": .30, "skills_match": .20, "experience_evidence": .25,
                 "job_requirement": .15, "resume_quality": .10}


class StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid", allow_inf_nan=False)


class TaskPinV1(StrictModel):
    pin_id: str = Field(min_length=1)
    kernel_build: str = Field(min_length=1)
    protocol_version: Literal[PROTOCOL] = PROTOCOL
    toolset_version: Literal[TOOLSET] = TOOLSET
    result_schema_version: Literal[RESULT] = RESULT
    policy_version: Literal[POLICY] = POLICY
    instruction_version: Literal[INSTRUCTIONS] = INSTRUCTIONS
    model_config_revision: str = Field(min_length=1)


class TaskBudgetV1(StrictModel):
    max_turns: int = Field(default=32, ge=1, le=64)
    max_tool_calls: int = Field(default=256, ge=1, le=512)
    max_duration_seconds: int = Field(default=600, ge=1, le=1800)
    max_ocr_pages: int = Field(default=30, ge=1, le=100)
    max_tokens: int = Field(default=120000, ge=1, le=1000000)


class ModelConfigV1(StrictModel):
    api_style: Literal["responses", "chat_json"]
    base_url: str = Field(min_length=1)
    model_name: str = Field(min_length=1)
    structured_output_mode: str = "json_compat"
    timeout_seconds: float = Field(default=120, gt=0, le=1800)
    retry_count: int = Field(default=1, ge=0, le=5)
    insecure_skip_verify: bool = False


class ArtifactV1(StrictModel):
    path: str = Field(min_length=1, max_length=1024)
    checksum: str = Field(pattern=r"^[a-f0-9]{64}$")
    media_type: Literal["application/pdf"] = "application/pdf"
    size_bytes: int = Field(gt=0, le=33554432)
    expires_at: int = Field(gt=0)
    signature: str = Field(pattern=r"^[a-f0-9]{64}$")


class CandidateContextV1(StrictModel):
    ref: str = Field(min_length=1, max_length=128)
    highest_major: str = ""
    highest_education: str = ""


class JobRequirementV1(StrictModel):
    ref: str = Field(min_length=1, max_length=128)
    content_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    entity: str = ""
    public_name: str = ""
    position_name: str = ""
    category: str = ""
    job_family: str = ""
    location: str = ""
    education: str = ""
    required_majors: list[str] = Field(default_factory=list)
    responsibilities: str = ""
    department_ref: str = ""
    department_name: str = ""


class MajorAliasV1(StrictModel):
    name: str
    category: str
    match_type: str


class AnalysisScopeV1(StrictModel):
    candidate: CandidateContextV1
    volunteer_ref: str = Field(min_length=1, max_length=128)
    artifact: ArtifactV1
    jobs: list[JobRequirementV1] = Field(min_length=1, max_length=2000)
    taxonomy: list[MajorAliasV1] = Field(default_factory=list)

    @model_validator(mode="after")
    def unique_jobs(self):
        if len({job.ref for job in self.jobs}) != len(self.jobs):
            raise ValueError("duplicate job reference")
        return self


class AnalysisRequestV1(StrictModel):
    protocol_version: Literal[PROTOCOL] = PROTOCOL
    task_kind: Literal["candidate.resume_job_match"] = "candidate.resume_job_match"
    task_id: str = Field(min_length=1, max_length=128)
    idempotency_key: str = Field(min_length=1, max_length=256)
    trigger: str = "processing_run"
    workflow_revision: int = Field(ge=0)
    pin: TaskPinV1
    scope: AnalysisScopeV1
    model: ModelConfigV1
    budget: TaskBudgetV1 = Field(default_factory=TaskBudgetV1)


class EvidenceV1(StrictModel):
    quote: str = Field(min_length=8)
    page: int = Field(ge=1)
    start_line: int = Field(ge=1)
    end_line: int = Field(ge=1)


class ClaimDetailsV1(StrictModel):
    school_name: str = ""
    degree: str = ""
    major: str = ""
    period: str = ""
    name: str = ""
    role: str = ""


class ClaimV1(StrictModel):
    details: ClaimDetailsV1 = Field(default_factory=ClaimDetailsV1)
    confidence: float = Field(default=0, ge=0, le=1)
    kind: Literal["education", "project", "internship", "skill", "certificate", "major_direction", "agent_experience", "risk"]
    summary: str = Field(min_length=1)
    evidence: list[EvidenceV1] = Field(min_length=1)


class CandidateProfileV1(StrictModel):
    source_text: str = Field(default="", max_length=1000000)
    claims: list[ClaimV1] = Field(min_length=1)
    risks: list[str]


class ScoreBreakdown(StrictModel):
    major_match: float = Field(ge=0, le=1)
    skills_match: float = Field(ge=0, le=1)
    experience_evidence: float = Field(ge=0, le=1)
    job_requirement: float = Field(ge=0, le=1)
    resume_quality: float = Field(ge=0, le=1)


class JobMatchV1(StrictModel):
    job_ref: str
    dimensions: ScoreBreakdown
    confidence: float = Field(ge=0, le=1)
    evidence: list[EvidenceV1] = Field(min_length=1)
    risks: list[str]
    reason: str = Field(min_length=1)
    score: float = Field(ge=0, le=1)
    rank: int = Field(ge=1)


class TaskManifestV1(StrictModel):
    reused_from_task_id: str = ""
    input_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    resume_checksum: str
    covered_jobs: list[str]
    tool_versions: dict[str, str]
    warnings: list[str]
    terminal_state: Literal["DONE", "FAILED"]
    failure_code: str = ""
    ocr_pages: int = Field(ge=0)


class TaskToolTraceV1(StrictModel):
    name: str = Field(max_length=200)
    status: str = Field(max_length=64)
    duration_ms: int = Field(ge=0)
    item_count: int = Field(ge=0)


class TaskSafeTraceV1(StrictModel):
    trace_id: str = ""
    kernel_build: str = ""
    started_at: Optional[datetime] = None
    finished_at: Optional[datetime] = None
    turns: int = Field(ge=0, le=64)
    tool_call_count: int = Field(default=0, ge=0, le=512)
    tool_calls: list[TaskToolTraceV1] = Field(default_factory=list)
    input_tokens: int = Field(default=0, ge=0)
    output_tokens: int = Field(default=0, ge=0)
    status: str = Field(default="", max_length=32)


class AnalysisResponseV1(StrictModel):
    protocol_version: Literal[PROTOCOL] = PROTOCOL
    task_id: str
    idempotency_key: str
    pin: TaskPinV1
    workflow_revision: int = Field(ge=0)
    profile: Optional[CandidateProfileV1] = None
    matches: list[JobMatchV1]
    manifest: TaskManifestV1
    safe_trace: TaskSafeTraceV1
