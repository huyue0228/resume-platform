# 简历筛选与内部岗位分配：双 Agent 项目设计

日期：2026-09-14。状态：设计稿，尚未实现。本文是本次跨仓改造的主设计，不替代现有四份历史设计文档，也不表示功能已上线。

目标流程：**当前有效投递 → 简历筛选 Agent → 平台判定达标入池 → 分配 Agent → 平台执行部门与 HC 需求归属。**

配套文件：[目标流程图](../architecture/two-agent-screening-allocation.html)、[图源](../architecture/two-agent-screening-allocation.workflow.json)、[合成验收案例](two-agent-allocation-cases.json)、[设计检查记录](two-agent-design-review.md)。后两项用于记录设计验证，不是已经可调用的协议或生产测试结果。

## 1. 已确认的业务决定

| 编号 | 决定 | 实现含义 |
|---|---|---|
| B01 | 先筛选，再分配 | 未达标、评估失败或未完成的候选人不能进入分配输入 |
| B02 | 筛选针对唯一当前投递标准 | 保留有效志愿、主体、学校与学历准入边界；不能分析所有同名岗位 |
| B03 | 通过后进入对应内部职位池 | 资格绑定候选人、当前投递、评估标准、标准版本和标签版本 |
| B04 | 分配不再分析原始简历 | 分配输入、工具、日志均不含正文、原文摘录、简历摘要或文档访问地址 |
| B05 | 分配沿用既定优先级 | 标签匹配和人工配置优先级由确定性代码执行，模型不能修改规则 |
| B06 | 公开、非公开内部岗位共同参与 | 是否公开不成为分配资格条件；必须有明确映射且处于接收状态 |
| B07 | HC 表示内部用人需求归属 | 一条需求可以关联多名候选人；分配不占用、扣减或释放招聘人数 |
| B08 | 均衡服从优先级 | 只打破同等匹配、同等配置优先级下的并列；不能为了均衡降低条件 |
| B09 | 平台唯一执行业务写入 | Agent 返回方案；平台校验最新状态、幂等并落库 |
| B10 | 一名候选人只有一个当前有效去向 | 保留历史分配；新方案不自动改派已经下发的候选人 |

本设计中的“分配到 HC”统一称为“归属内部用人需求”。第一期 `demand_id` 对应现有 `core_job.id`，不是为 `headcount=3` 建立三个只能挂一个候选人的名额。部门是该需求所属部门。正式录用、Offer 预留和编制核销不在本期范围。

## 2. 当前源码基线与差距

本次核对本地工作区，不等同于生产部署状态：

| 仓库 | 本地 HEAD | 工作区说明 |
|---|---|---|
| resume-contracts | `a8c5681ff4461dc09c22d932ca96bb4ce1ac159d` | 核对时无未提交修改 |
| resume-agent-kernel | `9ece0ad2ee170ff7753cb717ea63ad325d71ef50` | 核对时无未提交修改 |
| resume-platform | `87dc63ab06ff362f2a92df467b41df7ee7b1547a` | 含任务中心、志愿生命周期、取消复核等既有未提交修改；以下描述包含这些修改 |

| 当前行为与源码 | 本次目标 |
|---|---|
| [公开筛选输入](../../../resume-agent-kernel/internal/protocol/analysis.go)仅接收一名候选人、一个标准；已有 `resume-analysis/v3` | 保持输入范围和评分语义，新增独立分配任务契约 |
| [入池保存](../../internal/platform/pools.go)的 `savePoolAssessment` 保存结果后立即调用 `allocatePoolMember` | 入池事务提交资格及持久化分配任务；分配异步执行 |
| `eligiblePoolRules` 按优先标签命中数降序、规则 priority 升序、job ID 升序 | 保留前两项；同级时加入供给均衡，ID 保留最终兜底 |
| `allocatePoolMember` 读取简历文件并计算摘要，且按 `run_id + job_id` 占用 `HC × 系数` | 分配依赖版本化事实与需求快照；不访问简历文件，不产生 HC 占用 |
| [任务初始化](../../internal/platform/runs.go)建立每批次岗位容量，[反馈处理](../../internal/platform/workflows.go)释放历史容量 | 新任务停止建立容量；仅对历史记录保留兼容释放逻辑 |
| [职位池 API](../../frontend/src/api/pools.js)有配置、详情、修订标签和单条重新分配 | 保留入口，新增持久化分配任务、方案、试算和明确的异步状态 |
| [部门拒绝](../../internal/platform/applications.go)关闭当前入池记录，等待下一有效志愿或进入人才库 | 本期沿用；不顺带增加同池自动转荐，也不重新启用人工复核阶段 |

当前 `core_agentdispatchdecision` 同时保存筛选结果及推荐部门/岗位，且后续分配会更新该行。本次把“筛选事实”与“分配决定”分别存证；旧表可继续作为兼容展示投影，不能继续充当唯一权威事实。

