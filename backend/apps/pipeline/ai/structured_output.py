"""共享的 AI 结构化输出调用、校验、兼容降级和有限纠错。"""

from __future__ import annotations

import json
import logging
import re
import time
from datetime import datetime, timezone as datetime_timezone
from email.utils import parsedate_to_datetime

from pydantic import ValidationError

from apps.pipeline import ai_config

from . import concurrency


logger = logging.getLogger(__name__)
_SAFE_LOCATION_PART = re.compile(r"^[A-Za-z_][A-Za-z0-9_]{0,63}$")


def _strip_nul_bytes(value):
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


from apps.pipeline.errors import AIServiceError


class StructuredOutputIssue(Exception):
    def __init__(self, kind, paths=()):
        super().__init__(kind)
        self.kind = kind
        self.paths = tuple(paths)


def safe_model_error(exc):
    """把供应商异常映射为不会泄漏请求内容或鉴权信息的摘要。"""
    name = type(exc).__name__.lower()
    response = getattr(exc, "response", None)
    status_code = getattr(exc, "status_code", None) or getattr(
        response, "status_code", None
    )
    if "timeout" in name:
        return "llm_timeout", "模型请求超时，请检查网络、服务状态或超时配置"
    if status_code in {401, 403} or "authentication" in name or "permission" in name:
        return "ai_connection_error", "模型认证失败，请检查 API Key 与服务权限"
    if status_code == 404 or "notfound" in name:
        return "ai_connection_error", "模型或 API 地址不可用，请检查模型名称和 Base URL"
    if status_code == 429 or "ratelimit" in name:
        return "ai_rate_limited", "模型服务限流，请稍后重试或调整并发"
    if "connection" in name or "connect" in name or "network" in name:
        return "ai_connection_error", "模型连接失败，请检查 Base URL、网络、代理和证书"
    return "ai_connection_error", "模型服务调用失败，请通过服务端日志查看错误类型"


def model_failure_kind(exc):
    """返回并发反馈类型、是否可重试及 Retry-After 秒数。"""
    response = getattr(exc, "response", None)
    status_code = getattr(exc, "status_code", None) or getattr(
        response, "status_code", None
    )
    name = type(exc).__name__.lower()
    retry_after = 0.0
    headers = getattr(response, "headers", None)
    if headers:
        raw_retry_after = headers.get("retry-after")
        try:
            retry_after = float(raw_retry_after or 0)
        except (TypeError, ValueError):
            try:
                retry_at = parsedate_to_datetime(raw_retry_after)
                if retry_at.tzinfo is None:
                    retry_at = retry_at.replace(tzinfo=datetime_timezone.utc)
                retry_after = max(
                    0.0,
                    (retry_at - datetime.now(datetime_timezone.utc)).total_seconds(),
                )
            except (TypeError, ValueError, OverflowError):
                retry_after = 0.0
    if status_code == 429 or "ratelimit" in name:
        return "rate_limit", True, retry_after
    if (
        (status_code is not None and int(status_code) >= 500)
        or "timeout" in name
        or "connection" in name
        or "connect" in name
        or "network" in name
    ):
        return "transient", True, retry_after
    return "neutral", False, retry_after


def release_model_slot(slot, outcome, *, retry_after=0):
    try:
        slot.release(outcome, retry_after=retry_after)
    except concurrency.AIConcurrencyError as exc:
        raise AIServiceError(
            "ai_limiter_unavailable", "AI 并发控制器不可用或任务已取消"
        ) from exc


def _validation_issue(exc):
    paths = []
    invalid_json = False
    if isinstance(exc, ValidationError):
        for item in exc.errors(
            include_url=False,
            include_context=False,
            include_input=False,
        )[:5]:
            error_type = str(item.get("type") or "validation_error")
            if not _SAFE_LOCATION_PART.fullmatch(error_type):
                error_type = "validation_error"
            if error_type == "json_invalid":
                invalid_json = True
                continue
            safe_parts = []
            for part in item.get("loc") or ():
                if isinstance(part, int):
                    safe_parts.append(str(part))
                elif isinstance(part, str) and _SAFE_LOCATION_PART.fullmatch(part):
                    safe_parts.append(part)
                else:
                    safe_parts.append("<field>")
            location = ".".join(safe_parts)
            paths.append(f"{location or '<root>'}:{error_type}")
    elif isinstance(exc, json.JSONDecodeError):
        invalid_json = True
    if invalid_json:
        return StructuredOutputIssue("invalid_json")
    return StructuredOutputIssue("schema", paths)


def _issue_from_exception(exc):
    name = type(exc).__name__.lower()
    if "lengthfinishreason" in name or "max_tokens" in name:
        return StructuredOutputIssue("truncated")
    if "contentfilter" in name or "refusal" in name:
        return StructuredOutputIssue("refusal")
    if isinstance(exc, (ValidationError, json.JSONDecodeError)):
        return _validation_issue(exc)
    if isinstance(exc, (TypeError, ValueError)):
        return StructuredOutputIssue("schema")
    return None


