"""AI Agent 配置抽象。

模型连接只从管理员在系统设置保存的 Config 记录读取。内网模型共用一套连接
字段，不区分服务商或 Profile，也不从环境变量或部署文件读取连接信息。
"""
import base64
import hashlib
import json
from dataclasses import dataclass
from urllib.parse import urlparse

from django.conf import settings
from django.db import transaction
from django.utils import timezone

PUBLIC_AI_CONFIG_REGISTRY = {
    "ai_dispatch_threshold": {
        "label": "AI 自动下发阈值",
        "description": "置信度大于等于该值时，AI 建议可进入待下发。",
        "section": "runtime",
        "value_type": "number",
        "default": 0.75,
        "min": 0,
        "max": 1,
    },
    "ai_review_threshold": {
        "label": "AI 人工复核阈值",
        "description": "置信度大于等于该值且低于自动下发阈值时，进入 HR 复核。",
        "section": "runtime",
        "value_type": "number",
        "default": 0.5,
        "min": 0,
        "max": 1,
    },
    "ai_timeout_seconds": {
        "label": "AI 超时时间",
        "description": "单次 AI 调用超时时间，单位秒。",
        "section": "runtime",
        "value_type": "integer",
        "default": 60,
        "min": 5,
        "max": 600,
    },
    "ai_concurrency": {
        "label": "AI 并发上限",
        "description": "所有后台任务共享的模型调用并发上限；系统会根据限流情况自动升降。",
        "section": "runtime",
        "value_type": "integer",
        "default": 8,
        "min": 1,
        "max": 20,
    },
    "ai_retry_count": {
        "label": "AI 重试次数",
        "description": "AI 失败后允许自动重试的次数。",
        "section": "runtime",
        "value_type": "integer",
        "default": 1,
        "min": 0,
        "max": 5,
    },
    "ai_retry_backoff_seconds": {
        "label": "AI 重试退避",
        "description": "AI 重试间隔，单位秒。",
        "section": "runtime",
        "value_type": "integer",
        "default": 10,
        "min": 0,
        "max": 300,
    },
    "ai_special_route_enabled": {
        "label": "AI 专项强制分配",
        "description": "命中 AI 专项人才条件后，自动分流至配置的三级部门。",
        "section": "special_route",
        "value_type": "boolean",
        "default": False,
    },
    "ai_special_route_threshold": {
        "label": "AI 专项分流阈值",
        "description": "专项置信度必须严格大于该值才触发；默认 0.90。",
        "section": "special_route",
        "value_type": "number",
        "default": 0.9,
        "min": 0.9,
        "max": 1,
    },
    "ai_special_route_secondary_department_id": {
        "label": "AI 专项父级二级部门",
        "description": "专项分流首先进入的二级部门；0 表示未配置。",
        "section": "special_route",
        "value_type": "integer",
        "default": 0,
        "min": 0,
    },
    "ai_special_route_tertiary_department_id": {
        "label": "AI 专项目标三级部门",
        "description": "专项分流的固定三级部门；0 表示未配置。",
        "section": "special_route",
        "value_type": "integer",
        "default": 0,
        "min": 0,
    },
}


AI_CONNECTION_CONFIG_KEYS = {
    "api_style": "ai_connection_api_style",
    "model_name": "ai_connection_model_name",
    "base_url": "ai_connection_base_url",
    "api_key": "ai_connection_api_key",
}

AI_CONNECTION_TEST_FINGERPRINT_KEY = "ai_connection_test_fingerprint"
AI_CONNECTION_TESTED_AT_KEY = "ai_connection_tested_at"
AI_CONNECTION_STRUCTURED_OUTPUT_MODE_KEY = "ai_connection_structured_output_mode"

STRUCTURED_OUTPUT_MODE_STRICT = "strict_schema"
STRUCTURED_OUTPUT_MODE_JSON_COMPAT = "json_compat"
STRUCTURED_OUTPUT_MODE_LEGACY = "legacy_compat"
STRUCTURED_OUTPUT_MODES = {
    STRUCTURED_OUTPUT_MODE_STRICT,
    STRUCTURED_OUTPUT_MODE_JSON_COMPAT,
}


