import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Drawer, Input, Modal, Select, Space, Spin, Tag, Timeline, message } from 'antd'
import { fetchPoolMember, actOnPoolMember } from '../api/pools'
import { retryAgentDecision } from '../api/services'
import { useRole } from '../contexts/roleState'
import CandidateAnalysis from './CandidateAnalysis'
import { AllocationTaskDetail } from './AllocationWorkspace'

const POOL_STATUS = { pending_allocation: '入池待分配', allocated: '已分配', needs_reanalysis: '需要重新评估', closed: '资格已关闭', rejected: '历史复核未通过' }
const EVENTS = { review_stage_removed: '按入池判定继续处理', assessment_saved: '投递评估完成', review_approved: '复核通过', review_rejected: '历史复核未通过', tags_revised: '人工修订标签', allocation_waiting: '等待分配', allocated: '完成分配', closed: '关闭入池资格' }

export default function PoolMemberDrawer({ memberId, onClose, onChange }) {
  const request = useRef(0)
  const { hasPermission } = useRole()
  const [member, setMember] = useState(null)
  const [busy, setBusy] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [edit, setEdit] = useState(null)
  const [analysis, setAnalysis] = useState(null)
  const [taskId, setTaskId] = useState(null)
  const canManage = hasPermission('attempt.dispatch')
  const open = async (id) => {
    const generation = ++request.current
    setLoadError(false)
    try {
      const { data } = await fetchPoolMember(id)
      if (generation === request.current) setMember(data)
    } catch {
      if (generation === request.current) setLoadError(true)
    }
  }
  useEffect(() => {
    const generation = ++request.current
    setMember(null); setEdit(null); setAnalysis(null); setTaskId(null); setLoadError(false)
    if (memberId) fetchPoolMember(memberId).then(({ data }) => {
      if (generation === request.current) setMember(data)
    }).catch(() => { if (generation === request.current) setLoadError(true) })
    const pending = request
    return () => { ++pending.current }
  }, [memberId])
  const close = () => { ++request.current; setMember(null); setEdit(null); onClose() }
  const act = async (action, values = {}) => {
    setBusy(true)
    try {
      const { data } = await actOnPoolMember(member.id, action, { revision: member.revision, ...values })
      message.success(data.detail)
      if (data.task_id) { setTaskId(data.task_id); window.dispatchEvent(new Event('srf:allocation-task-created')) }
      setEdit(null)
      await open(member.id)
      onChange?.()
    } catch (error) {
      if (error?.response?.status === 409) { setEdit(null); await open(member.id) }
    } finally { setBusy(false) }
  }
  return <>
    <Drawer title={member ? `${member.assessment?.pool?.name} · ${POOL_STATUS[member.status]}` : '入池与分配详情'} open={!!memberId} onClose={close} width={800}>
      {loadError && <Alert type="error" message="入池详情加载失败" action={<Button onClick={() => open(memberId)}>重试</Button>} />}
      {!member && !loadError && <Spin />}
      {member && <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <div><h3>{member.assessment?.standard?.name}</h3><p>{member.assessment?.reason}</p><Button onClick={() => setAnalysis({ id: member.decision_id })}>查看本次投递评估与原文证据</Button></div>
        <div><h3>分配状态与需求归属</h3><p>{member.allocation?.reason || POOL_STATUS[member.status]}</p>{member.allocation?.target && <p>{member.allocation.target.department_name} · {member.allocation.target.demand_name || '部门分配'} · 需求 #{member.allocation.target.demand_id || '未指定'}</p>}{member.allocation?.task_id && <Button onClick={() => setTaskId(member.allocation.task_id)}>查看独立分配任务</Button>}</div>
        <Space wrap>
          {canManage && member.status === 'pending_allocation' && <Button type="primary" loading={busy} onClick={() => act('allocate')}>重新执行分配</Button>}
          {canManage && member.status === 'pending_allocation' && <Button onClick={() => setEdit({ action: 'tags', note: '', supported: (member.tags || []).filter((t) => t.status === 'supported').map((t) => t.code), uncertain: (member.tags || []).filter((t) => t.status === 'needs_verification').map((t) => t.code) })}>修订能力标签</Button>}
          {hasPermission('pipeline.run') && member.status === 'needs_reanalysis' && <Button loading={busy} onClick={async () => { setBusy(true); try { await retryAgentDecision(member.decision_id); message.success('已创建重新评估任务'); window.dispatchEvent(new Event('srf:processing-run-created')); close(); onChange?.() } finally { setBusy(false) } }}>重新评估当前投递</Button>}
        </Space>
        <div><h3>标签证据</h3>{(member.tags || []).map((tag) => <div key={tag.code}><Tag color={tag.status === 'supported' ? 'blue' : 'orange'}>{member.assessment?.tag_catalog?.find((d) => d.code === tag.code)?.name || tag.code}</Tag><span>{tag.status === 'supported' ? '已确认' : '待核实'} · {tag.source === 'manual' ? '人工修订' : `置信度 ${Math.round(tag.confidence * 100)}%`}</span>{tag.note && <p>修订依据：{tag.note}</p>}{(tag.evidence || []).map((e, i) => <blockquote key={i}>{e.quote}<small>（第 {e.page} 页，第 {e.start_line}–{e.end_line} 行）</small></blockquote>)}</div>)}</div>
        <div><h3>入池与分配记录</h3><Timeline items={(member.events || []).map((e) => ({ key: e.id, children: <><strong>{EVENTS[e.kind] || e.kind}</strong><p>{e.payload?.message || e.payload?.reason || e.payload?.note}</p>{e.kind === 'tags_revised' && <p>修订后：{(e.payload.tags || []).map((t) => `${member.assessment?.tag_catalog?.find((d) => d.code === t.code)?.name || t.code}（${t.status === 'supported' ? '已确认' : '待核实'}）`).join('、') || '无标签'}</p>}<small>{new Date(e.created_at).toLocaleString('zh-CN')}{e.actor_id ? ` · 操作人 ID ${e.actor_id}` : ' · 系统'}</small></> }))} /></div>
      </Space>}
    </Drawer>
    <Modal title="修订能力标签" open={!!edit} onCancel={() => setEdit(null)} confirmLoading={busy} okText="保存" cancelText="取消" okButtonProps={{ disabled: !edit?.note?.trim() }} onOk={() => act('tags', { note: edit.note, tags: [...edit.supported.map((code) => ({ code, status: 'supported' })), ...edit.uncertain.map((code) => ({ code, status: 'needs_verification' }))] })}>
      {edit && <>{[['supported', '已确认'], ['uncertain', '待核实']].map(([field, label]) => <div key={field}><p>{label}</p><Select aria-label={label} mode="multiple" style={{ width: '100%' }} value={edit[field]} options={(member?.assessment?.tag_catalog || []).filter((t) => !edit[field === 'supported' ? 'uncertain' : 'supported'].includes(t.code)).map((t) => ({ value: t.code, label: t.name }))} onChange={(value) => setEdit({ ...edit, [field]: value })} /></div>)}</>}
      <p>请填写修订依据，操作会保留历史。</p><Input.TextArea aria-label="操作依据" rows={3} value={edit?.note} onChange={(e) => setEdit({ ...edit, note: e.target.value })} />
    </Modal>
    <Drawer title={`分配任务 #${taskId}`} width={1050} open={!!taskId} onClose={() => setTaskId(null)}>{taskId && <AllocationTaskDetail taskId={taskId} onTaskChange={setTaskId} />}</Drawer>
    <Drawer width={1000} open={!!analysis} onClose={() => setAnalysis(null)} title="当前投递评估"><CandidateAnalysis decision={analysis} /></Drawer>
  </>
}
