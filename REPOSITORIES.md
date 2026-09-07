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

- 平台：`cd backend && python -m resume_contracts.verify && python manage.py test apps.pipeline apps.ingestion apps.api apps.accounts`；前端 `npm run lint && npm test && npm run build`。
- 内核：在自己的仓库执行 `make check build` 或 `make image KERNEL_VERSION=<version>`。
- 协议：在自己的仓库执行 `make check`。两个消费者 CI 不要求检出协议仓。
- Compose 只构建平台镜像；设置 `AGENT_KERNEL_IMAGE` 为已发布镜像（生产建议固定 digest），`AGENT_KERNEL_VERSION` 为该镜像报告的 build。
- 离线发布仍用原脚本，但必须先加载/拉取指定内核镜像，并提供 `AGENT_KERNEL_IMAGE`、`AGENT_KERNEL_VERSION`。平台版本仍是 `APP_VERSION`。
- `/v2/tasks/execute` 的路径保留，但旧 `resume-task/v1` 线协议已被新版本取代；升级前停止接单并排空旧版本任务，再部署一组已通过契约测试的版本。不支持进行中任务跨协议热升级。

## 交接前的安全检查

旧公开仓 `huyue0228/smart-resume-filter` 的历史包含 Kernel 源码，本次不修改其可见性、不覆盖或删除旧历史。新平台仓不继承它的历史；这只能隔离后续开发，不能撤回已经公开的内容。不要将本地旧 Git 备份、真实数据库、媒体、连接密钥交给外包。
旧 Python AI 路径仅用于现存 embedded/shadow 基线，隔离在 `legacy_baseline.py` 之后；不是正式远端路径的失败降级。真实模型与 OCR 验收完成后再退役，不能在拆仓时直接删除以牺牲回归基线。
模型连接仍由平台受控系统设置管理；真实连接与生产密钥不交给外包。简历仍使用只读共享卷和带摘要的短期授权；跨机器文档提供器不在本次迁移范围内。

## 协作与版本发布

- 默认负责人为 `@huyue0228`。外包账号确认前不授权；确认后只授予平台仓协作权限。Kernel 源码不给外包，协议变更走双方评审。
- 功能分支提交 PR，等待各仓 `check.yml` 通过。`.github/CODEOWNERS` 明确负责人，但只有 GitHub 分支保护启用后，审批才是强制门禁。个人私有仓的强制保护取决于账户套餐；不能用文件代替服务端权限。
- `release.yml` 可手动执行演练：运行回归测试、构建并保留 Actions 产物，不发布镜像、不创建 Release。维护者从已合并的 `main` 提交推送 `vX.Y.Z`（或预发布标签）才自动发布。标签必须指向 main 历史；已发布版本不得覆盖。
- 平台发布五种独立镜像（backend、frontend、postgres、redis、backup），Kernel 发布自己的镜像和 linux/amd64 二进制，协议仓发布 wheel/sdist。发布产物均有 SHA-256；镜像清单记录不可变 digest。只发布制品，不自动部署生产。
- 平台 GHCR 镜像命名为 `ghcr.io/huyue0228/resume-platform-<component>:<tag>`；Kernel 为 `ghcr.io/huyue0228/resume-agent-kernel:<tag>`。私有仓 Actions 使用本仓 `GITHUB_TOKEN`，无需跨仓源码令牌；部署方拉私有镜像需另行提供最小只读 Packages 凭据。
- 部署显式设置各 `*_IMAGE`（建议 digest）、`APP_VERSION` 和独立 `AGENT_KERNEL_VERSION`。平台可升级前后端而不重建 Kernel；Kernel 在协议兼容时单独升级。协议升级先发布 contracts，再在两个消费者 PR 同步固定副本、验证黄金样本，最后人工选定兼容版本组合上线。
- 当前仍是迁移基线：真实模型、扫描件 OCR 和招聘黄金样本验收未完成；发布流程通过不代表可以切换 enforced。保留 shadow → review_only → enforced 的人工放行，不自动退回旧 AI 路径。
