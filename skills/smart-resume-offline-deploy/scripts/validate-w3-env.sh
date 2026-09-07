#!/usr/bin/env bash
set -Eeuo pipefail

ENV_FILE="${ENV_FILE:-.env}"

die() {
  printf 'W3 OAuth2 配置错误：%s\n' "$*" >&2
  exit 1
}

env_value() {
  local key="$1"
  sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n 1
}

normalized_bool() {
  local key="$1"
  local default_value="$2"
  local value
  value="$(env_value "$key")"
  [[ -n "$value" ]] || value="$default_value"
  value="$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]')"
  case "$value" in
    1|true|yes|on) printf 'true' ;;
    0|false|no|off) printf 'false' ;;
    *) die "${key} 只接受 True/False、1/0、yes/no 或 on/off" ;;
  esac
}

require_w3_value() {
  local key="$1"
  local value
  value="$(env_value "$key")"
  if [[ -z "$value" || "$value" == change-me* || "$value" == *"你的"* ]]; then
    die "请在 ${ENV_FILE} 设置有效的 ${key}"
  fi
}

validate_https_url() {
  local key="$1"
  local value rest authority
  value="$(env_value "$key")"
  [[ "$value" == https://* ]] || die "${key} 必须使用 HTTPS"
  rest="${value#https://}"
  authority="${rest%%/*}"
  [[ -n "$authority" ]] || die "${key} 缺少主机名"
  if [[ "$authority" == *[[:space:]]* || "$authority" == *'@'* || \
        "$authority" == *'?'* || "$authority" == *'#'* || \
        "$authority" == *';'* || "$authority" == *'\'* ]]; then
    die "${key} 的主机部分无效"
  fi
}

validate_field_path() {
  local key="$1"
  local value
  require_w3_value "$key"
  value="$(env_value "$key")"
  if [[ "$value" == .* || "$value" == *. || "$value" == *..* || \
        "$value" == *[[:space:]]* ]]; then
    die "${key} 必须是无空段的 UserInfo 点路径"
  fi
}

validate_positive_number() {
  local key="$1"
  local value
  require_w3_value "$key"
  value="$(env_value "$key")"
  awk -v value="$value" 'BEGIN {
    valid = value ~ /^([0-9]+([.][0-9]+)?|[.][0-9]+)$/ && value + 0 > 0
    exit valid ? 0 : 1
  }' || die "${key} 必须是正数"
}

validate_positive_integer() {
  local key="$1"
  local value
  require_w3_value "$key"
  value="$(env_value "$key")"
  [[ "$value" =~ ^[0-9]+$ ]] && (( 10#$value > 0 )) || \
    die "${key} 必须是正整数"
}

validate_frontend_callback() {
  local value query pair key
  value="$(env_value W3_OAUTH2_FRONTEND_CALLBACK_URL)"
  [[ -n "$value" ]] || value="/login"
  case "$value" in
    /login|/login\?*) ;;
    *) die "W3_OAUTH2_FRONTEND_CALLBACK_URL 必须指向本站 /login" ;;
  esac
  if [[ "$value" == *'#'* || "$value" == *';'* || "$value" == *'\'* || \
        "$value" == *$'\r'* || "$value" == *$'\n'* ]]; then
    die "W3_OAUTH2_FRONTEND_CALLBACK_URL 不得携带 params、fragment、反斜杠或换行"
  fi
  [[ "$value" == *'?'* ]] || return 0
  query="${value#*\?}"
  while IFS= read -r pair; do
    key="${pair%%=*}"
    if [[ "$key" == "oauth2" || "$key" == "oauth2_error" || "$key" == *'%'* ]]; then
      die "W3_OAUTH2_FRONTEND_CALLBACK_URL 不得预置 oauth2/oauth2_error 保留参数"
    fi
  done < <(printf '%s\n' "$query" | tr '&' '\n')
}

[[ -f "$ENV_FILE" ]] || die "缺少环境文件 ${ENV_FILE}"

if [[ "$(normalized_bool DJANGO_DEBUG False)" != "false" ]]; then
  die "生产部署必须设置 DJANGO_DEBUG=False"
fi

if [[ "$(normalized_bool W3_OAUTH2_ENABLED False)" != "true" ]]; then
  die "前端仅支持 W3 登录；部署前必须设置 W3_OAUTH2_ENABLED=True 并补齐 OAuth2 必填项"
fi

require_w3_value W3_OAUTH2_CLIENT_ID
require_w3_value W3_OAUTH2_AUTHORIZE_URL
require_w3_value W3_OAUTH2_TOKEN_URL
require_w3_value W3_OAUTH2_USERINFO_URL
require_w3_value W3_OAUTH2_REDIRECT_URI
require_w3_value W3_OAUTH2_CLIENT_AUTH_METHOD
validate_field_path W3_OAUTH2_EMPLOYEE_NO_FIELD
validate_field_path W3_OAUTH2_EMAIL_FIELD

validate_https_url W3_OAUTH2_AUTHORIZE_URL
validate_https_url W3_OAUTH2_TOKEN_URL
validate_https_url W3_OAUTH2_USERINFO_URL
validate_https_url W3_OAUTH2_REDIRECT_URI

redirect_uri="$(env_value W3_OAUTH2_REDIRECT_URI)"
redirect_rest="${redirect_uri#https://}"
[[ "$redirect_rest" == */* ]] || \
  die "W3_OAUTH2_REDIRECT_URI 必须精确指向 /api/auth/w3/callback/"
redirect_path="/${redirect_rest#*/}"
[[ "$redirect_path" == "/api/auth/w3/callback/" ]] || \
  die "W3_OAUTH2_REDIRECT_URI 必须精确指向 /api/auth/w3/callback/，且不得携带参数、查询或片段"

client_auth_method="$(env_value W3_OAUTH2_CLIENT_AUTH_METHOD)"
case "$client_auth_method" in
  client_secret_basic|client_secret_post)
    require_w3_value W3_OAUTH2_CLIENT_SECRET
    ;;
  none) ;;
  *) die "W3_OAUTH2_CLIENT_AUTH_METHOD 只接受 client_secret_basic、client_secret_post 或 none" ;;
esac

normalized_bool W3_OAUTH2_USE_PKCE True >/dev/null
validate_positive_number W3_OAUTH2_TIMEOUT_SECONDS
validate_positive_integer W3_OAUTH2_TRANSACTION_TTL_SECONDS
validate_frontend_callback

echo "生产 DEBUG 与 W3 OAuth2 登录配置校验通过（密钥未显示）。"
