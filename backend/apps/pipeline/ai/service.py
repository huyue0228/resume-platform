"""AI 简历筛选服务。

模型只产出结构化建议；岗位、部门、接口人引用和最终置信度由后端确定性校验。
不会保存模型原始响应，也不会在失败时回退 Rule。
"""

from __future__ import annotations

import atexit
import hashlib
import json
import logging
import os
import threading
import time
from dataclasses import dataclass
from typing import Optional

import httpx
from django.conf import settings
from django.utils import timezone

from apps.core import models as m
from apps.core.departments import secondary_department
from apps.ingestion.sources import RESUME_SUBDIR
from apps.pipeline import ai_config

from . import concurrency, prompt_harness
from .schemas import ResumeScreeningOutput
from .structured_output import (
    AIServiceError,
    call_structured_model,
    probe_structured_output_mode,
    safe_model_error as _safe_model_error,
)


logger = logging.getLogger(__name__)
_CLIENT_CACHE = {}
_CLIENT_CACHE_LOCK = threading.Lock()
_OCR_SEMAPHORE_LOCK = threading.Lock()
_OCR_SEMAPHORES = {}


SCORE_WEIGHTS = {
    "major_match": 0.30,
    "skills_match": 0.20,
    "experience_evidence": 0.25,
    "job_requirement": 0.15,
    "resume_quality": 0.10,
}
MAX_JOB_RESPONSIBILITIES_CHARS = 12000


def _strip_nul_bytes(value):
    """递归移除 PostgreSQL text/jsonb 不接受的 NUL 字符。"""
    if isinstance(value, str):
        return value.replace("\x00", "")
    if isinstance(value, list):
        return [_strip_nul_bytes(item) for item in value]
    if isinstance(value, tuple):
        return tuple(_strip_nul_bytes(item) for item in value)
    if isinstance(value, dict):
        return {
            _strip_nul_bytes(key): _strip_nul_bytes(item)
            for key, item in value.items()
        }
    return value


def _sanitize_screening_output(output):
    return ResumeScreeningOutput.model_validate(
        _strip_nul_bytes(output.model_dump())
    )


def _client_cache_key(model_config, runtime_config):
    api_key_fingerprint = hashlib.sha256(model_config.api_key.encode("utf-8")).hexdigest()
    return (
        model_config.api_style,
        model_config.base_url,
        api_key_fingerprint,
        runtime_config.timeout_seconds,
    )


def _remove_internal_placeholder_auth(request):
    """无鉴权服务使用 SDK 占位密钥初始化，但请求发出前移除认证头。"""
    request.headers.pop("Authorization", None)


def _get_openai_client(OpenAI, model_config, runtime_config):
    """按连接配置在当前 worker 进程内复用 OpenAI/httpx 客户端。"""
    cache_key = _client_cache_key(model_config, runtime_config)
    with _CLIENT_CACHE_LOCK:
        cached = _CLIENT_CACHE.get(cache_key)
        if cached:
            return cached[0]

        http_client_kwargs = {"verify": False}
        if not model_config.api_key:
            http_client_kwargs["event_hooks"] = {
                "request": [_remove_internal_placeholder_auth]
            }
        http_client = httpx.Client(**http_client_kwargs)
        kwargs = {
            # OpenAI SDK 要求 api_key 非空；无鉴权内网服务使用占位值初始化。
            "api_key": model_config.api_key or "internal-no-key",
            "timeout": runtime_config.timeout_seconds,
            "max_retries": 0,
            "http_client": http_client,
        }
        if model_config.base_url:
            kwargs["base_url"] = model_config.base_url
        try:
            client = OpenAI(**kwargs)
        except Exception:
            http_client.close()
            raise
        _CLIENT_CACHE[cache_key] = (client, http_client)
        return client


def close_cached_ai_clients():
    """关闭当前进程缓存的客户端，供 worker 退出和测试清理。"""
    with _CLIENT_CACHE_LOCK:
        cached_clients = list(_CLIENT_CACHE.values())
        _CLIENT_CACHE.clear()
    for client, http_client in cached_clients:
        close_client = getattr(client, "close", None)
        if callable(close_client):
            try:
                close_client()
                continue
            except Exception:
                pass
        try:
            http_client.close()
        except Exception:
            pass


