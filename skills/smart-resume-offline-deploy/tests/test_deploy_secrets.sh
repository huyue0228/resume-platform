#!/usr/bin/env bash
set -Eeuo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SCRIPT="${TEST_DIR}/../scripts/deploy.sh"
ENV_TEMPLATE="${ENV_TEMPLATE:-${TEST_DIR}/../../../.env.example}"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/smart-resume-deploy-secrets-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

PACKAGE_ROOT="${TEST_ROOT}/package"
FAKE_BIN="${TEST_ROOT}/bin"
mkdir -p "$PACKAGE_ROOT" "$FAKE_BIN" "${TEST_ROOT}/backups"
touch "${PACKAGE_ROOT}/docker-compose.yml"

cat > "${FAKE_BIN}/docker" <<'EOF'
#!/usr/bin/env bash
set -eu

if [[ "${1:-}" == "compose" && "${2:-}" == "version" ]]; then
  exit 0
fi
if [[ "${1:-}" == "info" ]]; then
  exit 0
fi
if [[ "${1:-}" == "ps" && "${2:-}" == "-aq" ]]; then
  [[ -z "${FAKE_EXISTING_RESOURCES:-}" ]] || printf '%s\n' "existing-container"
  exit 0
fi
if [[ "${1:-}" == "volume" && "${2:-}" == "ls" ]]; then
  [[ -z "${FAKE_EXISTING_RESOURCES:-}" ]] || printf '%s\n' "existing-volume"
  exit 0
fi
if [[ "${1:-}" == "compose" && "$*" == *"config --services"* ]]; then
  printf 'agent-kernel\ndb\nredis\nbackend\nworker\nai-worker\nfrontend\nbackup-scheduler\n'
  exit 0
fi

echo "测试 Docker 替身收到未预期命令。" >&2
exit 1
EOF
chmod +x "${FAKE_BIN}/docker"

env_value() {
  local env_file="$1"
  local key="$2"
  sed -n "s/^${key}=//p" "$env_file" | tail -n 1
}

assert_random_secret() {
  local key="$1"
  local value="$2"
  [[ "$value" =~ ^[0-9a-f]{96}$ ]] || {
    echo "失败：${key} 未生成 96 位十六进制随机值。"
    exit 1
  }
}

run_deploy() {
  local input="$1"
  local output_file="$2"
  shift 2
  printf '%s\n' "$input" | env \
    PATH="${FAKE_BIN}:$PATH" \
    DEPLOY_ROOT="$PACKAGE_ROOT" \
    ENV_FILE=.env \
    "$@" \
    bash "$DEPLOY_SCRIPT" >"$output_file" 2>&1
}

cp "$ENV_TEMPLATE" "${PACKAGE_ROOT}/.env.example"
first_output="${TEST_ROOT}/first.log"
if ! run_deploy 1 "$first_output"; then
  echo "失败：首次部署密钥初始化脚本异常退出。"
  sed -n '1,120p' "$first_output"
  exit 1
fi

secret_values=()
for key in DJANGO_SECRET_KEY POSTGRES_PASSWORD RESTIC_PASSWORD USAGE_METRICS_TOKEN AGENT_KERNEL_TOKEN; do
  value="$(env_value "${PACKAGE_ROOT}/.env" "$key")"
  assert_random_secret "$key" "$value"
  grep -Fq "$value" "$first_output" && {
    echo "失败：首次部署输出泄露了 ${key}。"
    exit 1
  }
  for previous in "${secret_values[@]:-}"; do
    [[ "$value" != "$previous" ]] || {
      echo "失败：首次部署复用了随机密钥。"
      exit 1
    }
  done
  secret_values+=("$value")
done

env_mode="$(stat -f '%Lp' "${PACKAGE_ROOT}/.env" 2>/dev/null || stat -c '%a' "${PACKAGE_ROOT}/.env")"
[[ "$env_mode" == "600" ]] || {
  echo "失败：.env 权限应为 600，实际为 ${env_mode}。"
  exit 1
}

case "$(uname -m)" in
  x86_64|amd64) docker_platform="linux/amd64" ;;
  aarch64|arm64) docker_platform="linux/arm64" ;;
  *) echo "跳过升级场景：当前架构不受部署脚本支持。"; exit 0 ;;
esac

