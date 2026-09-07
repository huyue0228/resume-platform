"""平台领域结果：供人工工作流与内核适配器共享，不包含模型调用。"""
from dataclasses import dataclass
from typing import Literal, Optional
from pydantic import BaseModel, ConfigDict, Field
from apps.core import models as m

class ExperienceItem(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str
    role: str
    period: str
    description: str
    evidence: str


class EducationItem(BaseModel):
    model_config = ConfigDict(extra="forbid")

    school_name: str = ""
    degree: str = ""
    major: str = ""
    period: str = ""
    evidence: str = ""


class ScoreBreakdown(BaseModel):
    model_config = ConfigDict(extra="forbid")

    major_match: float = Field(ge=0, le=1)
    skills_match: float = Field(ge=0, le=1)
    experience_evidence: float = Field(ge=0, le=1)
    job_requirement: float = Field(ge=0, le=1)
    resume_quality: float = Field(ge=0, le=1)


class ResumeProfileOutput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    major_direction: str
    educations: list[EducationItem] = Field(default_factory=list)
    projects: list[ExperienceItem]
    internships: list[ExperienceItem]
    skills: list[str]
    certificates: list[str]
    summary: str
    risk_flags: list[str]


class DispatchRecommendationOutput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    recommendation: Literal["dispatch", "review", "archive"]
    score_breakdown: ScoreBreakdown
    summary: str
    reason: str
    evidence: list[str]
    risks: list[str]
    ai_specialist_match: bool = False
    ai_specialist_confidence: float = Field(default=0, ge=0, le=1)
    ai_specialist_evidence: list[str] = Field(default_factory=list)


class ResumeScreeningOutput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    profile: ResumeProfileOutput
    decision: DispatchRecommendationOutput

@dataclass(frozen=True)
class ScreeningResult:
    profile: m.ResumeProfile
    output: ResumeScreeningOutput
    job: Optional[m.Job]
    department: Optional[m.Department]
    confidence: float
    score_breakdown: dict
    model_name: str
    prompt_version: str
    decision_version: str
    kernel_pin_id: str = ""
    kernel_build: str = ""
    protocol_version: str = ""
    toolset_version: str = ""
    safe_trace: Optional[dict] = None
    kernel_task: Optional[dict] = None
