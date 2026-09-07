"""OpenAI 结构化输出 schema；所有字段均由服务端再次校验。"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field


from apps.pipeline.screening_types import (ExperienceItem, EducationItem, ScoreBreakdown,
    ResumeProfileOutput, DispatchRecommendationOutput, ResumeScreeningOutput)


class SchoolProvinceItem(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1, max_length=128)
    province: str = Field(default="", max_length=32)


class SchoolProvinceOutput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    schools: list[SchoolProvinceItem] = Field(max_length=50)