atexit.register(close_cached_ai_clients)


def test_model_connection():
    """使用真实业务 Schema 验证连接并探测严格/兼容输出能力。"""
    model_config = ai_config.get_ai_model_config()
    runtime_config = ai_config.get_ai_runtime_config()
    try:
        from openai import OpenAI
    except ImportError as exc:
        raise AIServiceError("ai_not_configured", "服务端未安装 OpenAI SDK") from exc

    try:
        client = _get_openai_client(OpenAI, model_config, runtime_config)
        structured_output_mode = probe_structured_output_mode(
            client=client,
            model_config=model_config,
            schema_model=ResumeScreeningOutput,
            messages=[
                {
                    "role": "system",
                    "content": "这是结构化能力测试，只返回符合指定 Schema 的 JSON。",
                },
                {
                    "role": "user",
                    "content": (
                        "返回一个最小但完整的测试结果：所有文本可为空，列表可为空，"
                        "recommendation 使用 review，所有分数使用 0。"
                    ),
                },
            ],
        )
    except AIServiceError as exc:
        logger.warning(
            "AI connection test failed model=%s api_style=%s code=%s error_type=%s",
            model_config.model_name,
            model_config.api_style,
            exc.code,
            type(exc.__cause__ or exc).__name__,
        )
        raise
    except Exception as exc:  # SDK 供应商异常类型随版本变化，统一输出脱敏摘要
        code, detail = _safe_model_error(exc)
        logger.warning(
            "AI connection test failed model=%s api_style=%s code=%s error_type=%s",
            model_config.model_name,
            model_config.api_style,
            code,
            type(exc).__name__,
        )
        raise AIServiceError(code, detail) from exc
    return {
        "model_name": model_config.model_name,
        "api_style": model_config.api_style,
        "base_url": model_config.base_url,
        "structured_output_mode": structured_output_mode,
    }


def list_available_models(*, base_url, api_key=""):
    """从 OpenAI 兼容的 ``GET /models`` 端点读取模型 ID。"""
    base_url, effective_api_key = ai_config.get_ai_discovery_config(
        base_url=base_url,
        api_key=api_key,
    )
    headers = {"Accept": "application/json"}
    if effective_api_key:
        headers["Authorization"] = f"Bearer {effective_api_key}"
    try:
        response = httpx.get(
            f"{base_url}/models",
            headers=headers,
            timeout=ai_config.get_ai_runtime_config().timeout_seconds,
            verify=False,
        )
        response.raise_for_status()
        payload = response.json()
    except Exception as exc:
        code, detail = _safe_model_error(exc)
        logger.warning(
            "AI model discovery failed base_url=%s code=%s error_type=%s",
            base_url,
            code,
            type(exc).__name__,
        )
        raise AIServiceError(code, detail) from exc
    items = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(items, list):
        raise AIServiceError("invalid_ai_output", "模型列表响应缺少 data 数组")
    model_names = sorted(
        {
            item["id"].strip()
            for item in items
            if isinstance(item, dict)
            and isinstance(item.get("id"), str)
            and item["id"].strip()
        }
    )
    if not model_names:
        raise AIServiceError("invalid_ai_output", "模型服务未返回可用的模型名称")
    return model_names


from apps.pipeline.screening_types import ScreeningResult


@dataclass(frozen=True)
class PreparedScreening:
    """Django 控制面完成的确定性准备结果。"""

    resume: m.Resume
    job: m.Job
    department: Optional[m.Department]
    profile: m.ResumeProfile
    checksum: str
    text: str
    ocr_used: bool
    job_context: dict
    model_config: ai_config.AIModelConfig


def _resume_path(resume):
    filename = os.path.basename(resume.resume_file or "")
    if not filename:
        raise AIServiceError("pdf_missing", "缺少 PDF 简历文件")
    if not filename.lower().endswith(".pdf"):
        raise AIServiceError("pdf_parse_failed", "AI 筛选仅支持 PDF 简历")
    path = os.path.join(settings.MEDIA_ROOT, RESUME_SUBDIR, filename)
    if not os.path.isfile(path):
        raise AIServiceError("pdf_missing", "PDF 简历文件不存在")
    return path


