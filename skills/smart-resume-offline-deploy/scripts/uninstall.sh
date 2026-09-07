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

[[ -f "$ENV_FILE" ]] || { echo "缺少 ${ENV_FILE}，无法确认目标部署。"; exit 1; }

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

echo "目标项目：${PROJECT_NAME}"
compose ps -a || true
choose "选择卸载范围" \
  "常规卸载：删除容器和网络，保留数据库与上传文件卷" \
  "彻底卸载：删除容器、网络和数据卷" \
  "完全清理：删除容器、网络、数据卷和本项目镜像" \
  "取消"

case "$MENU_CHOICE" in
  1)
    compose down --remove-orphans
    echo "常规卸载完成，业务数据已保留。"
    exit 0
    ;;
  4)
    echo "已取消，未执行卸载。"
    exit 0
    ;;
esac

UNINSTALL_SCOPE="$MENU_CHOICE"
choose "不可恢复操作" "确认永久删除" "返回并保留数据"
[[ "$MENU_CHOICE" == "1" ]] || { echo "已返回，未删除数据或镜像。"; exit 0; }

compose down --volumes --remove-orphans
echo "数据卷已删除。"

if [[ "$UNINSTALL_SCOPE" == "3" ]]; then
  images=()
  while IFS= read -r image; do
    [[ -n "$image" ]] && images+=("$image")
  done < <(compose config --images)
  if [[ "${#images[@]}" -gt 0 ]]; then
    docker image rm "${images[@]}" || echo "部分镜像仍被其它容器使用，未能删除。"
  fi
fi

echo "卸载流程结束。"