@dataclass(frozen=True)
class AIModelConfig:
    api_style: str
    model_name: str
    api_key: str
    base_url: str


@dataclass(frozen=True)
class AIRuntimeConfig:
    dispatch_threshold: float
    review_threshold: float
    timeout_seconds: int
    concurrency: int
    retry_count: int
    retry_backoff_seconds: int


@dataclass(frozen=True)
class AISpecialRouteConfig:
    enabled: bool
    threshold: float
    secondary_department_id: int
    tertiary_department_id: int

    def snapshot(self):
        payload = {
            "enabled": self.enabled,
            "threshold": self.threshold,
            "secondary_department_id": self.secondary_department_id,
            "tertiary_department_id": self.tertiary_department_id,
        }
        payload["version"] = hashlib.sha256(
            json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8")
        ).hexdigest()[:16]
        return payload


def _connection_value(name):
    from apps.core import models as m

    config = m.Config.objects.filter(key=AI_CONNECTION_CONFIG_KEYS[name]).first()
    if not config:
        return None
    value = config.value
    return value.get("value") if isinstance(value, dict) else value


def _secret_fernet():
    """用 Django SECRET_KEY 派生数据库密钥的加密器，不将 API Key 明文落库。"""
    try:
        from cryptography.fernet import Fernet
    except ImportError as exc:
        raise RuntimeError("服务端缺少 cryptography，无法安全保存模型 API Key") from exc
    digest = hashlib.sha256(
        f"{settings.SECRET_KEY}:smart-resume-filter:ai-connection:v1".encode("utf-8")
    ).digest()
    return Fernet(base64.urlsafe_b64encode(digest))


def encrypt_api_key(value):
    return _secret_fernet().encrypt(value.encode("utf-8")).decode("utf-8")


def decrypt_api_key(value):
    if not value:
        return ""
    try:
        return _secret_fernet().decrypt(value.encode("utf-8")).decode("utf-8")
    except Exception as exc:  # noqa: BLE001 - 密钥轮换或数据损坏均不得泄漏原文
        raise ValueError("已保存的模型 API Key 无法解密，请由管理员重新保存") from exc


@transaction.atomic
def save_ai_connection_config(payload):
    """保存管理员维护的连接参数；API Key 仅接受写入并以密文保存。"""
    if payload["api_style"] not in {"responses", "chat_json"}:
        raise ValueError("API 风格必须是 responses 或 chat_json")
    if not isinstance(payload["model_name"], str) or not payload["model_name"].strip():
        raise ValueError("模型名称不能为空")
    normalized_base_url = validate_ai_base_url(payload["base_url"])
    api_key_value = payload.get("api_key", "")
    if api_key_value is not None and not isinstance(api_key_value, str):
        raise ValueError("API Key 格式不正确")
    api_key_value = (api_key_value or "").strip()
    from apps.core import models as m

    m.Config.objects.filter(key="ai_enabled").delete()
    previous_base_url = _connection_value("base_url")
    base_url_unchanged = _base_urls_match(previous_base_url, normalized_base_url)
    for field in ["api_style", "model_name", "base_url"]:
        value = normalized_base_url if field == "base_url" else payload[field].strip()
        m.Config.objects.update_or_create(
            key=AI_CONNECTION_CONFIG_KEYS[field], defaults={"value": value}
        )
    # 项目尚未上线，不保留已废弃的服务商/Profile 配置。
    m.Config.objects.filter(key="ai_connection_profile").delete()
    if payload.get("clear_api_key"):
        m.Config.objects.filter(key=AI_CONNECTION_CONFIG_KEYS["api_key"]).delete()
    elif api_key_value:
        m.Config.objects.update_or_create(
            key=AI_CONNECTION_CONFIG_KEYS["api_key"],
            defaults={"value": encrypt_api_key(api_key_value)},
        )
    elif not base_url_unchanged:
        # 访问令牌只绑定原 Base URL，不得在地址变化后静默转发到新服务。
        m.Config.objects.filter(key=AI_CONNECTION_CONFIG_KEYS["api_key"]).delete()
    invalidate_ai_connection_test()