## 3. 两个 Agent 与平台的职责

| 组件 | 允许读取 | 负责产出 | 禁止承担 |
|---|---|---|---|
| 筛选 Agent | `ResumeTextV2` 完整文本、唯一投递标准、受控标签字典 | 既有评分、依据、标签与置信度 | 部门选择、跨志愿选择、HC 控制、业务写入 |
| 分配 Agent | 平台生成的资格投影、标签、内部需求映射、优先级、接收状态、供给快照 | 全覆盖分配方案、排序向量、原因码 | 原始简历分析、语义补标签、修改筛选结论、改变优先级或扣减 HC |
| 平台 | 业务数据、完整筛选证据、分配快照及当前状态 | 准入、达标入池、鉴权、任务编排、校验、执行、审计 | 将业务正确性外包给模型的自由文本解释 |

### 3.1 分配 Agent 的运行方式

第一期采用 `execution_mode=deterministic`：有独立任务、输入输出、工具目录、执行轨迹与预算，由固定执行器调用分配工具，**不强制发起 LLM 请求**。这是独立的分配能力模块，不把固定工具流水线宣传为已验证的自主模型规划。

本期用户要求优先级固定、禁止重读简历，因此增加模型推理不能改变合法排序结果。以后确有需要时可以另加模型编排模式，但必须使用相同工具和校验器，独立评测、单独版本化，不能通过更换提示词悄悄改变当前规则。

两个 Agent 第一阶段共用 Kernel 部署和基础运行设施，但各自使用独立任务服务、Registry、capabilities、pin、会话缓存命名空间和并发池。分配 Registry 不继承筛选工具，也不继承全局 external MCP Provider；无需新建第五个常驻容器。

## 4. 主流程与状态

### 4.1 筛选和入池

1. 平台按既有规则固定当前有效投递与唯一标准。
2. 筛选 Agent 返回评估结果。平台检查协议、完整性、原文证据、标签范围，并按既有权重重算分数及入池阈值。
3. 达标：保存不可变的筛选资格版本、标签事实版本，建立 `pending_allocation` 入池记录；在同一数据库事务插入持久化分配工作项。
4. 未达标：沿用当前志愿推进逻辑。模型超时、协议异常、证据不完整属于运行失败，不能当作“不达标”。
5. 资格事务提交即表示筛选阶段完成。分配失败不会把筛选任务改成失败，也不会撤销已验证资格。

标签字典必须覆盖该投递标准明确映射的内部岗位所需分配标签；不能因此把其他投递的岗位职责送给筛选 Agent。配置发布时校验：每个可达需求的必需/优先标签均在该标准标签字典中。存在缺口时阻止该新配置发布并列出缺失标签。

标签未被简历证据支持、`needs_verification` 或模型置信度低于既有阈值时，不计为可用标签。分配暂不可行不代表筛选失败。确需补充标签时，创建明确的筛选重新评估任务，分配 Agent 不访问全文、不自动循环触发重分析。

### 4.2 分配任务

1. 持久化调度器按 `招聘主体 + pool_code` 聚合待分配成员，所有历史批次共享一个分配范围；Redis 只用于唤醒，数据库是任务真相来源。
2. 平台以短事务生成一致快照：成员及版本、允许目标关系、需求版本、规则版本、供给统计、快照时间及范围版本。
3. 事务外调用分配 Agent；不能持有业务行锁等待 Kernel 或模型。
4. Agent 对每个成员给出 `assign` 或 `wait`。平台先完成全量结构/语义校验，再进入执行事务。
5. 平台复核快照版本，按固定顺序创建分配记录、保存需求归属、增加范围版本、更新入池状态及工作项结果；一次事务提交。
6. 快照过期时整个方案不执行，保存 `stale` 结果，重新生成受影响范围的快照。不得静默修改 Agent 返回的方案后仍沿用原解释。

默认每个范围同时只有一个有效执行租约；不同主体或不同池可以并行。单次最多 100 名候选人、200 条需求、2 MiB 请求，作为初始工程上限而非性能保证。超限按入池时间、成员 ID 稳定拆分；不能静默截断规则、可达需求或标签。200 条需求限制需在配置发布时校验，超限不能靠随机拆需求改变候选人的可选集合。

### 4.3 状态语义

| 对象 | 状态 | 含义 |
|---|---|---|
| 入池成员 | `pending_allocation` | 资格有效，尚无当前分配；可有等待原因 |
| 入池成员 | `allocated` | 平台已成功创建有效分配及需求归属 |
| 入池成员 | `needs_reanalysis` | 标准语义、源文件版本或资格事实已失效；需要筛选侧重新评估 |
| 入池成员 | `closed` | 有效志愿切换、流程终结、部门拒绝等导致资格关闭 |
| 分配任务 | `pending / running / completed / failed / cancelled` | 任务运行状态；失败不代表候选人不合格 |
| 分配方案 | `proposed / simulated / committed / stale / invalid / cancelled` | 方案是否仅试算、已执行或不可执行 |
| 分配工作项 | `pending / leased / assigned / waiting / failed / cancelled` | 成员在本轮任务的进度；等待不会立即无休止重试 |

