"""W3 OAuth2 Authorization Code 接入的提供方无关实现。"""

from __future__ import annotations

import base64
import hashlib
import secrets
from dataclasses import dataclass
from urllib.parse import parse_qsl, urlencode, urlparse, urlunparse

import httpx
from django.conf import settings
from django.core.exceptions import ValidationError
from django.core.validators import validate_email


class OAuth2ConfigurationError(ValueError):
    pass


class OAuth2ProtocolError(RuntimeError):
    def __init__(self, code):
        super().__init__(code)
        self.code = code


@dataclass(frozen=True)
class OAuth2Config:
    enabled: bool
    client_id: str
    client_secret: str
    authorize_url: str
    token_url: str
    userinfo_url: str
    redirect_uri: str
    frontend_callback_url: str
    scope: str
    employee_no_field: str
    email_field: str
    client_auth_method: str
    use_pkce: bool
    timeout_seconds: float
    transaction_ttl_seconds: int

    def require_ready(self):
        if not self.enabled:
            raise OAuth2ConfigurationError("W3 OAuth2 未启用")

        required = {
            "W3_OAUTH2_CLIENT_ID": self.client_id,
            "W3_OAUTH2_AUTHORIZE_URL": self.authorize_url,
            "W3_OAUTH2_TOKEN_URL": self.token_url,
            "W3_OAUTH2_USERINFO_URL": self.userinfo_url,
            "W3_OAUTH2_REDIRECT_URI": self.redirect_uri,
            "W3_OAUTH2_EMPLOYEE_NO_FIELD": self.employee_no_field,
            "W3_OAUTH2_EMAIL_FIELD": self.email_field,
        }
        if self.client_auth_method != "none":
            required["W3_OAUTH2_CLIENT_SECRET"] = self.client_secret
        missing = [key for key, value in required.items() if not value]
        if missing:
            raise OAuth2ConfigurationError(
                f"W3 OAuth2 缺少配置：{', '.join(missing)}"
            )

        if self.client_auth_method not in {
            "client_secret_basic",
            "client_secret_post",
            "none",
        }:
            raise OAuth2ConfigurationError("W3 OAuth2 客户端认证方式无效")
        if self.timeout_seconds <= 0 or self.transaction_ttl_seconds <= 0:
            raise OAuth2ConfigurationError("W3 OAuth2 超时配置必须为正数")

        for label, value in (
            ("授权地址", self.authorize_url),
            ("令牌地址", self.token_url),
            ("用户信息地址", self.userinfo_url),
            ("redirect_uri", self.redirect_uri),
        ):
            _validate_external_url(label, value)
        redirect = urlparse(self.redirect_uri)
        if (
            redirect.path != "/api/auth/w3/callback/"
            or redirect.params
            or redirect.query
            or redirect.fragment
        ):
            raise OAuth2ConfigurationError(
                "W3 OAuth2 redirect_uri 必须精确指向 /api/auth/w3/callback/"
            )
        _validate_frontend_callback_url(self.frontend_callback_url)


def get_config():
    return OAuth2Config(
        enabled=bool(getattr(settings, "W3_OAUTH2_ENABLED", False)),
        client_id=str(getattr(settings, "W3_OAUTH2_CLIENT_ID", "")).strip(),
        client_secret=str(getattr(settings, "W3_OAUTH2_CLIENT_SECRET", "")),
        authorize_url=str(getattr(settings, "W3_OAUTH2_AUTHORIZE_URL", "")).strip(),
        token_url=str(getattr(settings, "W3_OAUTH2_TOKEN_URL", "")).strip(),
        userinfo_url=str(getattr(settings, "W3_OAUTH2_USERINFO_URL", "")).strip(),
        redirect_uri=str(getattr(settings, "W3_OAUTH2_REDIRECT_URI", "")).strip(),
        frontend_callback_url=str(
            getattr(settings, "W3_OAUTH2_FRONTEND_CALLBACK_URL", "/login")
        ).strip(),
        scope=str(getattr(settings, "W3_OAUTH2_SCOPE", "")).strip(),
        employee_no_field=str(
            getattr(settings, "W3_OAUTH2_EMPLOYEE_NO_FIELD", "employeeNumber")
        ).strip(),
        email_field=str(getattr(settings, "W3_OAUTH2_EMAIL_FIELD", "email")).strip(),
        client_auth_method=str(
            getattr(
                settings,
                "W3_OAUTH2_CLIENT_AUTH_METHOD",
                "client_secret_basic",
            )
        ).strip(),
        use_pkce=bool(getattr(settings, "W3_OAUTH2_USE_PKCE", True)),
        timeout_seconds=float(getattr(settings, "W3_OAUTH2_TIMEOUT_SECONDS", 10)),
        transaction_ttl_seconds=int(
            getattr(settings, "W3_OAUTH2_TRANSACTION_TTL_SECONDS", 300)
        ),
    )