def model_config_fingerprint(config):
    """为同一次读取的完整连接计算指纹；不得通过接口或日志输出。"""
    payload = {
        "api_style": config.api_style,
        "model_name": config.model_name,
        "base_url": config.base_url,
        "api_key_hash": hashlib.sha256(config.api_key.encode("utf-8")).hexdigest(),
    }
    return hashlib.sha256(
        json.dumps(payload, ensure_ascii=False, sort_keys=True).encode("utf-8")
    ).hexdigest()


def _connection_fingerprint():
    return model_config_fingerprint(get_ai_model_config())


def invalidate_ai_connection_test():
    from apps.core import models as m

    m.Config.objects.filter(
        key__in=[
            AI_CONNECTION_TEST_FINGERPRINT_KEY,
            AI_CONNECTION_TESTED_AT_KEY,
            AI_CONNECTION_STRUCTURED_OUTPUT_MODE_KEY,
        ]
    ).delete()


def mark_ai_connection_tested(*, structured_output_mode=None):
    from apps.core import models as m

    if structured_output_mode is not None:
        if structured_output_mode not in STRUCTURED_OUTPUT_MODES:
            raise ValueError("结构化输出模式不正确")
        m.Config.objects.update_or_create(
            key=AI_CONNECTION_STRUCTURED_OUTPUT_MODE_KEY,
            defaults={"value": structured_output_mode},
        )
    fingerprint = _connection_fingerprint()
    tested_at = timezone.now().isoformat()
    m.Config.objects.update_or_create(
        key=AI_CONNECTION_TEST_FINGERPRINT_KEY,
        defaults={"value": fingerprint},
    )
    m.Config.objects.update_or_create(
        key=AI_CONNECTION_TESTED_AT_KEY,
        defaults={"value": tested_at},
    )
    return tested_at


def get_structured_output_mode(*, api_style=None):
    """返回当前连接的运行模式；旧连接沿用升级前对应 API 风格。"""
    stored = _connection_value_by_key(AI_CONNECTION_STRUCTURED_OUTPUT_MODE_KEY)
    if stored in STRUCTURED_OUTPUT_MODES:
        return stored
    if api_style is None:
        api_style = _connection_value("api_style") or "chat_json"
    # 旧版 Responses 已使用 SDK typed parse，Chat 则使用 JSON Object。
    if api_style == "responses":
        return STRUCTURED_OUTPUT_MODE_STRICT
    return STRUCTURED_OUTPUT_MODE_JSON_COMPAT


def get_structured_output_status():
    stored = _connection_value_by_key(AI_CONNECTION_STRUCTURED_OUTPUT_MODE_KEY)
    if stored in STRUCTURED_OUTPUT_MODES:
        return stored
    return STRUCTURED_OUTPUT_MODE_LEGACY


def is_ai_connection_tested():
    from apps.core import models as m

    stored = m.Config.objects.filter(
        key=AI_CONNECTION_TEST_FINGERPRINT_KEY
    ).values_list("value", flat=True).first()
    if not stored:
        return False
    try:
        return stored == _connection_fingerprint()
    except (RuntimeError, ValueError):
        return False


def current_ai_connection_fingerprint():
    """供后端发布校验使用；不得通过 API、日志或异常返回。"""
    return _connection_fingerprint()


def get_ai_connection_status():
    stored_key = _connection_value("api_key")
    tested_at = _connection_value_by_key(AI_CONNECTION_TESTED_AT_KEY) or ""
    return {
        "api_style": _connection_value("api_style") or "chat_json",
        "model_name": _connection_value("model_name") or "",
        "base_url": _connection_value("base_url") or "",
        "api_key_configured": bool(stored_key),
        "api_key_source": "system_config" if stored_key else "not_configured",
        "test_passed": is_ai_connection_tested(),
        "tested_at": tested_at,
        "structured_output_mode": get_structured_output_status(),
    }


