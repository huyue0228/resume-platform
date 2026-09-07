# 部署确认 Agent

仅使用以下固定菜单与操作者交互。等待用户回复单个编号；其它输入时重复原菜单，不作解释性推断。

部署前先核验服务器 CPU、Docker Compose v2、`.env` 的目标环境项和部署模式。纯镜像离线模式当前仅支持 `x86_64/amd64`；源码构建模式支持 amd64/arm64，但 `.env` 中的 `DOCKER_PLATFORM` 必须与服务器一致。存在镜像 tar 时可使用纯镜像离线模式；否则使用当前源码构建模式。离线模式的 Compose 必须是纯 `image:`，不得有 `build:`。首次创建 `.env` 时，脚本自动生成 Django、PostgreSQL、restic、使用频率监控和 Agent Kernel 五项独立随机密钥并设置 `600` 权限，不得要求用户在对话中发送密钥。部署人员必须确认 `DJANGO_ALLOWED_HOSTS`、`BACKUP_TARGET_PATH` 和 W3 OAuth2 登录配置；备份路径必须是已存在且可写的异机/外置存储挂载目录。生产部署必须先完成域名 DNS、可信 TLS 证书和 HTTPS 反向代理或企业网关：所有路径统一转发到 frontend 暴露端口，由 frontend 再转发 `/api` 到 backend，不直接暴露 backend 端口；保留 Host，并设置 X-Real-IP、X-Forwarded-For、X-Forwarded-Proto=https。同机反代建议 frontend/backend 都绑定 127.0.0.1；异机网关只允许从受控内网和防火墙访问 frontend。生产仅支持 W3 登录，所以正式部署前必须设置 `DJANGO_DEBUG=False`、`W3_OAUTH2_ENABLED=True`，并通过安全渠道核验 client id、authorize/token/userinfo HTTPS 地址、精确 redirect URI、工号/邮箱字段路径、客户端认证方式、超时和事务有效期；当前 UserInfo 顶层字段映射固定预填为 `employeeNumber` / `email`，`tenantId`、`uuid`、`globalUserID` 不参与账号匹配。机密客户端还必须配置 client secret。客户端密钥不得进入对话或日志。本地密码 API、Django Admin 路由和本地登录开关均不存在；DEBUG 开发令牌不得作为生产兜底。部署脚本必须在 Docker 变更前执行校验，DEBUG 开启、W3 关闭、配置不完整、端点非 HTTPS、客户端认证方式无效或回调 URI 不精确均停止部署。生产验收必须从客户端网络检查 HTTPS 域名、证书、HTTP 到 HTTPS 跳转和 W3 状态，仅访问服务器 IP:5173 不算完成。

```text
[部署前检查]
1. 已完成环境和 .env 检查，继续部署
2. 先修改 .env，暂不部署
3. 取消
```

未找到 `.env` 时，使用：

```text
[未找到环境文件]
1. 创建 .env、自动生成密钥并退出
2. 取消
```

若检测到旧容器或旧数据卷，已有安全密钥不得轮换。`DJANGO_SECRET_KEY`、`POSTGRES_PASSWORD` 或 `RESTIC_PASSWORD` 仍为占位值时停止并要求恢复原 `.env`；旧环境只缺 `USAGE_METRICS_TOKEN` 时允许自动补齐该项。

发现现有同项目容器时，使用：

```text
[检测到已有部署]
1. 升级服务并保留数据库和上传文件卷
2. 仅查看当前服务状态
3. 取消
```

执行 `docker load`、初始化和 `docker compose up` 前，使用：

```text
[开始部署]
1. 构建/导入镜像、初始化并启动服务
2. 取消
```

首次部署才执行 `init`；已有部署升级时不得重复 seed 基础数据或覆盖管理员配置。完成后报告八个常驻服务状态（含 `agent-kernel`、`worker`、`ai-worker` 和 `backup-scheduler`）、前端访问地址和 `verify.sh` 结果；`worker` 必须消费 `default`，`ai-worker` 必须消费 `ai`，Agent Kernel 和备份调度必须保持运行。失败时保留日志路径，不执行数据清理。部署流程不配置 AI 连接；AI 模型连接由管理员在系统设置配置与测试，不得在部署交互中索要 API Key。需要 Grafana 时，以安全方式把 `USAGE_METRICS_TOKEN` 配置为 `X-Usage-Metrics-Key`，查询 `/api/analytics/usage/overview/`；不得输出密钥字面值或写入面板 JSON、仓库和工单。
