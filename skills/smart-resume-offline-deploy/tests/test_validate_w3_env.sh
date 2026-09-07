#!/usr/bin/env bash
set -Eeuo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECKER="${TEST_DIR}/../scripts/validate-w3-env.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/smart-resume-w3-env-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

write_env() {
  local target="$1"
  shift
  printf '%s\n' "$@" > "$target"
}

expect_success() {
  local name="$1"
  local env_file="$2"
  if ! ENV_FILE="$env_file" bash "$CHECKER" >/dev/null; then
    echo "失败：${name} 应校验成功"
    exit 1
  fi
}

expect_failure() {
  local name="$1"
  local env_file="$2"
  local expected="$3"
  local output_file="${TEST_ROOT}/error.log"
  if ENV_FILE="$env_file" bash "$CHECKER" >"$output_file" 2>&1; then
    echo "失败：${name} 应校验失败"
    exit 1
  fi
  grep -Fq "$expected" "$output_file" || {
    echo "失败：${name} 未返回预期错误"
    exit 1
  }
}

disabled_env="${TEST_ROOT}/disabled.env"
write_env "$disabled_env" "W3_OAUTH2_ENABLED=False"
expect_failure "W3 未启用" "$disabled_env" "W3_OAUTH2_ENABLED=True"

valid_env="${TEST_ROOT}/valid.env"
write_env "$valid_env" \
  "DJANGO_DEBUG=False" \
  "W3_OAUTH2_ENABLED=True" \
  "W3_OAUTH2_CLIENT_ID=client-id" \
  "W3_OAUTH2_CLIENT_SECRET=client-secret" \
  "W3_OAUTH2_AUTHORIZE_URL=https://w3.example.com/oauth/authorize" \
  "W3_OAUTH2_TOKEN_URL=https://w3.example.com/oauth/token" \
  "W3_OAUTH2_USERINFO_URL=https://w3.example.com/oauth/userinfo" \
  "W3_OAUTH2_REDIRECT_URI=https://resume.example.com/api/auth/w3/callback/" \
  "W3_OAUTH2_FRONTEND_CALLBACK_URL=/login" \
  "W3_OAUTH2_EMPLOYEE_NO_FIELD=employeeNumber" \
  "W3_OAUTH2_EMAIL_FIELD=email" \
  "W3_OAUTH2_CLIENT_AUTH_METHOD=client_secret_basic" \
  "W3_OAUTH2_USE_PKCE=True" \
  "W3_OAUTH2_TIMEOUT_SECONDS=10" \
  "W3_OAUTH2_TRANSACTION_TTL_SECONDS=300"
expect_success "完整 W3 配置" "$valid_env"

for required_key in \
  W3_OAUTH2_CLIENT_ID \
  W3_OAUTH2_AUTHORIZE_URL \
  W3_OAUTH2_TOKEN_URL \
  W3_OAUTH2_USERINFO_URL \
  W3_OAUTH2_REDIRECT_URI \
  W3_OAUTH2_EMPLOYEE_NO_FIELD \
  W3_OAUTH2_EMAIL_FIELD \
  W3_OAUTH2_TIMEOUT_SECONDS \
  W3_OAUTH2_TRANSACTION_TTL_SECONDS; do
  missing_required_env="${TEST_ROOT}/missing-${required_key}.env"
  sed "s#^${required_key}=.*#${required_key}=#" "$valid_env" > "$missing_required_env"
  expect_failure "缺少 ${required_key}" "$missing_required_env" "$required_key"
done

missing_auth_method_env="${TEST_ROOT}/missing-auth-method.env"
sed 's/W3_OAUTH2_CLIENT_AUTH_METHOD=.*/W3_OAUTH2_CLIENT_AUTH_METHOD=/' "$valid_env" > "$missing_auth_method_env"
expect_failure "缺少客户端认证方式" "$missing_auth_method_env" "W3_OAUTH2_CLIENT_AUTH_METHOD"

no_secret_env="${TEST_ROOT}/no-secret.env"
sed '/W3_OAUTH2_CLIENT_SECRET=/d; s/client_secret_basic/none/' "$valid_env" > "$no_secret_env"
expect_success "公开客户端不要求密钥" "$no_secret_env"

defaulted_env="${TEST_ROOT}/defaulted.env"
sed '/W3_OAUTH2_FRONTEND_CALLBACK_URL=/d; /W3_OAUTH2_USE_PKCE=/d; /DJANGO_DEBUG=/d' "$valid_env" > "$defaulted_env"
expect_success "安全默认项可省略" "$defaulted_env"

debug_env="${TEST_ROOT}/debug.env"
sed 's/DJANGO_DEBUG=False/DJANGO_DEBUG=True/' "$valid_env" > "$debug_env"
expect_failure "生产环境禁止 DEBUG" "$debug_env" "DJANGO_DEBUG=False"

missing_email_env="${TEST_ROOT}/missing-email.env"
sed 's/W3_OAUTH2_EMAIL_FIELD=.*/W3_OAUTH2_EMAIL_FIELD=/' "$valid_env" > "$missing_email_env"
expect_failure "缺少邮箱字段" "$missing_email_env" "W3_OAUTH2_EMAIL_FIELD"

query_redirect_env="${TEST_ROOT}/query-redirect.env"
sed 's#W3_OAUTH2_REDIRECT_URI=.*#W3_OAUTH2_REDIRECT_URI=https://resume.example.com/api/auth/w3/callback/?next=/login#' "$valid_env" > "$query_redirect_env"
expect_failure "redirect_uri 携带查询" "$query_redirect_env" "不得携带参数、查询或片段"

http_provider_env="${TEST_ROOT}/http-provider.env"
sed 's#W3_OAUTH2_TOKEN_URL=https://#W3_OAUTH2_TOKEN_URL=http://#' "$valid_env" > "$http_provider_env"
expect_failure "提供方端点不是 HTTPS" "$http_provider_env" "W3_OAUTH2_TOKEN_URL 必须使用 HTTPS"

missing_secret_env="${TEST_ROOT}/missing-secret.env"
sed 's/W3_OAUTH2_CLIENT_SECRET=.*/W3_OAUTH2_CLIENT_SECRET=/' "$valid_env" > "$missing_secret_env"
expect_failure "机密客户端缺少密钥" "$missing_secret_env" "W3_OAUTH2_CLIENT_SECRET"

invalid_bool_env="${TEST_ROOT}/invalid-bool.env"
sed 's/W3_OAUTH2_USE_PKCE=True/W3_OAUTH2_USE_PKCE=maybe/' "$valid_env" > "$invalid_bool_env"
expect_failure "布尔值无效" "$invalid_bool_env" "W3_OAUTH2_USE_PKCE"

fixed_query_env="${TEST_ROOT}/fixed-query.env"
sed 's#W3_OAUTH2_FRONTEND_CALLBACK_URL=/login#W3_OAUTH2_FRONTEND_CALLBACK_URL=/login?tenant=campus#' "$valid_env" > "$fixed_query_env"
expect_success "前端回跳固定查询参数" "$fixed_query_env"

reserved_query_env="${TEST_ROOT}/reserved-query.env"
sed 's#W3_OAUTH2_FRONTEND_CALLBACK_URL=/login#W3_OAUTH2_FRONTEND_CALLBACK_URL=/login?oauth2=forged#' "$valid_env" > "$reserved_query_env"
expect_failure "前端回跳预置保留参数" "$reserved_query_env" "不得预置 oauth2/oauth2_error"

echo "W3 OAuth2 部署环境校验测试通过。"
