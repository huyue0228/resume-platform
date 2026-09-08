#!/usr/bin/env bash
set -Eeuo pipefail

SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -n "${DEPLOY_ROOT:-}" ]]; then
  PACKAGE_DIR="$(cd "$DEPLOY_ROOT" && pwd)"
elif [[ -f "$SKILL_DIR/../docker-compose.yml" ]]; then
  PACKAGE_DIR="$(cd "$SKILL_DIR/.." && pwd)"
elif [[ -f "$SKILL_DIR/../../docker-compose.yml" ]]; then
  PACKAGE_DIR="$(cd "$SKILL_DIR/../.." && pwd)"
else
  echo "未找到 docker-compose.yml；请设置 DEPLOY_ROOT。"
  exit 1
fi
cd "$PACKAGE_DIR"

PROJECT_NAME="${COMPOSE_PROJECT_NAME:-smart-resume-filter}"
ENV_FILE="${ENV_FILE:-.env}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yml}"

[[ -f "$ENV_FILE" ]] || { echo "缺少 ${ENV_FILE}。"; exit 1; }
ENV_FILE="$ENV_FILE" bash "$SKILL_DIR/scripts/validate-w3-env.sh"

source "$SKILL_DIR/scripts/compose.sh"

command -v docker >/dev/null || { echo "未找到 docker。"; exit 1; }
docker compose version >/dev/null || { echo "未找到 Docker Compose v2。"; exit 1; }
compose config --images >/dev/null
configured_services="$(compose config --services)"
for service in agent-kernel db redis backend worker ai-worker frontend; do
  if ! grep -Fxq "$service" <<< "$configured_services"; then
    echo "Compose 缺少必需服务：${service}"
    exit 1
  fi
done
compose ps
running_services="$(compose ps --status running --services)"
for service in agent-kernel db redis backend worker ai-worker frontend; do
  if ! grep -Fxq "$service" <<< "$running_services"; then
    echo "服务未处于运行状态：${service}"
    exit 1
  fi
done
compose exec -T backend python manage.py check
compose exec -T frontend nginx -t
if [[ -n "$(model_ca_bundle)" ]]; then
  compose exec -T agent-kernel sh -c 'test -r "$SSL_CERT_FILE" && test -s "$SSL_CERT_FILE"' || {
    echo "Agent Kernel 的 CA 文件不存在、为空或 agent 用户不可读，请检查挂载和文件权限。"
    exit 1
  }
  echo "Agent Kernel CA 挂载可读；仍需用真实 Agent 分析验证模型路由器的证书链和域名。"
fi
echo "部署验证通过。"