def _normalize_compat_json(content):
    if not isinstance(content, str):
        raise StructuredOutputIssue("empty")
    text = content.lstrip("\ufeff").strip()
    if not text:
        raise StructuredOutputIssue("empty")
    lines = text.splitlines()
    if (
        len(lines) >= 3
        and lines[0].strip().lower() in {"```", "```json"}
        and lines[-1].strip() == "```"
    ):
        text = "\n".join(lines[1:-1]).strip()
    return text


def _has_responses_refusal(response):
    for item in getattr(response, "output", None) or []:
        for content in getattr(item, "content", None) or []:
            if getattr(content, "type", "") == "refusal":
                return True
    return False


def _check_responses_status(response):
    if _has_responses_refusal(response):
        raise StructuredOutputIssue("refusal")
    if getattr(response, "status", "") == "incomplete":
        reason = getattr(getattr(response, "incomplete_details", None), "reason", "")
        if reason in {"max_output_tokens", "max_tokens"}:
            raise StructuredOutputIssue("truncated")
        raise StructuredOutputIssue("empty")


def _check_chat_choice(choice):
    finish_reason = getattr(choice, "finish_reason", "")
    if finish_reason in {"length", "max_tokens"}:
        raise StructuredOutputIssue("truncated")
    if finish_reason in {"content_filter", "refusal"}:
        raise StructuredOutputIssue("refusal")
    message = getattr(choice, "message", None)
    if message is None:
        raise StructuredOutputIssue("empty")
    if getattr(message, "refusal", None):
        raise StructuredOutputIssue("refusal")
    return message


def _execute_once(client, model_config, mode, messages, schema_model):
    if model_config.api_style == "chat_json":
        if mode == ai_config.STRUCTURED_OUTPUT_MODE_STRICT:
            response = client.chat.completions.parse(
                model=model_config.model_name,
                messages=messages,
                response_format=schema_model,
            )
            choices = getattr(response, "choices", None) or []
            if not choices:
                raise StructuredOutputIssue("empty")
            message = _check_chat_choice(choices[0])
            parsed = getattr(message, "parsed", None)
            if parsed is None:
                raise StructuredOutputIssue("empty")
            return schema_model.model_validate(parsed)

        response = client.chat.completions.create(
            model=model_config.model_name,
            messages=messages,
            response_format={"type": "json_object"},
            stream=False,
        )
        choices = getattr(response, "choices", None) or []
        if not choices:
            raise StructuredOutputIssue("empty")
        message = _check_chat_choice(choices[0])
        content = _normalize_compat_json(getattr(message, "content", None))
        try:
            return schema_model.model_validate_json(content)
        except (ValidationError, json.JSONDecodeError, TypeError, ValueError) as exc:
            raise _validation_issue(exc) from exc

    if mode == ai_config.STRUCTURED_OUTPUT_MODE_STRICT:
        response = client.responses.parse(
            model=model_config.model_name,
            input=messages,
            text_format=schema_model,
            store=False,
        )
        _check_responses_status(response)
        parsed = getattr(response, "output_parsed", None)
        if parsed is None:
            raise StructuredOutputIssue("empty")
        return schema_model.model_validate(parsed)

    response = client.responses.create(
        model=model_config.model_name,
        input=messages,
        text={"format": {"type": "json_object"}},
        store=False,
    )
    _check_responses_status(response)
    content = _normalize_compat_json(getattr(response, "output_text", None))
    try:
        return schema_model.model_validate_json(content)
    except (ValidationError, json.JSONDecodeError, TypeError, ValueError) as exc:
        raise _validation_issue(exc) from exc


def _correction_instruction(issue):
    if issue.kind == "schema" and issue.paths:
        details = "；".join(issue.paths)
        return (
            "上次返回未通过结构校验。仅修正以下字段路径与错误类型："
            f"{details}。请重新生成完整 JSON，不得解释、补充 Markdown 或省略字段。"
        )
    if issue.kind == "invalid_json":
        return "上次返回不是合法 JSON。请重新生成完整 JSON，不得输出 Markdown 或解释。"
    if issue.kind == "truncated":
        return "上次结构化结果被截断。请压缩文字并重新生成完整 JSON，不得省略字段。"
    return "上次没有返回结构化内容。请重新生成完整 JSON，不得输出 Markdown 或解释。"


def _with_correction(messages, issue):
    corrected = [dict(item) for item in messages]
    instruction = _correction_instruction(issue)
    for item in corrected:
        if item.get("role") == "system" and isinstance(item.get("content"), str):
            item["content"] = f"{item['content']}\n\n结构纠错要求：{instruction}"
            return corrected
    return [{"role": "system", "content": instruction}, *corrected]


