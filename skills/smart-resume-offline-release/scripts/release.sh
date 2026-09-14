#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILL_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${SKILL_DIR}/../.." && pwd)"
ASSET_DIR="${SKILL_DIR}/assets"

VERSION="$(date '+%Y%m%d-%H%M')-amd64"
DRIVE_PATH="/Volumes/ZiTai"
COPY_TO_DRIVE=0
SKIP_BUILD=0
CHECK_ONLY=0
TARGET_PLATFORM="linux/amd64"

usage() {
  cat <<'EOF'
用法：release.sh [选项]

  --version VERSION  指定发布版本，默认 YYYYMMDD-HHMM-amd64
  --drive PATH       指定移动硬盘挂载点，默认 /Volumes/ZiTai
  --no-copy          只生成本地 release，不复制到移动硬盘
  --skip-build       复用同版本的现有镜像，仅重新封装
  --check            只执行环境检查
  -h, --help         显示帮助
EOF
}

log() {
  printf '\n[%s] %s\n' "$(date '+%H:%M:%S')" "$*"
}

die() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      [[ $# -ge 2 ]] || die "--version 缺少参数"
      VERSION="$2"
      shift 2
      ;;
    --drive)
      [[ $# -ge 2 ]] || die "--drive 缺少参数"
      DRIVE_PATH="$2"
      COPY_TO_DRIVE=1
      shift 2
      ;;
    --no-copy)
      COPY_TO_DRIVE=0
      shift
      ;;
    --skip-build)
      SKIP_BUILD=1
      shift
      ;;
    --check)
      CHECK_ONLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "未知参数：$1"
      ;;
  esac
done

[[ "$VERSION" =~ ^[0-9]{8}-[0-9]{4}-amd64$ ]] || \
  die "版本格式必须为 YYYYMMDD-HHMM-amd64"

RELEASE_NAME="smart-resume-filter-offline-${VERSION}"
RELEASE_ROOT="${REPO_ROOT}/release"
PACKAGE_DIR="${RELEASE_ROOT}/${RELEASE_NAME}"
ARCHIVE_PATH="${RELEASE_ROOT}/${RELEASE_NAME}.tar.gz"
CHECKSUM_PATH="${ARCHIVE_PATH}.sha256"
IMAGE_TAR="${PACKAGE_DIR}/smart-resume-filter-images-amd64.tar"

APP_IMAGE="smart-resume-filter-app:${VERSION}"
KERNEL_VERSION="${AGENT_KERNEL_VERSION:-}"
KERNEL_IMAGE="${AGENT_KERNEL_IMAGE:-}"
[[ "$KERNEL_VERSION" =~ ^[A-Za-z0-9._-]+$ && -n "$KERNEL_IMAGE" ]] || die "拆仓后必须提供 AGENT_KERNEL_VERSION 和已发布的 AGENT_KERNEL_IMAGE"
[[ "$KERNEL_IMAGE" =~ ^[A-Za-z0-9._/:@-]+$ ]] || die "AGENT_KERNEL_IMAGE 格式无效"
POSTGRES_RELEASE_IMAGE="smart-resume-filter-postgres:16"
REDIS_RELEASE_IMAGE="smart-resume-filter-redis:7"
IMAGES=("$KERNEL_IMAGE" "$APP_IMAGE" "$POSTGRES_RELEASE_IMAGE" "$REDIS_RELEASE_IMAGE")

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

check_usage_metrics_contract() {
  local env_template="$1"
  local compose_template="$2"
  local deploy_script="$3"
  grep -Fxq 'USAGE_METRICS_TOKEN=auto-generate-on-first-deploy' "$env_template" || \
    die "环境变量模板缺少 USAGE_METRICS_TOKEN 自动生成占位值"
  grep -Fq 'USAGE_METRICS_TOKEN: ${USAGE_METRICS_TOKEN:?Set USAGE_METRICS_TOKEN in .env}' "$compose_template" || \
    die "Compose 模板未向后端服务传入 USAGE_METRICS_TOKEN"
  grep -Eq 'GENERATED_SECRET_KEYS=.*USAGE_METRICS_TOKEN' "$deploy_script" || \
    die "部署脚本未自动生成 USAGE_METRICS_TOKEN"
  grep -Fq 'require_value USAGE_METRICS_TOKEN' "$deploy_script" || \
    die "部署脚本未校验 USAGE_METRICS_TOKEN"
  grep -Fxq 'AGENT_KERNEL_TOKEN=auto-generate-on-first-deploy' "$env_template" || \
    die "环境变量模板缺少 AGENT_KERNEL_TOKEN 自动生成占位值"
  grep -Fq 'AGENT_KERNEL_TOKEN: ${AGENT_KERNEL_TOKEN:?Set AGENT_KERNEL_TOKEN in .env}' "$compose_template" || \
    die "Compose 模板未向 Agent Kernel 传入认证令牌"
  grep -Eq 'GENERATED_SECRET_KEYS=.*AGENT_KERNEL_TOKEN' "$deploy_script" || \
    die "部署脚本未自动生成 AGENT_KERNEL_TOKEN"
  grep -Fq 'require_value AGENT_KERNEL_TOKEN' "$deploy_script" || \
    die "部署脚本未校验 AGENT_KERNEL_TOKEN"
}