cat > "${PACKAGE_ROOT}/.env" <<EOF
DOCKER_PLATFORM=${docker_platform}
DJANGO_SECRET_KEY=existing-django-secret
DJANGO_DEBUG=False
DJANGO_ALLOWED_HOSTS=resume.example.com
POSTGRES_PASSWORD=existing-postgres-secret
RESTIC_PASSWORD=existing-restic-secret
BACKUP_TARGET_PATH=${TEST_ROOT}/backups
W3_OAUTH2_ENABLED=True
W3_OAUTH2_CLIENT_ID=client-id
W3_OAUTH2_CLIENT_SECRET=client-secret
W3_OAUTH2_AUTHORIZE_URL=https://w3.example.com/oauth/authorize
W3_OAUTH2_TOKEN_URL=https://w3.example.com/oauth/token
W3_OAUTH2_USERINFO_URL=https://w3.example.com/oauth/userinfo
W3_OAUTH2_REDIRECT_URI=https://resume.example.com/api/auth/w3/callback/
W3_OAUTH2_FRONTEND_CALLBACK_URL=/login
W3_OAUTH2_EMPLOYEE_NO_FIELD=employeeNumber
W3_OAUTH2_EMAIL_FIELD=email
W3_OAUTH2_CLIENT_AUTH_METHOD=client_secret_basic
W3_OAUTH2_USE_PKCE=True
W3_OAUTH2_TIMEOUT_SECONDS=10
W3_OAUTH2_TRANSACTION_TTL_SECONDS=300
EOF
chmod 600 "${PACKAGE_ROOT}/.env"

upgrade_output="${TEST_ROOT}/upgrade.log"
if ! run_deploy 2 "$upgrade_output" FAKE_EXISTING_RESOURCES=1; then
  echo "失败：旧部署补齐监控密钥时异常退出。"
  sed -n '1,120p' "$upgrade_output"
  exit 1
fi
usage_token="$(env_value "${PACKAGE_ROOT}/.env" USAGE_METRICS_TOKEN)"
assert_random_secret USAGE_METRICS_TOKEN "$usage_token"
kernel_token="$(env_value "${PACKAGE_ROOT}/.env" AGENT_KERNEL_TOKEN)"
assert_random_secret AGENT_KERNEL_TOKEN "$kernel_token"
for existing_pair in \
  "DJANGO_SECRET_KEY=existing-django-secret" \
  "POSTGRES_PASSWORD=existing-postgres-secret" \
  "RESTIC_PASSWORD=existing-restic-secret"; do
  existing_key="${existing_pair%%=*}"
  existing_value="${existing_pair#*=}"
  [[ "$(env_value "${PACKAGE_ROOT}/.env" "$existing_key")" == "$existing_value" ]] || {
    echo "失败：旧部署的 ${existing_key} 被修改。"
    exit 1
  }
done

repeat_output="${TEST_ROOT}/repeat.log"
if ! run_deploy 2 "$repeat_output" FAKE_EXISTING_RESOURCES=1; then
  echo "失败：已有监控密钥的重复部署检查异常退出。"
  sed -n '1,120p' "$repeat_output"
  exit 1
fi
[[ "$(env_value "${PACKAGE_ROOT}/.env" USAGE_METRICS_TOKEN)" == "$usage_token" ]] || {
  echo "失败：已有 USAGE_METRICS_TOKEN 被无故轮换。"
  exit 1
}
[[ "$(env_value "${PACKAGE_ROOT}/.env" AGENT_KERNEL_TOKEN)" == "$kernel_token" ]] || {
  echo "失败：已有 AGENT_KERNEL_TOKEN 被无故轮换。"
  exit 1
}

sed 's/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=auto-generate-on-first-deploy/' \
  "${PACKAGE_ROOT}/.env" > "${PACKAGE_ROOT}/.env.invalid"
mv "${PACKAGE_ROOT}/.env.invalid" "${PACKAGE_ROOT}/.env"
chmod 600 "${PACKAGE_ROOT}/.env"
blocked_output="${TEST_ROOT}/blocked.log"
if run_deploy 2 "$blocked_output" FAKE_EXISTING_RESOURCES=1; then
  echo "失败：已有资源时不应替换 PostgreSQL 密钥占位值。"
  exit 1
fi
grep -Fq "请恢复原 .env" "$blocked_output" || {
  echo "失败：已有资源缺少原密钥时未返回恢复提示。"
  exit 1
}
[[ "$(env_value "${PACKAGE_ROOT}/.env" USAGE_METRICS_TOKEN)" == "$usage_token" ]] || {
  echo "失败：阻断路径修改了已有 USAGE_METRICS_TOKEN。"
  exit 1
}
[[ "$(env_value "${PACKAGE_ROOT}/.env" AGENT_KERNEL_TOKEN)" == "$kernel_token" ]] || {
  echo "失败：阻断路径修改了已有 AGENT_KERNEL_TOKEN。"
  exit 1
}

echo "部署随机密钥生成、升级补齐、非轮换和旧密钥保护测试通过。"