def _non_whitespace_length(value):
    return len("".join(str(value or "").split()))


def _ocr_semaphore(limit):
    with _OCR_SEMAPHORE_LOCK:
        semaphore = _OCR_SEMAPHORES.get(limit)
        if semaphore is None:
            semaphore = threading.BoundedSemaphore(limit)
            _OCR_SEMAPHORES[limit] = semaphore
        return semaphore


def _ocr_pdf(path):
    """在单 worker 有界并发和总时限内执行本地中英文 OCR。"""
    max_pages = max(1, int(settings.RESUME_OCR_MAX_PAGES))
    dpi = max(72, int(settings.RESUME_OCR_DPI))
    timeout_seconds = max(1, int(settings.RESUME_OCR_TIMEOUT_SECONDS))
    concurrency_limit = max(1, int(settings.RESUME_OCR_CONCURRENCY))
    deadline = time.monotonic() + timeout_seconds
    semaphore = _ocr_semaphore(concurrency_limit)
    if not semaphore.acquire(timeout=timeout_seconds):
        raise AIServiceError("pdf_parse_failed", "OCR 并发繁忙且等待超时")
    try:
        try:
            import fitz
            import pytesseract
            from PIL import Image
        except ImportError as exc:
            raise AIServiceError("pdf_parse_failed", "服务端缺少 OCR 运行依赖") from exc

        texts = []
        try:
            with fitz.open(path) as document:
                for page_index in range(min(document.page_count, max_pages)):
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise AIServiceError("pdf_parse_failed", "PDF OCR 总处理超时")
                    pixmap = document.load_page(page_index).get_pixmap(
                        dpi=dpi, alpha=False
                    )
                    image = Image.frombytes(
                        "RGB", (pixmap.width, pixmap.height), pixmap.samples
                    )
                    page_text = pytesseract.image_to_string(
                        image,
                        lang="chi_sim+eng",
                        timeout=max(1, int(remaining)),
                    )
                    if page_text.strip():
                        texts.append(page_text.strip())
        except AIServiceError:
            raise
        except RuntimeError as exc:
            raise AIServiceError("pdf_parse_failed", "PDF OCR 处理超时") from exc
        except Exception as exc:
            raise AIServiceError("pdf_parse_failed", "PDF OCR 处理失败") from exc
        return "\n\n".join(texts)
    finally:
        semaphore.release()


def _extract_pdf(resume):
    path = _resume_path(resume)
    with open(path, "rb") as file_obj:
        content = file_obj.read()
    checksum = hashlib.sha256(content).hexdigest()
    primary_error = None
    try:
        from pypdf import PdfReader

        reader = PdfReader(path)
        text = "\n\n".join((page.extract_text() or "").strip() for page in reader.pages)
    except Exception as exc:
        primary_error = exc
        text = ""
    text = _strip_nul_bytes(text).strip()
    ocr_used = _non_whitespace_length(text) < 50
    if ocr_used:
        try:
            ocr_text = _strip_nul_bytes(_ocr_pdf(path)).strip()
        except AIServiceError as exc:
            if primary_error:
                raise AIServiceError("pdf_parse_failed", "PDF 正文抽取和 OCR 均失败") from exc
            raise
        text = "\n\n".join(part for part in [text, ocr_text] if part).strip()
    if _non_whitespace_length(text) < 50:
        raise AIServiceError("pdf_parse_failed", "PDF 未抽取到足够的可用正文")
    return checksum, text, ocr_used


def _current_job_context(job):
    """只给模型岗位业务描述，不暴露或要求它选择岗位、部门、接口人引用。"""
    responsibilities = _strip_nul_bytes(job.responsibilities or "").strip()
    if not responsibilities:
        raise AIServiceError(
            "job_responsibility_missing",
            "岗位缺少工作职责，请先在岗位需求中补充后重新处理",
        )
    return {
        "entity": job.entity,
        "public_name": job.public_name,
        "position_name": job.position_name,
        "category": job.category,
        "job_family": job.job_family,
        "location": job.location,
        "required_majors": [item.major for item in job.majors.all()],
        "responsibilities": responsibilities[:MAX_JOB_RESPONSIBILITIES_CHARS],
    }


