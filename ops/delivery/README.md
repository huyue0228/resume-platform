# 平台版本制品

本包是在线部署模板，不含 Docker 镜像，不是移动硬盘离线包，也不会自动部署。
先核对外层 SHA-256，解压后执行 `sha256sum -c SHA256SUMS`。只有镜像清单中
`published: true` 的正式标签制品可以从配置的公司镜像仓库拉取；手动演练仅验证构建。

1. 使用只读凭据登录公司镜像仓库。不要把凭据写进 Compose 或提交到源码仓。
2. 从 `env.example` 创建本地 `.env`，将 `images.env` 中的平台版本及三个平台镜像（app、postgres、redis）复制进去。
3. 独立选择已经过业务验收的 Kernel 镜像（建议 digest），设置 `AGENT_KERNEL_IMAGE`
   和与其 build 相同的 `AGENT_KERNEL_VERSION`。包中不替你选择 Kernel 版本。
4. 配置真实部署参数：W3、域名、四项独立随机密钥及数据库配置。
   `auto-generate-on-first-deploy` 只是原离线安装器的占位，本包不会自动替换它；
   不得将其当作生产密钥。不要启用开发 Mock。
5. 使用 `docker compose --env-file .env -f compose.yml config --quiet` 检查配置。
   先在验收环境部署并完成黄金样本、模型/OCR 与人工工作流验收，再安排生产窗口。

平台 v1.2.0 起默认运行四个容器：`app`、`agent-kernel`、`db`、`redis`。
`app` 镜像包含前端 Nginx、API Gunicorn 和分别消费 default/ai 的后台进程。
默认 NAME 分别是 `smart-resume-filter-app`、`smart-resume-filter-agent-kernel`、
`smart-resume-filter-postgres`、`smart-resume-filter-redis`；由 `COMPOSE_PROJECT_NAME` 统一指定前缀。
首次初始化 `init` 复用 app 镜像，通过 profile 一次性运行，不常驻。

从 v1.1.x 升级时，将新 `images.env` 中的 `APP_VERSION`、`APP_IMAGE`、
`POSTGRES_IMAGE_RELEASE`、`REDIS_IMAGE_RELEASE` 更新到原 `.env`；保留原项目名、
密钥、W3 和 Kernel/CA 配置。`BACKEND_IMAGE`、`FRONTEND_IMAGE` 已不再使用。
前端仍使用 `FRONTEND_BIND`/`FRONTEND_PORT`，API 8000 仅在 app 内部监听。
用 `up -d --remove-orphans` 清理旧 backend、worker、ai-worker、frontend 容器。
查看业务日志使用 `docker compose --env-file .env -f compose.yml logs -f app`。
检查 API 和后台队列使用 `exec -T app python application.py --healthcheck`。

升级时保留原 Compose project name、卷与数据库，停止接单并排空旧任务；
不要因目录改名创建新数据卷。需要数据库迁移时，代码回退不等于数据回退。
默认 review_only；enforced 需人工放行，旧 shadow/embedded 已删除，旧任务必须重新提交。此制品不自动执行任何上线或回滚。

需要完整离线交付时，在平台源码仓使用既有离线发布 Skill，并预先加载指定的 Kernel 镜像。

模型路由器使用企业 CA 时，在 `.env` 设置 `AGENT_KERNEL_CA_BUNDLE=/etc/company-ca/model-router-ca.pem`（替换为现场已有 CA PEM 文件的绝对路径），并保持 `AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY=False`。后续所有 Compose 操作都须叠加随包的覆盖文件，例如：

```bash
docker compose --env-file .env -f compose.yml -f compose.model-ca.yml config --quiet
docker compose --env-file .env -f compose.yml -f compose.model-ca.yml up -d --remove-orphans
```

沿用原 Compose project name；CA 只读挂载到 Kernel 的 `/etc/agent-kernel/model-ca.pem`，通过 `SSL_CERT_FILE` 加载，文件须可被容器内 `agent` 用户读取。CA 内容变更后，用相同 Compose 参数执行 `up -d --force-recreate agent-kernel`。平台模型 TEST 当前使用 `verify=False`，并不验证 Kernel 的 TLS 信任链；上线验收必须额外完成一次真实 Agent 分析。交付包不包含现场 CA 或私钥。
