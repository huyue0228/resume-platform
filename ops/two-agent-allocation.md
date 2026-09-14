# 双 Agent 分配：启用与运行

本地实现基于协议包 3.1.0。筛选仍使用 `resume-analysis/v3`，分配使用 `resume-allocation/v1` / `resume-allocation-plan/v1`。两者共用 Kernel 部署，分配是独立的确定性执行器，不调用模型或简历工具。四个常驻容器保持不变。

## 流程与业务口径

当前有效投递 → 原筛选标准和评分 → 平台确认达标、保存不可变资格与持久化工作项 → 独立分配任务 → 平台复算、校验最新版本 → 事务提交部门与内部用人需求归属。

- 同一 HC 需求可关联多名候选人。新路径不创建 `core_processingrunjobcapacity` 或容量 reservation；历史容量仍按原流程释放。
- 候选人按实际入池时间（微秒）及成员 ID 处理。需求按优先标签命中数降序、priority 升序、近 7 天供给升序、最后分配序号升序、需求 ID 升序。均衡不覆盖前两项。
- 非公开岗位只要主体/池映射有效、岗位启用、接收中且标签满足，就参与分配。HC=0 也可以接收。
- 每个成员返回 assign 或 wait；无标签、需求暂停、映射缺失均保留通过资格。标准语义、标签定义、当前投递、源版本或基础资格变化会要求重评。
- 供给按主体/池共享，按候选人+需求去重；待下发计入，下发前取消排除，下发后拒绝保留历史推荐。DTO 的 `counted_demand_ids` 让批内更新遵守同一去重口径。
- 已下发成员不自动改派；人工改派沿用原权限、状态约束和审计。

## 启用

1. 先升级配套 Kernel 与平台。新表采用增量迁移；所有存量范围默认 `legacy`，不会自动切换生产行为。
2. 在岗位维护检查内部职位池、需求映射、标签及接收状态。进入「任务中心 → 分配任务」或「内部职位池 → 分配任务与供给」。看板可筛选非公开岗位和近 7 天零供给。
3. 使用「试算待分配候选人」审查排序、命中标签、等待原因及 HC=0 的接收需求。手动试算不会创建实际归属。
4. 配置范围模式并填写原因。`simulate` 的自动任务保存新方案，再执行历史规则；它用于观察与比较，不能把试算数字当成实际分配数字。`execute_v1` 才执行新方案。切换要求旧范围租约已经排空，递增 epoch 并使旧任务失效。
5. 启用 `execute_v1` 时一次性验证历史待分配资格及归属来源。可证明的 HC 耗尽等待成员进入队列；不能证明的成员标记 `needs_reanalysis`。原始简历当前 `job_id` 不能作为历史需求证据。未知需求单列展示，不计入某个具体需求；人工部门归属保留为 `department_only`。

历史同工作流存在多个有效尝试时，迁移报错并要求明确处理冲突，不删除任何有效记录。新数据库唯一索引继续约束一个有效去向。

## 暂停、取消与重试

- 「暂停新分配」使运行中方案失效、取消旧任务并保留待分配工作项；保留筛选资格和已提交归属。恢复 execute_v1 后按新快照继续；legacy 仍按原入口触发分配。
- 取消任务只取消未提交的分配，不撤销已经执行的业务记录；已完成任务的取消返回冲突。用户取消的成员不会被自动队列立即重新领取，可显式创建新任务或重试。
- 失败、已取消或已完成任务可「用新快照重试」；创建新 task_id 并保留旧方案。已经有有效归属的成员跳过，不再次分析简历。
- 快照冲突最多自动处理 3 代，超过上限持久化失败；不反复筛选。超时、服务不可用或非法方案可独立重试。
- 业务回退先暂停新分配，保留新增数据。不得直接运行不认识接收状态、epoch 与归属账本的旧二进制写库。发布交付不自动切换生产范围。

## API 与权限

| API | 用途 |
| --- | --- |
| `GET /api/position-pools/allocation-scopes/` | 可访问的主体/池、模式、版本、暂停状态 |
| `PATCH /api/position-pools/allocation-scopes/{id}/` | `allocation_mode` 或 `paused`，附 `expected_revision`、`reason` |
| `GET /api/position-pools/allocation-scopes/{id}/supply/` | 7 天供给、待下发/反馈、最后分配、历史未知归属 |
| `PATCH /api/jobs/{id}/reception/` | receiving/paused/closed，附 `expected_revision`、`reason` |
| `POST /api/position-pools/allocation-tasks/` | scope_id、mode=simulate/execute、可选 member_ids、idempotency_key；返回 202+task_id |
| `GET /api/position-pools/allocation-tasks/` | 按 scope_id 过滤，数据库分页 |
| `GET /api/position-pools/allocation-tasks/{id}/` | 状态、工作项、方案、筛选批次追溯 |
| `POST .../{id}/retry/` / `cancel/` | 新快照重试须附新幂等键；取消保留历史 |
| `GET /api/position-pools/allocation-plans/{id}/` | 单个方案及校验结果 |
| `POST /api/position-pools/members/{id}/allocate/` | 原单条分配入口改为 202+task_id |

需求维护复用 `job.manage` 并校验部门覆盖；分配复用 `attempt.dispatch`、`resume.view` 及现有部门范围。查看整池方案要求覆盖全部有效需求，防止借方案看到其他部门数据。范围模式/暂停使用管理员 `settings.manage_config`。任务创建者停用或权限收回时，执行和提交都会拒绝。新分配配置页移除 HC 系数编辑；历史值用于兼容旧记录。

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

协议和两个独立计算器覆盖设计案例及历史供给去重；平台集成测试覆盖真实 Kernel HTTP、HC=0/非公开需求、多批次供给、重复任务和并发领取、快照失效、取消/暂停与租约接管、独立失败重试、历史证据回填、文件完整性、权限拒绝及拒绝反馈后的供给。筛选模型和文本提取使用固定合成测试输入；这些证据不等同于真实模型、W3、生产负载或生产启用验收。
