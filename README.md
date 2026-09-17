# 海纳智聘

面向校园招聘的简历筛选平台。Go 程序提供业务 API、后台任务和嵌入的 React 页面；PostgreSQL 保存业务与任务状态，Redis 提供通知和并发控制。独立 Go Agent Kernel 只分析平台提取的完整文本与当前投递评估标准。

## 运行流程

提交任务 → 查重与有效志愿排序 → 学校/学历准入 → 固定当前投递标准 → PDF 全文提取 → Kernel 契合度评估与能力标签 → 达标直接入池 → 独立任务按标签和需求供给分配部门。

提交接口只冻结范围和任务节点并返回 HTTP 202。后台逐份提取、分析和保存；单份失败不会阻断其他简历。取消任务会停止提取进程和模型请求，并阻止后续结果写入。重试按文件 SHA256、提取器版本及冻结分析输入判断可复用内容。

平台使用 Poppler 的 `pdfinfo`、`pdftotext -layout` 和 `pdfimages`。按页保存全文，保留空白页、页内换行和全局行号；不使用 OCR。无有效文本、疑似扫描页、字体或字符映射诊断均进入材料待处理状态。上传总量、单文件、ZIP 解压总量及 PDF 文件体积不设应用层上限；大文件上传暂存和简历包解压使用磁盘，需预留临时及材料存储空间。反向代理应设置 `client_max_body_size 0` 并配置适当的上传超时。PDF 提取最多 100 页、全文最大 1 MiB，提交 Kernel 前校验实际 JSON 请求不超过 2 MiB；不会静默截断。

平台保留既有业务 API、Token 会话、W3/RBAC、导入导出、简历库、配置、分配反馈与统计接口。`/api/auth/login/`、`/admin/` 和匿名 `/media/` 不开放；简历预览和下载经过鉴权 API。学校、学历、志愿、岗位池、HC、人工工作流与最终业务写入均归平台负责。

## 职位池配置与操作

管理员在「岗位需求 → 筛选与分配配置」维护标准来源、能力标签、内部职位池和部门规则，支持批量编辑及保存前影响预览。岗位新增、修改、删除和导入会同步维护关联：同主体、同投递名称生成一个标准，同主体、同内部职位生成一个池。职责、学历和专业要求来自岗位资料。同名投递存在不同要求时必须明确选择来源岗位；系统不会拼接或猜测标准。

能力标签按原文证据提取，分为专业方向、技术技能、实践经历；专业大类词表及别名接口已移除。新需求规则是停用草稿，管理员定义标签、将标签纳入筛选标准后再启用。配置不全时明确区分“不能筛选”和“可入池、暂不能分配”。提交处理前检查当前范围；修复后可批量重新处理配置受阻或需要重评的候选人。

契合度达到入池阈值（默认 0.75）即保存入池资格和独立分配工作项，没有人工复核阶段。分配仅访问同主体、同池的有效接收需求：必需标签全部满足，优先标签命中数降序、规则优先级升序、近 7 天供给升序、最后分配序号升序、需求 ID 升序。HC 只作为计划人数，HC=0 和非公开岗位也可接收。无标签匹配或接收需求时保留资格等待；调整分配规则后不重复调用筛选模型。

标准、标签定义、正文或候选人基础资格变化会要求重评；配置保存立即使受影响的待分配资格失效并取消其分配工作项，版本和操作记录可追溯。切换有效志愿关闭旧资格，已下发成员不自动改派。分配任务、试算、暂停、重试和供给统计统一在「任务中心 → 分配任务」。详见 [筛选与分配运维](ops/two-agent-allocation.md)。

## 任务创建与查询

简历库和任务中心共用「处理简历」入口，在同一表单中选择范围、名称和执行安排（立即、指定时间、每天、每周）。打开表单时冻结勾选和筛选条件，切换执行方式不改变范围；定时执行按保存条件匹配届时的候选人。时间均为北京时间 UTC+8。

「处理任务」分为「执行记录 / 定时计划」。执行记录支持搜索、状态、来源、创建日期、创建人和计划历史筛选，数据库分页查询；概览统计整个查询范围。计划可暂停、恢复、取消或查看全部执行历史；详情和查询条件可通过 URL 直接访问。操作规则、产品参考及 API 见 [任务中心说明](ops/task-center.md)。

## 技术栈与目录

- Go 1.25：`cmd/resume-platform/`、`internal/platform/`。
- PDF 提取：`internal/pdftext/`；运行环境需要 Poppler 及中文 CMap 数据。
- 固定协议副本：`internal/contract/bundle/`，由 resume-contracts 4.0.0 生成。
- PostgreSQL 结构与 API 元数据：`internal/compat/`。
- React/Vite：`frontend/`；嵌入页面服务位于 `internal/web/`。
- 发布和验收工具：`tools/`、`skills/`；Python 仅用于部分构建与验收工具，不进入业务运行镜像。

