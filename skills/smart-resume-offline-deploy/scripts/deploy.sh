#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -n "${DEPLOY_ROOT:-}" ]]; then
  PACKAGE_DIR="$(cd "$DEPLOY_ROOT" && pwd)"
elif [[ -f "$SKILL_DIR/../docker-compose.yml" ]]; then
  # 离线包内的 Skill 位于包根目录下一层。
  PACKAGE_DIR="$(cd "$SKILL_DIR/.." && pwd)"
elif [[ -f "$SKILL_DIR/../../docker-compose.yml" ]]; then
  # 源码仓库中的 Skill 位于 skills/ 下。
  PACKAGE_DIR="$(cd "$SKILL_DIR/../.." && pwd)"
else
  echo "未找到 docker-compose.yml；请在部署包/源码根目录执行，或设置 DEPLOY_ROOT。"
  exit 1
fi
cd "$PACKAGE_DIR"

PROJECT_NAME="${COMPOSE_PROJECT_NAME:-smart-resume-filter}"
ENV_FILE="${ENV_FILE:-.env}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yml}"
IMAGE_TAR="${IMAGE_TAR:-smart-resume-filter-images-amd64.tar}"
DEPLOY_MODE="${DEPLOY_MODE:-auto}"
PERSISTENT_SECRET_KEYS=(DJANGO_SECRET_KEY POSTGRES_PASSWORD RESTIC_PASSWORD)
GENERATED_SECRET_KEYS=("${PERSISTENT_SECRET_KEYS[@]}" USAGE_METRICS_TOKEN AGENT_KERNEL_TOKEN)