def is_ai_available():
    """当前连接已通过与完整配置指纹一致的真实测试时，AI 模式可用。"""
    return (settings.DEBUG and settings.AGENT_KERNEL_ALLOW_MOCK) or is_ai_connection_tested()


def get_ai_model_config(*, require_tested=False):
    if settings.DEBUG and settings.AGENT_KERNEL_ALLOW_MOCK:
        return AIModelConfig(api_style="chat_json",model_name="contract-mock",base_url="http://127.0.0.1:1",
            api_key="")
    from apps.core import models as m

    # 保存连接会在一个事务内更新多行；一次查询避免读到新旧连接的混合值。
    stored = dict(m.Config.objects.filter(
        key__in=[*AI_CONNECTION_CONFIG_KEYS.values(), AI_CONNECTION_TEST_FINGERPRINT_KEY]
    ).values_list("key", "value"))

    def value(name):
        item = stored.get(AI_CONNECTION_CONFIG_KEYS[name])
        return item.get("value") if isinstance(item, dict) else item

    api_style = value("api_style")
    model_name = value("model_name")
    base_url = value("base_url")
    if not all(isinstance(value, str) and value.strip() for value in [api_style, model_name, base_url]):
        raise ValueError("AI 模型连接尚未完成配置，请由管理员在系统设置中保存")
    if api_style not in ["responses", "chat_json"]:
        raise ValueError("AI 模型连接的 api_style 必须是 responses 或 chat_json")
    validate_ai_base_url(base_url)
    saved_api_key = value("api_key")
    config = AIModelConfig(
        api_style=api_style,
        model_name=model_name,
        api_key=decrypt_api_key(saved_api_key) if saved_api_key else "",
        base_url=base_url,
    )
    if require_tested and stored.get(AI_CONNECTION_TEST_FINGERPRINT_KEY) != model_config_fingerprint(config):
        from apps.pipeline.errors import AIServiceError
        raise AIServiceError("ai_not_configured", "当前模型连接尚未测试通过，请在模型连接页面完成测试后重试")
    return config


def validate_ai_base_url(value):
    if not isinstance(value, str) or not value.strip():
        raise ValueError("Base URL 不能为空")
    parsed = urlparse(value.strip())
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ValueError("Base URL 必须是完整的 http(s) 地址")
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("Base URL 不能包含账号、密码、查询参数或锚点")
    return value.strip().rstrip("/")


def _base_urls_match(left, right):
    try:
        return validate_ai_base_url(left) == validate_ai_base_url(right)
    except ValueError:
        return False


def get_ai_discovery_config(*, base_url, api_key=""):
    """返回模型发现所需连接参数；未提交新令牌时复用已保存令牌。"""
    normalized_base_url = validate_ai_base_url(base_url)
    if api_key is not None and not isinstance(api_key, str):
        raise ValueError("API Key 格式不正确")
    submitted_api_key = (api_key or "").strip()
    saved_api_key = _connection_value("api_key")
    saved_base_url = _connection_value("base_url")
    can_reuse_saved_key = _base_urls_match(saved_base_url, normalized_base_url)
    effective_api_key = submitted_api_key
    if not effective_api_key and saved_api_key and can_reuse_saved_key:
        effective_api_key = decrypt_api_key(saved_api_key)
    return normalized_base_url, effective_api_key


def _config_value(key, default):
    from apps.core import models as m

    config = m.Config.objects.filter(key=key).first()
    value = config.value if config else default
    if isinstance(value, dict):
        return value.get("value", default)
    return value


def _connection_value_by_key(key):
    from apps.core import models as m

    config = m.Config.objects.filter(key=key).first()
    if not config:
        return None
    value = config.value
    return value.get("value") if isinstance(value, dict) else value


def _config_float(key):
    default = PUBLIC_AI_CONFIG_REGISTRY[key]["default"]
    try:
        return float(_config_value(key, default))
    except (TypeError, ValueError):
        return default