def _final_issue_message(issue, *, repaired):
    suffix = "，系统已自动纠错 1 次仍未通过" if repaired else ""
    if issue.kind == "refusal":
        return "模型拒绝生成结构化结果，请由 HR 人工处理"
    if issue.kind == "truncated":
        return f"模型输出被截断{suffix}"
    if issue.kind == "invalid_json":
        return f"AI 返回的 JSON 无法解析{suffix}"
    if issue.kind == "schema":
        fields = [item.split(":", 1)[0] for item in issue.paths]
        field_text = f"（{', '.join(fields)}）" if fields else ""
        return f"AI 返回内容存在缺失或错误字段{field_text}{suffix}"
    return f"模型未返回结构化内容{suffix}"


def call_structured_model(
    *,
    client,
    model_config,
    runtime_config,
    messages,
    schema_model,
    processing_run_id=None,
    cancelled=None,
    operation="AI structured output",
):
    """按独立的传输和结构预算调用模型，避免重试次数相乘。"""
    mode = ai_config.get_structured_output_mode(api_style=model_config.api_style)
    transport_remaining = max(0, runtime_config.retry_count)
    repair_remaining = 1
    correction_issue = None
    repaired = False
    attempt = 0

    while True:
        attempt += 1
        try:
            slot = concurrency.acquire_slot(
                model_config,
                runtime_config,
                run_id=processing_run_id,
                cancelled=cancelled,
            )
        except concurrency.AIConcurrencyError as exc:
            raise AIServiceError(
                "ai_limiter_unavailable", "AI 并发控制器不可用或任务已取消"
            ) from exc

        call_messages = (
            _with_correction(messages, correction_issue)
            if correction_issue is not None
            else messages
        )
        caught_issue = None
        try:
            output = _execute_once(
                client,
                model_config,
                mode,
                call_messages,
                schema_model,
            )
            release_model_slot(slot, "success")
            return output
        except StructuredOutputIssue as exc:
            caught_issue = exc
            release_model_slot(slot, "success")
        except Exception as exc:  # SDK 异常类型随供应商和版本变化
            caught_issue = _issue_from_exception(exc)
            if caught_issue is not None:
                release_model_slot(slot, "success")
            else:
                code, message = safe_model_error(exc)
                failure_kind, retryable, retry_after = model_failure_kind(exc)
                release_model_slot(slot, failure_kind, retry_after=retry_after)
                if failure_kind == "rate_limit":
                    concurrency.record_rate_limit(processing_run_id)
                logger.warning(
                    "%s transport failure model=%s api_style=%s mode=%s attempt=%s code=%s error_type=%s",
                    operation,
                    model_config.model_name,
                    model_config.api_style,
                    mode,
                    attempt,
                    code,
                    type(exc).__name__,
                )
                if not retryable or transport_remaining <= 0:
                    raise AIServiceError(code, message) from exc
                transport_remaining -= 1
                concurrency.record_retry(processing_run_id)
                delay = concurrency.retry_delay(
                    runtime_config,
                    max(0, runtime_config.retry_count - transport_remaining - 1),
                    retry_after=retry_after,
                )
                if delay:
                    time.sleep(delay)
                continue

        issue = caught_issue
        logger.warning(
            "%s validation failure model=%s api_style=%s mode=%s attempt=%s kind=%s paths=%s",
            operation,
            model_config.model_name,
            model_config.api_style,
            mode,
            attempt,
            issue.kind,
            ",".join(issue.paths),
        )
        if issue.kind != "refusal" and repair_remaining > 0:
            repair_remaining -= 1
            repaired = True
            correction_issue = issue
            concurrency.record_retry(processing_run_id)
            continue
        raise AIServiceError(
            "ai_invalid_output",
            _final_issue_message(issue, repaired=repaired),
        )


def _probe_error(exc):
    if isinstance(exc, StructuredOutputIssue):
        return AIServiceError(
            "ai_invalid_output", _final_issue_message(exc, repaired=False)
        )
    issue = _issue_from_exception(exc)
    if issue is not None:
        return AIServiceError(
            "ai_invalid_output", _final_issue_message(issue, repaired=False)
        )
    code, message = safe_model_error(exc)
    return AIServiceError(code, message)


def probe_structured_output_mode(
    *, client, model_config, messages, schema_model
):
    """以真实业务 Schema 探测严格输出；只对明确格式不支持做兼容降级。"""
    try:
        _execute_once(
            client,
            model_config,
            ai_config.STRUCTURED_OUTPUT_MODE_STRICT,
            messages,
            schema_model,
        )
        return ai_config.STRUCTURED_OUTPUT_MODE_STRICT
    except Exception as exc:
        status_code = getattr(exc, "status_code", None) or getattr(
            getattr(exc, "response", None), "status_code", None
        )
        if status_code not in {400, 422}:
            raise _probe_error(exc) from exc

    try:
        _execute_once(
            client,
            model_config,
            ai_config.STRUCTURED_OUTPUT_MODE_JSON_COMPAT,
            messages,
            schema_model,
        )
        return ai_config.STRUCTURED_OUTPUT_MODE_JSON_COMPAT
    except Exception as exc:
        raise _probe_error(exc) from exc
