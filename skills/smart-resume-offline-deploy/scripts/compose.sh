#!/usr/bin/env bash
# 由 deploy/verify/uninstall 共用，保证后续操作保留同一份 CA 挂载配置。

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
