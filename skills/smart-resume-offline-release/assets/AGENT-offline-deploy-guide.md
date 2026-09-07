# 内网部署 Agent 指南

发布包：`__RELEASE_NAME__`，目标平台：`linux/amd64`。

1. 检查 `uname -m`、`docker --version` 和 `docker compose version`；仅在服务器为 `x86_64/amd64` 时继续。
2. 执行 `sha256sum -c SHA256SUMS`，任何失败都停止。
3. 首次运行 `smart-resume-offline-deploy-skill/scripts/deploy.sh`，选择创建 `.env`。脚本自动生成互不复用的 `DJANGO_SECRET_KEY`、`POSTGRES_PASSWORD`、`RESTIC_PASSWORD`、`USAGE_METRICS_TOKEN` 和 `AGENT_KERNEL_TOKEN` 并设置 `600` 权限，任何密钥不得在对话或日志中输出。
4. 管理员必须在 `.env` 补充实际 `DJANGO_ALLOWED_HOSTS`，确认异机/外置存储已挂载到默认 `/mnt/smart-resume-filter-backups`，并配置 W3 OAuth2。生产环境先完成域名 DNS、可信 TLS 证书和 HTTPS 反向代理或企业网关，把所有路径统一转发到 frontend 暴露端口；不要直接暴露 backend 的 `8000`。同机反代建议 `FRONTEND_BIND=127.0.0.1`，异机网关则只向受控内网开放 frontend。生产仅支持 W3 登录，所以正式部署前必须设置 `DJANGO_DEBUG=False`、`W3_OAUTH2_ENABLED=True`，填写 client id、authorize/token/userinfo HTTPS 地址、精确 redirect URI、工号/邮箱字段路径、客户端认证方式、超时和事务有效期；当前 UserInfo 顶层字段映射已预填为 `employeeNumber` / `email`。机密客户端还必须填写 client secret，scope 按 W3 要求填写。客户端密钥不得出现在对话或日志中。本地密码 API、Django Admin 路由和本地登录开关均不存在；DEBUG 开发令牌不得作为生产兜底。部署脚本会在 Docker 变更前校验，失败时先修正 `.env`。再次运行部署脚本，并使用同目录 `verify.sh` 验证，最后从客户端网络验收 HTTPS 域名、证书、HTTP 跳转和 W3 状态。
5. 升级时保留 `pgdata` 和 `media_data` volumes；未获得明确确认不得删除数据卷。
6. 完成后报告八个容器状态；确认 `agent-kernel` 健康、`worker` 消费 `default`、`ai-worker` 使用 threads 池消费 `ai`、`backup-scheduler` 正常运行，再报告后端检查、Nginx 检查和访问地址。
7. 如交付 Grafana，使用安全渠道把 `.env` 中的 `USAGE_METRICS_TOKEN` 配置为 `X-Usage-Metrics-Key` 请求头，查询 `/api/analytics/usage/overview/`。最小验证只在当前 shell 注入密钥后执行，禁止在对话、日志、面板 JSON 或命令中写入密钥字面值。

检测到旧容器或旧数据卷时不得替换已有安全密钥；旧环境只允许补齐缺失的 `USAGE_METRICS_TOKEN`。原 `.env` 缺失应停止并走密钥恢复流程。