成员和任务状态分开：Agent 全覆盖输出且部分成员 `wait`，任务可正常 `completed`；界面展示“已分配 N、待分配 M”，不能显示“全部已分配”。

## 5. HC 需求与接收状态

第一期复用 `core_job` 作为内部用人需求主记录。对外名称用于已有投递映射，内部岗位名称与部门用于归属展示；不以名称字符串代替需求 ID。

新增需求接收设置：

| 字段 | 设计 |
|---|---|
| `demand_id` | FK `core_job.id`，稳定的内部需求标识 |
| `reception_state` | `receiving / paused / closed`；是否参与新分配 |
| `revision` | 接收设置版本；变化使未执行的分配快照过期 |
| `updated_by / updated_at / reason` | 状态调整审计；关闭后再次接收须显式操作 |

`is_active=false` 仍表示主记录不可用于新分配。`is_public=false` 不阻止分配。`headcount=0` 也不自动阻止分配；如果业务希望该需求停止接收，使用 `paused` 或 `closed`。分配输入完全不传 HC 数量，因此不会从 HC 推导排序、权重或容量。

第一期不设置候选人硬容量。部门积压只作为看板和接收管理依据；未来如需处理上限，必须新增独立业务配置并单独验收，不能恢复 `HC × 系数`。

需求部门变更影响新方案；历史归属保留部门和岗位名称快照。已下发记录不随主数据变更自动转部门。现有纯部门人工分配保留 `target_kind=department_only`，没有明确需求时不得猜一个 HC ID；此类记录单列显示，不能污染具体需求的供给统计。

## 6. 分配优先级与均衡算法

### 6.1 资格过滤先于排序

成员必须是当前有效投递、入池资格有效、状态为待分配，且无有效分配。目标必须满足：

- 同招聘主体、同允许的内部职位池，并存在有效的成员到需求映射；不能仅因岗位名称相同而扩大范围。
- 需求启用、部门有效、接收状态为 `receiving`。
- 所有必需标签均命中；标签属于本资格的受控字典。
- 可用标签沿用当前口径：`supported`，且模型置信度至少 0.8，或有合规的人工确认来源。
- 沿用当前至少命中一个标签的要求；仅有优先标签的规则，零命中不能入选。

平台冻结 `allowed_demand_refs` 作为授权范围：从当前标准显式关联的 pool，再沿该 pool 已启用的部门需求规则推导，不新增任何按岗位名称相似度或同名字符串扩展的路径。已经映射的非公开内部需求不再要求其对外名称等于投递名称；这与筛选侧只能看到当前投递标准是两个不同边界。配置试算必须列出每个标准的实际可达需求。Kernel 根据同一标签规则复算，结果必须是授权集合的子集；授权范围不等于自动满足匹配条件。

### 6.2 确定性排序

候选人处理顺序保持入池 `created_at ASC, membership_id ASC`。第一期不增加新的“稀缺候选人优先”、跨志愿竞价、总分跨岗位排序或全局最大化策略，避免改变已经确定的优先级。

对每名候选人的可选需求，按下面的向量升序比较：

```text
(-preferred_tag_hit_count,
 rule_priority,
 recent_supply_count + provisional_assignments,
 last_allocation_sequence,
 demand_id)
```

前两项严格沿用当前语义：命中的优先标签越多越优先，规则 priority 数字越小越优先。第三、四项是本设计新增的同级并列处理；最终 demand ID 保证稳定。不能让均衡越过前两项，也不使用候选人的公共筛选分数重新排序内部需求。

每完成一个拟分配，工具立即增加该需求的临时计数，并更新临时分配序号，供本方案的后续成员使用。`last_allocation_sequence` 为范围内单调递增整数；从未获得分配使用 0，实时墙钟不进入方案循环，避免同一快照重放结果不同。

### 6.3 供给统计口径

初始统计窗口为快照时间之前 7 天，使用 UTC 半开区间 `[snapshot_at - 7d, snapshot_at)`，属于可版本化的业务配置。改窗口只影响新快照，不改已执行方案。

`recent_supply_count` 统计该需求在窗口内仍有效的首次分配，或已经实际下发的历史推荐；按候选人和需求去重，不按处理任务累计。待下发也计入当前供给，防止并发计划都选择同一个“零供给”岗位。

- 下发前取消：从有效供给中剔除；如影响最后分配序号，投影按有效台账重算。
- 已下发后拒绝或撤回：仍计为获得过推荐，避免通过拒绝来持续获得优先供给。
- 同一候选人同一需求的重复尝试：统计窗口内去重；一次方案内也不能重复分配。
- 试算、Agent 返回成功但未提交、过期方案：全部不计入。
- 其他导入批次：同范围统一计入；已经处理完成不等于历史上从未获得推荐。