def _prompt(
    resume,
    text,
    job_context,
    *,
    prompt_version=None,
    prompt_modules=None,
):
    if prompt_modules is None:
        _resolved_version, prompt_modules = prompt_harness.get_prompt_modules(
            prompt_version
        )
    payload = prompt_harness.build_screening_payload(resume, text, job_context)
    return prompt_harness.build_screening_prompt(prompt_modules, payload)


def _call_model(
    resume,
    text,
    job_context,
    *,
    processing_run_id=None,
    cancelled=None,
    prompt_version=None,
    prompt_modules=None,
):
    model_config = ai_config.get_ai_model_config(prompt_version=prompt_version)
    runtime_config = ai_config.get_ai_runtime_config()
    try:
        from openai import OpenAI
    except ImportError as exc:
        raise AIServiceError("ai_not_configured", "服务端未安装 OpenAI SDK") from exc

    system, user = _prompt(
        resume,
        text,
        job_context,
        prompt_version=prompt_version,
        prompt_modules=prompt_modules,
    )
    system_with_protocol = prompt_harness.append_structured_output_protocol(
        system, ResumeScreeningOutput
    )
    try:
        client = _get_openai_client(OpenAI, model_config, runtime_config)
    except Exception as exc:
        code, message = _safe_model_error(exc)
        logger.warning(
            "AI client initialization failed model=%s api_style=%s code=%s error_type=%s",
            model_config.model_name,
            model_config.api_style,
            code,
            type(exc).__name__,
        )
        raise AIServiceError(code, message) from exc
    return call_structured_model(
        client=client,
        model_config=model_config,
        runtime_config=runtime_config,
        messages=[
            {"role": "system", "content": system_with_protocol},
            {"role": "user", "content": user},
        ],
        schema_model=ResumeScreeningOutput,
        processing_run_id=processing_run_id,
        cancelled=cancelled,
        operation="AI screening",
    )


def _validate_specialist_evidence(output, text):
    """只保留可定位的专项证据；证据不可靠时退回普通 AI 结果。"""
    decision = output.decision
    if not decision.ai_specialist_match:
        decision.ai_specialist_confidence = 0
        decision.ai_specialist_evidence = []
        return
    normalized_text = "".join(str(text or "").casefold().split())
    matched = []
    for evidence in decision.ai_specialist_evidence:
        normalized_evidence = "".join(str(evidence or "").casefold().split())
        # 至少保留一段带上下文的证据，避免“模型”“智能体开发”等孤立关键词触发。
        if len(normalized_evidence) >= 8 and normalized_evidence in normalized_text:
            matched.append(evidence)
    if not matched:
        decision.ai_specialist_match = False
        decision.ai_specialist_confidence = 0
        decision.ai_specialist_evidence = []
        return
    decision.ai_specialist_evidence = matched


def _score(output, text):
    breakdown = output.decision.score_breakdown.model_dump()
    profile = output.profile
    incomplete = len(text.strip()) < 200 or not (
        profile.projects or profile.internships or profile.skills
    )
    if incomplete:
        breakdown["resume_quality"] = min(breakdown["resume_quality"], 0.35)
        if "简历画像信息不足，必须人工复核" not in output.decision.risks:
            output.decision.risks.append("简历画像信息不足，必须人工复核")
        if "profile_incomplete" not in profile.risk_flags:
            profile.risk_flags.append("profile_incomplete")
    confidence = sum(breakdown[key] * weight for key, weight in SCORE_WEIGHTS.items())
    if incomplete:
        dispatch_threshold = ai_config.get_ai_runtime_config().dispatch_threshold
        confidence = min(confidence, max(0, dispatch_threshold - 0.01))
    return round(max(0.0, min(1.0, confidence)), 4), breakdown


