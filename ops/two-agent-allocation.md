# 双 Agent 分配：启用与运行

本地实现基于协议包 4.0.0。筛选使用 `resume-analysis/v4`，分配使用 `resume-allocation/v1` / `resume-allocation-plan/v1`。两者共用 Kernel 部署，分配是独立的确定性执行器，不调用模型或简历工具。四个常驻容器保持不变。

## 流程与业务口径

当前有效投递 → 岗位来源标准和评分 → 平台确认达标、保存不可变资格与持久化工作项 → 独立分配任务 → 平台复算、校验最新版本 → 事务提交部门与内部用人需求归属。

- 同一 HC 需求可关联多名候选人。不使用 HC 容量表、预约或系数。
- 候选人按实际入池时间（微秒）及成员 ID 处理。需求按优先标签命中数降序、priority 升序、近 7 天供给升序、最后分配序号升序、需求 ID 升序。均衡不覆盖前两项。
- 非公开岗位只要主体/池映射有效、岗位启用、接收中且标签满足，就参与分配。HC=0 也可以接收。
- 每个成员返回 assign 或 wait；无标签、需求暂停、映射缺失均保留通过资格。标准语义、标签定义、当前投递、源版本或基础资格变化会要求重评。
- 供给按主体/池共享，按候选人+需求去重；待下发计入，下发前取消排除，下发后拒绝保留历史推荐。DTO 的 `counted_demand_ids` 让批内更新遵守同一去重口径。
- 已下发成员不自动改派；人工改派沿用原权限、状态约束和审计。

## 启用

1. 使用配套的 Platform 与 Kernel 构建，在独立数据库部署；本次不提供历史词表、容量表和分配模式兼容迁移。
2. 在「岗位需求」导入或编辑岗位资料。系统自动生成同主体、同投递名称的标准和同主体、同内部职位的池；同名投递的要求冲突时，进入「筛选与分配配置」明确选择标准来源。
3. 维护受控能力标签，配置各投递标准需要提取的标签，再启用部门规则草稿。检查列表中的筛选就绪状态及可达需求。只完成标准关联也可以筛选入池，未启用规则时等待分配。
4. 提交简历处理时查看当前范围的配置检查；修复后可在配置页批量重处理受阻候选人。标准或标签语义变化会使待分配资格失效，保存前可预览人数。
5. 所有池自动使用独立分配。进入「任务中心 → 分配任务」查看任务、等待原因和供给。手动试算仅保存方案，不创建实际归属；需要先观察待分配人群时，可先暂停该范围的自动分配再试算。

## 暂停、取消与重试

- 「暂停新分配」使运行中方案失效、取消旧任务并保留待分配工作项；保留筛选资格和已提交归属。恢复后按新快照继续。
- 取消任务只取消未提交的分配，不撤销已经执行的业务记录；已完成任务的取消返回冲突。用户取消的成员不会被自动队列立即重新领取，可显式创建新任务或重试。
- 失败、已取消或已完成任务可「用新快照重试」；创建新 task_id 并保留旧方案。已经有有效归属的成员跳过，不再次分析简历。
- 快照冲突最多自动处理 3 代，超过上限持久化失败；不反复筛选。超时、服务不可用或非法方案可独立重试。
- 业务回退先暂停新分配，保留新增数据。不得直接运行不认识接收状态、epoch 与归属账本的旧二进制写库。

## API 与权限

