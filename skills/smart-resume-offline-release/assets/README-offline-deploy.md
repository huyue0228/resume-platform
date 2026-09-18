# 海纳智聘离线部署

## v5.0.0 部署范围与岗位配置

本包配套平台 v5.0.0、Kernel v5.0.0 和 resume-contracts 5.0.0，筛选协议为 `resume-analysis/v5`，独立分配协议保持 `resume-allocation/v1`。筛选与分配路径分别为 `/v2/tasks/execute`、`/v2/allocation/tasks/execute`。

v5 将单次上下文上限与任务累计 Token 预算分开，默认分别为 32768 和 120000，并在分析详情展示逐轮模型耗时、Token 和停止原因。平台与 Kernel 必须配套使用 v5，不复用旧版冻结任务。

候选人入池状态统一在简历库筛选，处理和分配任务统一在“任务中心”。首次初始化不再生成示例部门人员授权；请导入或新增真实人员授权后开展部门流转。

**本版仅支持新数据库部署，不提供 v4 及更旧版本的原位升级或历史数据迁移。** 旧环境的 Compose 项目、数据卷、配置与密钥必须保留。创建独立的 v5 项目和空数据卷，不能把本版指向旧业务库，也不能通过删除旧卷来绕过。下文的保留项目名、密钥和数据说明仅适用于已建立的 v5 实例。

在第一次执行部署脚本前，选择一个尚未使用的项目名，例如 `export COMPOSE_PROJECT_NAME=smart-resume-filter-v5`；生成 `.env` 后把其中的 `COMPOSE_PROJECT_NAME` 同步为该名称。与旧实例同机部署时另选空闲端口，保留旧实例。后续运维固定使用这一 v5 项目名。

管理员在「岗位管理 → 筛选与分配配置」维护能力标签、投递标准及部门规则。创建或导入岗位会生成投递标准与职位池，职责和需求专业来自岗位；同一投递的来源冲突需要明确选择。自动生成不会猜测能力标签或启用部门规则。处理前检查可提示岗位配置阻塞，修复后可批量重新处理；入池后的独立分配在任务中心查看、试算和执行。专业大类词表、HC 容量控制和历史分配模式已移除。

部门必须配置可登录且具备反馈权限的有效接口人；当前岗位配置预检尚不覆盖接收人可用性。WeLink 下发目前记录业务状态和通知占位信息，没有真实消息发送；应由部门登录系统处理，不能把下发成功视为通知送达。

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

首次执行只创建 `.env` 和四项随机密钥后退出；补齐生产域名、反向代理、W3 配置后，再次执行同一条部署命令完成镜像导入、初始化和启动。检测到已有安全的 `USAGE_METRICS_TOKEN`、`AGENT_KERNEL_TOKEN` 时不会轮换；既有 v5 实例缺少其中一项时只补齐新密钥，不替换其它密钥。

启动后应有 `app`、`agent-kernel`、`db`、`redis` 四个常驻容器。Go app 统一提供 React 页面、业务 API 和后台任务；`resume-platform healthcheck` 检查 HTTP、PostgreSQL 和 Redis。查看业务日志使用 `docker compose logs -f app`。维护既有 v5 实例时沿用其 `COMPOSE_PROJECT_NAME`、数据卷和密钥；app 与 Kernel 必须使用已验收的配套版本，W3 和现场 CA 配置保持有效。此项不表示支持将旧版本数据库升级为 v5。

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

数据库和上传文件位于 Docker volumes。维护既有 v5 实例时保留数据卷和原 `.env`；部署脚本会清理同项目中已移除的服务容器，不删除数据卷，也不会重新生成已有安全密钥。