供给统计只影响同级候选人的去向，不等于招聘完成率。不保证每个岗位人数相同；没有符合条件的人时，非公开岗位仍可为零，页面必须能解释原因。

## 7. 筛选事实到分配快照的投影

禁止把 `platform_pool_memberships` 整行或其 `assessment` 直接序列化给分配 Agent：当前 `assessment` 包含原文证据和自由文本，整行转发会破坏隔离。

新增显式 DTO 投影白名单：

| 区域 | 允许字段 |
|---|---|
| 成员 | 不透明候选人引用、成员引用、当前投递引用、工作流 revision、成员 revision、资格引用、资格版本、标准引用、池引用、入池顺序 |
| 资格 | `admitted=true`、标准 hash、标签集合 hash、事实版本、受控标签 code/status/confidence/source、平台证据校验标记、不透明断言引用 |
| 需求 | demand_ref、department_ref、主体/池引用、接收状态、需求 revision、必需/优先标签 code、priority |
| 供给 | recent_supply_count、last_allocation_sequence、next_sequence、统计时间范围、范围 revision |
| 策略 | 固定排序版本、标签阈值、窗口长度、候选处理顺序、规则版本 |

不发送姓名、手机号、学校名称、原始简历、`ResumeTextV2`、文件路径、签名 URL、原文 quote、画像 source_text/claims、筛选 reason/summary、部门自由文本反馈。部门及需求可读名称由平台 UI 关联展示，分配本身使用引用。

证据原文留在平台筛选详情。`assertion_ref` 仅用于追溯，分配工具没有通过它下载原文的能力。缺少平台验证标记或标签版本不匹配的断言必须拒绝。

源文件只在导入、替换及筛选准备时生成不可变内容版本；分配校验 source revision 和资格 hash，不打开文件重新计算摘要。需要先落实受管文件不可原位覆盖、所有替换入口递增版本；异常外部文件变更由完整性巡检发现并使资格失效，不能由第二个 Agent 读取文件补救。

## 8. 跨仓协议设计

以下名称是拟定契约，尚未发布；公开 Schema 的唯一实现来源仍是 `resume-contracts/resume_contracts/models.py`。本文不手写或修改消费者 bundle。

### 8.1 保持筛选 v3，新增独立分配协议

| 能力 | 筛选（保持） | 分配（新增） |
|---|---|---|
| 协议 | `resume-analysis/v3` | `resume-allocation/v1` |
| task_kind | `candidate.application_assessment` | `pool.candidate_allocation` |
| 结果 | `resume-application-assessment/v1` | `resume-allocation-plan/v1` |
| 执行入口 | `POST /v2/tasks/execute` | `POST /v2/allocation/tasks/execute` |
| 能力发现 | `GET /v2/capabilities` | `GET /v2/allocation/capabilities` |
| 输入粒度 | 单候选人、唯一标准 | 同主体同池的有界待分配集合 |
| 默认运行 | 既有模型工具循环 | 确定性分配工具流水线 |

选择独立入口是为了保持旧 v3 的严格 Schema 与能力发现结果不变。不能在旧 `scope.jobs` 中塞入内部需求列表，也不能直接给旧 capabilities 添加其 Schema 不允许的字段。新增协议并不要求升级筛选评分或重新分析所有简历。

协议仓新增 allocation request/response/capabilities 模型及 `allocation.*.schema.json`、合成样例，统一收录在新包版本 manifest；发布新包版本，不覆盖 3.0.0。版本号在实现和兼容性验证时确定，不把本设计名称当成已发布版本。

### 8.2 分配请求字段

| 字段 | 类型/约束 |
|---|---|
| `protocol_version / task_kind` | 上述固定字符串；extra 字段禁止 |
| `task_id / idempotency_key` | 非空、有界；幂等键包括任务代次和快照 hash |
| `snapshot_id / snapshot_hash / snapshot_at` | 平台持久化快照身份；hash 覆盖规范化整个 scope 与排序策略 |
| `allocation_scope_ref / scope_revision` | 主体加池的范围引用及单调版本 |
| `pin` | Kernel build、分配工具版本、固定执行程序版本、策略版本、协议/结果版本；deterministic 不要求模型密钥 |
| `execution_mode` | 第一期只允许 `deterministic` |
| `scope` | 第 7 节 DTO；members、demands、allowed edges、statistics、policy |
| `budget` | max_members=100、max_demands=200、max_duration_seconds=30、max_tool_calls=16 的初始默认值；上限由服务端约束，不能由调用方无限放大 |

按字段明确最大长度、数组边界、非负计数和唯一性。hash 的规范化使用协议仓生成的跨语言测试向量：对象键排序、集合引用排序、UTF-8 编码；置信度投影为 0–10000 的整数基点，避免 Go/Python 浮点序列化差异。时间统一 RFC3339 UTC。

