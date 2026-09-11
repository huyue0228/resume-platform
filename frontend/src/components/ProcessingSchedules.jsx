import { useState } from 'react'
import { Alert, Button, Popconfirm, Space, Table, Tag, Typography, message } from 'antd'
import useTaskRecords from './useTaskRecords'
import { cancelProcessingSchedule, fetchProcessingSchedules, pauseProcessingSchedule, resumeProcessingSchedule } from '../api/services'

const statusLabels = { active: '已启用', paused: '已暂停', completed: '已触发', cancelled: '已取消', failed: '触发失败' }
const runLabels = { pending: '排队中', running: '处理中', waiting_conflict: '等待冲突解除', cancelling: '取消中', cancelled: '已取消', success: '已完成', needs_attention: '需处理', partial_failed: '部分失败', failed: '失败' }
const formatTime = (value) => value ? new Date(value).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }) : '—'

export default function ProcessingSchedules({ onOpenRun, onHistory, query = {}, onQueryChange = () => {} }) {
  const [cancelling, setCancelling] = useState(null)
  const { results, count, loading, error, refresh } = useTaskRecords(fetchProcessingSchedules, { page_size: 20, ...query }, { eventName: 'srf:processing-schedule-created' })
  const cancel = async (record) => {
    setCancelling(record.id)
    try {
      await cancelProcessingSchedule(record.id)
      message.success('已取消后续定时触发')
      await refresh()
    } catch (error) {
      message.error(error.response?.data?.detail || '取消失败，请重试')
    } finally {
      setCancelling(null)
    }
  }
  const toggle = async (record, action) => {
    setCancelling(record.id)
    try {
      await (action === 'pause' ? pauseProcessingSchedule(record.id) : resumeProcessingSchedule(record.id))
      message.success(action === 'pause' ? '已暂停后续触发' : '已恢复计划')
      await refresh()
    } catch (error) { message.error(error.response?.data?.detail || '操作失败，请重试') }
    finally { setCancelling(null) }
  }
  return <section aria-label="定时计划" className="processing-schedules">
    <div className="processing-plan-note"><Typography.Text type="secondary">计划定义何时触发；每次执行都会生成独立记录。暂停计划不会中止已生成的任务。</Typography.Text><Button onClick={refresh} loading={loading}>刷新计划</Button></div>
    {error && <Alert showIcon type="warning" message="定时任务刷新失败，请重试" />}
    <Table rowKey="id" size="small" dataSource={results} loading={loading} scroll={{ x: 1100 }}
      locale={{ emptyText: '暂无定时任务' }}
      pagination={{ current: Number(query.page) || 1, pageSize: 20, total: count, showSizeChanger: false, showTotal: (total) => `共 ${total} 项计划`, onChange: (page) => onQueryChange({ page }) }}
      columns={[
        { title: '计划名称', dataIndex: 'name', width: 180, render: (name, record) => <><div>{name}</div><Typography.Text type="secondary">{record.created_by_username_snapshot}</Typography.Text></> },
        { title: '处理范围', dataIndex: 'scope_label', width: 230 },
        { title: '触发安排', width: 200, render: (_, record) => <><div>{({ once: '仅一次', daily: '每天', weekly: '每周' })[record.repeat]} · 首次 {formatTime(record.run_at)}</div><div>{record.status === 'paused' ? '已暂停触发' : `下次：${formatTime(record.next_run_at)}`}</div></> },
        { title: '状态', dataIndex: 'status', width: 100, render: (value) => <Tag color={value === 'failed' ? 'error' : value === 'active' ? 'processing' : 'default'}>{statusLabels[value] || value}</Tag> },
        { title: '最近触发', width: 250, render: (_, record) => <><div>{formatTime(record.last_triggered_at)}</div><div>{record.last_message || '尚未触发'}</div>{record.last_run_id && <Button type="link" size="small" onClick={() => onOpenRun(record.last_run_id)}>处理任务 #{record.last_run_id} · {runLabels[record.last_run_status] || '查看结果'}</Button>}</> },
        { title: '操作', width: 190, fixed: 'right', render: (_, record) => <Space size={4} wrap>
          <Button type="link" size="small" onClick={() => onHistory?.(record)}>执行历史</Button>
          {record.can_pause && <Button size="small" loading={cancelling === record.id} onClick={() => toggle(record, 'pause')}>暂停</Button>}
          {record.can_resume && <Button size="small" loading={cancelling === record.id} onClick={() => toggle(record, 'resume')}>恢复</Button>}
          {record.can_cancel && <Popconfirm title="取消后续定时触发？" description="取消后保留历史记录；已生成的处理任务需单独取消。" onConfirm={() => cancel(record)} okText="确认取消" cancelText="返回"><Button danger size="small" loading={cancelling === record.id}>取消定时</Button></Popconfirm>}
        </Space> },
      ]}
    />
  </section>
}