check_prerequisites() {
  log "检查发布环境"
  [[ -f "${REPO_ROOT}/AGENTS.md" && -f "${REPO_ROOT}/cmd/resume-platform/main.go" && -f "${REPO_ROOT}/frontend/package.json" ]] || \
    die "必须从 smart-resume 项目 Skill 执行"
  [[ -f "${REPO_ROOT}/docker-compose.yml" ]] || die "缺少仓库 docker-compose.yml"
  [[ -d "${REPO_ROOT}/skills/smart-resume-offline-deploy" ]] || die "缺少部署 Skill"
  [[ -f "${ASSET_DIR}/docker-compose.yml" ]] || die "缺少离线 Compose 模板"
  [[ -f "${ASSET_DIR}/env.example" ]] || die "缺少环境变量模板"
  [[ -f "${ASSET_DIR}/README-offline-deploy.md" ]] || die "缺少部署说明模板"
  [[ -f "${ASSET_DIR}/AGENT-offline-deploy-guide.md" ]] || die "缺少 Agent 指南模板"
  check_usage_metrics_contract \
    "${ASSET_DIR}/env.example" \
    "${ASSET_DIR}/docker-compose.yml" \
    "${REPO_ROOT}/skills/smart-resume-offline-deploy/scripts/deploy.sh"
  require_command docker
  require_command shasum
  require_command tar
  require_command cmp
  docker info >/dev/null 2>&1 || die "Docker daemon 不可用"
  docker compose version >/dev/null 2>&1 || die "Docker Compose v2 不可用"
  docker image inspect "$KERNEL_IMAGE" >/dev/null 2>&1 || die "请先加载或拉取指定内核镜像；平台仓不再构建内核源码"
  if grep -Eq '^[[:space:]]*build:' "${ASSET_DIR}/docker-compose.yml"; then
    die "离线 Compose 模板不得包含 build:"
  fi
  docker compose --env-file "${ASSET_DIR}/env.example" \
    -f "${ASSET_DIR}/docker-compose.yml" config >/dev/null
  if [[ "$COPY_TO_DRIVE" -eq 1 ]]; then
    [[ -d "$DRIVE_PATH" ]] || die "移动硬盘未挂载：${DRIVE_PATH}"
    [[ -w "$DRIVE_PATH" ]] || die "移动硬盘不可写：${DRIVE_PATH}"
  fi
  printf '仓库：%s\n版本：%s\n目标：%s\n' "$REPO_ROOT" "$VERSION" "$TARGET_PLATFORM"
  [[ "$COPY_TO_DRIVE" -eq 0 ]] || printf '移动硬盘：%s\n' "$DRIVE_PATH"
}

check_image_architecture() {
  local image="$1"
  local actual
  actual="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")"
  [[ "$actual" == "$TARGET_PLATFORM" ]] || die "镜像架构错误：${image} 为 ${actual}"
  printf '架构 OK：%s -> %s\n' "$image" "$actual"
}

render_template() {
  local source="$1"
  local target="$2"
  sed \
    -e "s/__APP_VERSION__/${VERSION}/g" \
    -e "s|__AGENT_KERNEL_VERSION__|${KERNEL_VERSION}|g" \
    -e "s|__AGENT_KERNEL_IMAGE__|${KERNEL_IMAGE}|g" \
    -e "s/__RELEASE_NAME__/${RELEASE_NAME}/g" \
    "$source" > "$target"
}

generate_inner_checksums() {
  (
    cd "$PACKAGE_DIR"
    : > SHA256SUMS
    find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | while IFS= read -r file; do
      shasum -a 256 "${file#./}"
    done >> SHA256SUMS
    shasum -a 256 -c SHA256SUMS
  )
}

check_prerequisites
if [[ "$CHECK_ONLY" -eq 1 ]]; then
  log "环境检查通过"
  exit 0
fi

[[ ! -e "$PACKAGE_DIR" ]] || die "发布目录已存在，请使用新版本号：${PACKAGE_DIR}"
[[ ! -e "$ARCHIVE_PATH" ]] || die "压缩包已存在，请使用新版本号：${ARCHIVE_PATH}"
[[ ! -e "$CHECKSUM_PATH" ]] || die "校验文件已存在，请使用新版本号：${CHECKSUM_PATH}"

mkdir -p "$RELEASE_ROOT"
BUILD_ENV="$(mktemp "${TMPDIR:-/private/tmp}/smart-resume-release.XXXXXX")"
trap 'rm -f "$BUILD_ENV"' EXIT
cat > "$BUILD_ENV" <<EOF
APP_VERSION=${VERSION}
AGENT_KERNEL_VERSION=${KERNEL_VERSION}
AGENT_KERNEL_IMAGE=${KERNEL_IMAGE}
DOCKER_PLATFORM=${TARGET_PLATFORM}
DJANGO_SECRET_KEY=build-only-not-for-deployment
USAGE_METRICS_TOKEN=build-only-not-for-deployment
AGENT_KERNEL_TOKEN=build-only-not-for-deployment
DJANGO_ALLOWED_HOSTS=localhost
POSTGRES_DB=srf
POSTGRES_USER=srf_user
POSTGRES_PASSWORD=build-only-not-for-deployment
EOF

