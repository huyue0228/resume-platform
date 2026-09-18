# 内网部署 Agent 指南

发布包：`__RELEASE_NAME__`，目标平台：`linux/amd64`。

本包是 v5.0.0 全新数据库部署包，不支持 v4 或更旧数据库原位升级。先选择尚未使用的独立项目名，例如 `export COMPOSE_PROJECT_NAME=smart-resume-filter-v5`，并将生成的 `.env` 中同名配置设为一致值；保留旧实例、数据卷、密钥和配置。同机部署另选空闲端口。

1. 检查 `uname -m`、`docker --version` 和 `docker compose version`；仅在服务器为 `x86_64/amd64` 时继续。
2. 执行 `sha256sum -c SHA256SUMS`，任何失败都停止。
3. 首次运行 `smart-resume-offline-deploy-skill/scripts/deploy.sh`，选择创建 `.env`。脚本自动生成互不复用的 `DJANGO_SECRET_KEY`、`POSTGRES_PASSWORD`、`USAGE_METRICS_TOKEN` 和 `AGENT_KERNEL_TOKEN` 并设置 `600` 权限，任何密钥不得在对话或日志中输出。
4. 管理员必须在 `.env` 补充实际 `DJANGO_ALLOWED_HOSTS`，并配置 W3 OAuth2。生产环境先完成域名 DNS、可信 TLS 证书和 HTTPS 反向代理或企业网关，把所有路径统一转发到 app 暴露端口；页面与 API 共用 app 的同一端口。同机反代建议 `FRONTEND_BIND=127.0.0.1`，异机网关则只向受控内网开放 frontend。生产仅支持 W3 登录，所以正式部署前必须设置 `DJANGO_DEBUG=False`、`W3_OAUTH2_ENABLED=True`，填写 client id、authorize/token/userinfo HTTPS 地址、精确 redirect URI、工号/邮箱字段路径、客户端认证方式、超时和事务有效期；当前 UserInfo 顶层字段映射已预填为 `employeeNumber` / `email`。机密客户端还必须填写 client secret，scope 按 W3 要求填写。客户端密钥不得出现在对话或日志中。本地密码 API、Django Admin 路由和本地登录开关均不存在；DEBUG 开发令牌不得作为生产兜底。部署脚本会在 Docker 变更前校验，失败时先修正 `.env`。再次运行部署脚本，并使用同目录 `verify.sh` 验证，最后从客户端网络验收 HTTPS 域名、证书、HTTP 跳转和 W3 状态。
5. 本版使用 Platform v5.0.0、Kernel v5.0.0、协议包 5.0.0。按 `README-offline-deploy.md` 在岗位管理中补齐筛选标准、能力标签和部门接收规则，并配置具备反馈权限的部门接口人。无专业大类词表、HC 容量限制或历史模式迁移；不允许将 v5 指向旧业务库或删除旧卷绕过。WeLink 尚未真实发送，部门需登录系统处理。
6. 完成后报告 app、agent-kernel、db、redis 四个常驻容器状态；确认 Kernel 健康、Go app 的 HTTP/PostgreSQL/Redis 健康检查通过，再报告运行自检结果和访问地址。
7. 如交付 Grafana，使用安全渠道把 `.env` 中的 `USAGE_METRICS_TOKEN` 配置为 `X-Usage-Metrics-Key` 请求头，查询 `/api/analytics/usage/overview/`。最小验证只在当前 shell 注入密钥后执行，禁止在对话、日志、面板 JSON 或命令中写入密钥字面值。

检测到旧版本容器或数据卷时，保留该实例并选择新的 v5 项目；不得覆盖已有安全密钥。维护同一 v5 实例时，原 `.env` 缺失应停止并走密钥恢复流程。

模型路由器的 CA 是独立验收项：平台模型 TEST 与 Kernel 分析均默认校验 TLS，通过 TEST 或 `/healthz` 不能证明 Kernel 的 TLS 信任链正常。模型路由器使用企业 CA 时，在 `.env` 设置 `AGENT_KERNEL_CA_BUNDLE` 为现场 CA PEM 文件绝对路径，由部署 Skill 自动只读挂载到 app 与 Kernel，并设置各自的 `SSL_CERT_FILE`；保持 TLS 校验开启。检查 `agent` 用户可读权限，更新 CA 后重建 app 与 Kernel。最终必须完成一次真实 Agent 分析；仅模型 TEST 成功不得报告 Agent 功能验收通过。详见随包部署 Skill 的企业 CA 小节。
