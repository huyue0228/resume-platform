"""OpenAI 结构化输出 schema；所有字段均由服务端再次校验。"""

from __future__ import annotations

from pydantic import BaseModel, ConfigDict, Field


class ConnectionProbeOutput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    ok: bool


class SchoolProvinceItem(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1, max_length=128)
    province: str = Field(default="", max_length=32)


class SchoolProvinceOutput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    schools: list[SchoolProvinceItem] = Field(max_length=50)