cd "$REPO_ROOT"
if [[ "$SKIP_BUILD" -eq 0 ]]; then
  log "构建业务平台镜像，复用独立发布的内核镜像"
  docker compose --env-file "$BUILD_ENV" config --images
  docker compose --env-file "$BUILD_ENV" build db redis app
else
  log "跳过构建，复用同版本镜像"
fi

log "检查镜像架构和容器配置"
for image_name in "${IMAGES[@]}"; do
  check_image_architecture "$image_name"
done
docker run --rm --platform "$TARGET_PLATFORM" -e RUN_MIGRATIONS=0 -e RUN_SEED_BASE=0 "$APP_IMAGE" check
docker run --rm --platform "$TARGET_PLATFORM" --entrypoint /bin/sh "$KERNEL_IMAGE" -c 'test -x /usr/local/bin/agent-kernel'

log "组装离线包"
mkdir -p "$PACKAGE_DIR"
render_template "${ASSET_DIR}/docker-compose.yml" "${PACKAGE_DIR}/docker-compose.yml"
render_template "${ASSET_DIR}/env.example" "${PACKAGE_DIR}/.env.example"
render_template "${ASSET_DIR}/README-offline-deploy.md" "${PACKAGE_DIR}/README-offline-deploy.md"
render_template "${ASSET_DIR}/AGENT-offline-deploy-guide.md" "${PACKAGE_DIR}/AGENT-offline-deploy-guide.md"
cp "${REPO_ROOT}/ops/two-agent-allocation.md" "${PACKAGE_DIR}/two-agent-allocation.md"
cp "${REPO_ROOT}/ops/task-center.md" "${PACKAGE_DIR}/task-center.md"
cp -R "${REPO_ROOT}/skills/smart-resume-offline-deploy" \
  "${PACKAGE_DIR}/smart-resume-offline-deploy-skill"
check_usage_metrics_contract \
  "${PACKAGE_DIR}/.env.example" \
  "${PACKAGE_DIR}/docker-compose.yml" \
  "${PACKAGE_DIR}/smart-resume-offline-deploy-skill/scripts/deploy.sh"

if grep -Eq '^[[:space:]]*build:' "${PACKAGE_DIR}/docker-compose.yml"; then
  die "离线 Compose 不得包含 build:"
fi
docker compose --env-file "${PACKAGE_DIR}/.env.example" \
  -f "${PACKAGE_DIR}/docker-compose.yml" config --images

log "导出镜像"
docker save -o "$IMAGE_TAR" "${IMAGES[@]}"
generate_inner_checksums

log "回读镜像 tar"
docker load -i "$IMAGE_TAR" >/dev/null

log "生成外层压缩包和校验文件"
(
  cd "$RELEASE_ROOT"
  tar -czf "${RELEASE_NAME}.tar.gz" "$RELEASE_NAME"
  shasum -a 256 "${RELEASE_NAME}.tar.gz" > "${RELEASE_NAME}.tar.gz.sha256"
  shasum -a 256 -c "${RELEASE_NAME}.tar.gz.sha256"
)

if [[ "$COPY_TO_DRIVE" -eq 1 ]]; then
  DRIVE_ARCHIVE="${DRIVE_PATH}/${RELEASE_NAME}.tar.gz"
  DRIVE_CHECKSUM="${DRIVE_ARCHIVE}.sha256"
  [[ ! -e "$DRIVE_ARCHIVE" && ! -e "$DRIVE_CHECKSUM" ]] || \
    die "移动硬盘已有同名文件，请使用新版本号"
  log "复制到移动硬盘并复验"
  cp "$ARCHIVE_PATH" "$CHECKSUM_PATH" "$DRIVE_PATH/"
  sync
  (
    cd "$DRIVE_PATH"
    shasum -a 256 -c "${RELEASE_NAME}.tar.gz.sha256"
  )
  cmp "$ARCHIVE_PATH" "$DRIVE_ARCHIVE"
fi

ARCHIVE_SHA="$(shasum -a 256 "$ARCHIVE_PATH" | awk '{print $1}')"
if ! ARCHIVE_SIZE="$(stat -c '%s' "$ARCHIVE_PATH" 2>/dev/null)"; then
  ARCHIVE_SIZE="$(stat -f '%z' "$ARCHIVE_PATH")"
fi

log "发布完成"
printf '版本：%s\n本地包：%s\n校验文件：%s\n大小：%s bytes\nSHA-256：%s\n' \
  "$VERSION" "$ARCHIVE_PATH" "$CHECKSUM_PATH" "$ARCHIVE_SIZE" "$ARCHIVE_SHA"
if [[ "$COPY_TO_DRIVE" -eq 1 ]]; then
  printf '移动硬盘包：%s\n移动硬盘校验：OK\n' "$DRIVE_ARCHIVE"
fi
