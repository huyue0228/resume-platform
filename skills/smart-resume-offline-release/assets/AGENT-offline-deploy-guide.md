# 内网部署 Agent 指南

发布包：`__RELEASE_NAME__`，目标平台：`linux/amd64`。

1. 检查 `uname -m`、`docker --version` 和 `docker compose version`；仅在服务器为 `x86_64/amd64` 时继续。
2. 执行 `sha256sum -c SHA256SUMS`，任何失败都停止。
3. 首次运行 `smart-resume-offline-deploy-skill/scripts/deploy.sh`，选择创建 `.env`。脚本自动生成互不复用的 `DJANGO_SECRET_KEY`、`POSTGRES_PASSWORD`、`USAGE_METRICS_TOKEN` 和 `AGENT_KERNEL_TOKEN` 并设置 `600` 权限，任何密钥不得在对话或日志中输出。
4. 管理员必须在 `.env` 补充实际 `DJANGO_ALLOWED_HOSTS`，并配置 W3 OAuth2。生产环境先完成域名 DNS、可信 TLS 证书和 HTTPS 反向代理或企业网关，把所有路径统一转发到 app 暴露端口；页面与 API 共用 app 的同一端口。同机反代建议 `FRONTEND_BIND=127.0.0.1`，异机网关则只向受控内网开放 frontend。生产仅支持 W3 登录，所以正式部署前必须设置 `DJANGO_DEBUG=False`、`W3_OAUTH2_ENABLED=True`，填写 client id、authorize/token/userinfo HTTPS 地址、精确 redirect URI、工号/邮箱字段路径、客户端认证方式、超时和事务有效期；当前 UserInfo 顶层字段映射已预填为 `employeeNumber` / `email`。机密客户端还必须填写 client secret，scope 按 W3 要求填写。客户端密钥不得出现在对话或日志中。本地密码 API、Django Admin 路由和本地登录开关均不存在；DEBUG 开发令牌不得作为生产兜底。部署脚本会在 Docker 变更前校验，失败时先修正 `.env`。再次运行部署脚本，并使用同目录 `verify.sh` 验证，最后从客户端网络验收 HTTPS 域名、证书、HTTP 跳转和 W3 状态。
5. v3.1.0 同时升级 app 与 Kernel；来自 v2 或更旧版本时先完成或取消旧协议在途任务。存量池默认 legacy，不自动启用新分配；按 two-agent-allocation.md 检查接收状态、试算并显式切换 execute_v1。保留原 Compose 项目名、`pgdata`、`media_data`、密钥、W3 和 CA；未获得明确确认不得删除数据卷。启动后按 `README-offline-deploy.md` 配置投递标准、职位池、能力标签和部门分配规则；旧分析不自动转为入池资格。
6. 完成后报告 app、agent-kernel、db、redis 四个常驻容器状态；确认 Kernel 健康、Go app 的 HTTP/PostgreSQL/Redis 健康检查通过，再报告运行自检结果和访问地址。
7. 如交付 Grafana，使用安全渠道把 `.env` 中的 `USAGE_METRICS_TOKEN` 配置为 `X-Usage-Metrics-Key` 请求头，查询 `/api/analytics/usage/overview/`。最小验证只在当前 shell 注入密钥后执行，禁止在对话、日志、面板 JSON 或命令中写入密钥字面值。

检测到旧容器或旧数据卷时不得替换已有安全密钥；旧环境只允许补齐缺失的 `USAGE_METRICS_TOKEN`。原 `.env` 缺失应停止并走密钥恢复流程。

模型路由器的 CA 是独立验收项：平台模型 TEST 与 Kernel 分析均默认校验 TLS，通过 TEST 或 `/healthz` 不能证明 Kernel 的 TLS 信任链正常。模型路由器使用企业 CA 时，在 `.env` 设置 `AGENT_KERNEL_CA_BUNDLE` 为现场 CA PEM 文件绝对路径，由部署 Skill 自动只读挂载到 app 与 Kernel，并设置各自的 `SSL_CERT_FILE`；保持 TLS 校验开启。检查 `agent` 用户可读权限，更新 CA 后重建 app 与 Kernel。最终必须完成一次真实 Agent 分析；仅模型 TEST 成功不得报告 Agent 功能验收通过。详见随包部署 Skill 的企业 CA 小节。
