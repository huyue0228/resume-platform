# 仓库边界与本地交接入口

`smart-resume/` 是本地工作目录，不是 Git 仓；其下 `resume-platform/`、`resume-agent-kernel/`、`resume-contracts/` 是三个独立私有仓。名称、远端与职责见 `repositories.json`。平台从审核后的当前文件建立新历史，旧 `.git` 保留在本地备份，不推送到新仓。

## 职责

- 平台：React、Django、身份合并、导入、RBAC、主数据、志愿/准入/岗位池、HC、人工工作流与事务。确定性冻结规则入口为 `backend/apps/pipeline/services/admission_snapshot.py`。
- 内核：文档/OCR、画像、池内匹配、模型循环、工具/MCP、证据校验、预算和运行记录。没有数据库依赖。
- 协议：`resume-analysis/v1` 的 Schema、Python DTO、合成样例和模拟服务。平台的 `backend/resume_contracts` 与内核 `internal/contract/bundle` 是固定分发副本，不是两个权威来源。

## 外包开发不需要内核源码或真实模型密钥

从 `backend/` 使用现有依赖环境，在两个终端执行：

```sh
python -m resume_contracts.mock_server --port 8091 --token local-contract-mock
```

```sh
python manage.py migrate --settings=config.settings_mock
python manage.py seed_base --settings=config.settings_mock
python manage.py runserver 127.0.0.1:8001 --settings=config.settings_mock
```

前端执行 `VITE_API_PROXY_TARGET=http://127.0.0.1:8001 npm run dev`。
登录令牌使用 `issue_dev_token --username <已创建员工号> --settings=config.settings_mock` 签发。
此配置使用独立 `db.mock.sqlite3`、`media.mock/`，默认 review_only，模拟结果没有真实招聘含义；不要导入真实简历或用于生产。模拟服务支持成功、低匹配、失败、预算耗尽、超时、非法引用、覆盖不足与非法结构场景。

## 独立验证和发布

- 平台：`make check`；分别运行可用 `check-backend`、`check-frontend`、`check-release`。先安装各自 requirements/npm lock 依赖。本地虚拟环境可传入绝对路径的 `PYTHON`。
- Kernel：`make check build`、`make package KERNEL_VERSION=<version>`、`make image`；无需 Django 或其他仓源码。
- 协议：`make check package RELEASE_VERSION=v1.1.0`；先安装 build/setuptools/wheel。
- 平台镜像：`make images APP_VERSION=v2.0.0 IMAGE_PREFIX=gitlab.internal:5000/resume/platform`。默认只构建到本机；发布需显式加 `PUSH=--push`。
- 平台模板：`make package APP_VERSION=v2.0.0`；消费 `release/v2.0.0/images/` 中五个镜像记录，输出带双层校验的 `dist/resume-platform-v2.0.0.tar.gz`。该包不包含镜像，不是完整离线包。
- 完整离线发布继续使用既有 Skill：预先加载独立 Kernel 镜像，不构建兄弟仓源码。

三个仓均有薄的 GitLab 检查入口，镜像可用 CI Variables 指向公司镜像源。发布逻辑位于各仓 Makefile/tools；
GitLab/GitHub 只负责调度、凭据和制品上传。暂不假定公司 GitLab 地址、Runner 或发布权限，也不自动部署。

## 内部版本与公开协议

1. 平台调用认证的 `GET /v2/capabilities`，只检查支持的公开协议/结果结构及开发 Mock 隔离。
2. Kernel 返回 build、完整冻结工具注册表的 SHA-256、嵌入指令的 SHA-256。平台注册自己的 policy_version。
3. 每个新批次只发现一次版本，并冻结到 ProcessingRun 和候选人快照。执行任务必须使用完全相同的 pin。
4. Kernel 更新后，旧 pin 不匹配会失败待处理；不自动改版本、不继续旧 AI 路径。排空旧任务后升级最安全。
5. 平台默认不限定 Kernel 内部版本。部署可通过 `AGENT_KERNEL_BUILD` 显式锁定；
   Compose 的 `AGENT_KERNEL_VERSION` 仍对应实际 image build，且与 `APP_VERSION` 独立。
6. 工具/Prompt/Kernel 内部实现更新无需修改共享 SDK；公开字段、任务种类、证据格式或评分语义变化才升级协议并由双方验收。

旧 Python 简历筛选、embedded、shadow 与 Go `/v1/evaluate` 已删除。删除 `AGENT_KERNEL_MODE` 环境变量，
只保留 `review_only → enforced`：默认前者，后者需真实模型/OCR 与黄金样本人工放行。
不支持旧任务续跑，需重新提交当前版本任务；不删除已有业务数据或审计记录。

## 维护和交接边界

- 外包维护平台前后端、主数据、确定性政策与人工流程；你维护 Kernel 的文档、模型循环、工具和评测。
- 协议仓由双方评审；外包使用合成 Mock 即可开发，不需要 Kernel 源码、真实简历或模型密钥。
- 普通迭代只改所属仓；公共接口变更先更新协议，再在两个消费者通过合并请求同步固定快照。
- 每仓独立版本与制品；已发布版本不可覆盖。部署明确记录平台版本、Kernel digest/build 和协议版本组合。
- 变更先在验收环境验证，再安排生产窗口；停止接单、排空任务、备份数据库和媒体。保留 Compose project name 与数据卷，代码回退不代表数据库回退。
- 模型连接测试、院校省份补全仍是平台功能；它们不是旧简历 AI 基线。
- 不向外包交付本地 backups、旧 Git 历史、数据库、media 或生产配置。原公开仓历史不在本次清理范围内。