### 8.3 分配响应字段

- 完整回传任务、快照、范围 revision、pin，便于平台对照。
- `outcomes` 恰好覆盖请求中的每个 member_ref，一人一条；不允许遗漏、重复或出现请求外成员。
- `assign`：member_ref、qualification_revision、demand_ref、department_ref、matched_tag_codes、排序向量、选中规则引用、备选需求及其向量、机器可校验的原因码。
- `wait`：member_ref、reason_code、必要的缺失标签/不可接收需求引用；不能输出 screening_rejected 或要求扣减 HC。
- manifest：`DONE/FAILED`、覆盖人数、输入 hash、工具版本；trace：耗时、调用次数、脱敏错误码。第一期模型调用数及 token 用量均为 0。

`DONE` 仅说明方案计算完成。平台执行成功才算分配完成。响应文本解释仅供展示，不能替代排序向量与证据引用；使用模板从确定性结果生成解释即可。

### 8.4 只读/纯计算工具

| 工具 | 职责 |
|---|---|
| `allocation.read_snapshot` | 读取当前任务内的有界快照摘要 |
| `allocation.filter_eligible` | 按资格、允许映射、接收状态与标签输出可行关系 |
| `allocation.rank_demands` | 执行固定排序，返回比较向量及排除原因 |
| `allocation.simulate_plan` | 按固定成员顺序更新任务内临时供给，生成完整方案 |
| `allocation.validate_plan` | 检查覆盖、唯一去向、规则遵循与工具计算一致性 |
| `allocation.submit_plan` | 只提交任务结果到 Kernel 内存收集器，没有业务副作用 |

所有工具只操作传入快照及任务内状态；没有数据库、HTTP、Shell、文件访问、MCP 或业务 commit 工具。提交结果与 `task_done` 的语义不等于平台落库。

## 9. 数据模型、并发与执行

### 9.1 新增业务对象

优先使用平台扩展表，避免把新领域语义挤进历史兼容表；实现时新增幂等迁移，保留历史字段与约束。

| 对象 | 关键字段与约束 |
|---|---|
| `platform_screening_qualifications` | member_id、decision_id、revision、standard_hash、source_revision、tag_set_hash、admitted、snapshot、created_at；`(member_id, revision)` 唯一，不可变 |
| `platform_demand_settings` | demand_id PK/FK core_job、reception_state、revision、操作审计 |
| `platform_allocation_scopes` | `(entity, pool_code)` 唯一，revision、next_sequence、lease_owner、lease_expiry；稳定的并发协调行 |
| `platform_allocation_tasks` | scope_ref、task_id、generation、mode、snapshot/hash、pin、lease、status、进度、错误、时间；任务代次及幂等键唯一 |
| `platform_allocation_work_items` | member_id、qualification_revision、task_id、lease/status、wake_reason；同资格最多一个 pending/leased 工作项 |
| `platform_allocation_plans` | task_id、plan_hash、原始结构化输出、校验结果、执行状态与时间；每代任务最多一个有效方案 |
| `platform_assignment_targets` | attempt_id PK/FK、member_id、qualification_revision、plan_id、demand_id、department_id、名称快照、sequence；与分配记录同事务写入 |

历史自动分配的需求引用可按容量 reservation.job_id、原分配事件中的 job_id、当时决策快照依次交叉验证回填；冲突或无法证明时标记未知，不用当前 `resume.job_id` 猜历史归属。未识别归属显示在迁移报告中，不能计入某条具体需求的平衡统计。

### 9.2 原子执行与幂等

平台执行器独立复算可行关系、成员顺序和排序向量。固定算法首期要求输出与参考结果一致，不能只检查“目标存在”。

全范围相关写入采用同一锁序：范围行按 ID → 候选人工作流按 ID → 入池成员按 ID → 需求设置按 ID → 尝试/方案行。跨范围人工操作同时按序锁定涉及的范围；先回填已有数据，再使所有新入口遵守该锁序。

快照默认最多有效 60 秒；统计窗口固定在 snapshot_at，短时间内跨越窗口边界不临时改变统计口径。超过有效期或任一相关业务版本变化均重新生成快照。请求预算、快照有效期和重试间隔在执行配置中一起校验。

在一个短事务内：

1. 查任务的 committed 结果；同幂等键、同 hash 重试直接返回原结果。同键不同 hash 返回冲突。
2. 锁定范围，检查范围 revision、任务代次/租约 fencing token、资格/规则/需求版本。租约过期的旧 worker 无权提交。
3. 重新检查所有成员仍为当前有效投递且没有有效分配。任何影响本计划的状态差异使整份方案 `stale`，本轮零业务写入。
4. 为 assign 项创建 `core_assignmentattempt` 和 `platform_assignment_targets`，更新成员为 allocated；wait 项只写等待原因与工作项状态。
5. 保存 committed 方案、范围供给投影/sequence/revision、工作项终态和审计事件，提交事务。

