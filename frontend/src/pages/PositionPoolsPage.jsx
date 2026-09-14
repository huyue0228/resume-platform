import { useRef, useState } from 'react'
import { PageContainer } from '@ant-design/pro-components'
import { Alert, Button, Drawer, Input, Modal, Select, Space, Tag, Timeline, message } from 'antd'
import { fetchPoolMembers, fetchPoolMember, actOnPoolMember } from '../api/pools'
import { retryAgentDecision } from '../api/services'
import { useRole } from '../contexts/roleState'
import SmartDataTable from '../components/SmartDataTable'
import CandidateAnalysis from '../components/CandidateAnalysis'
import AllocationWorkspace, { AllocationTaskDetail } from '../components/AllocationWorkspace'

const POOL_STATUS = { pending_allocation: '入池待分配', allocated: '已分配', needs_reanalysis: '需要重新评估', closed: '资格已关闭', rejected: '历史复核未通过' }
const EVENTS = { review_stage_removed: '按入池判定继续处理', assessment_saved: '投递评估完成', review_approved: '复核通过', review_rejected: '历史复核未通过', tags_revised: '人工修订标签', allocation_waiting: '等待分配', allocated: '完成分配', closed: '关闭入池资格' }

export default function PositionPoolsPage() {
  const actionRef = useRef()
  const request = useRef(0)
  const { hasPermission } = useRole()
  const [member, setMember] = useState(null)
  const [busy, setBusy] = useState(false)
  const [edit, setEdit] = useState(null)
  const [analysis, setAnalysis] = useState(null)
  const [allocationOpen, setAllocationOpen] = useState(false)
  const [taskId, setTaskId] = useState(null)
  const canManage = hasPermission('attempt.dispatch')
  const open = async (id) => {
    const generation = ++request.current
    const { data } = await fetchPoolMember(id)
    if (generation === request.current) setMember(data)
  }
  const close = () => { ++request.current; setMember(null); setEdit(null) }
  const act = async (action, values = {}) => {
    setBusy(true)
    try {
      const { data } = await actOnPoolMember(member.id, action, { revision: member.revision, ...values })
      message.success(data.detail)
      if (data.task_id) { setTaskId(data.task_id); window.dispatchEvent(new Event('srf:allocation-task-created')) }
      setEdit(null)
      await open(member.id)
      actionRef.current?.reload()
    } catch (error) {
      if (error?.response?.status === 409) { setEdit(null); await open(member.id) }
    } finally { setBusy(false) }
  }
  const columns = [
    { title: '候选人', dataIndex: 'candidate_name', width: 130 },
    { title: '投递编号', dataIndex: 'apply_id', width: 140 },
    { title: '本次投递', dataIndex: 'position_name', width: 180 },
    { title: '内部职位池', dataIndex: 'pool_code', width: 180, filter: { type: 'text', param: 'pool_name', placeholder: '筛选内部职位池' }, render: (_, m) => m.assessment?.pool?.name || m.pool_code },
    { title: '状态', dataIndex: 'status', width: 150, filter: { type: 'select', param: 'status', options: Object.entries(POOL_STATUS).map(([value, label]) => ({ value, label })) }, render: (v) => <Tag>{POOL_STATUS[v] || v}</Tag> },
    { title: '能力标签', dataIndex: 'tags', width: 260, render: (_, m) => (m.tags || []).map((tag) => <Tag key={tag.code} color={tag.status === 'supported' ? 'blue' : 'orange'}>{m.assessment?.tag_catalog?.find((d) => d.code === tag.code)?.name || tag.code}{tag.status !== 'supported' ? ' · 待核实' : ''}</Tag>) },
    { title: '入池记录时间', dataIndex: 'created_at', width: 180, render: (v) => new Date(v).toLocaleString('zh-CN') },
    { title: '操作', valueType: 'option', width: 100, render: (_, m) => <Button type="link" onClick={() => open(m.id)}>查看详情</Button> },
  ]
  return <PageContainer title="职位候选人池">
    <Alert showIcon type="info" message="按当前投递评估，通过后进入对应内部职位池" description="评估达标后直接入池，按标签、配置优先级与同级供给均衡选择内部需求。等待分配不影响筛选通过资格。" style={{ marginBottom: 16 }} />
    <Button onClick={() => setAllocationOpen(true)} style={{ marginBottom: 16 }}>需求供给与分配任务</Button>
    <SmartDataTable tableKey="position-pools" rowKey="id" actionRef={actionRef} columns={columns} request={fetchPoolMembers} />
    <Drawer title={member ? `${member.assessment?.pool?.name} · ${POOL_STATUS[member.status]}` : ''} open={!!member} onClose={close} width={800}>
      {member && <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <div><h3>{member.assessment?.standard?.name}</h3><p>{member.assessment?.reason}</p><Button onClick={() => setAnalysis({ id: member.decision_id })}>查看本次投递评估与原文证据</Button></div>
        <div><h3>分配状态与需求归属</h3><p>{member.allocation?.reason || POOL_STATUS[member.status]}</p>{member.allocation?.target && <p>{member.allocation.target.department_name} · {member.allocation.target.demand_name || '部门分配'} · 需求 #{member.allocation.target.demand_id || '未指定'}</p>}{member.allocation?.task_id && <Button onClick={() => setTaskId(member.allocation.task_id)}>查看独立分配任务</Button>}</div>
        <Space wrap>
          {canManage && member.status === 'pending_allocation' && <Button type="primary" loading={busy} onClick={() => act('allocate')}>重新执行分配</Button>}
          {canManage && member.status === 'pending_allocation' && <Button onClick={() => setEdit({ action: 'tags', note: '', supported: (member.tags || []).filter((t) => t.status === 'supported').map((t) => t.code), uncertain: (member.tags || []).filter((t) => t.status === 'needs_verification').map((t) => t.code) })}>修订能力标签</Button>}
          {hasPermission('pipeline.run') && member.status === 'needs_reanalysis' && <Button loading={busy} onClick={async () => { setBusy(true); try { await retryAgentDecision(member.decision_id); message.success('已创建重新评估任务'); window.dispatchEvent(new Event('srf:processing-run-created')); close(); actionRef.current?.reload() } finally { setBusy(false) } }}>重新评估当前投递</Button>}
        </Space>
        <div><h3>标签证据</h3>{(member.tags || []).map((tag) => <div key={tag.code}><Tag color={tag.status === 'supported' ? 'blue' : 'orange'}>{member.assessment?.tag_catalog?.find((d) => d.code === tag.code)?.name || tag.code}</Tag><span>{tag.status === 'supported' ? '已确认' : '待核实'} · {tag.source === 'manual' ? '人工修订' : `置信度 ${Math.round(tag.confidence * 100)}%`}</span>{tag.note && <p>修订依据：{tag.note}</p>}{(tag.evidence || []).map((e, i) => <blockquote key={i}>{e.quote}<small>（第 {e.page} 页，第 {e.start_line}–{e.end_line} 行）</small></blockquote>)}</div>)}</div>
        <div><h3>入池与分配记录</h3><Timeline items={(member.events || []).map((e) => ({ key: e.id, children: <><strong>{EVENTS[e.kind] || e.kind}</strong><p>{e.payload?.message || e.payload?.reason || e.payload?.note}</p>{e.kind === 'tags_revised' && <p>修订后：{(e.payload.tags || []).map((t) => `${member.assessment?.tag_catalog?.find((d) => d.code === t.code)?.name || t.code}（${t.status === 'supported' ? '已确认' : '待核实'}）`).join('、') || '无标签'}</p>}<small>{new Date(e.created_at).toLocaleString('zh-CN')}{e.actor_id ? ` · 操作人 ID ${e.actor_id}` : ' · 系统'}</small></> }))} /></div>
      </Space>}
    </Drawer>
    <Modal title="修订能力标签" open={!!edit} onCancel={() => setEdit(null)} confirmLoading={busy} okButtonProps={{ disabled: !edit?.note?.trim() }} onOk={() => act('tags', { note: edit.note, tags: [...edit.supported.map((code) => ({ code, status: 'supported' })), ...edit.uncertain.map((code) => ({ code, status: 'needs_verification' }))] })}>
      {edit && <>{[['supported', '已确认'], ['uncertain', '待核实']].map(([field, label]) => <div key={field}><p>{label}</p><Select aria-label={label} mode="multiple" style={{ width: '100%' }} value={edit[field]} options={(member?.assessment?.tag_catalog || []).filter((t) => !edit[field === 'supported' ? 'uncertain' : 'supported'].includes(t.code)).map((t) => ({ value: t.code, label: t.name }))} onChange={(value) => setEdit({ ...edit, [field]: value })} /></div>)}</>}
      <p>请填写修订依据，操作会保留历史。</p><Input.TextArea aria-label="操作依据" rows={3} value={edit?.note} onChange={(e) => setEdit({ ...edit, note: e.target.value })} />
    </Modal>
    <Drawer title="需求供给与分配任务" width={1200} open={allocationOpen} onClose={() => setAllocationOpen(false)}>{allocationOpen && <AllocationWorkspace />}</Drawer>
    <Drawer title={`分配任务 #${taskId}`} width={1050} open={!!taskId} onClose={() => setTaskId(null)}>{taskId && <AllocationTaskDetail taskId={taskId} onTaskChange={setTaskId} />}</Drawer>
    <Drawer width={1000} open={!!analysis} onClose={() => setAnalysis(null)} title="当前投递评估"><CandidateAnalysis decision={analysis} /></Drawer>
  </PageContainer>
}
