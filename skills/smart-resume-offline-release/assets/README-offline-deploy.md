# 海纳智聘离线部署

## v3.1.0 升级与双 Agent 分配

本包配套平台 v3.1.0、Kernel v3.1.0 和协议 resume-contracts 3.1.0。筛选保持 `resume-analysis/v3`，新增独立确定性分配协议 `resume-allocation/v1`。平台与 Kernel 一起升级；筛选路径仍为 `/v2/tasks/execute`，分配路径为 `/v2/allocation/tasks/execute`。

已有环境保留原 Compose 项目名、数据卷、密钥、W3 和 CA。参考本包 `.env.example` 同步 app、Kernel 的版本与镜像引用，不要整体覆盖现场 `.env`，不要重新初始化已有业务库。来自 v2 及更旧版本的部署须先完成或取消旧协议在途任务；迁移会拒绝带旧协议的未完成任务。已有有效分配冲突会报错，不能以删除历史的方式绕过。

存量主体/职位池默认保持 `legacy`，升级不会自动启用新分配。管理员先在「配置项 → 职位池与标签」核对标签、内部职位池、投递标准与需求规则，再在「任务中心 → 分配任务」检查需求接收状态并试算。启用 `execute_v1` 时要求旧范围执行排空，校验历史资格，更新执行代次。不能证明的历史资格要求重评；未知需求归属单列，不推测其归属。

筛选达标直接入池并保存资格，不再经过人工复核。新分配只读取经过验证的结构化标签，按优先标签、priority 和同级 7 天供给均衡选择部门与需求。非公开岗位和 HC=0 的有效接收需求也参与；HC 是计划人数，不限制候选人数。分配失败可独立重试，等待不会撤销筛选通过资格。

部门不通过后结束当前志愿，有下一志愿则回到待处理，等待下一轮计划或手动处理；全部志愿不通过进入人才库。本期没有同池自动转荐、Offer 或录用 HC 核销。暂停新分配保留资格及已提交归属；不要运行不认识新增状态与归属账本的旧二进制继续写库。详见随包 [双 Agent 分配运维](two-agent-allocation.md) 和 [任务中心说明](task-center.md)。

本目录是 `linux/amd64` 纯镜像离线包。优先使用随包附带的部署 Skill：

```bash
bash smart-resume-offline-deploy-skill/scripts/deploy.sh
```

首次运行选择“创建 `.env`、自动生成密钥并退出”。脚本会固定使用包内已确定的镜像、端口、Go 后台并发、文本提取、数据库标识，并自动生成 `DJANGO_SECRET_KEY`、`POSTGRES_PASSWORD`、`USAGE_METRICS_TOKEN` 和 `AGENT_KERNEL_TOKEN`。部署人员只需：

1. 把生产域名写入 `DJANGO_ALLOWED_HOSTS`，配置 DNS、可信 TLS 证书和 HTTPS 反向代理或企业网关；所有路径统一转发到 app 暴露端口，页面与 API 共用 app 的同一端口。同机反代建议将 `FRONTEND_BIND` 改为 `127.0.0.1`。
2. 按下文补齐 W3 OAuth2 配置后，再次执行同一条部署命令。

生产只提供 W3 登录，因此 W3 OAuth2 是可用部署的必要条件。模板中的 `W3_OAUTH2_ENABLED=False` 只用于首次安全生成 `.env`；正式部署前须保持 `DJANGO_DEBUG=False`、通过安全渠道把 W3 开关改为 `True`，并填写 client id、authorize/token/userinfo HTTPS 地址、精确 redirect URI、工号/邮箱字段路径、客户端认证方式、超时和事务有效期。`W3_OAUTH2_REDIRECT_URI` 必须与反向代理的 HTTPS 域名完全一致，例如 `https://resume.example.com/api/auth/w3/callback/`。当前 UserInfo 顶层字段映射已预填为 `W3_OAUTH2_EMPLOYEE_NO_FIELD=employeeNumber`、`W3_OAUTH2_EMAIL_FIELD=email`；`tenantId`、`uuid`、`globalUserID` 当前不参与账号匹配。机密客户端还必须填写 client secret，scope 按 W3 要求填写。部署脚本会在任何 Docker 变更前校验，DEBUG 开启、W3 关闭或配置不完整都会停止。本地密码 API、Django Admin 路由和本地登录开关均不存在；DEBUG 开发令牌不是生产应急入口。

离线部署的最短流程仍必须经过部署 Skill，不允许用 `docker compose init/up` 绕过 W3、域名和已有数据保护校验：

```bash
sha256sum -c SHA256SUMS
bash smart-resume-offline-deploy-skill/scripts/deploy.sh
```

首次执行只创建 `.env` 和四项随机密钥后退出；补齐生产域名、反向代理、W3 配置后，再次执行同一条部署命令完成镜像导入、初始化和启动。检测到已有安全的 `USAGE_METRICS_TOKEN`、`AGENT_KERNEL_TOKEN` 时不会轮换；旧环境缺少其中一项时只补齐新密钥，不替换其它密钥。

启动后应有 `app`、`agent-kernel`、`db`、`redis` 四个常驻容器。Go app 统一提供 React 页面、业务 API 和后台任务；`resume-platform healthcheck` 检查 HTTP、PostgreSQL 和 Redis。查看业务日志使用 `docker compose logs -f app`。升级时沿用原 `COMPOSE_PROJECT_NAME`，更新 `APP_VERSION` 和 app 镜像引用；本次协议升级还需同步更新 `AGENT_KERNEL_IMAGE` 和 `AGENT_KERNEL_VERSION`，保留原密钥、W3 和现场 CA 配置。

**模型路由器使用企业 CA 时，必须为平台和 Kernel 配置 CA。** 将现场已有的 CA PEM 文件绝对路径写入 `.env` 的 `AGENT_KERNEL_CA_BUNDLE`，保持 `AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY=False`。部署脚本向两端自动只读挂载并设置 `SSL_CERT_FILE`；文件须可被容器内 `agent` 用户读取。CA 内容更新后需重建平台和 Kernel 容器。

平台模型 TEST 与 Kernel 分析均默认校验 TLS，TEST 成功不代表 Kernel 信任了模型路由器。`verify.sh` 只检查 CA 挂载是否可读，还必须从简历库完成一次真实 Agent 分析。完整命令及直接使用 Compose 时的覆盖文件要求见 [部署 Skill](smart-resume-offline-deploy-skill/SKILL.md)。

验证：

```bash
bash smart-resume-offline-deploy-skill/scripts/verify.sh
```

Grafana JSON 数据源使用 `GET /api/analytics/usage/overview/`，从安全配置注入 `.env` 中的 `USAGE_METRICS_TOKEN` 并发送 `X-Usage-Metrics-Key` 请求头。查询支持 `date_from`、`date_to`、`granularity=hour|day|week` 和可选页面筛选；默认最近 30 天、最长 90 天，按 `Asia/Shanghai` 聚合，不返回个人名单。不要把密钥写入面板 JSON、文档或命令历史。通过安全方式注入当前 shell 后执行最小验证：

```bash
curl --fail --silent --show-error \
  -H "X-Usage-Metrics-Key: ${USAGE_METRICS_TOKEN}" \
  "https://resume.example.com/api/analytics/usage/overview/?granularity=day"
```

停止服务但保留数据：

```bash
docker compose --env-file .env down
```

数据库和上传文件位于 Docker volumes。升级时保留数据卷和原 `.env`；部署脚本会清理同项目中已移除的服务容器，不删除数据卷，也不会重新生成已有安全密钥。