| API | 用途 |
| --- | --- |
| `GET /api/position-pools/config/` | 岗位来源、配置版本、筛选检查及可达需求 |
| `POST /api/position-pools/config/` | 从当前岗位幂等生成关联和规则草稿 |
| `PUT /api/position-pools/config/` | 携带 version；preview=true 只预览变更影响，否则保存并使旧资格失效 |
| `POST /api/pipeline/config-check/` | 按处理 scope 检查筛选阻塞和分配等待 |
| `POST /api/position-pools/config/reprocess/` | 批量重新提交配置受阻或需要重评的人选 |
| `GET /api/position-pools/allocation-scopes/` | 可访问的主体/池、版本、暂停状态 |
| `PATCH /api/position-pools/allocation-scopes/{id}/` | `paused`，附 `expected_revision`、`reason` |
| `GET /api/position-pools/allocation-scopes/{id}/supply/` | 7 天供给、待下发/反馈、最后分配 |
| `PATCH /api/jobs/{id}/reception/` | receiving/paused/closed，附 `expected_revision`、`reason` |
| `POST /api/position-pools/allocation-tasks/` | scope_id、mode=simulate/execute、可选 member_ids、idempotency_key；返回 202+task_id |
| `GET /api/position-pools/allocation-tasks/` | 按 scope_id 过滤，数据库分页 |
| `GET /api/position-pools/allocation-tasks/{id}/` | 状态、工作项、方案、筛选批次追溯 |
| `POST .../{id}/retry/` / `cancel/` | 新快照重试须附新幂等键；取消保留历史 |
| `GET /api/position-pools/allocation-plans/{id}/` | 单个方案及校验结果 |
| `POST /api/position-pools/members/{id}/allocate/` | 原单条分配入口改为 202+task_id |

需求维护复用 `job.manage` 并校验部门覆盖；分配复用 `attempt.dispatch`、`resume.view` 及现有部门范围。查看整池方案要求覆盖全部有效需求，防止借方案看到其他部门数据。范围暂停使用管理员 `settings.manage_config`。任务创建者停用或权限收回时，执行和提交都会拒绝。HC 系数及专业大类词表配置已移除。

## 持久化与边界

PostgreSQL 是任务真相，Redis 只发唤醒提示；2 个平台分配 worker 定期查库，Redis 消息丢失不丢任务。每范围一个 90 秒租约，过期接管递增代次并更换令牌。快照生成和方案提交使用短事务，Kernel 请求在事务外；平台独立复算整个结果，版本、权限、租约或结果不符时整单不执行。

默认每任务 100 人、200 条需求、2 MiB、30 秒计算/16 次工具调用、60 秒快照有效期；当前执行器恰好 6 步。Kernel 有独立 4 个并发槽和 256 项/5 分钟结果缓存。自动队列按入池顺序拆成员，不拆需求集合；配置发布拒绝超过 200 条规则的池。

导入使用唯一文件名固定原文版本，保留旧文件。正常替换立即更新源版本。平台另有每分钟最多 20 份、每轮 20 秒的文件完整性检查，外部直接篡改文件并非即时检测；分配 DTO 和执行器都不打开原文。资格、标签哈希、源版本及允许映射由平台生成，分配白名单拒绝正文、摘录、摘要、文件地址、自由评价及 HC/公开字段。

## 本地验证

```sh
# resume-contracts
make check
python3 tools/build_bundle.py --check --platform ../resume-platform --kernel ../resume-agent-kernel
# resume-agent-kernel
make check build
# resume-platform，使用独立测试 PostgreSQL、Redis 及已构建 Kernel
TEST_DATABASE_URL='postgres://…独立测试库…' TEST_REDIS_URL='redis://…独立测试实例…' TEST_ALLOCATION_KERNEL_URL='http://127.0.0.1:58098' make check build
```

不设置 `TEST_ALLOCATION_KERNEL_URL` 时，平台 CI 使用本仓合同响应夹具验证业务流程；指定该变量后验证真实 Kernel HTTP。协议和两个独立计算器覆盖设计案例及历史供给去重；平台集成测试覆盖真实 Kernel HTTP、HC=0/非公开需求、多批次供给、重复任务和并发领取、快照失效、取消/暂停与租约接管、独立失败重试、岗位关联与配置修复、文件完整性、权限拒绝及拒绝反馈后的供给。筛选模型和文本提取使用固定合成测试输入；这些证据不等同于真实模型、W3、生产负载或生产启用验收。