需要增加/校准数据库约束以兜底并发：每 workflow 最多一条当前有效 attempt，至少覆盖 pending_dispatch/dispatched/passed，并兼容迁移前 pending_review；每 attempt 最多一个需求归属；每资格最多一个活跃分配工作项。启用唯一约束前报告并解决既有冲突，不能静默删除历史。

取消、人工改派、部门反馈、需求状态/映射修改、标签修订均必须使相关范围 revision 递增；批量接口、定时任务和单条接口不能绕过。供给窗口内的历史记录从数据库台账计算，缓存只做派生投影。

如果方案计算完成后网络超时，平台按持久化 task/plan 查询结果；不能把 HTTP 重试变成另一次实际分配。Kernel 会话缓存用于节省重复计算，数据库幂等约束才提供持久保证。

### 9.3 失败与唤醒

| 原因码 | 处理 |
|---|---|
| `no_active_mapping` | 留池等待；映射发布后唤醒 |
| `no_receiving_demand` | 留池等待；接收状态变化后唤醒 |
| `required_tags_unavailable` | 留池等待，列出缺失/待核实标签；人工修订或筛选重评后唤醒 |
| `qualification_stale` | 转 needs_reanalysis；重新筛选产生新资格后再分配 |
| `allocation_snapshot_stale` | 方案零写入；退避并生成新快照，最多连续 3 代自动重试后保留待重试状态和明确提示 |
| `allocation_output_invalid` | 记录完整错误分类，不执行方案；不能悄悄回到 HC 分配算法 |
| `allocation_timeout / kernel_unavailable` | 资格保留、任务可重试；不重新调用筛选 Agent |
| `candidate_already_assigned` | 原有效分配保留；终结过期工作项，不重复创建 attempt |

等待成员不持续空转。映射、标签、接收状态、有效志愿等事件建立新的持久化工作项；保留一个低频数据库扫描作为漏唤醒恢复。纯统计时间推进只影响存在合法目标但等待执行的成员，不产生新的筛选调用。

## 10. 页面、API 与权限

### 10.1 用户页面

| 页面 | 本期变化 |
|---|---|
| 岗位/内部需求管理 | 显示需求 ID、部门、HC（计划人数）、对外发布、接收状态；支持有审计的接收/暂停/结束 |
| 职位池列表 | 分别显示筛选通过时间、资格版本、分配状态、等待原因、当前部门与 HC 需求 |
| 成员详情 | 筛选结论和分配依据分区；分配区显示标签命中、优先级、供给比较，原文证据仍从既有鉴权筛选详情查看 |
| 任务中心 | 筛选任务与分配任务独立状态、相互链接；显示真实已筛选/已入池/已分配/等待/失败进度 |
| 分配配置 | 撤下 HC 系数和岗位容量；保留规则优先级，新增接收状态与同级均衡说明 |
| 需求供给看板 | 近 7 天获得推荐数、待下发/待反馈数、最后分配时间；可筛选非公开岗位和长期零供给岗位 |

页面文案使用“归属需求”“等待接收”“缺少可用标签”，新流程不显示“HC 已满”。历史容量数据明确标记为旧规则记录。“试算完成”不能显示为“分配完成”。

### 10.2 平台 API 草案

| 接口 | 语义 |
|---|---|
| `PATCH /api/jobs/{id}/reception/` | 接收状态变更，必须携带 expected_revision 和原因 |
| `POST /api/position-pools/allocation-tasks/` | 主体/池及可选成员范围，mode=`simulate` 或 `execute`；返回 202 和 task_id |
| `GET /api/position-pools/allocation-tasks/{id}/` | 实际进度、等待原因和关联方案 |
| `GET /api/position-pools/allocation-plans/{id}/` | 方案结果与可解释的比较向量 |
| `POST /api/position-pools/allocation-tasks/{id}/retry/` | 用新快照创建新代次；按幂等键去重 |
| `POST /api/position-pools/allocation-tasks/{id}/cancel/` | 取消尚未提交的任务；已提交方案不能伪装成撤销 |
| `POST /api/position-pools/members/{id}/allocate/` | 保留现有路径，第一期经同一任务服务执行单条请求；返回现有 code/detail 并增加 task_id，HTTP 202；同步更新前端 |

试算方案不能在过期后“直接执行”；execute 总是创建/校验当前快照，必要时结果会与试算不同，并明确展示变化。普通 execute 模式在规则校验通过后自动落库，不引入每位候选人必须人工批准的流程。

沿用平台既有 RBAC 的资源范围。初始权限映射：查看池使用既有查看权限；试算/执行/重试使用 `attempt.dispatch` 并校验成员与部门范围；修改需求接收状态沿用岗位维护权限；取消任务需要执行者权限或管理权限。具体 permission code 复用实现中的权限字典，不凭本文新增越权别名。部门接口人不能通过分配任务详情查看其他部门候选人的原文或全池数据。

