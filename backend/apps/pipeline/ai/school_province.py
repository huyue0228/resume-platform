"""使用当前已测试通过的 AI 连接补全院校所在省份。"""

from __future__ import annotations

import logging

from apps.pipeline import ai_config
from apps.pipeline.regions import NORTH_PROVINCES, SOUTH_PROVINCES

from . import concurrency, prompt_harness
from .schemas import SchoolProvinceOutput
from .service import (
    _get_openai_client,
    _safe_model_error,
)
from .structured_output import AIServiceError, call_structured_model


logger = logging.getLogger(__name__)
MAX_SCHOOLS_PER_REQUEST = 50
SUPPORTED_PROVINCES = tuple(sorted(NORTH_PROVINCES | SOUTH_PROVINCES))


def _canonical_province(value):
    text = str(value or "").strip()
    for province in sorted(SUPPORTED_PROVINCES, key=len, reverse=True):
        if province in text:
            return province
    return ""


def _prompt(
    school_names,
    *,
    prompt_version=None,
    prompt_modules=None,
):
    if prompt_modules is None:
        _resolved_version, prompt_modules = prompt_harness.get_prompt_modules(
            prompt_version
        )
    return prompt_harness.build_school_prompt(prompt_modules, school_names)


def call_school_province_model(
    school_names,
    *,
    prompt_version=None,
    prompt_modules=None,
):
    """通过正式结构化调用路径返回模型原始 Pydantic 结果。"""
    names = list(dict.fromkeys(str(name or "").strip() for name in school_names))
    names = [name for name in names if name]
    if not names:
        return SchoolProvinceOutput(schools=[])
    if len(names) > MAX_SCHOOLS_PER_REQUEST:
        raise ValueError(f"单次最多补全 {MAX_SCHOOLS_PER_REQUEST} 所院校")

    model_config = ai_config.get_ai_model_config(prompt_version=prompt_version)
    runtime_config = ai_config.get_ai_runtime_config()
    try:
        from openai import OpenAI
    except ImportError as exc:
        raise AIServiceError("ai_not_configured", "服务端未安装 OpenAI SDK") from exc

    try:
        client = _get_openai_client(OpenAI, model_config, runtime_config)
    except Exception as exc:
        code, message = _safe_model_error(exc)
        logger.warning(
            "School province AI client initialization failed code=%s error_type=%s",
            code,
            type(exc).__name__,
        )
        raise AIServiceError(code, message) from exc

    system, user = _prompt(
        names,
        prompt_version=prompt_version,
        prompt_modules=prompt_modules,
    )
    system_with_protocol = prompt_harness.append_structured_output_protocol(
        system, SchoolProvinceOutput
    )
    return call_structured_model(
        client=client,
        model_config=model_config,
        runtime_config=runtime_config,
        messages=[
            {"role": "system", "content": system_with_protocol},
            {"role": "user", "content": user},
        ],
        schema_model=SchoolProvinceOutput,
        operation="School province enrichment",
    )


def infer_school_provinces(
    school_names,
    *,
    prompt_version=None,
    prompt_modules=None,
):
    """返回 ``{院校名称: 标准省份简称}``，无把握或越界输出不会进入结果。"""
    names = list(dict.fromkeys(str(name or "").strip() for name in school_names))
    names = [name for name in names if name]
    output = call_school_province_model(
        names,
        prompt_version=prompt_version,
        prompt_modules=prompt_modules,
    )
    requested_names = set(names)
    result = {}
    for item in output.schools:
        name = item.name.strip()
        province = _canonical_province(item.province)
        if name in requested_names and province:
            result[name] = province
    return result
