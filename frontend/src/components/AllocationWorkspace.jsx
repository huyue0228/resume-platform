import TableRowActions from './TableRowActions'
import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Checkbox, Drawer, Empty, Input, Modal, Select, Space, Table, Tabs, Tag, message } from 'antd'
import { useRole } from '../contexts/roleState'
import { fetchAllocationScopes, fetchAllocationSupply, fetchAllocationTasks, fetchAllocationTask, createAllocationTask, retryAllocationTask, cancelAllocationTask, updateAllocationScope, updateDemandReception } from '../api/pools'

// oxlint-disable-next-line react/only-export-components
export const RECEPTION = { receiving: '接收中', paused: '暂停接收', closed: '已结束' }
const STATES = { pending: '排队中', running: '计算中', completed: '已完成', failed: '失败，可独立重试', cancelled: '已取消' }
const WORK_STATES = { pending: '等待分配', leased: '分配中', assigned: '已归属需求', waiting: '等待条件满足', failed: '分配失败', cancelled: '已取消' }
const PLAN_STATES = { proposed: '待校验', simulated: '试算完成', committed: '已执行', stale: '快照失效', invalid: '校验未通过', cancelled: '已取消' }
const REASONS = { allocated: '已归属需求', no_active_mapping: '等待内部需求映射', no_receiving_demand: '等待需求接收', required_tags_unavailable: '缺少可用标签', qualification_stale: '资格已变化，需要重新筛选', allocation_snapshot_stale: '输入变化，请重试', kernel_unavailable: '分配服务暂不可用', allocation_output_invalid: '方案未通过平台校验', allocation_timeout: '分配超时', allocation_scope_paused: '分配暂停状态已调整', allocation_lease_expired: '工作进程租约过期', task_cancelled: '任务已取消' }
const errorText = (error) => (typeof error?.response?.data?.detail === 'string' ? error.response.data.detail : '') || '操作失败，请刷新后重试'

