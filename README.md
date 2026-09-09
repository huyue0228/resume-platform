# 海纳智聘

“海纳智聘”是面向校园招聘的智能简历筛选平台。候选人主流程为「查重与志愿排序 → 院校和学历 Policy Gate → 固定当前岗位/部门引用 → Agent Kernel 证据化筛选 → Django 校验并落库」。岗位需求、部门和接口人是独立维护的基础数据。Django 负责认证、RBAC、业务约束和最终写入；独立 Go Agent Kernel 只运行模型和白名单只读工具，返回无写权限的结构化建议。

`docs/` 保留既有产品设计材料，供追溯历史决策；当前工程边界和运行约定以源码、迁移与 `AGENTS.md` 为准，不自动同步四份设计文档。

当前实现已包含：候选人聚合简历库、批量删除、招聘分析看板、精确处理结果/原因筛选、可拖拽列宽、PDF 预览和筛选导出、W3 OAuth2 登录与项目 Token 会话、RBAC，以及真实 PDF/OCR 解析、Agent Kernel 只读工具循环、版本钉死、证据复核、后台智能路由审计和 HR 处置闭环。

> 上生产前仍需完成真实数据隐私评审、模型评测、容量压测和外部系统联调；本轮不包含 worker heartbeat 超时恢复、Prometheus/集中日志/告警、3 万条压测、CI 或浏览器 E2E。

关键实现落点：

- 后端分页：`backend/apps/api/pagination.py`，统一支持 `page_size`，最大 500。
- 后端导出/预览/筛选：`backend/apps/api/views.py`，候选人、岗位、院校、接口人和分配尝试列表按查询参数过滤。
- 后端测试：`backend/apps/api/tests.py` 覆盖分页、表头筛选、候选人导出、简历预览和接口人 replace 导入边界。
- 前端共享表格能力：`frontend/src/components/DataTableControls.jsx`、`frontend/src/components/ResizableHeaderCell.jsx`、`frontend/src/index.css`。
- 前端简历预览：`frontend/src/components/ResumePreview.jsx`，简历库详情和分配尝试详情复用。

## 技术栈

- 后端：Django 4.2、Django REST Framework、Celery。
- 智能内核：独立仓库的 Go 1.25 编译二进制，使用 `resume-analysis/v1` 分析协议。
- 前端：Vite、React 18、Ant Design Pro、JavaScript `.jsx`。
- 本地开发：SQLite + Celery eager，同步执行任务，不依赖 Redis。
- 生产预期：PostgreSQL + Redis + Celery `default` worker + threads `ai` worker。

## 目录结构

```text
backend/    Django + DRF 后端，包含 accounts/core/ingestion/pipeline/api
backend/resume_contracts/ 固定版本的跨仓协议分发包
frontend/   Vite + React 前端
docs/       四篇核心设计文档与原始材料
```

## 本地启动

### 后端

```bash
cd backend
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
python manage.py migrate
python manage.py seed_base
python manage.py gen_sample
python manage.py load_sample
python manage.py runserver 8000
```

`seed_base` 会初始化：

- RBAC 权限点与预置角色。
- AI、WeLink 等非敏感运行配置。
- 多接口人功能测试账号和对应 Contact/Department。

### 前端

```bash
cd frontend
npm install
npm run dev
```