def create_authorization_request(config):
    config.require_ready()
    state = secrets.token_urlsafe(32)
    verifier = secrets.token_urlsafe(64) if config.use_pkce else ""
    params = {
        "response_type": "code",
        "client_id": config.client_id,
        "redirect_uri": config.redirect_uri,
        "state": state,
    }
    if config.scope:
        params["scope"] = config.scope
    if verifier:
        digest = hashlib.sha256(verifier.encode("ascii")).digest()
        params.update(
            {
                "code_challenge": base64.urlsafe_b64encode(digest)
                .rstrip(b"=")
                .decode("ascii"),
                "code_challenge_method": "S256",
            }
        )
    return add_query_params(config.authorize_url, params), state, verifier


def exchange_code(config, code, verifier=""):
    data = {
        "grant_type": "authorization_code",
        "code": code,
        "redirect_uri": config.redirect_uri,
    }
    auth = None
    if config.client_auth_method == "client_secret_basic":
        auth = (config.client_id, config.client_secret)
    elif config.client_auth_method == "client_secret_post":
        data.update(
            {
                "client_id": config.client_id,
                "client_secret": config.client_secret,
            }
        )
    else:
        data["client_id"] = config.client_id
    if config.use_pkce:
        if not verifier:
            raise OAuth2ProtocolError("state_invalid")
        data["code_verifier"] = verifier

    try:
        response = httpx.post(
            config.token_url,
            data=data,
            auth=auth,
            headers={"Accept": "application/json"},
            timeout=config.timeout_seconds,
        )
        response.raise_for_status()
        payload = response.json()
    except (httpx.HTTPError, ValueError, TypeError) as exc:
        raise OAuth2ProtocolError("token_exchange_failed") from exc
    access_token = payload.get("access_token") if isinstance(payload, dict) else None
    if not isinstance(access_token, str) or not access_token.strip():
        raise OAuth2ProtocolError("token_exchange_failed")
    return access_token.strip()


def fetch_userinfo(config, access_token):
    try:
        response = httpx.get(
            config.userinfo_url,
            params={
                "access_token": access_token,
                "scope": config.scope,
                "client_id": config.client_id,
            },
            headers={"Accept": "application/json"},
            timeout=config.timeout_seconds,
        )
        response.raise_for_status()
        payload = response.json()
    except (httpx.HTTPError, ValueError, TypeError) as exc:
        raise OAuth2ProtocolError("userinfo_failed") from exc
    if not isinstance(payload, dict):
        raise OAuth2ProtocolError("userinfo_failed")
    return payload


def extract_employee_no(payload, field_path):
    value = payload
    for part in field_path.split("."):
        if not part or not isinstance(value, dict) or part not in value:
            raise OAuth2ProtocolError("employee_no_missing")
        value = value[part]
    if isinstance(value, bool) or not isinstance(value, (str, int)):
        raise OAuth2ProtocolError("employee_no_missing")
    employee_no = str(value).strip()
    if not employee_no:
        raise OAuth2ProtocolError("employee_no_missing")
    return employee_no


def extract_email(payload, field_path):
    value = payload
    for part in field_path.split("."):
        if not part or not isinstance(value, dict) or part not in value:
            raise OAuth2ProtocolError("email_missing")
        value = value[part]
    if not isinstance(value, str):
        raise OAuth2ProtocolError("email_missing")
    email = value.strip().casefold()
    try:
        validate_email(email)
    except ValidationError as exc:
        raise OAuth2ProtocolError("email_missing") from exc
    return email


def add_query_params(url, params):
    parsed = urlparse(url)
    query = dict(parse_qsl(parsed.query, keep_blank_values=True))
    query.update({key: value for key, value in params.items() if value is not None})
    return urlunparse(parsed._replace(query=urlencode(query)))


def _validate_external_url(label, value):
    parsed = urlparse(value)
    if parsed.scheme == "https" and parsed.netloc:
        return
    if (
        settings.DEBUG
        and parsed.scheme == "http"
        and parsed.hostname in {"localhost", "127.0.0.1", "::1"}
    ):
        return
    raise OAuth2ConfigurationError(f"W3 OAuth2 {label} 必须使用 HTTPS")


def _validate_frontend_callback_url(value):
    parsed = urlparse(value)
    if (
        value.startswith("/")
        and not value.startswith("//")
        and not parsed.scheme
        and not parsed.netloc
        and parsed.path == "/login"
        and not parsed.params
        and not parsed.fragment
        and not {"oauth2", "oauth2_error"}.intersection(
            dict(parse_qsl(parsed.query, keep_blank_values=True))
        )
        and "\\" not in value
        and "\r" not in value
        and "\n" not in value
    ):
        return
    raise OAuth2ConfigurationError("W3 OAuth2 前端回跳地址必须指向本站 /login")
