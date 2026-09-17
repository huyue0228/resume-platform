# Resume Platform

- Go 1.25 业务平台，React JSX 前端嵌入 Go；PostgreSQL、Redis 和独立 Go Agent Kernel，共四个常驻容器。Python 仅用于构建/验收工具。
- 主入口 `cmd/resume-platform/main.go`；API、RBAC、导入、业务工作流和任务编排在 `internal/platform`，Poppler 提取在 `internal/pdftext`。
- 提交只冻结范围并返回 202；后台按有效志愿和准入固定一个投递标准，Kernel 只收到单候选人完整文本、该标准和受控标签字典；平台判定达标并事务保存不可变资格和分配工作项；独立分配只接收结构化标签，平台复算后归属部门/需求。HC 仅为计划人数，不使用 HC 容量限制。
- 公开协议 `resume-analysis/v4`，结果 `resume-application-assessment/v1`。`internal/contract/bundle` 是 resume-contracts 4.0.0 固定副本，仅由协议仓生成工具更新，禁止手改。
- 分配协议 `resume-allocation/v1`，结果 `resume-allocation-plan/v1`；数据库为任务真相，Redis 仅唤醒。主体/池统一独立分配；试算仅返回方案，暂停控制新分配。快照生成、提交是短事务，Kernel 调用在事务外。
- 当前版本面向新数据库，不提供旧词表、HC 容量与历史分配模式迁移；`internal/compat` 仍是基础数据库结构和 API 元数据入口。不得删除已有运行数据卷。
- `make check` 验证 Go race/vet/build、React 与发布工具。集成测试使用独立 `TEST_DATABASE_URL`、`TEST_REDIS_URL`，不得连接生产库。`make build` 构建并嵌入 React。
- 生产登录仅 W3；账号保持不可用密码，禁止恢复密码登录或 admin 页面。开发 Token 仅在 DEBUG 且 W3 未就绪时签发。
- 导入以业务键合并；Candidate 按规范姓名/手机号，Resume 按 apply_id。保留历史和权限边界；业务逻辑不直接依赖外部表头。
- 简历预览、导出和下载只通过鉴权 API，保持现有导出计数/文件名头。前端复用表格、表头过滤和预览组件，不恢复 Rule/AI 模式选择。
- 模型配置由权限保护的系统设置管理；密钥不进入源码、日志、样例或测试输出。平台与 Kernel 均保留 TLS CA 信任。
- 完整发布包含四镜像 amd64 离线包；按用户授权推送/发布，旧标签不移动。上传 GitHub 并回下载校验后清理本地临时制品。
- 不创建源码备份，代码回退依赖 Git 提交和版本标签；不因清理代码删除运行数据卷。未获请求时不 stage、commit、push。
- 不自动同步 `docs/` 四份设计文档。更新与本次代码清理、构建及发布直接相关的操作说明；不把历史设计当作当前实现。
