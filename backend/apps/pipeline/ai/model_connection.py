"""平台模型连接探测与院校补全的客户端；不执行简历分析。"""
from __future__ import annotations
import atexit
import hashlib
import logging
import threading
import httpx
from apps.pipeline import ai_config
from .schemas import ConnectionProbeOutput
from .structured_output import AIServiceError, probe_structured_output_mode, safe_model_error as _safe_model_error

logger = logging.getLogger(__name__)
_CLIENT_CACHE = {}
_CLIENT_CACHE_LOCK = threading.Lock()

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
    """使用独立最小 Schema 验证连接并探测严格/兼容输出能力。"""
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
            schema_model=ConnectionProbeOutput,
            messages=[
                {
                    "role": "system",
                    "content": "这是结构化能力测试，只返回符合指定 Schema 的 JSON。",
                },
                {
                    "role": "user",
                    "content": (
                        '返回 {"ok": true}。'
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
