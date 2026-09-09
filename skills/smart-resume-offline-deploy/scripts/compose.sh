#!/usr/bin/env bash
# 由 deploy/verify/uninstall 共用，保证后续操作保留同一份 CA 挂载配置。

# 同一项目名同时决定容器 NAME、网络和数据卷，脚本与直接 Compose 保持一致。
PROJECT_NAME="${COMPOSE_PROJECT_NAME:-}"
if [[ -z "$PROJECT_NAME" && -f "$ENV_FILE" ]]; then
  PROJECT_NAME="$(sed -n 's/^COMPOSE_PROJECT_NAME=//p' "$ENV_FILE" | tail -n 1)"
  PROJECT_NAME="${PROJECT_NAME%\"}"; PROJECT_NAME="${PROJECT_NAME#\"}"
  PROJECT_NAME="${PROJECT_NAME%\'}"; PROJECT_NAME="${PROJECT_NAME#\'}"
fi
PROJECT_NAME="${PROJECT_NAME:-smart-resume-filter}"
if [[ ! "$PROJECT_NAME" =~ ^[a-z0-9][a-z0-9_-]*$ ]]; then
  echo "COMPOSE_PROJECT_NAME 仅支持小写字母、数字、横线和下划线，并以字母或数字开头。"
  exit 1
fi
export COMPOSE_PROJECT_NAME="$PROJECT_NAME"

model_ca_bundle() {
  local value
  value="${AGENT_KERNEL_CA_BUNDLE-$(sed -n 's/^AGENT_KERNEL_CA_BUNDLE=//p' "$ENV_FILE" | tail -n 1)}"
  # 与模板一致使用普通路径，同时允许部署人员用引号包裹带空格的路径。
  value="${value%\"}"; value="${value#\"}"
  value="${value%\'}"; value="${value#\'}"
  printf '%s' "$value"
}

compose() {
  local ca_bundle
  local files=(-f "$COMPOSE_FILE")
  ca_bundle="$(model_ca_bundle)"
  if [[ -n "$ca_bundle" ]]; then
    files+=(-f "$SKILL_DIR/assets/compose.model-ca.yml")
  fi
  docker compose --project-name "$PROJECT_NAME" --env-file "$ENV_FILE" "${files[@]}" "$@"
}