人工直接指定需求必须满足当前有效投递、主体/映射、需求接收等硬条件；如产品保留特殊强制改派，作为独立有权限、有理由的动作审计，不能伪装成 Agent 按优先级得出的结果。统计使用实际执行归属。

## 11. 三仓实施拆分

| 顺序 | 仓库/模块 | 交付及验收门槛 |
|---|---|---|
| P1 | resume-contracts | allocation 独立模型、capabilities、样例、模拟服务场景、跨语言 hash 向量；旧筛选 v3 全部兼容检查继续通过 |
| P2 | resume-agent-kernel | 新执行入口、独立 Registry、确定性工具/预算/幂等；禁止文档工具与全局 MCP 泄漏；独立 `make check build` |
| P3 | resume-platform 数据层 | 不可变资格、需求接收设置、工作项/方案/归属表、需求范围版本与迁移审计；所有写入入口统一锁序 |
| P4 | resume-platform 执行层 | 入池后异步分配、快照投影、结果复算、原子落库、事件唤醒、取消重试及真实进度；新记录不建 HC reservation |
| P5 | resume-platform 前端 | 接收设置、独立任务进度、分配依据、需求供给看板、HC 系数撤下；保持既有鉴权证据查看 |
| P6 | 三仓联调 | 合成验收案例、故障与并发、旧数据迁移、固定样例回放、独立构建与协议 bundle 漂移检查 |

协议实现从 `models.py` 开始，通过协议仓工具同步两个消费者固定副本；工具需扩展分发新 allocation 文件及其 manifest，禁止手改生成物。第一期设计文档不更改现有代码、AGENTS 或协议版本；实施 Kernel 新能力时同步调整工作约定，准确表达“筛选不分配、分配只产方案、平台执行”。

建议新建的实现单元：Kernel `internal/allocation/`；平台 `allocation_snapshot.go`、`allocation_tasks.go`、`allocation_execute.go`、`allocation_metrics.go`、`allocation_migration.go`。复用运行基础设施，不把所有业务继续堆入 `pools.go`。筛选与分配客户端分别做 capability 校验和 pin 冻结。

## 12. 历史数据、启用和回退

### 12.1 迁移

1. 新增表、约束预检和需求接收状态。主记录启用且规则有效的需求默认 receiving，其余 paused；不读取 HC 数量决定默认值。启用前报告以前因 HC=0 被挡住、现在将参与分配的需求。
2. 从已验证的评估快照回填资格。无法证明原文/标准/标签版本一致的记录标记需重评；不伪造 admitted 或把旧待复核直接当成新 Agent 已通过。
3. 从有证据的历史事件/容量引用回填已分配需求归属，并建立近期供给投影。已下发、已通过记录原地保留，未知历史单列。
4. 对原 `job_hc_exhausted` 的有效待分配成员重新生成工作项，只重跑分配；不重新分析简历，不自动切换志愿。
5. 新任务停止创建 `core_processingrunjobcapacity`、停止写 reservation ID；历史表和老 attempt 的 releaseCapacity 继续保留兼容。旧 HC 系数只读保留历史，不继续影响新任务。

### 12.2 启用边界

按主体/池设置版本化 `allocation_mode=legacy / simulate / execute_v1`。试算阶段只比较方案，执行仍按既有明确模式；进入 execute_v1 前等待该范围旧分配事务结束、暂停旧分配入口，记录切换 epoch，重排有效待分配工作项。筛选可以继续产生资格，但只有当前 epoch 的分配执行器能提交。

旧处理任务可以完成原有筛选；其分配部分必须经过统一路由，不能因携带旧 HC 快照就绕过新执行器。任何阶段均不能同时让两个分配器对同一成员创建结果。

功能回退优先暂停新分配，保留入池资格和已执行归属。代码回退依赖 Git 版本；不能直接回到不认识新接收状态、分配归属与 epoch 的旧二进制继续写库。数据采取增量迁移，不自动回滚或删除新增表。已执行方案的业务撤销使用现有取消/改派流程逐条审计，不能通过删任务或改库复原。

部署仍由平台、Kernel、PostgreSQL、Redis 四个常驻容器组成。正式发布、镜像构建、推送、上线不属于本次设计工作；实施时按独立版本兼容矩阵交付。

## 13. 验收矩阵与证据边界