def _config_int(key):
    default = PUBLIC_AI_CONFIG_REGISTRY[key]["default"]
    try:
        return int(_config_value(key, default))
    except (TypeError, ValueError):
        return default


def get_ai_runtime_config():
    return AIRuntimeConfig(
        dispatch_threshold=_config_float("ai_dispatch_threshold"),
        review_threshold=_config_float("ai_review_threshold"),
        timeout_seconds=_config_int("ai_timeout_seconds"),
        concurrency=_config_int("ai_concurrency"),
        retry_count=_config_int("ai_retry_count"),
        retry_backoff_seconds=_config_int("ai_retry_backoff_seconds"),
    )


def get_ai_special_route_config(*, overrides=None, validate=False):
    """读取专项部门分流配置，并在启用时校验部门层级关系。"""
    overrides = overrides or {}

    def value(key):
        if key in overrides:
            return overrides[key]
        return _config_value(key, PUBLIC_AI_CONFIG_REGISTRY[key]["default"])

    config = AISpecialRouteConfig(
        enabled=bool(value("ai_special_route_enabled")),
        threshold=float(value("ai_special_route_threshold")),
        secondary_department_id=int(
            value("ai_special_route_secondary_department_id") or 0
        ),
        tertiary_department_id=int(
            value("ai_special_route_tertiary_department_id") or 0
        ),
    )
    if not validate or not config.enabled:
        return config

    from apps.core import models as m

    secondary = m.Department.objects.filter(
        pk=config.secondary_department_id, level=2
    ).first()
    tertiary = m.Department.objects.select_related("parent").filter(
        pk=config.tertiary_department_id, level=3
    ).first()
    if not secondary:
        raise ValueError("AI 专项分流的二级部门不存在或层级不正确")
    if not tertiary:
        raise ValueError("AI 专项分流的三级部门不存在或层级不正确")
    if tertiary.parent_id != secondary.id:
        raise ValueError("AI 专项分流的二级、三级部门不属于同一上下级关系")
    return config


def get_public_ai_config_item(key):
    if key not in PUBLIC_AI_CONFIG_REGISTRY:
        raise ValueError("未知 AI 配置项")
    meta = PUBLIC_AI_CONFIG_REGISTRY[key]
    return {
        "key": key,
        "value": _config_value(key, meta["default"]),
        **meta,
    }


def list_public_ai_config_items():
    return [get_public_ai_config_item(key) for key in PUBLIC_AI_CONFIG_REGISTRY]


@transaction.atomic
def save_public_ai_config(key, value):
    """由 AI 模型连接权限维护运行参数和专项配置。"""
    if key not in PUBLIC_AI_CONFIG_REGISTRY:
        raise ValueError("未知 AI 配置项")
    meta = PUBLIC_AI_CONFIG_REGISTRY[key]
    value_type = meta["value_type"]
    if value_type == "boolean":
        if not isinstance(value, bool):
            raise ValueError("配置值必须是布尔值")
    elif value_type in {"number", "integer"}:
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ValueError("配置值类型不正确")
        if value_type == "integer" and not isinstance(value, int):
            raise ValueError("配置值必须是整数")
        if "min" in meta and value < meta["min"] or "max" in meta and value > meta["max"]:
            raise ValueError(f"配置值必须在 {meta.get('min')} 到 {meta.get('max')} 之间")

    if key == "ai_review_threshold" and float(value) > _config_float("ai_dispatch_threshold"):
        raise ValueError("人工复核阈值不能高于自动下发阈值")
    if key == "ai_dispatch_threshold" and float(value) < _config_float("ai_review_threshold"):
        raise ValueError("自动下发阈值不能低于人工复核阈值")
    if key.startswith("ai_special_route_"):
        get_ai_special_route_config(overrides={key: value}, validate=True)

    from apps.core import models as m

    m.Config.objects.update_or_create(key=key, defaults={"value": value})
    return get_public_ai_config_item(key)