export function AllocationTaskDetail({ taskId, onTaskChange }) {
  const [activeId, setActiveId] = useState(taskId)
  useEffect(() => setActiveId(taskId), [taskId])
  const { hasPermission } = useRole()
  const [task, setTask] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [refreshKey, setRefreshKey] = useState(0)
  useEffect(() => {
    if (!activeId) return undefined
    let active = true
    let timer
    const load = async () => {
      try {
        const { data } = await fetchAllocationTask(activeId)
        if (!active) return
        setTask(data); setError('')
        if (['pending', 'running'].includes(data.status)) timer = setTimeout(load, 2500)
      } catch (e) { if (active) setError(errorText(e)) }
    }
    setTask(null); load()
    return () => { active = false; clearTimeout(timer) }
  }, [activeId, refreshKey])
  const act = async (action) => {
    setBusy(true)
    try {
      if (action === 'retry') {
        const { data } = await retryAllocationTask(task.id, { idempotency_key: crypto.randomUUID() })
        message.success(`已创建重试任务 #${data.task_id}`)
        setActiveId(data.task_id); onTaskChange?.(data.task_id)
        window.dispatchEvent(new Event('srf:allocation-task-created'))
      } else { await cancelAllocationTask(task.id); setRefreshKey((n) => n + 1) }
    } catch (e) { setError(errorText(e)) } finally { setBusy(false) }
  }
  const plan = task?.plans?.at(-1)
  return <Space direction="vertical" style={{ width: '100%' }}>
    {error && <Alert type="error" showIcon message={error} action={<Button onClick={() => setRefreshKey((n) => n + 1)}>刷新</Button>} />}
    {task && <>
      <p>任务 #{task.id} · {task.mode === 'simulate' ? '试算' : '执行'} · <Tag>{STATES[task.status]}</Tag></p>
      <Alert type={task.status === 'failed' ? 'warning' : 'info'} showIcon message={task.mode === 'simulate' ? '试算结果不代表已经分配' : '筛选资格已保存，分配失败可独立重试'} description={REASONS[task.error_code] || task.error_code || `已${task.mode === 'simulate' ? '拟' : ''}分配 ${task.progress?.assigned || 0}，等待 ${task.progress?.waiting || 0}`} />
      <Space>{hasPermission('attempt.dispatch') && ['failed', 'cancelled', 'completed'].includes(task.status) && <Button loading={busy} onClick={() => act('retry')}>用新快照重试</Button>}{hasPermission('attempt.dispatch') && ['pending', 'running'].includes(task.status) && <Button loading={busy} onClick={() => act('cancel')}>取消未执行任务</Button>}</Space>
      {!!task.screening_runs?.length && <Space wrap>来源筛选批次：{task.screening_runs.map((r) => <a key={r.run_id} href={`/processing-tasks?tab=runs&run_id=${r.run_id}`}>#{r.run_id}</a>)}</Space>}
      {plan && <><p>方案 #{plan.id} · {PLAN_STATES[plan.status] || plan.status} · {REASONS[plan.validation_code] || plan.validation_code}</p><Table rowKey="member_id" size="small" pagination={false} scroll={{ x: 900 }} dataSource={plan.result?.decisions || []} columns={[
        { title: '入池记录', dataIndex: 'member_id' }, { title: '归属需求', dataIndex: 'demand_id', render: (v) => v || '等待' },
        { title: '部门引用', dataIndex: 'department_ref' }, { title: '命中标签', dataIndex: 'matched_tags', render: (v) => v.join('、') || '—' },
        { title: '优先标签 / 优先级 / 供给 / 最后序号', dataIndex: 'order', render: (v) => v?.length ? `${-v[0]} / ${v[1]} / ${v[2]} / ${v[3]}` : '—' },
        { title: '原因', dataIndex: 'reason_code', render: (v) => REASONS[v] || v },
      ]} /></>}
      {!!task.items?.length && <Table rowKey="id" size="small" dataSource={task.items} columns={[{ title: '入池记录', dataIndex: 'member_id' }, { title: '进度', dataIndex: 'status', render: (v) => WORK_STATES[v] || v }, { title: '等待或失败原因', dataIndex: 'reason_code', render: (v) => REASONS[v] || v }]} />}
    </>}
  </Space>
}

export default function AllocationWorkspace() {
  const { hasPermission } = useRole()
  const [scopes, setScopes] = useState([])
  const [scopeId, setScopeId] = useState(null)
  const [supply, setSupply] = useState([])
  const [tasks, setTasks] = useState([])
  const [selected, setSelected] = useState(null)
  const [edit, setEdit] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [nonpublic, setNonpublic] = useState(false)
  const [zeroSupply, setZeroSupply] = useState(false)
  const refreshGeneration = useRef(0)
  const refresh = useCallback(async () => {
    const generation = ++refreshGeneration.current
    try {
      const { data } = await fetchAllocationScopes()
      if (generation !== refreshGeneration.current) return
      setScopes(data.results || [])
      if (!scopeId) { setScopeId(data.results?.[0]?.id || null); return }
      const [s, t] = await Promise.all([fetchAllocationSupply(scopeId), fetchAllocationTasks({ scope_id: scopeId, page_size: 100 })])
      if (generation !== refreshGeneration.current) return
      setSupply(s.data.results || []); setTasks(t.data.results || []); setError('')
    } catch (e) { setError(errorText(e)) }
  }, [scopeId])
  // This ref is a request generation counter, intentionally invalidated on cleanup.
  // oxlint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { refresh(); const timer = setInterval(refresh, 5000); window.addEventListener('srf:allocation-task-created', refresh); return () => { ++refreshGeneration.current; clearInterval(timer); window.removeEventListener('srf:allocation-task-created', refresh) } }, [refresh])
  const scope = scopes.find((s) => s.id === scopeId)
  const create = async (mode) => {
    setBusy(true)
    try { const { data } = await createAllocationTask({ scope_id: scopeId, mode, idempotency_key: crypto.randomUUID() }); setSelected(data.task_id); await refresh() } catch (e) { setError(errorText(e)) } finally { setBusy(false) }
  }
  const save = async () => {
    setBusy(true)
    try {
      if (edit.kind === 'pause') await updateAllocationScope(scopeId, { paused: edit.value, expected_revision: edit.revision, reason: edit.reason })
      else await updateDemandReception(edit.demand.demand_id, { reception_state: edit.value, expected_revision: edit.demand.revision, reason: edit.reason })
      setEdit(null); await refresh()
    } catch (e) { setError(errorText(e)) } finally { setBusy(false) }
  }
  return <Space direction="vertical" size="middle" style={{ width: '100%' }}>
    <Alert showIcon type="info" message="标签与配置优先级先于供给均衡" description="同等条件下优先选择近 7 天获得推荐较少的需求。公开与非公开岗位共同参与，HC 为计划招聘人数，不限制候选人数量。" />
    {error && <Alert showIcon type="error" message={error} />}
    <Space wrap><Select aria-label="分配范围" placeholder="选择主体与职位池" value={scopeId} style={{ minWidth: 250 }} options={scopes.map((s) => ({ value: s.id, label: `${s.entity} · ${s.pool_code}` }))} onChange={setScopeId} /><Tag>独立分配</Tag><Button onClick={refresh}>刷新供给与任务</Button>
      {hasPermission('settings.manage_config') && scope && <Button onClick={() => setEdit({ kind: 'pause', value: !scope.paused, revision: scope.revision, reason: '' })}>{scope.paused ? '恢复新分配' : '暂停新分配'}</Button>}
      {hasPermission('attempt.dispatch') && scope && <><Button loading={busy} onClick={() => create('simulate')}>试算待分配候选人</Button><Button type="primary" loading={busy} disabled={scope.paused} onClick={() => create('execute')}>执行分配</Button></>}
    </Space>
    {scope?.paused && <Alert type="warning" showIcon message="新分配已暂停" description="筛选继续入池，已有资格和归属保留。恢复后待分配成员使用新快照执行。" />}
    {!scope && <Empty description="暂无可访问的内部职位池，请先配置投递标准和部门需求" />}
    <Tabs items={[
      { key: 'supply', label: '需求供给', children: <Card size="small"><Space><Checkbox checked={nonpublic} onChange={(e) => setNonpublic(e.target.checked)}>仅非公开岗位</Checkbox><Checkbox checked={zeroSupply} onChange={(e) => setZeroSupply(e.target.checked)}>近 7 天零供给</Checkbox></Space><Table rowKey="demand_id" scroll={{ x: 1000 }} dataSource={supply.filter((d) => (!nonpublic || !d.is_public) && (!zeroSupply || !d.recent_supply_count))} columns={[
        { title: '需求 ID', dataIndex: 'demand_id' }, { title: '内部岗位', dataIndex: 'position_name' }, { title: '部门', dataIndex: 'department_name' }, { title: '对外公开', dataIndex: 'is_public', render: (v) => v ? '是' : '否' }, { title: 'HC（计划人数）', dataIndex: 'headcount' },
        { title: '接收状态', dataIndex: 'reception_state', render: (v) => RECEPTION[v] }, { title: '近 7 天推荐', dataIndex: 'recent_supply_count' }, { title: '待下发', dataIndex: 'pending_dispatch' }, { title: '待反馈', dataIndex: 'awaiting_feedback' }, { title: '最后分配', dataIndex: 'last_allocated_at', render: (v) => v ? new Date(v).toLocaleString('zh-CN') : '从未分配' },
        { title: '', width: 64, render: (_, d) => hasPermission('job.manage') && <TableRowActions><Button onClick={() => setEdit({ kind: 'reception', demand: d, value: d.reception_state, reason: '' })}>调整接收状态</Button></TableRowActions> },
      ]} /></Card> },
      { key: 'tasks', label: '分配任务', children: <Table rowKey="id" dataSource={tasks} columns={[{ title: '任务', dataIndex: 'id', render: (v) => <Button type="link" onClick={() => setSelected(v)}>#{v}</Button> }, { title: '模式', dataIndex: 'mode', render: (v) => v === 'simulate' ? '试算' : '执行' }, { title: '状态', dataIndex: 'status', render: (v) => STATES[v] }, { title: '结果', render: (_, t) => `${t.mode === 'simulate' ? '拟' : '已'}分配 ${t.progress?.assigned || 0}，等待 ${t.progress?.waiting || 0}` }, { title: '原因', dataIndex: 'error_code', render: (v) => REASONS[v] || v }]} /> },
    ]} />
    <Modal okText="保存" cancelText="取消" title={edit?.kind === 'pause' ? (edit.value ? '暂停新分配' : '恢复新分配') : '调整需求接收状态'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} confirmLoading={busy} okButtonProps={{ disabled: !edit?.reason?.trim() }}>
      {edit?.kind === 'pause' && <p>{edit.value ? '暂停后，运行中的方案将失效；保留筛选资格、待分配成员和已提交归属。' : '恢复后，系统将继续处理待分配成员。'}</p>}
      {edit?.kind !== 'pause' && <Select aria-label="目标状态" value={edit?.value} style={{ width: '100%' }} options={Object.entries(RECEPTION).map(([value, label]) => ({ value, label }))} onChange={(value) => setEdit({ ...edit, value })} />}
      <Input.TextArea aria-label="调整原因" placeholder="填写调整原因，操作会保留记录" value={edit?.reason} onChange={(e) => setEdit({ ...edit, reason: e.target.value })} style={{ marginTop: 12 }} />
    </Modal>
    <Drawer title={`分配任务 #${selected}`} width={1050} open={!!selected} onClose={() => setSelected(null)}>{selected && <AllocationTaskDetail taskId={selected} onTaskChange={setSelected} />}</Drawer>
  </Space>
}