前端默认运行在 [http://localhost:5173](http://localhost:5173)，并通过 Vite proxy 访问后端 `/api/`。

## Docker Compose 部署手册

本节用于没有 Codex/Agent 工具的服务器环境。服务器上只需要 Git、Docker 和 Docker Compose v2，按下面步骤操作即可构建项目镜像并拉起完整栈：

- PostgreSQL：业务数据库。
- Redis：Celery broker/result backend。
- Agent Kernel：独立 Go 二进制，只访问任务信封、白名单只读工具和管理员已配置的模型服务。
- app 业务镜像：包含 React 构建产物、Nginx、Django/Gunicorn 和两个 Celery 队列进程。普通队列采用 prefork，AI 队列采用 threads；各进程独立运行，由统一入口管理生命周期。

当前 `docker-compose.yml` 适合内网试运行、验收和单机部署。W3 生产回调要求 HTTPS 域名，因此正式部署必须在 app 前配置 HTTPS 反向代理、WAF 或企业统一网关；外层入口统一转发到 app，由项目内置 Nginx 托管静态资源并继续转发 `/api`。

app 容器内通过 Nginx `proxy_pass http://127.0.0.1:8000` 访问 API；本地非 Docker 开发时 Vite 仍默认代理到 `http://localhost:8000`。

### 1. 服务器前置条件

推荐服务器环境：

```bash
docker --version
docker compose version
git --version
```

建议版本：

- Docker Engine 24+。
- Docker Compose v2。
- 同机反向代理只向客户端开放 `80/443`，frontend `5173` 绑定回环地址；异机企业网关仅通过受控内网访问 frontend `5173`。backend `8000` 不对客户端或外层网关开放。
- 至少 2 CPU / 4GB 内存；真实 AI 任务或大批量简历处理建议 4 CPU / 8GB 以上。

### 2. 拉取代码

```bash
git clone https://github.com/huyue0228/smart-resume-filter.git
cd smart-resume-filter
git fetch --tags origin
git checkout --detach <交付说明中的发布标签或 commit>
```

如果服务器已经有旧代码：

```bash
cd smart-resume-filter
git fetch --tags origin
git checkout --detach <交付说明中的发布标签或 commit>
```

### 3. 创建服务器 `.env`

推荐直接使用项目部署脚本。首次运行会创建权限为 `600` 的 `.env`，自动生成四项独立随机密钥，然后退出等待确认目标服务器信息：

```bash
bash skills/smart-resume-offline-deploy/scripts/deploy.sh
```

镜像版本、端口、Gunicorn/worker/OCR 参数、数据库名与用户均已预先写入模板。现场只需确认：

- `DJANGO_ALLOWED_HOSTS`：生产环境填写反向代理对外域名，多个值用英文逗号分隔。
- `AGENT_KERNEL_CA_BUNDLE`：模型路由器使用企业 CA 时，填写现场 CA PEM 文件的宿主机绝对路径；部署脚本自动只读挂载到 `agent-kernel` 并设置 `SSL_CERT_FILE`。

确认后再次运行同一命令即可部署。`DJANGO_SECRET_KEY`、`POSTGRES_PASSWORD`、`USAGE_METRICS_TOKEN` 和 `AGENT_KERNEL_TOKEN` 由脚本从 `/dev/urandom` 自动生成且不回显。检测到旧容器或旧数据卷时，脚本绝不替换已有安全值；缺少新增的监控或 Kernel 密钥时只补齐缺失项。升级时必须保留原 `.env`，否则数据库可能无法连接，既有 AI 连接密文也可能无法解密。

系统生产只提供 W3 登录，因此 W3 OAuth2 是可用部署的必要条件。模板中的 `W3_OAUTH2_ENABLED=False` 只是首次生成 `.env` 的安全占位；正式部署前必须改为 `True`，填写 client id、授权/Token/UserInfo HTTPS 地址、工号和邮箱字段路径、客户端认证方式、超时、事务有效期，并把 `W3_OAUTH2_REDIRECT_URI` 设置为 W3 平台登记的精确地址，例如 `https://你的域名/api/auth/w3/callback/`。当前 W3 UserInfo 的工号和邮箱字段分别为顶层 `employeeNumber`、`email`，模板已预填；`tenantId`、`uuid`、`globalUserID` 不参与账号匹配。机密客户端还必须填写 client secret，scope 按 W3 要求填写。部署脚本会在任何 Docker 变更前强制 `DJANGO_DEBUG=False` 并校验 W3，DEBUG 开启、W3 关闭或配置不完整都会停止且不显示密钥。系统不提供本地密码登录，模板也不包含本地登录开关。

源码部署到 ARM 服务器时才需要把 `DOCKER_PLATFORM` 改为 `linux/arm64`；离线发布包已经固定为 `linux/amd64`，`APP_VERSION` 也由发布脚本写入，不在部署现场决定。

启用 Agent 处理前，使用管理员账号进入「系统设置 → AI 模型连接」完成连接配置并执行测试。页面配置 Base URL、可选访问令牌和 API 风格，并通过 OpenAI-compatible `GET /models` 获取模型 ID（也可直接输入），不配置服务商/Profile。模型连接只从系统设置中的数据库配置读取。API Key 非空时仅加密保存且不会回显；无鉴权内网服务可留空。提交后在后台检查模型和 Kernel；未就绪会在任务中心的“检查处理服务”节点显示失败，后续节点标为未执行。

**模型 TEST 成功不等于 Agent 模型链路可用。** 当前平台 TEST 使用 `verify=False`，而 Agent Kernel 默认校验 TLS；宿主机或 backend 拥有 CA，不代表 Kernel 容器也拥有。企业 CA 的挂载、权限、更新重建及真实 Agent 分析验收步骤见 [部署 Skill](skills/smart-resume-offline-deploy/SKILL.md)。

`RUN_SEED_BASE` 默认保持 `0`。不要在长期运行环境里把它改成 `1`，否则每次 app 重启都可能把配置页中的参数重置为种子默认值。首次初始化请使用下一节的一次性 `init` 命令。

上传大小不再设置固定 Nginx 上限：`client_max_body_size 0` 表示由服务器磁盘、Docker volume、CPU/内存和超时时间决定实际可处理上限。大文件上传临时目录默认是 `/app/media/tmp_uploads`，位于 `media_data` 持久化卷；`GUNICORN_TIMEOUT` 默认 `1800` 秒。

### 4. 首次启动完整栈

```bash
docker compose build
docker compose --profile init run --rm init
docker compose up -d
```

首次执行 `docker compose build` 会下载基础镜像、安装依赖并生成 app、PostgreSQL、Redis 三个平台镜像；Agent Kernel 使用独立发布的镜像，时间会比较久。查看启动状态：

```bash
docker compose ps
docker compose logs -f app
```

app 启动时先执行迁移，再启动 Web、API 和后台队列进程：

```bash
python manage.py migrate
python application.py
```

一次性 `init` 命令会执行迁移和 `seed_base`，初始化 RBAC 权限、预置角色、配置项、功能测试账号、接口人和部门基础数据。后续升级和重启只执行迁移，不重复 seed，避免覆盖管理员在配置页维护的参数。

### 5. 访问系统

本地或隔离内网试运行且未配置反向代理时，才可临时访问：

```text
http://服务器IP:5173/
```

生产环境只通过 `https://生产域名/` 访问。外层反向代理把所有路径统一转发到 frontend 暴露端口，frontend 容器再把 `/api` 转发到 backend；不要让浏览器或外层网关绕过 frontend 直接调用 `5173` 或 `8000`。同机反代建议把 `FRONTEND_BIND` 设置为 `127.0.0.1`，异机企业网关应通过受控内网和防火墙访问 frontend。

初始化会创建以下功能测试账号；所有账号密码均不可用，生产只能通过匹配工号和邮箱的 W3 身份登录：

| 用户名 | 角色 | 用途 |
| --- | --- | --- |
| `admin` | 管理员 | 配置项、用户、角色、权限管理 |
| `hr` | HR | 数据导入、流水线处理、分配、下发、AI 复核 |
| `L2001` | 二级接口人 | 查看 HR 下发给技术二部的分配并转派三级 |
| `L2002` | 二级接口人 | 查看 HR 下发给产品二部的分配并转派三级 |
| `T3001` | 三级接口人 | 查看转派给自己的分配并反馈 |
| `T3002` | 三级接口人 | 查看转派给自己的分配并反馈 |
| `T3003` | 三级接口人 | 查看转派给自己的分配并反馈 |

### 6. 可选：生成并加载样例数据

如果服务器用于演示或验收，可以加载样例数据：

```bash
docker compose exec app python manage.py gen_sample
docker compose exec app python manage.py load_sample
```

加载后刷新前端页面，即可在简历库、岗位、院校和接口人页面看到样例数据；分配结果在简历库中查看。

如果服务器承载真实数据，不要执行样例数据命令。

### 7. 页面使用要点

- 简历库按候选人聚合展示，一名候选人一行；详情抽屉可查看全部投递、分配尝试、反馈和 PDF 预览。
- 简历库、岗位、院校、接口人等主要表格支持表头筛选和列宽拖拽；筛选在后端执行，分页接口支持 `page_size`，单页最大 500。
- 简历库可以导出选中候选人或当前筛选全部，候选人详情中的分配尝试也复用同一导出弹窗。每次打开默认只下载 `简历库清单.xlsx`；勾选“同时下载简历原件”后才返回包含 Excel、简历原件和可选缺失清单的 ZIP，并提示候选人数、原件数和缺失数。
- 部门接口人导入要求“工号 + 邮箱”，并自动创建或更新同工号、同邮箱的登录账号；每次同步都保持密码不可用。清空重导时，本次文件中不存在的旧接口人及绑定账号会同步停用。

### 8. 常用运维命令

查看所有服务状态：

```bash
docker compose ps
```

查看日志：

```bash
docker compose logs -f app
docker compose logs -f agent-kernel
docker compose logs -f db
docker compose logs -f redis
```

重启服务：

```bash
docker compose restart app
```

停止服务但保留数据库卷：

```bash
docker compose down
```

停止并删除数据库卷，慎用，会清空 PostgreSQL 数据：

```bash
docker compose down -v
```

进入后端容器执行 Django 命令：

```bash
docker compose exec app python manage.py check
docker compose exec app python manage.py migrate
docker compose exec app python manage.py seed_base
```

### 9. 更新版本

每次发布新代码后，在服务器项目目录执行：

```bash
git fetch origin
git checkout main
git pull --ff-only origin main
docker compose build
docker compose up -d --remove-orphans
docker compose ps
```

v1.2.0 起默认四个常驻容器，名称为 `${COMPOSE_PROJECT_NAME}-app`、`-agent-kernel`、`-postgres`、`-redis`；默认前缀为 `smart-resume-filter`。升级必须保持原项目名和数据卷。在线发布包改用 `APP_IMAGE`，不再使用 `BACKEND_IMAGE`/`FRONTEND_IMAGE`；按包内 `images.env` 更新三个平台镜像引用，保留原密钥、W3、Kernel 与 CA 配置。app 发布/重启会同时重启 Web、API 和后台进程；运维应先排空处理任务。

当前 Compose 在本机构建业务平台镜像，Agent Kernel 改为消费 `AGENT_KERNEL_IMAGE` 指定的独立发布镜像，build 由 `AGENT_KERNEL_VERSION` 固定。仓库边界、模拟开发与发布兼容说明见 [REPOSITORIES.md](REPOSITORIES.md)。只改 `.env` 时不需要重新 build，只需 `docker compose up -d`。

如果新版本明确要求重新初始化基础权限或新增种子字典，再手动执行：

```bash
docker compose --profile init run --rm init
```

不要把 `RUN_SEED_BASE` 长期开成 `1`。

更新后建议检查：

```bash
docker compose exec app python manage.py check
docker compose logs --tail=100 app
```

### 10. AI Agent 配置

「系统设置 → AI 模型连接」包含“模型连接”“AI 运行参数”和“AI 专项”三个页签。AI 运行参数页维护：

- `ai_dispatch_threshold`
- `ai_review_threshold`
- `ai_timeout_seconds`
- `ai_concurrency`（所有 worker/运行共享的自适应并发上限，默认 8，范围 1–20）
- `ai_retry_count`
- `ai_retry_backoff_seconds`

模型连接和运行参数均由 `settings.manage_ai_connection` 保护；管理员角色默认拥有该权限，也可在「用户权限」按角色授予。模型连接页配置共享内网 Base URL、API 风格和可选 API Key，通过 `GET /models` 获取模型 ID，同时允许直接输入模型 ID，并执行一次最小真实模型测试，不展示服务商/Profile。API Key 非空时仅可写入；服务端用 Django `SECRET_KEY` 派生的 Fernet 密钥加密存储，GET、前端状态和测试结果都不会返回明文或密文。未获授权的 HR 和接口人不可见、不可调用相关配置、模型发现和测试接口。系统不再提供全局 AI 分配开关；当前完整连接配置测试有效时，上传和处理简历才可选择 AI。

「系统设置 → Prompt 管理」（`/prompt-management`）复用同一 `settings.manage_ai_connection` 权限，管理员以整套方式维护五个固定业务模块：筛选角色与任务目标、学历/院校/志愿/当前岗位等业务边界、专业/项目/实习/技能/岗位职责评价口径、AI 专项人才识别口径，以及院校省份/校区/分校判断口径。后端始终按固定顺序组装，并追加不可编辑的最小安全底座、动态 JSON 数据、省份白名单和 Pydantic JSON Schema/结构化输出协议；简历、岗位职责和院校名称中的指令一律按不可信数据处理，不能改变任务、固定岗位或输出协议。管理员不能编辑模板变量、载荷字段或 Schema。

Prompt 采用“共享草稿 → 真实模型测试 → 原子发布 → 历史恢复”流程。五个模块均必填，保存时移除 NUL 和首尾空白，单模块最多 8,000 字符、整套最多 24,000 字符，未知或缺失模块会被拒绝；保存、重置、发布和历史恢复携带 `lock_version`，并发覆盖返回 409。真实测试使用内置脱敏简历和院校样例，分别走当前 `responses` 或 `chat_json` 正式调用路径，只保存模型名、时间和脱敏摘要，不保存原始模型响应，也不返回内部连接指纹。草稿或模型连接变化会使 Prompt 测试失效；只有草稿内容哈希、当前连接指纹和成功测试仍一致时才能发布。迁移初始化激活版本 `resume-screening-v2` 和一份相同的未测试草稿，后续激活版本命名为 `prompt-vNNNNNN-<hash8>`；发布时旧激活版本归档并自动创建同内容的新草稿，历史恢复只复制到草稿，仍需重新测试和发布。

“AI 专项”页签维护默认关闭的 `ai_special_route_enabled / ai_special_route_threshold / ai_special_route_secondary_contact_id / ai_special_route_tertiary_contact_id`：获授权用户选择父级二级接口人后，只能选择其下属三级接口人作为固定目标；已启用状态下切换目标时，页面会先安全关闭专项、更新链路并按最终开关状态恢复。专项命中、证据和内部审计仍不在候选人详情、处理原因或招聘分析中展示。专项证据不足或目标配置失效时会继续普通 AI 结论，不产生候选人报错。

模型连接仅由管理员保存的数据库配置决定；运行时不会读取部署环境变量中的 API 风格、模型、Base URL 或 API Key，也不读取模型服务商/Profile 模板。通常无需为改动模型连接重启 backend/worker。

AI 运行中的连接异常会继续写入 `AgentDispatchDecision.error_code` / `error_message`，但内容仅为稳定错误码和脱敏摘要。第三方 SDK 原始异常不会进入数据库、API 响应或日志；backend 和 worker 标准输出仅记录 model、api_style、错误码和异常类型。排查时执行：

```bash
docker compose logs --tail=200 app
docker compose logs --tail=200 agent-kernel
```

### 11. 服务器安全检查

上线前至少完成：

- `.env` 不提交 Git，不复制到公开位置。
- `.env` 权限为 `600`，且 `DJANGO_SECRET_KEY`、`POSTGRES_PASSWORD`、`USAGE_METRICS_TOKEN`、`AGENT_KERNEL_TOKEN` 已由首次部署脚本分别生成强随机值。
- `DJANGO_DEBUG=False`。
- `DJANGO_ALLOWED_HOSTS` 只填写实际 IP/域名，避免长期使用 `*`。
- PostgreSQL `5432` 和 Redis `6379` 不暴露到公网；如无外部访问需求，只允许内网或安全组限制。
- W3 启用前核对授权、Token、UserInfo 地址均为 HTTPS，`redirect_uri` 与平台登记值完全一致；本地密码登录和 Django Admin 路由均不存在。
- 内置受保护管理员使用工号 `012358`、邮箱 `huyue2@ueascend.com` 进行 W3 双字段映射；该账号无本地可用密码且不可通过用户管理编辑、停用或删除。

### 12. 常见问题

`backend` 反复重启：

```bash
docker compose logs --tail=200 app
```

重点看数据库连接、迁移错误、环境变量拼写和 `DJANGO_ALLOWED_HOSTS`。

前端页面能打开但接口失败：

```bash
docker compose logs --tail=200 app
```

当前前端容器使用 Nginx 托管静态资源，`/api` 会反代到 backend。确认 `backend` 服务健康；内网试运行时检查 `FRONTEND_PORT`，生产环境则从 HTTPS 域名和外层反向代理入口排查。

数据库密码改了但服务起不来：

如果 PostgreSQL 数据卷已经初始化，单纯修改 `.env` 的 `POSTGRES_PASSWORD` 不会自动修改旧数据库用户密码。试运行环境可清空重建：

```bash
docker compose down -v
docker compose up -d
```

真实数据环境不要执行 `down -v`。应在 PostgreSQL 内修改用户密码，并同步更新 `.env`。

镜像构建慢：

首次 `docker compose build` 会安装 Python 和 npm 依赖，慢是正常现象。后续只要依赖文件没有变化，Docker 会复用缓存；如果服务器无法访问 Docker Hub、PyPI 或 npm registry，需要提前在可联网环境构建并导出完整离线镜像包，再在服务器 `docker load`。

## 本地开发账号

`seed_base` 初始化以下账号，但不会生成密码；所有账号都保存 Django 不可用密码标记。

| 用户名 | 角色 | 用途 |
| --- | --- | --- |
| `admin` | 管理员 | 配置项、用户、角色、权限管理 |
| `hr` | HR | 数据导入、流水线处理、分配、下发、AI 复核 |
| `L2001` | 二级接口人 | 查看 HR 下发给技术二部的分配并转派三级 |
| `L2002` | 二级接口人 | 查看 HR 下发给产品二部的分配并转派三级 |
| `T3001` | 三级接口人 | 查看转派给自己的分配并反馈 |
| `T3002` | 三级接口人 | 查看转派给自己的分配并反馈 |
| `T3003` | 三级接口人 | 查看转派给自己的分配并反馈 |

这些账号用于权限链路的本地测试。W3 登录按 UserInfo 顶层 `employeeNumber` 与 `email` 同时匹配已有且启用的 `User.username + User.email`，不会自动创建账号，并继续复用 RBAC 角色和接口人 `Contact`。

仅在本地 `DEBUG=True` 且 W3 未就绪时，可从 `backend/` 为一个既有且启用的账号签发开发令牌：

```bash
python manage.py issue_dev_token --username admin
```

命令每次都会废止该账号的旧 Token。把新令牌粘贴到登录页“开发令牌”输入框后，前端先调用 `/api/me/` 验证，成功才保存本地登录态。非 DEBUG、账号不存在或已停用时命令会拒绝执行；开发令牌不是生产应急登录方式。

## 权限与配置

系统生产只提供 W3 OAuth2 登录，本地密码 API 和 Django Admin `/admin/` 路由均已删除，访问 `/api/auth/login/` 或 `/admin/` 都返回 404。W3 登录时，服务端完成授权码和 UserInfo 交换，再通过浏览器 Session 一次性交付项目 DRF Token；前端随后调用 `/api/me/` 获取用户、角色、权限码、绑定接口人和数据范围。用户新增/编辑页面不维护密码，用户 API 收到 `password` 字段时返回 400。

权限边界：

- 管理员：维护用户、角色、权限、认证与安全配置，也可维护业务规则。
- HR：导入主数据、运行处理流程、手动分配、下发二级接口人、查看全部分配，并维护获授权的院校准入、专业词表等业务规则。
- 二级接口人：只能查看下发给自己的分配，只能转派给本二级部门下的三级接口人。
- 三级接口人：只能查看转派给自己的分配，并提交通过/未通过反馈。

「系统设置 → AI 模型连接」的“AI 运行参数”页签维护：

- `ai_dispatch_threshold`
- `ai_review_threshold`
- `ai_timeout_seconds`
- `ai_concurrency`（所有 worker/运行共享的自适应并发上限，默认 8，范围 1–20）
- `ai_retry_count`
- `ai_retry_backoff_seconds`

「配置项」已删除“系统参数”页签，只保留院校标签、院校准入和专业词表等业务配置。`welink_enabled` 开关移动到「数据管理 → 部门接口人」页面，由部门接口人维护权限控制；全局 AI 分配开关不再提供。

拥有 `settings.manage_ai_connection` 的角色可在「系统设置 → AI 模型连接」配置共享内网 Base URL、API 风格和可选 API Key，通过 `GET /models` 选择或直接输入模型 ID，并执行最小真实模型测试；同页还集中维护 AI 运行参数和“AI 专项”路由。管理员角色默认拥有该权限，HR/接口人未被授权时不可访问。页面不展示服务商/Profile；专项内部命中证据和审计字段仍不对外展示。Key 非空时仅允许写入、不会被读取接口返回，服务端以由 Django `SECRET_KEY` 派生的 Fernet 密文存储。运行时只读取该数据库配置。日常修改连接请使用授权角色的配置页，避免在 shell、文档或工单中传播 API Key。当前完整连接配置测试成功后，上传和“处理简历”弹窗才会开放 AI 模式；保存连接或清除 Key 后测试状态失效，只能选择 Rule，直至重新测试成功。

同一权限还控制独立「Prompt 管理」页面、菜单、路由和全部 `/api/ai-prompts/` 接口。页面展示激活版本、共享草稿、测试模型/时间、五个独立编辑器和字符计数，支持保存、真实测试、发布、恢复激活值/系统默认值、只读组装顺序预览、历史分页、模块级差异和恢复到草稿；有未保存内容、未测试或测试已失效时不能发布。发布确认明确提示“只影响新提交的 AI 任务”，不会改变模型连接测试状态。

## 主要流程

1. 使用 `admin` 或 `hr` 登录。
2. 在简历库、岗位需求、院校清单、部门接口人页面导入对应 Excel/简历包；岗位表的“工作职责”列必填，缺失职责的岗位行会被跳过并返回行号，其余行继续导入。也可先执行 `gen_sample` 和 `load_sample`。
3. 上传简历后，系统在 Agent Kernel 与模型连接就绪时自动创建一条处理任务；未就绪时导入仍成功，简历保留为待处理。人工“处理简历”不再选择运行模式。任务创建时冻结 Kernel build、协议、工具集、Policy、模型连接和 Prompt 版本；发布新版不会改变已创建或排队任务。Agent 会把当前岗位工作职责（最多 12,000 字符）纳入证据化分析；历史岗位未补工作职责时转为“需处理”，不调用模型。生产 Agent 阶段由有界调度器投递专用 `ai` 队列，所有 AI worker 共享 Redis 自适应并发上限。
4. HR 在简历库查看处理完成、需处理、模型超时失败及精确原因，并处置待下发、待复核和 AI 自动分配结果。后台智能路由不显示独立标签、原因或证据。
5. HR 单条、批量或一键全部下发给二级接口人。
6. 二级接口人登录后仅看到自己的分配，可导出简历并转派本部门三级接口人。
7. 三级接口人登录后仅看到转派给自己的分配，可导出简历并提交反馈。
8. HR/管理员按权限维护业务配置；仅管理员在用户权限页维护 RBAC 和安全设置。

## API 速览

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/auth/login/` | 已删除；访问返回 404 |
| POST | `/api/auth/logout/` | 退出登录并删除 Token |
| GET | `/api/auth/w3/status/` | 返回 W3 是否可用、授权入口和 `debug_token_login_enabled`，不返回密钥或提供方地址 |
| GET | `/api/auth/w3/start/` | 生成 state/PKCE 并跳转 W3 授权地址 |
| GET | `/api/auth/w3/callback/` | 固定 OAuth2 回调；服务端换取身份并按工号、邮箱共同映射账号 |
| POST | `/api/auth/w3/complete/` | 同一浏览器 Session 一次性领取项目 Token 和当前用户 |
| GET | `/api/me/` | 当前用户、角色、权限码、接口人绑定和数据范围 |
| GET/POST/PATCH | `/api/users/` | 用户管理；不暴露密码，携带 `password` 返回 400；内置受保护管理员只读且不可删除 |
| GET/POST/PATCH | `/api/roles/` | 角色管理与角色权限绑定 |
| GET | `/api/permissions/` | 后端预置权限树 |
| GET | `/api/configs/`、`/api/configs/{key}/` | 查询白名单内的非敏感配置项 |
| PATCH | `/api/configs/{key}/` | 更新白名单配置值 |
| POST | `/api/import/` | 上传简历列表、岗位、院校、接口人和简历包；岗位缺工作职责的行跳过并在 `warnings` 返回行号；不接受 `processing_mode`，Agent 未就绪时返回 `agent_processing=pending` |
| GET | `/api/resumes/` | 投递清单 |
| GET | `/api/resumes/{id}/preview/` | 预览单条投递 PDF |
| GET | `/api/candidates/` | 候选人聚合列表 |
| POST | `/api/candidates/bulk-delete/` | 按候选人 ID 批量删除；逐条校验受保护历史并返回成功/失败明细 |
| GET | `/api/candidates/export/` | 按候选人 ID 或当前筛选条件导出；`include_resume_files=false` 返回独立 XLSX，`true` 返回组合 ZIP，缺参按 `true` 兼容旧客户端 |
| GET/POST/PATCH | `/api/jobs/` `/api/schools/` `/api/departments/` `/api/contacts/` | 主数据维护 |
| POST | `/api/pipeline/run/` | 提交 `step + scope` 创建 Agent 运行；`mode`/`modes` 被拒绝，Kernel 或模型连接未就绪时返回 409。生产异步返回 202，本地 `CELERY_TASK_ALWAYS_EAGER=True` 同步完成返回 200，均返回 `processing_runs` |
| GET | `/api/pipeline/runs/` | 处理运行记录 |
| GET | `/api/analytics/recruitment-overview/` | 需要 `analytics.view`；按导入 cohort 返回招聘总览、转化、耗时、趋势和分布，默认最近 30 天，缓存 5 分钟 |
| POST | `/api/analytics/usage/page-view/` | 登录用户静默上报页面访问；事件 ID 幂等，不用于逐条 API 审计 |
| GET | `/api/analytics/usage/overview/` | 使用 `X-Usage-Metrics-Key`，或登录用户具有 `analytics.view`；返回 PV、会话、活跃用户、补零趋势和页面排行 |
| GET/PATCH | `/api/ai-connection/settings/`、`/api/ai-connection/settings/{key}/` | 具有 `settings.manage_ai_connection` 权限时读取/更新 AI 运行参数和“AI 专项”配置；页面按 `runtime / special_route` 分页签展示 |
| GET | `/api/ai-prompts/` | 具有 `settings.manage_ai_connection` 权限时读取五模块定义、限制、系统默认值、只读组装预览、激活版本和共享草稿 |
| PATCH | `/api/ai-prompts/draft/` | 携带完整五模块集合和 `lock_version` 保存共享草稿；校验失败返回 400，并发冲突返回 409 |
| POST | `/api/ai-prompts/draft/reset/` | 携带 `source=active|default` 和 `lock_version`，将共享草稿恢复为激活版本或系统默认值 |
| POST | `/api/ai-prompts/draft/test/` | 对已保存共享草稿执行简历筛选和院校省份两条真实模型测试 |
| POST | `/api/ai-prompts/draft/publish/` | 携带 `lock_version` 原子发布测试仍有效的共享草稿，只影响新提交的 AI 任务 |
| GET | `/api/ai-prompts/versions/` | 分页读取激活和归档 Prompt 历史摘要 |
| GET | `/api/ai-prompts/versions/{version}/` | 读取不可编辑的 Prompt 历史版本及五模块全文 |
| POST | `/api/ai-prompts/versions/{version}/restore/` | 携带当前草稿 `lock_version`，将历史版本复制到共享草稿；仍需重新测试和发布 |
| GET | `/api/workflow-attempts/` | 分配尝试，后端按登录用户过滤数据范围 |
| POST | `/api/workflow-attempts/{id}/dispatch/` | HR 单条下发 |
| POST | `/api/workflow-attempts/bulk-dispatch/` | HR 批量或一键全部下发 |
| POST | `/api/workflow-attempts/{id}/assign-sub-contact/` | 二级接口人转派三级接口人 |
| POST | `/api/workflow-attempts/{id}/feedback/` | 三级接口人提交反馈 |
| GET | `/api/workflow-attempts/export/` | 按尝试数据范围导出；`include_resume_files=false` 返回独立 XLSX，`true` 返回组合 ZIP，缺参按 `true` 兼容旧客户端 |
| GET | `/api/workflow-attempts/{id}/resume-preview/` | 按分配尝试数据范围预览 PDF |
| GET | `/api/agent-decisions/` | AI 决策查看 |
| POST | `/api/agent-decisions/{id}/retry/` | AI 决策重试 |

列表接口默认分页，支持 `page` / `page_size` 查询参数；`page_size` 最大 500。

Grafana JSON 数据源调用使用频率查询接口时，在受控配置中注入 `.env` 的 `USAGE_METRICS_TOKEN`，并发送请求头 `X-Usage-Metrics-Key`。接口支持 `date_from`、`date_to`、`granularity=hour|day|week` 和可选页面筛选；默认最近 30 天，单次范围最长 90 天，按 `Asia/Shanghai` 聚合且不返回个人访问名单。不要把监控密钥写入面板 JSON、仓库、工单或命令历史。通过安全方式把密钥注入当前 shell 后，可做最小连通性验证：

```bash
curl --fail --silent --show-error \
  -H "X-Usage-Metrics-Key: ${USAGE_METRICS_TOKEN}" \
  "https://resume.example.com/api/analytics/usage/overview/?granularity=day"
```

## 验证命令

后端：

```bash
cd backend
./.venv/bin/python manage.py check
./.venv/bin/python manage.py test apps.pipeline apps.api apps.ingestion
./.venv/bin/python manage.py makemigrations accounts core --check --dry-run
```

前端：

```bash
cd frontend
npm run lint
npm run build
```

## 生产与外部系统预留

- W3 认证：非 OIDC 的 OAuth2 Authorization Code 适配层已经实现，默认使用 state 和 PKCE S256，按 UserInfo 顶层 `employeeNumber` 与 `email` 提取工号和邮箱，并同时匹配已有 `User.username + User.email`；字段仍允许通过环境变量覆盖为点路径。`tenantId`、`uuid`、`globalUserID` 当前不参与匹配或落库。仍需公司提供真实端点、客户端凭据、scope、客户端认证方式和生产 `redirect_uri` 后完成联调。
- WeLink：当前下发流程已保留状态和消息 ID 字段，`welink_enabled` 控制是否启用真实外部下发；开关位于「数据管理 → 部门接口人」页面。真实接口确认后在服务层替换发送实现。
- 数据库：本地默认 SQLite；生产环境通过 `POSTGRES_DB`、`POSTGRES_USER`、`POSTGRES_PASSWORD`、`POSTGRES_HOST`、`POSTGRES_PORT` 切换 PostgreSQL。
- Celery：本地默认 `CELERY_TASK_ALWAYS_EAGER=True`；生产环境应配置 Redis broker/backend，并同时启动 Agent Kernel、`default` worker 与 threads `ai` worker。
