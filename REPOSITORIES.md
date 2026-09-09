# 仓库边界与独立交付

三个独立仓库的地址与职责见 `repositories.json`。

- 平台：Go + React，管理身份/RBAC、主数据、导入、志愿/准入/岗位池、PDF 文本提取、HC、人工工作流、结果校验与业务写入。
- Kernel：Go，管理全文阅读/搜索、证据化画像、池内岗位匹配、模型循环、只读工具/MCP、预算和引用验证；不读取 PDF，不接入业务数据库。
- 协议：`resume-analysis/v2` 的 JSON Schema、Python DTO、合成样例和模拟服务。两个 Go 消费者均持有 `internal/contract/bundle` 固定副本。

消费者构建不检出兄弟仓库。协议维护者在公开结构变更后运行 `tools/build_bundle.py --platform ../resume-platform --kernel ../resume-agent-kernel`，再在三个仓分别验证。结果仍为 `resume-job-match/v1`，公开协议和内部工具/指令版本分别管理。

平台调用认证的 `GET /v2/capabilities`，冻结 build、工具、指令、平台策略与模型配置版本。Kernel 必须拒绝不匹配的任务；升级前结束或取消旧任务，不自动修改旧 pin。

## 开发与验证

- 平台：安装 Go、Node，`cd frontend && npm ci && cd ..` 后执行 `make check build`。完整后台集成验证需要独立 PostgreSQL/Redis。
- Kernel：`make check build`，无需平台或业务数据库；使用 `KERNEL_VERSION` 独立构建。
- 协议：在 Python 环境安装依赖后 `make check`；安装 build/setuptools/wheel 后 `make package RELEASE_VERSION=v2.0.0`。
- 模拟服务：从已安装的协议包运行 `resume-kernel-mock --port 8091 --token <本地随机令牌>`。所有结果标为 MOCK_ONLY，仅用于隔离开发，不能代表真实模型质量。

旧 Django 源码通过 Git 的 v1.2.0 标签获取，不在当前 Go 代码树或备份目录中保留。旧 PostgreSQL 数据结构保留兼容映射；模型连接 TEST 和学校省份补全仍是平台功能。

## 发布

平台与 Kernel 的 OCI 镜像、协议 wheel/sdist 各自独立发布。平台 Release 另附四镜像完整离线包和 SHA-256；在线配置小包只提供已发布 digest，不能替代离线交付。

每次版本由 Git 提交及不可变标签定位。构建和回下载仅使用临时制品；GitHub 附件验证完成后清理本地 release 与历史备份。生产升级沿用项目名、数据卷、密钥和 CA；代码回退不等于数据库回退。