## 构建与检查

```sh
cd frontend && npm ci && cd ..
make check
make build
```

`make build` 构建 React 并嵌入 `dist/resume-platform`。独立运行仍需要 PostgreSQL、Redis、Poppler、系统 CA 与时区文件。推荐使用多阶段 Docker 构建，运行镜像不包含 Go/Node/Python 编译工具、Nginx、Gunicorn、Celery 或 Tesseract。

Go 数据库/队列集成测试需要设置指向独立验收环境的 `TEST_DATABASE_URL` 和 `TEST_REDIS_URL`；未设置时相关用例会明确跳过。GitHub CI 启动临时 PostgreSQL/Redis 并启用这些测试。PDF 样本生成器与 HTTPS 合成模型服务位于 `tools/acceptance/`，仅用于测试。

历史 API 对照可从 Git 的 v1.2.0 检出临时旧平台，使用 `tools/acceptance/export_legacy_api.py --backend <旧检出目录/backend>` 导出隔离库响应；Go 仓不再保留 Django 源码。`docs/` 下四份设计文档保留历史版本，当前运行和部署入口以本 README、代码及发布包说明为准。

## 部署

默认四个常驻容器：`app`、`agent-kernel`、`db`、`redis`。`init` 仅首次初始化时运行一次。app 同时提供页面、API 和 Go 后台工作线程，无应用内 Nginx。

完整离线包从 [GitHub Releases](https://github.com/huyue0228/resume-platform/releases) 下载 `.tar.gz` 及对应 `.sha256`，包含四个 amd64 镜像。GitHub 自动生成的 Source code 和 `resume-platform-v*.tar.gz` 在线配置小包都不能代替完整离线包。

```sh
sha256sum -c smart-resume-filter-offline-<构建号>.tar.gz.sha256
tar -xzf smart-resume-filter-offline-<构建号>.tar.gz
cd smart-resume-filter-offline-<构建号>
bash smart-resume-offline-deploy-skill/scripts/deploy.sh
```

首次执行生成 `.env` 和随机密钥后退出；补齐实际域名、W3 OAuth2 配置后再次执行。服务器预先安装 Docker 和 Compose v2；模型服务、W3 和企业 CA 由部署环境提供，不随通用包封装。

生产保持 `DJANGO_DEBUG=False`、W3 登录就绪，并通过企业 HTTPS 网关统一转发到 app 端口。`DJANGO_SECRET_KEY`、`DJANGO_ALLOWED_HOSTS` 等旧环境变量名称为升级兼容保留，实际运行时是 Go。模型连接由有 `settings.manage_ai_connection` 权限的管理员在「系统设置 → AI 模型连接」保存和测试，不从 `.env` 读取模型密钥。

企业模型路由器使用私有 CA 时，在 `.env` 配置 `AGENT_KERNEL_CA_BUNDLE` 的宿主机 PEM 路径。部署脚本将其只读挂载到平台和 Kernel，并设置 `SSL_CERT_FILE`；保持 `AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY=False`。平台 TEST 和 Kernel 分析都默认校验 TLS。健康检查和合成模型测试不能代替一次真实模型任务验收。

```sh
bash smart-resume-offline-deploy-skill/scripts/verify.sh
docker compose logs -f app
docker compose exec app resume-platform healthcheck
```

本地开发可设置 `DJANGO_DEBUG=True` 且关闭 W3，在初始化后为已启用账号签发开发 Token：`resume-platform issue-dev-token --username 012358`。Token 只用于本地开发，不作为生产登录方式；不要写入代码或公开日志。

## 升级和版本

本版本使用 `resume-analysis/v4`，结果为 `resume-application-assessment/v1`，固定契约版本 4.0.0。HTTP 路径仍为 `/v2/tasks/execute`，每次只接受一个当前投递标准；全文格式仍为 `ResumeTextV2`。v4 删除 `scope.taxonomy` 和内置专业词表查询工具。分配使用 `resume-allocation/v1`。

本次面向新数据库部署，不提供旧任务、专业词表、HC 容量或历史分配模式的数据迁移。平台与 Kernel 必须使用配套协议构建，部署到独立数据库和媒体目录；不得把新版本直接覆盖连接旧数据库，也不要删除已有数据卷。

代码版本回退使用 Git 提交与不可变标签；Git 不回退运行中的数据库或上传文件。清理源码时不创建副本。发布制品上传 GitHub 并回下载验证后，清理本地临时发布包，不长期保留 `release/`、版本分发目录或历史代码备份。