def prepare_screening(
    resume,
    job,
    *,
    department=None,
    force=False,
    prompt_version=None,
):
    """在 Django 内完成 PDF/OCR、固定引用和画像缓存准备。"""

    job_context = _current_job_context(job)
    if department is None:
        department = secondary_department(job.department)

    extracted = _extract_pdf(resume)
    checksum, text = extracted[:2]
    ocr_used = bool(extracted[2]) if len(extracted) > 2 else False
    text = _strip_nul_bytes(text).strip()
    if not text:
        raise AIServiceError("pdf_parse_failed", "PDF 未抽取到可用正文，可能是扫描件")
    model_config = ai_config.get_ai_model_config(prompt_version=prompt_version)
    profile, _ = m.ResumeProfile.objects.get_or_create(resume=resume)
    cache_valid = (
        not force
        and profile.file_checksum == checksum
        and profile.parse_model == model_config.parser_version
        and profile.profile_version == model_config.profile_version
        and profile.parse_status == "parsed"
    )
    profile.file_checksum = checksum
    profile.parse_model = model_config.parser_version
    profile.profile_version = model_config.profile_version
    profile.raw_text = text
    profile.profile_risk_flags = ["ocr_fallback"] if ocr_used else []
    profile.parse_status = "text_extracted"
    profile.parse_error = ""
    profile.parsed_at = timezone.now()
    profile.save()
    _ = cache_valid
    return PreparedScreening(
        resume=resume,
        job=job,
        department=department,
        profile=profile,
        checksum=checksum,
        text=text,
        ocr_used=ocr_used,
        job_context=job_context,
        model_config=model_config,
    )


def complete_screening(prepared, output, *, kernel_metadata=None):
    """对 Kernel 建议再次校验、计分并持久化画像。"""

    profile = prepared.profile
    output = _sanitize_screening_output(output)
    _validate_specialist_evidence(output, prepared.text)
    confidence, breakdown = _score(output, prepared.text)

    data = output.profile
    # Agent 提取的教育经历只用于展示；学历和院校准入仍由 Policy Gate 固化。
    profile.education_experiences = [item.model_dump() for item in data.educations]
    profile.project_experiences = [item.model_dump() for item in data.projects]
    profile.internship_experiences = [item.model_dump() for item in data.internships]
    profile.skills = data.skills
    profile.certificates = data.certificates
    profile.major_direction = data.major_direction[:128]
    profile.summary = data.summary
    profile.profile_risk_flags = list(
        dict.fromkeys(
            [
                *data.risk_flags,
                *(["ocr_fallback"] if prepared.ocr_used else []),
            ]
        )
    )
    profile.parse_status = "parsed"
    profile.parse_error = ""
    profile.parsed_at = timezone.now()
    profile.save()
    from apps.pipeline.services.classify_school import sync_candidate_school_tags

    sync_candidate_school_tags(prepared.resume.candidate)
    kernel_metadata = kernel_metadata or {}
    return ScreeningResult(
        profile=profile,
        output=output,
        job=prepared.job,
        department=prepared.department,
        confidence=confidence,
        score_breakdown=breakdown,
        model_name=prepared.model_config.model_name,
        prompt_version=prepared.model_config.prompt_version,
        decision_version=prepared.model_config.decision_version,
        kernel_pin_id=kernel_metadata.get("pin_id", ""),
        kernel_build=kernel_metadata.get("kernel_build", ""),
        protocol_version=kernel_metadata.get("protocol_version", ""),
        toolset_version=kernel_metadata.get("toolset_version", ""),
        safe_trace=kernel_metadata.get("safe_trace"),
    )


def mark_screening_error(prepared, exc):
    """把受控错误关联到画像，供调用方生成可审计失败决策。"""

    if exc.profile is None:
        exc.profile = prepared.profile
    prepared.profile.parse_error = (
        exc.message if exc.code in {"ai_invalid_output", "agent_invalid_output"} else ""
    )
    prepared.profile.save(update_fields=["parse_error", "updated_at"])


def screen_resume(
    resume,
    job,
    *,
    department=None,
    force=False,
    processing_run_id=None,
    cancelled=None,
    prompt_version=None,
):
    """兼容适配器：在 Django 进程内完成一次受控模型评估。"""

    prepared = prepare_screening(
        resume,
        job,
        department=department,
        force=force,
        prompt_version=prompt_version,
    )
    try:
        output = _call_model(
            resume,
            prepared.text,
            prepared.job_context,
            processing_run_id=processing_run_id,
            cancelled=cancelled,
            prompt_version=prepared.model_config.prompt_version,
        )
        return complete_screening(prepared, output)
    except AIServiceError as exc:
        mark_screening_error(prepared, exc)
        raise
