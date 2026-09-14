import { useEffect, useState } from 'react'
import { Alert, Checkbox, Input, Modal, Radio, Space, Typography } from 'antd'

export default function ResumeProcessModal({
  open,
  allowScheduling = true,
  initialRepeat = 'now',
  filterSummary = [],
  processing,
  error,
  processCurrentSelected,
  processCandidateCount,
  processStatusSelection,
  statusOptions,
  onCurrentSelectedChange,
  onStatusChange,
  onConfirm,
  onCancel,
}) {
  const [name, setName] = useState('')
  const [runAt, setRunAt] = useState('')
  const [repeat, setRepeat] = useState('now')
  useEffect(() => {
    if (open) {
      setName('')
      setRunAt('')
      setRepeat(initialRepeat)
    }
  }, [open, initialRepeat])
  const scheduled = repeat !== 'now'
  const triggerTime = runAt ? new Date(`${runAt}+08:00`) : null
  const validSchedule = triggerTime?.getTime() > Date.now()
  return (
    <Modal
      title="处理简历"
      width={720}
      style={{ top: 32 }}
      styles={{ body: { maxHeight: 'calc(100vh - 190px)', overflowY: 'auto', paddingRight: 8 } }}
      open={open}
      okText={scheduled ? '创建定时任务' : '开始处理'}
      cancelText="取消"
      confirmLoading={processing}
      okButtonProps={{
        disabled: (!processCurrentSelected && !processStatusSelection.length) || (scheduled && !validSchedule),
      }}
      onOk={() => onConfirm({ name: name.trim(), ...(scheduled ? { run_at: triggerTime?.toISOString() } : {}), repeat })}
      onCancel={onCancel}
    >
      <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {error && <Alert type="error" showIcon message="处理任务提交失败" description={error} />}
        <div>
          <label htmlFor="schedule-name">任务名称 <Typography.Text type="secondary">（选填，便于检索）</Typography.Text></label>
          <Input id="schedule-name" aria-label="任务名称" value={name} maxLength={100} placeholder="例如：研发岗位夜间处理" onChange={(event) => setName(event.target.value)} style={{ marginTop: 8 }} />
        </div>
        <Typography.Text strong>1. 处理范围</Typography.Text>
        <Typography.Text type="secondary">选择勾选的候选人，或按下方状态匹配。状态范围沿用当前表格筛选条件；定时执行时重新匹配，包含之后符合条件的新简历。</Typography.Text>
        <Typography.Text type="secondary">按当前进度继续处理：AI 达标直接入池，不通过继续下一志愿；部门不通过后回到待处理，等待下一轮。人才库和已通过候选人保留现有结果。</Typography.Text>
        <Checkbox
          checked={processCurrentSelected}
          disabled={!processCandidateCount}
          onChange={onCurrentSelectedChange}
        >
          当前选中（{processCandidateCount}）
        </Checkbox>
        <Checkbox.Group
          value={processStatusSelection}
          onChange={onStatusChange}
          style={{ width: '100%' }}
        >
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 12 }}>
            {Object.entries(statusOptions).map(([value, item]) => (
              <Checkbox key={value} value={value}>
                {item.text}
              </Checkbox>
            ))}
          </div>
        </Checkbox.Group>
        {!processCurrentSelected && filterSummary.length > 0 && <Alert type="info" showIcon message="同时应用当前筛选" description={filterSummary.join('；')} />}
        <Typography.Text strong>2. 执行安排</Typography.Text>
        <Radio.Group aria-label="执行方式" value={repeat} onChange={(event) => setRepeat(event.target.value)} options={[
          { label: '立即执行', value: 'now' }, { label: '指定时间', value: 'once', disabled: !allowScheduling },
          { label: '每天', value: 'daily', disabled: !allowScheduling }, { label: '每周', value: 'weekly', disabled: !allowScheduling },
        ]} />
        {scheduled && <div>
          <label htmlFor="schedule-time">首次触发时间（北京时间 UTC+8）</label>
          <Input id="schedule-time" type="datetime-local" value={runAt} onChange={(event) => setRunAt(event.target.value)} style={{ marginTop: 8 }} />
          {runAt && !validSchedule && <Typography.Text type="danger">请选择未来的触发时间</Typography.Text>}
          {repeat !== 'once' && <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>按首次时间的时刻{repeat === 'weekly' ? '和星期' : ''}重复；上一轮未结束时跳过本轮。可在任务中心暂停或取消计划。</Typography.Paragraph>}
        </div>}
        <Alert type="info" showIcon message="提交摘要" description={`${processCurrentSelected ? `固定 ${processCandidateCount} 名候选人` : processStatusSelection.map((value) => statusOptions[value]?.text || value).join('、') || '尚未选择范围'}${!processCurrentSelected && filterSummary.length ? '，含当前筛选条件' : ''} · ${scheduled ? `${({ once: '指定时间执行', daily: '每天执行', weekly: '每周执行' })[repeat]}${runAt ? `，首次 ${runAt.replace('T', ' ')}（北京时间）` : '，待选择时间'}` : '提交后立即排队'}`} />
        {!processCurrentSelected && !processStatusSelection.length && (
          <Typography.Text type="secondary">
            请先勾选当前选中或需要处理的简历状态，再开始处理。
          </Typography.Text>
        )}
      </Space>
    </Modal>
  )
}