| 编号 | 场景 | 必须满足 |
|---|---|---|
| A01 | 筛选达标/未达标/模型超时 | 仅达标入池；超时不记为筛选拒绝 |
| A02 | 非公开岗位且 receiving | 映射、标签满足时可分配；不受 is_public 限制 |
| A03 | HC=0/1/100 或修改 HC | 同一其他输入下去向完全一致，无容量占用 |
| A04 | 优先标签命中数不同 | 命中更多者优先，即使其供给较多 |
| A05 | 同标签匹配、priority 不同 | 数字更小者优先，均衡不能覆盖 |
| A06 | 同匹配、同 priority | 近期供给更少者优先；批内临时计数生效 |
| A07 | 所有比较项相同 | 最终 demand ID 稳定；重复运行同快照结果一致 |
| A08 | 缺标签/低置信度/待核实 | 不擅自补标签；留池并指出原因，不能撤销筛选通过 |
| A09 | 接收暂停/需求关闭/映射越界 | 不参与分配；无可行目标时 wait；同名其他投递不能越界 |
| A10 | 分配输入注入正文/quote/URL/自由文本摘要 | 严格 DTO 拒绝；工具目录不存在原文读取能力 |
| A11 | 不同批次/多个 worker 并发 | 同范围统计统一；重复候选人最多一个有效去向 |
| A12 | 计算时标签、志愿、需求状态或供给变化 | 快照过期整单零业务写入；新代次重算 |
| A13 | 同幂等键重试/同键换输入/超时后重试 | 原结果复用/冲突拒绝/无重复分配 |
| A14 | Agent 少返回、多返回、重复返回或改优先级 | 整单拒绝，无部分静默落库 |
| A15 | 试算、旧 worker、取消和人工改派并发 | 试算不占用，旧租约无法提交，已生效记录稳定 |
| A16 | 下发前取消/下发后拒绝 | 供给统计按第 6.3 节处理，不能靠拒绝提高优先权 |
| A17 | 历史 HC 记录与未知需求回填 | 有证据才归属；新分配零 reservation；旧释放仍幂等 |
| A18 | 需求/标准变更 | 需求优先级或 HC 修改不重跑筛选；标准语义或标签定义变化使相关资格需重评 |
| A19 | 接口人与 HR 权限 | 不能通过方案、工具结果或导出扩展原文和跨部门访问范围 |
| A20 | 进程退出/Redis 消息丢失/数据库事务回滚 | 工作项可恢复，筛选不重复，状态与归属原子一致 |

合成案例 JSON 只覆盖 A02–A09 的纯排序设计和边界，不能代替 A11–A20 的数据库/接口集成验证。[设计参考计算器](verify-two-agent-design.py)可通过 `python3 docs/plans/verify-two-agent-design.py` 运行，它使用手工确定的预期结果，并检查 HC/公开状态和传输顺序变化不会改变规则结果。此脚本仅为设计演算，不是生产分配实现或已发布 DTO 校验器。必须把这些案例转为 Kernel 单测和平台独立复算的契约测试；不要让同一个有缺陷的实现充当唯一测试 oracle。

实施阶段命令：contracts `make check` 及 bundle 两消费者漂移检查；Kernel `make check build`；平台 `make check build`，数据库/Redis 集成测试使用独立测试实例。按更改范围完成一次必要验证，不因本文设计而提前构建镜像或执行全套发布。

业务试点记录：在符合条件且接收中的需求集合中统计非公开岗位供给、待分配原因、人工改派次数、处理等待时间。与切换前使用相同口径比较，不能把总人数趋于平均当成业务成功；部门筛选通过不等于正式录用。规则/合成/单测通过也不表示真实模型、W3 或内网业务验收已完成。

## 14. 本期范围与后续扩展

本期交付边界：双任务契约、结构化资格投影、确定性分配 Agent、内部需求接收状态、同级均衡、异步执行、归属审计、迁移与可观测性。

后续独立评估：同池内部转荐、模型参与多轮方案比较、不同岗位处理能力权重、正式 Offer/录用与 HC 核销。它们不作为本期实现依赖，也不默认继承“允许 Agent 自行改变规则”的权限。

设计采用的初始工程默认值：同级均衡近 7 天、每任务最多 100 人/200 需求、deterministic 模式、同池单执行租约、状态冲突最多自动重试 3 代。这些是可版本化的实现起点，不是用户已经指定的业务数值；更改时必须明确新版本和验证范围。


## 15. 2026-09-14 本地实施记录

协议 3.1.0、Kernel 独立确定性分配模块、平台资格/工作项/范围/方案/归属迁移、异步调度与独立复算、接收状态及任务/供给页面已实现。`paused` 为独立范围开关，暂停时使旧方案失效并保留资格与归属。供给快照使用微秒时间，并通过 `counted_demand_ids` 保持批内与历史统计一致去重。

原 16 个排序案例已转成协议 fixtures，补充 1 个历史重复推荐的去重案例，由 Kernel 和平台两个实现分别验证。三仓检查及平台隔离 PostgreSQL/Redis + 真实分配 Kernel HTTP 联调已执行；筛选和文本提取仍为合成测试夹具，未替代真实模型、W3 或生产负载验收。默认 legacy，未提交、推送、发布或启用生产范围。[启用、暂停、接口及验证说明](../../ops/two-agent-allocation.md)。