compose() {
  docker compose --project-name "$PROJECT_NAME" --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

choose() {
  local title="$1"
  shift
  local answer index=1 option
  while true; do
    index=1
    printf '\n[%s]\n' "$title"
    for option in "$@"; do
      printf '%d. %s\n' "$index" "$option"
      ((index++))
    done
    read -r -p "请选择 [1-$#]: " answer || { echo "已取消。"; exit 0; }
    if [[ "$answer" =~ ^[1-9][0-9]*$ ]] && (( answer >= 1 && answer <= $# )); then
      MENU_CHOICE="$answer"
      return
    fi
    echo "仅接受菜单中的编号，将重新显示菜单。"
  done
}

env_value() {
  local key="$1"
  sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n 1
}

env_is_true() {
  local value
  value="$(env_value "$1")"
  value="$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]')"
  case "$value" in
    1|true|yes|on) return 0 ;;
    *) return 1 ;;
  esac
}

is_placeholder_value() {
  local value="$1"
  [[ -z "$value" || "$value" == change-me* || "$value" == auto-generate* ]]
}

set_env_value() {
  local key="$1"
  local value="$2"
  local tmp_file="${ENV_FILE}.tmp.$$"
  awk -v key="$key" -v value="$value" '
    BEGIN { prefix = key "="; written = 0 }
    index($0, prefix) == 1 {
      if (!written) {
        print prefix value
        written = 1
      }
      next
    }
    { print }
    END {
      if (!written) print prefix value
    }
  ' "$ENV_FILE" > "$tmp_file"
  chmod 600 "$tmp_file"
  mv "$tmp_file" "$ENV_FILE"
}

generate_random_secret() {
  local secret
  secret="$(od -An -N 48 -tx1 /dev/urandom | tr -d '[:space:]')"
  [[ "${#secret}" -eq 96 ]] || {
    echo "无法从 /dev/urandom 生成部署密钥。"
    exit 1
  }
  printf '%s' "$secret"
}

has_existing_resources() {
  [[ -n "$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT_NAME}" 2>/dev/null)" ]] ||
    [[ -n "$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT_NAME}" 2>/dev/null)" ]]
}

needs_secret_generation() {
  local key value
  for key in "${GENERATED_SECRET_KEYS[@]}"; do
    value="$(env_value "$key")"
    if is_placeholder_value "$value"; then
      return 0
    fi
  done
  return 1
}

initialize_secret_values() {
  local key value generated_count=0
  needs_secret_generation || return 0
  if has_existing_resources; then
    for key in "${PERSISTENT_SECRET_KEYS[@]}"; do
      value="$(env_value "$key")"
      if is_placeholder_value "$value"; then
        echo "检测到已有容器或数据卷，但 ${ENV_FILE} 缺少原部署密钥。"
        echo "为避免数据库失联或 AI 连接密文无法解密，脚本不会生成新密钥；请恢复原 ${ENV_FILE}。"
        exit 1
      fi
    done
  fi
  for key in "${GENERATED_SECRET_KEYS[@]}"; do
    value="$(env_value "$key")"
    if is_placeholder_value "$value"; then
      set_env_value "$key" "$(generate_random_secret)"
      ((generated_count += 1))
    fi
  done
  chmod 600 "$ENV_FILE"
  echo "已在 ${ENV_FILE} 自动补齐 ${generated_count} 项独立随机密钥（内容不回显，已有安全值未更改）。"
}

require_value() {
  local key="$1"
  local value
  value="$(env_value "$key")"
  if is_placeholder_value "$value" || [[ "$value" == *"你的服务器"* ]]; then
    echo "请先在 ${ENV_FILE} 设置有效的 ${key}。"
    exit 1
  fi
}

validate_backup_target() {
  local path
  require_value BACKUP_TARGET_PATH
  path="$(env_value BACKUP_TARGET_PATH)"
  [[ "$path" == /* && "$path" != "/" ]] || {
    echo "BACKUP_TARGET_PATH 必须是异机挂载或外置磁盘上的绝对路径。"
    exit 1
  }
  [[ -d "$path" && -w "$path" ]] || {
    echo "备份目录不存在或不可写：${path}"
    echo "请先把异机存储/外置磁盘挂载到该目录，或修改 ${ENV_FILE} 的 BACKUP_TARGET_PATH。"
    exit 1
  }
}

verify_checksums() {
  [[ -f SHA256SUMS ]] || return 0
  if command -v sha256sum >/dev/null; then
    sha256sum -c SHA256SUMS
  elif command -v shasum >/dev/null; then
    while read -r expected filename; do
      [[ -n "$expected" && -n "$filename" ]] || continue
      [[ "$(shasum -a 256 "$filename" | awk '{print $1}')" == "$expected" ]] || {
        echo "校验失败：${filename}"
        exit 1
      }
      echo "${filename}: OK"
    done < SHA256SUMS
  else
    echo "未找到 sha256sum 或 shasum，无法校验 SHA256SUMS。"
    exit 1
  fi
}

[[ -f "$COMPOSE_FILE" ]] || { echo "缺少 ${COMPOSE_FILE}。"; exit 1; }
command -v docker >/dev/null || { echo "未找到 docker。"; exit 1; }
docker compose version >/dev/null || { echo "未找到 Docker Compose v2。"; exit 1; }
docker info >/dev/null 2>&1 || { echo "Docker daemon 不可用，请先启动 Docker。"; exit 1; }

if [[ ! -f "$ENV_FILE" ]]; then
  if has_existing_resources; then
    echo "检测到已有容器或数据卷，但缺少原 ${ENV_FILE}。"
    echo "为避免数据库失联或 AI 连接密文无法解密，脚本不会创建新环境文件；请恢复原 ${ENV_FILE}。"
    exit 1
  fi
  choose "未找到环境文件" "创建 .env、自动生成密钥并退出" "取消"
  [[ "$MENU_CHOICE" == "1" ]] || { echo "已取消，未执行 Docker 变更。"; exit 0; }
  cp .env.example "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  initialize_secret_values
  echo "已创建 ${ENV_FILE}。静态运行参数已预置，五项密钥已自动生成。"
  echo "请确认 DJANGO_ALLOWED_HOSTS、生产域名 DNS/证书/HTTPS 反向代理，并确保 BACKUP_TARGET_PATH 对应的外置/异机存储已挂载。"
  echo "前端仅支持 W3 登录。请补齐 OAuth2 必填项并设置 W3_OAUTH2_ENABLED=True；下次执行会在 Docker 变更前校验且不显示密钥。"
  echo "OAuth2 必填键：CLIENT_ID、AUTHORIZE_URL、TOKEN_URL、USERINFO_URL、REDIRECT_URI、EMPLOYEE_NO_FIELD、EMAIL_FIELD、CLIENT_AUTH_METHOD、TIMEOUT_SECONDS、TRANSACTION_TTL_SECONDS（均使用 W3_OAUTH2_ 前缀）。"
  echo "CLIENT_AUTH_METHOD 为 client_secret_basic/client_secret_post 时还必须填写 W3_OAUTH2_CLIENT_SECRET；SCOPE 按 W3 要求填写。"
  exit 0
fi

chmod 600 "$ENV_FILE"
initialize_secret_values
require_value DJANGO_SECRET_KEY
require_value USAGE_METRICS_TOKEN
require_value AGENT_KERNEL_TOKEN
require_value DJANGO_ALLOWED_HOSTS
require_value POSTGRES_PASSWORD
require_value RESTIC_PASSWORD
validate_backup_target
ENV_FILE="$ENV_FILE" bash "$SKILL_DIR/scripts/validate-w3-env.sh"

case "$DEPLOY_MODE" in
  auto)
    if [[ -f "$IMAGE_TAR" ]]; then
      DEPLOY_MODE="offline"
    else
      DEPLOY_MODE="source"
    fi
    ;;
  offline|source) ;;
  *) echo "DEPLOY_MODE 仅支持 auto、offline 或 source。"; exit 1 ;;
esac

if [[ "$DEPLOY_MODE" == "offline" ]]; then
  [[ -f "$IMAGE_TAR" ]] || { echo "离线部署缺少 ${IMAGE_TAR}。"; exit 1; }
  if grep -Eq '^[[:space:]]*build[[:space:]]*:' "$COMPOSE_FILE"; then
    echo "离线部署 compose 不得含 build:；请使用交付包中的纯 image: docker-compose.yml。"
    exit 1
  fi
fi

case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH="amd64" ;;
  aarch64|arm64) HOST_ARCH="arm64" ;;
  *) echo "不支持的 CPU 架构：$(uname -m)。"; exit 1 ;;
esac

if [[ "$DEPLOY_MODE" == "offline" && "$HOST_ARCH" != "amd64" ]]; then
  echo "当前离线镜像包仅支持 amd64；ARM 服务器请使用对应的 arm64 镜像包或源码构建。"
  exit 1
fi
if [[ "$DEPLOY_MODE" == "source" ]]; then
  DOCKER_PLATFORM="$(env_value DOCKER_PLATFORM)"
  [[ -n "$DOCKER_PLATFORM" ]] || DOCKER_PLATFORM="linux/amd64"
  if [[ "$DOCKER_PLATFORM" != "linux/${HOST_ARCH}" ]]; then
    echo "DOCKER_PLATFORM=${DOCKER_PLATFORM} 与当前 CPU=${HOST_ARCH} 不匹配；请修改 ${ENV_FILE} 后重试。"
    exit 1
  fi
fi

for service in agent-kernel db redis backend worker ai-worker frontend backup-scheduler; do
  if ! compose config --services | grep -Fxq "$service"; then
    echo "Compose 缺少必需服务：${service}。Agent 处理需要 Kernel、default worker 和 ai-worker 同时运行。"
    exit 1
  fi
done

echo "即将部署项目：${PROJECT_NAME}"
echo "- 部署模式：${DEPLOY_MODE}"
[[ "$DEPLOY_MODE" == "offline" ]] && echo "- 导入镜像：${IMAGE_TAR}"
[[ "$DEPLOY_MODE" == "source" ]] && echo "- 从当前源码构建项目镜像"
echo "- 使用环境文件：${ENV_FILE}（不会显示其中的密钥）"
echo "- 镜像、端口、并发、OCR、数据库标识、备份周期和保留策略已使用预设值"
if env_is_true W3_OAUTH2_ENABLED; then
  echo "- W3 OAuth2：登录必填配置和精确回调地址已校验；请确认 W3 平台登记值一致"
else
  echo "- W3 OAuth2：未启用，部署前置校验不应允许继续"
fi
echo "- 仅首次部署会写入基础权限和预置数据；升级不会重置管理员配置"

choose "部署前检查" "已完成环境和 .env 检查，继续部署" "先修改 .env，暂不部署" "取消"
case "$MENU_CHOICE" in
  1) ;;
  2) echo "请修改 ${ENV_FILE} 后重新执行本脚本。"; exit 0 ;;
  3) echo "已取消，未执行 Docker 变更。"; exit 0 ;;
esac

HAS_EXISTING_DEPLOYMENT=false
if [[ -n "$(compose ps -aq 2>/dev/null || true)" ]]; then
  HAS_EXISTING_DEPLOYMENT=true
  choose "检测到已有部署" "升级服务并保留数据库和上传文件卷" "仅查看当前服务状态" "取消"
  case "$MENU_CHOICE" in
    1) ;;
    2) compose ps; exit 0 ;;
    3) echo "已取消，未执行 Docker 变更。"; exit 0 ;;
  esac
fi
choose "开始部署" "构建/导入镜像、初始化并启动服务" "取消"
[[ "$MENU_CHOICE" == "1" ]] || { echo "已取消，未执行 Docker 变更。"; exit 0; }

if [[ "$DEPLOY_MODE" == "offline" ]]; then
  verify_checksums
  docker load -i "$IMAGE_TAR"
else
  compose build
fi

compose config --images
if [[ "$HAS_EXISTING_DEPLOYMENT" == "false" ]]; then
  compose --profile init run --rm init
else
  echo "已有环境升级：跳过 init，保留现有基础数据和管理员配置。"
fi
compose up -d --wait --wait-timeout 180
bash "$SKILL_DIR/scripts/verify.sh"

FRONTEND_BIND="$(env_value FRONTEND_BIND)"
FRONTEND_PORT="$(env_value FRONTEND_PORT)"
echo "容器前端入口：http://服务器地址:${FRONTEND_PORT:-5173}（绑定地址：${FRONTEND_BIND:-0.0.0.0}）"
echo "生产访问必须通过已配置的 HTTPS 域名反向代理；仅访问上述 HTTP 端口不视为生产验收完成。"
