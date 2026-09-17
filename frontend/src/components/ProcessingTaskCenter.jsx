import { confirmProcessingConfiguration } from './checkProcessingConfiguration'
import TableRowActions from './TableRowActions'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Alert, Button, Card, Empty, Drawer, Input, Pagination, Popconfirm, Progress, Segmented, Select, Space, Spin, Table, Tabs, Tag, Typography, message } from 'antd'
import {
  AppstoreOutlined,
  CheckCircleOutlined,
  ClockCircleOutlined,
  DownOutlined,
  ExclamationCircleOutlined,
  ReloadOutlined,
  RobotOutlined,
  StopOutlined,
  SyncOutlined,
  UpOutlined,
  UserOutlined,
  UnorderedListOutlined,
} from '@ant-design/icons'
import { cancelPipelineRun, fetchPipelineRun, createProcessingSchedule } from '../api/services'
import useProcessingRuns from './useProcessingRuns'
import './ProcessingTaskCenter.css'
import TaskMaterials from './TaskMaterials'
import ProcessingSchedules from './ProcessingSchedules'
import AllocationWorkspace from './AllocationWorkspace'
import useTaskRecords from './useTaskRecords'
import ResumeProcessModal from '../pages/resumes/ResumeProcessModal'
import { useRole } from '../contexts/roleState'
import { useProcessRunner } from './useProcessRunner'

const SOURCE_LABELS = { manual: '手动触发', schedule: '定时触发', resume_import: '导入后处理', ai_retry: '单份重试' }
const ACTIVE_STATUSES = new Set(['pending', 'running', 'waiting_conflict', 'cancelling'])
const STATUS_META = {
  pending: { text: '排队中', color: 'default' },
  running: { text: '处理中', color: 'processing' },
  waiting_conflict: { text: '等待同一候选人处理', color: 'warning' },
  cancelling: { text: '正在取消', color: 'warning' },
  cancelled: { text: '已取消', color: 'default' },
  success: { text: '已完成', color: 'success' },
  needs_attention: { text: '存在需处理项', color: 'warning' },
  partial_failed: { text: '部分失败', color: 'warning' },
  failed: { text: '失败', color: 'error' },
}
const FINISHED_STATUSES = new Set(['success', 'needs_attention', 'partial_failed', 'failed', 'cancelled'])
const ATTENTION_STATUSES = new Set(['needs_attention', 'partial_failed', 'failed'])

function formatTime(value) {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '-'
}

function formatDuration(value) {
  const totalSeconds = Math.max(0, Math.floor(Number(value) || 0))
  const days = Math.floor(totalSeconds / 86400)
  const hours = Math.floor((totalSeconds % 86400) / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (days) return `${days}天${hours ? `${hours}小时` : ''}`
  if (hours) return `${hours}小时${minutes ? `${minutes}分` : ''}`
  if (minutes) return `${minutes}分${seconds ? `${seconds}秒` : ''}`
  return `${seconds}秒`
}

function progressOf(run) {
  const stages = run.stages || []
  if (stages.length) {
    const completed = stages.filter((stage) => ['success', 'needs_attention', 'partial_failed'].includes(stage.status)).length
    const active = stages.find((stage) => stage.status === 'running')
    const activePart = active?.total_count
      ? Math.min(1, Number(active.processed_count || 0) / Number(active.total_count))
      : 0
    return Math.round(Math.min(100, ((completed + activePart) / stages.length) * 100))
  }
  return run.total_count
    ? Math.round(Math.min(100, (Number(run.processed_count || 0) / Number(run.total_count)) * 100))
    : 0
}

const NODE_STATUS = {
  pending: { label: '待执行', color: 'default' },
  running: { label: '进行中', color: 'processing' },
  success: { label: '已完成', color: 'success' },
  needs_attention: { label: '已完成 · 需处理', color: 'warning' },
  partial_failed: { label: '已完成 · 部分失败', color: 'warning' },
  failed: { label: '失败', color: 'error' },
  cancelled: { label: '已取消', color: 'default' },
  skipped: { label: '未执行', color: 'default' },
}

function currentNode(run) {
  return (run.stages || []).find((node) => node.step === run.current_stage)
    || (run.stages || []).find((node) => node.status === 'running')
}

function nextNode(run) {
  if (!ACTIVE_STATUSES.has(run.status) || run.status === 'cancelling') return null
  return (run.stages || []).find((node) => node.status === 'pending')
}

function stageCounter(run) {
  const node = currentNode(run)
  if (node) return node.total_count > 1 ? `${node.processed_count || 0} / ${node.total_count}` : null
  return run.total_count ? `${run.processed_count || 0} / ${run.total_count}` : null
}

function TaskExecutionNodes({ run }) {
  const nodes = run.stages || []
  if (!nodes.length) return null
  const active = currentNode(run)
  const next = nextNode(run)
  return (
    <section className="processing-task-nodes" aria-label={`任务 ${run.id} 的执行节点`}>
      <div className="processing-task-nodes-heading">
        <Typography.Text strong>任务节点</Typography.Text>
        <Typography.Text type="secondary">
          {run.status === 'cancelling' ? '正在停止，后续节点不会继续执行'
            : next ? `下一步：${next.label}`
              : ACTIVE_STATUSES.has(run.status) ? '当前为最后一个节点'
                : ['failed', 'cancelled'].includes(run.status) ? '任务已停止' : '全部节点已结束'}
        </Typography.Text>
      </div>
      <ol className="processing-task-node-list">
        {nodes.map((node, index) => {
          const state = NODE_STATUS[node.status] || { label: node.status, color: 'default' }
          const isCurrent = node === active
          const completed = ['success', 'needs_attention', 'partial_failed'].includes(node.status)
          return (
            <li key={node.step} className={`processing-task-node is-${node.status}`}
              aria-current={isCurrent ? 'step' : undefined}>
              <span className="processing-task-node-mark" aria-hidden="true">
                {node.status === 'running' ? <SyncOutlined spin />
                  : completed ? <CheckCircleOutlined />
                    : node.status === 'failed' ? <ExclamationCircleOutlined /> : index + 1}
              </span>
              <div className="processing-task-node-body">
                <div className="processing-task-node-title">
                  <span>{node.label}</span>
                  <Tag color={state.color} bordered={false}>
                    {node.step === 'queued' && node.status === 'running' ? '排队中' : state.label}
                  </Tag>
                </div>
                <div className="processing-task-node-description">{node.error || node.message || node.description}</div>
                {node.started_at && node.step !== 'queued' ? (
                  <div className="processing-task-node-metrics">
                    {node.total_count > 1 ? <span>已处理 {node.processed_count || 0} / {node.total_count}</span> : null}
                    {node.elapsed_seconds != null ? <span>耗时 {formatDuration(node.elapsed_seconds)}</span> : null}
                  </div>
                ) : null}
              </div>
            </li>
          )
        })}
      </ol>
    </section>
  )
}

function taskTitle(run) {
  if (run.scope_summary?.task_name || run.scope?.task_name) return run.scope_summary?.task_name || run.scope?.task_name
  if (run.step === 'resume_process') return '上传后候选人处理'
  if (run.step === 'step2') return '准入核验与岗位分析'
  if (run.step === 'all') return '候选人完整处理'
  return run.step || '处理任务'
}

function scopeSummaryText(run) {
  const summary = run.scope_summary || {}
  const parts = []
  if (summary.candidate_count != null) parts.push(`候选人 ${summary.candidate_count} 名`)
  if (summary.system_statuses?.length) parts.push(`状态 ${summary.system_statuses.map((status) => RESUME_STATUSES[status]?.text || status).join('、')}`)
  if (summary.source) parts.push(`来源 ${SOURCE_LABELS[summary.source] || '系统触发'}`)
  return parts.join(' · ')
}

const RESULT_METRICS = [
  { key: 'completed', label: '处理完成' },
  { key: 'needs_attention', label: '需处理', danger: true },
  { key: 'failed', label: '失败', danger: true },
  { key: 'skipped', label: '跳过' },
  { key: 'cancelled', label: '取消' },
]
const SUCCESS_DETAIL_METRICS = [
  { key: 'review', label: '历史复核' },
  { key: 'dispatch', label: '达标入池' },
  { key: 'archive', label: '志愿不通过' },
]

function hasTaskResults(run) {
  return Boolean(
    run.completed_count || run.needs_attention_count || run.failed_count || run.review_count || run.dispatch_count
      || run.archive_count || run.skipped_count || run.cancelled_count,
  )
}

function TaskResultMetrics({ run, onOpenCandidates }) {
  const [successDetailsOpen, setSuccessDetailsOpen] = useState(false)

  return (
    <>
      <div className="processing-task-results">
        {RESULT_METRICS.map(({ key, label, danger }) => {
          const count = Number(run[`${key}_count`] || 0)
          return (
            <div key={key} className="processing-task-result-item">
              <Button
                type="text"
                size="small"
                disabled={!count}
                className={`processing-task-result-button ${danger && count ? 'is-danger' : ''}`}
                onClick={() => onOpenCandidates(run, key)}
                aria-label={`筛选本任务${label}简历 ${count} 名`}
              >
                <strong>{count}</strong>{label}
              </Button>
              {key === 'completed' && run.mode === 'ai' ? (
                <Button
                  type="text"
                  size="small"
                  className="processing-task-result-toggle"
                  icon={successDetailsOpen ? <UpOutlined /> : <DownOutlined />}
                  aria-label={successDetailsOpen ? '收起成功子项' : '展开成功子项'}
                  onClick={() => setSuccessDetailsOpen((open) => !open)}
                />
              ) : null}
            </div>
          )
        })}
      </div>
      {run.mode === 'ai' && successDetailsOpen ? (
        <div className="processing-task-success-details">
          <div className="processing-task-success-metrics">
            {SUCCESS_DETAIL_METRICS.map(({ key, label }) => {
              const count = Number(run[`${key}_count`] || 0)
              return (
                <Button
                  key={key}
                  type="text"
                  size="small"
                  disabled={!count}
                  className="processing-task-result-button"
                  onClick={() => onOpenCandidates(run, key)}
                  aria-label={`筛选本任务${label}简历 ${count} 名`}
                >
                  <strong>{count}</strong>{label}
                </Button>
              )
            })}
          </div>
          <Typography.Text type="secondary">
            Agent 子项合计可小于处理完成总数；Policy 前置结束项不会产生 Agent 建议。
          </Typography.Text>
        </div>
      ) : null}
    </>
  )
}

function TaskCancelAction({ run, cancellingId, onCancel }) {
  if (!ACTIVE_STATUSES.has(run.status)) return null

  return (
    <Popconfirm
      title="取消处理任务？"
      description="已完成的候选人处理结果会保留，未开始的处理不会继续执行。"
      okText="取消任务"
      cancelText="返回"
      okButtonProps={{ danger: true }}
      onConfirm={() => onCancel(run)}
    >
      <Button danger size="small" icon={<StopOutlined />} loading={cancellingId === run.id}>
        取消任务
      </Button>
    </Popconfirm>
  )
}

function TaskListResults({ run, onOpenCandidates }) {
  const metrics = run.mode === 'ai'
    ? [...RESULT_METRICS, ...SUCCESS_DETAIL_METRICS]
    : RESULT_METRICS
  const availableMetrics = metrics.filter(({ key }) => Number(run[`${key}_count`] || 0) > 0)

  if (!availableMetrics.length) return <Typography.Text type="secondary">-</Typography.Text>

  return (
    <div className="processing-task-table-results">
      {availableMetrics.map(({ key, label, danger }) => {
        const count = Number(run[`${key}_count`] || 0)
        return (
          <Button
            key={key}
            type="link"
            size="small"
            danger={danger}
            onClick={() => onOpenCandidates(run, key)}
            aria-label={`筛选本任务${label}简历 ${count} 名`}
          >
            {label} {count}
          </Button>
        )
      })}
    </div>
  )
}

function TaskTable({ runs, cancellingId, onCancel, onOpenCandidates, onOpenRun, loading }) {
  const columns = [
    {
      title: '任务',
      key: 'task',
      width: 220,
      render: (_, run) => (
        <div className="processing-task-table-task">
          <div>
            <Button type="link" size="small" onClick={() => onOpenRun(run.id)}>{taskTitle(run)}</Button>
            <Typography.Text type="secondary">#{run.id}</Typography.Text>
          </div>
          <Typography.Text type="secondary">
            {SOURCE_LABELS[run.scope_summary?.source || run.scope?.source || 'manual'] || '系统触发'} · {run.created_by_username_snapshot || '系统'}
          </Typography.Text>
        </div>
      ),
    },
    {
      title: '状态',
      key: 'status',
      width: 130,
      render: (_, run) => {
        const status = STATUS_META[run.status] || { text: run.status || '未知', color: 'default' }
        return <Tag color={status.color} bordered={false}>{status.text}</Tag>
      },
    },
    {
      title: '当前与下一步',
      key: 'stage',
      width: 210,
      render: (_, run) => {
        const stage = currentNode(run)
        return (
          <div className="processing-task-table-stage">
            <Typography.Text>{stage?.label || run.message || '等待任务开始'}</Typography.Text>
            {nextNode(run) ? <Typography.Text type="secondary">下一步：{nextNode(run).label}</Typography.Text> : null}
            {stageCounter(run) ? (
              <Typography.Text type="secondary">{stageCounter(run)}</Typography.Text>
            ) : null}
            {run.error ? <span className="processing-task-table-error" title={run.error}>{run.error}</span> : null}
          </div>
        )
      },
    },
    {
      title: '进度',
      key: 'progress',
      width: 150,
      render: (_, run) => {
        const percent = progressOf(run)
        return (
          <div className="processing-task-table-progress">
            <Typography.Text>{percent}%</Typography.Text>
            <Progress
              percent={percent}
              showInfo={false}
              size="small"
              status={run.status === 'failed' ? 'exception' : run.status === 'success' ? 'success' : undefined}
            />
          </div>
        )
      },
    },
    {
      title: '处理结果',
      key: 'results',
      width: 280,
      render: (_, run) => <TaskListResults run={run} onOpenCandidates={onOpenCandidates} />,
    },
    {
      title: '任务耗时',
      dataIndex: 'elapsed_seconds',
      width: 110,
      render: formatDuration,
    },
    {
      title: '提交时间',
      dataIndex: 'created_at',
      width: 180,
      render: formatTime,
    },
    {
      title: '',
      key: 'action',
      fixed: 'right',
      width: 64,
      render: (_, run) => (
        <TableRowActions>{ACTIVE_STATUSES.has(run.status) && <TaskCancelAction run={run} cancellingId={cancellingId} onCancel={onCancel} />}</TableRowActions>
      ),
    },
  ]

  return (
    <div className="processing-task-table">
      <Table
        rowKey="id"
        columns={columns}
        dataSource={runs}
        loading={loading}
        pagination={false}
        expandable={{
          rowExpandable: (run) => Boolean(run.stages?.length),
          expandedRowRender: (run) => <><TaskExecutionNodes run={run} /><TaskMaterials run={run} /></>,
          expandIcon: ({ expanded, onExpand, record }) => record.stages?.length ? (
            <Button type="text" size="small" icon={expanded ? <UpOutlined /> : <DownOutlined />}
              aria-label={`${expanded ? '收起' : '展开'}任务 ${record.id} 的执行节点`}
              onClick={(event) => onExpand(record, event)} />
          ) : null,
        }}
        scroll={{ x: 1390 }}
        rowClassName={(run) => (ACTIVE_STATUSES.has(run.status) ? 'is-active' : '')}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无处理任务" /> }}
      />
    </div>
  )
}

function TaskCard({ run, cancellingId, onCancel, onOpenCandidates }) {
  const percent = progressOf(run)
  const stage = currentNode(run)
  const status = STATUS_META[run.status] || { text: run.status || '未知', color: 'default' }
  const scopeSummary = scopeSummaryText(run)
  return (
    <Card
      size="small"
      className={`processing-task-card ${ACTIVE_STATUSES.has(run.status) ? 'is-active' : ''}`}
    >
      <Space direction="vertical" size={12} className="processing-task-card-content">
        <div className="processing-task-heading">
          <div className="processing-task-title-wrap">
            <Typography.Text strong className="processing-task-title">
              {taskTitle(run)}
            </Typography.Text>
            <Typography.Text type="secondary" className="processing-task-id">
              #{run.id}
            </Typography.Text>
          </div>
          <Tag color={status.color} bordered={false} className="processing-task-status">
            {status.text}
          </Tag>
        </div>

        <div className="processing-task-meta">
          <span><UserOutlined /> {run.created_by_username_snapshot || '系统'}</span>
          <span><RobotOutlined /> {SOURCE_LABELS[run.scope_summary?.source || run.scope?.source || 'manual'] || '系统触发'}</span>
          <span>提交于 {formatTime(run.created_at)}</span>
        </div>

        <div className="processing-task-primary-metrics">
          <div>
            <span className="processing-task-metric-label">任务耗时</span>
            <strong><ClockCircleOutlined /> {formatDuration(run.elapsed_seconds)}</strong>
          </div>
          <div>
            <span className="processing-task-metric-label">处理进度</span>
            <strong>{percent}%</strong>
          </div>
        </div>

        <Progress
          percent={percent}
          showInfo={false}
          size="small"
          status={run.status === 'failed' ? 'exception' : run.status === 'success' ? 'success' : undefined}
        />

        <div className="processing-task-stage">
          <Typography.Text>
            {stage ? `当前：${stage.label}` : run.message || '等待任务开始'}
          </Typography.Text>
          {stageCounter(run) ? (
            <Typography.Text type="secondary">
              {stageCounter(run)}
            </Typography.Text>
          ) : null}
        </div>

        {run.activity ? (
          <Typography.Text type="secondary" className="processing-task-activity">
            正在处理 {run.activity.processing} 名 · 待处理 {run.activity.queued} 名
            {run.activity.waiting_conflict ? ` · 等待其他任务释放 ${run.activity.waiting_conflict} 名` : ''}
          </Typography.Text>
        ) : null}
        <TaskExecutionNodes run={run} />
        <TaskMaterials run={run} />

        {scopeSummary ? (
          <Typography.Text type="secondary" className="processing-task-scope">
            范围：{scopeSummary}
          </Typography.Text>
        ) : null}

        {hasTaskResults(run) ? (
          <TaskResultMetrics run={run} onOpenCandidates={onOpenCandidates} />
        ) : null}

        {run.mode === 'ai' && run.ai_concurrency_limit ? (
          <Typography.Text type="secondary" className="processing-task-audit">
            Agent 自适应并发 {run.ai_effective_concurrency || 1}/{run.ai_concurrency_limit}
            {' · '}模型重试 {run.ai_retry_count || 0}
            {' · '}429 限流 {run.ai_rate_limit_count || 0}
          </Typography.Text>
        ) : null}

        {run.error ? <Alert type="error" showIcon message="任务异常" description={run.error} /> : null}
        {run.cancel_requested_at ? (
          <Typography.Text type="secondary" className="processing-task-audit">
            取消请求 {formatTime(run.cancel_requested_at)} · {run.cancelled_at ? `已取消 ${formatTime(run.cancelled_at)}` : '等待安全停止'} · 操作人 {run.cancelled_by_username_snapshot || '系统'}
          </Typography.Text>
        ) : null}
        <TableRowActions>{ACTIVE_STATUSES.has(run.status) && <TaskCancelAction run={run} cancellingId={cancellingId} onCancel={onCancel} />}</TableRowActions>
      </Space>
    </Card>
  )
}

const fetchRunDetail = async ({ id }, options) => {
  const { data } = await fetchPipelineRun(id, options)
  return { data: { results: [data], count: 1 } }
}
const QUERY_KEYS = ['search', 'state', 'source', 'created_from', 'created_to', 'mine', 'created_by', 'schedule_id', 'status', 'repeat', 'ordering', 'page']
const STATUS_OPTIONS = Object.entries(STATUS_META).map(([value, item]) => ({ value, label: item.text }))
const RESUME_STATUSES = { raw: { text: '待处理' }, talent_pool: { text: '人才库' }, pending_allocation: { text: '入池待分配' }, archived: { text: '已归档' }, pending_reallocation: { text: '待重新分配' }, pending_dispatch: { text: '待下发' }, pending_screening: { text: '待业务反馈' }, screening_passed: { text: '通过' }, screening_rejected: { text: '不通过' } }

export default function ProcessingTaskCenter() {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { hasPermission } = useRole()
  const { run: submitRun } = useProcessRunner()
  const tab = ['schedules', 'allocation'].includes(searchParams.get('tab')) ? searchParams.get('tab') : 'runs'
  const viewMode = searchParams.get('view') === 'card' ? 'card' : 'list'
  const query = useMemo(() => Object.fromEntries(QUERY_KEYS.filter((key) => searchParams.get(key)).map((key) => [key, searchParams.get(key)])), [searchParams])
  const { runs, count, summary, loading, error: refreshError, refresh } = useProcessingRuns({ ...query, include_summary: 'true' }, tab === 'runs')
  const detailId = searchParams.get('run_id') || ''
  const detail = useTaskRecords(fetchRunDetail, { id: detailId }, { enabled: Boolean(detailId), activeStatus: ACTIVE_STATUSES })
  const selectedRun = detail.results[0]
  const [cancellingId, setCancellingId] = useState(null)
  const [searchText, setSearchText] = useState(query.search || '')
  const [createOpen, setCreateOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')
  const [scopeStatuses, setScopeStatuses] = useState(['raw'])
  useEffect(() => { setSearchText(query.search || '') }, [query.search])

  const updateQuery = (changes) => {
    const next = new URLSearchParams(searchParams)
    if (!Object.hasOwn(changes, 'page') && !Object.hasOwn(changes, 'run_id') && !Object.hasOwn(changes, 'view')) next.delete('page')
    for (const [key, value] of Object.entries(changes)) {
      if (value == null || value === '') next.delete(key)
      else next.set(key, String(value))
    }
    setSearchParams(next, { replace: !Object.hasOwn(changes, 'run_id') })
  }
  const switchTab = (value) => setSearchParams({ tab: value }, { replace: true })
  const openRun = (id) => updateQuery({ run_id: id })
  const openHistory = (record) => setSearchParams({ tab: 'runs', schedule_id: String(record.id), schedule_name: record.name })
  const cancel = async (run) => {
    setCancellingId(run.id)
    try {
      const { data } = await cancelPipelineRun(run.id)
      message.success(data.status === 'cancelled' ? '任务已取消' : '已请求取消任务')
      await Promise.all([refresh(), detail.refresh()])
    } catch (error) { message.error(error.response?.data?.detail || '取消失败，请重试') }
    finally { setCancellingId(null) }
  }
  const openCandidates = (run, result) => navigate({ pathname: '/resumes', search: `?processing_run_id=${run.id}&processing_result=${result}` })
  const create = async (timing) => {
    setCreating(true)
    setCreateError('')
    try {
      const scope = { system_statuses: scopeStatuses }
      if (timing.repeat === 'now') {
        const response = await submitRun([{ step: 'step2' }], '', { scope, ...(timing.name ? { name: timing.name } : {}) })
        if (!response.success) { setCreateError(response.error); return }
        const id = response.run?.processing_runs?.[0]?.id
        setSearchParams({ tab: 'runs', ...(id ? { run_id: String(id) } : {}) })
        message.success('已提交处理任务')
      } else {
        if (!await confirmProcessingConfiguration(scope)) return
        await createProcessingSchedule({ ...timing, name: timing.name || '定时简历处理', scope })
        window.dispatchEvent(new Event('srf:processing-schedule-created'))
        setSearchParams({ tab: 'schedules' })
        message.success('已创建定时计划')
      }
      setCreateOpen(false)
    } catch (error) {
      setCreateError(['ECONNABORTED', 'ETIMEDOUT'].includes(error.code) || [502, 504].includes(error.response?.status)
        ? '提交请求超时，计划可能已创建。请先查询任务中心，避免重复提交。'
        : error.response?.data?.detail || '创建失败，请重试')
    } finally { setCreating(false) }
  }
  const fallbackSummary = { total: count, active: runs.filter((run) => ACTIVE_STATUSES.has(run.status)).length, attention: runs.filter((run) => ATTENTION_STATUSES.has(run.status)).length, finished: runs.filter((run) => FINISHED_STATUSES.has(run.status)).length }
  const totals = summary || fallbackSummary

  return <section className="processing-task-center">
    <div className="processing-task-toolbar">
      <div><Typography.Title level={4}>任务中心</Typography.Title><Typography.Text type="secondary">创建处理任务，管理触发计划，查询每一次执行。</Typography.Text></div>
      {hasPermission('pipeline.run') && hasPermission('resume.view') && <Button type="primary" onClick={() => { setCreateError(''); setScopeStatuses(['raw']); setCreateOpen(true) }}>处理简历</Button>}
    </div>
    <Tabs className="processing-task-tabs" activeKey={tab} onChange={switchTab} items={[{ key: 'runs', label: '执行记录' }, { key: 'schedules', label: '定时计划' }, { key: 'allocation', label: '分配任务' }]} />
    {tab === 'allocation' && <AllocationWorkspace />}
    <div className="processing-task-filters" style={tab === 'allocation' ? { display: 'none' } : undefined}>
      <Input.Search aria-label="搜索任务" value={searchText} onChange={(event) => setSearchText(event.target.value)} onSearch={(value) => updateQuery({ search: value.trim() })} placeholder="名称、编号或创建人工号" allowClear style={{ width: 270 }} />
      <Select aria-label="创建人范围" value={query.mine || 'all'} onChange={(value) => updateQuery({ mine: value === 'all' ? '' : value })} options={[{ value: 'all', label: '全部创建人' }, { value: 'true', label: '我创建的' }]} style={{ width: 130 }} />
      {tab === 'runs' ? <>
        <Select aria-label="执行状态" value={query.status || undefined} placeholder="全部执行状态" allowClear onChange={(value) => updateQuery({ status: value, state: '' })} options={STATUS_OPTIONS} style={{ width: 155 }} />
        <Select aria-label="触发来源" value={query.source || undefined} placeholder="全部触发来源" allowClear onChange={(value) => updateQuery({ source: value })} options={Object.entries(SOURCE_LABELS).map(([value, label]) => ({ value, label }))} style={{ width: 155 }} />
      </> : <>
        <Select aria-label="计划状态" value={query.state || undefined} placeholder="全部计划状态" allowClear onChange={(value) => updateQuery({ state: value })} options={[{ value: 'active', label: '已启用' }, { value: 'paused', label: '已暂停' }, { value: 'completed', label: '已触发' }, { value: 'failed', label: '触发失败' }, { value: 'cancelled', label: '已取消' }]} style={{ width: 150 }} />
        <Select aria-label="触发频率" value={query.repeat || undefined} placeholder="全部触发频率" allowClear onChange={(value) => updateQuery({ repeat: value })} options={[{ value: 'once', label: '仅一次' }, { value: 'daily', label: '每天' }, { value: 'weekly', label: '每周' }]} style={{ width: 150 }} />
      </>}
      <Space size={6}><span>创建日期</span><Input aria-label="创建开始日期" type="date" value={query.created_from || ''} onChange={(event) => updateQuery({ created_from: event.target.value })} /><span>至</span><Input aria-label="创建结束日期" type="date" value={query.created_to || ''} onChange={(event) => updateQuery({ created_to: event.target.value })} /></Space>
      <Button type="link" onClick={() => setSearchParams({ tab }, { replace: true })}>重置筛选</Button>
    </div>
    {query.schedule_id && <Alert className="processing-history-context" type="info" showIcon message={`计划「${searchParams.get('schedule_name') || `#${query.schedule_id}`}」的执行历史`} action={<Button size="small" onClick={() => switchTab('schedules')}>返回定时计划</Button>} />}
    {tab === 'schedules' ? <ProcessingSchedules query={query} onQueryChange={updateQuery} onOpenRun={openRun} onHistory={openHistory} /> : tab === 'runs' ? <>
      <div className="processing-task-summary" aria-label="查询范围内任务概览">
        {[['', '全部执行', totals.total], ['active', '进行中', totals.active], ['attention', '需关注', totals.attention], ['finished', '已结束', totals.finished]].map(([key, label, total]) => <button key={key} type="button" className={`${query.state === key || (!query.state && !key) ? 'is-selected' : ''} ${key === 'attention' ? 'has-attention' : ''}`} onClick={() => updateQuery({ state: key, status: '' })} aria-pressed={(query.state || '') === key}><span>{label}</span><strong>{total}</strong></button>)}
      </div>
      <div className="processing-record-toolbar"><Typography.Text type="secondary">共 {count} 条匹配记录 · 概览按当前搜索、来源和日期范围统计 · 处理中自动刷新</Typography.Text><Space><Segmented aria-label="任务展示方式" value={viewMode} onChange={(value) => updateQuery({ view: value })} options={[{ label: '列表', value: 'list', icon: <UnorderedListOutlined /> }, { label: '卡片', value: 'card', icon: <AppstoreOutlined /> }]} /><Button aria-label="刷新任务" icon={<ReloadOutlined />} loading={loading} onClick={refresh}>刷新</Button></Space></div>
      {refreshError && <Alert banner showIcon type="warning" message="任务查询失败，请检查筛选条件或刷新重试" />}
      {viewMode === 'card' ? <div className="processing-task-list">{runs.length ? runs.map((run) => <TaskCard key={run.id} run={run} cancellingId={cancellingId} onCancel={cancel} onOpenCandidates={openCandidates} />) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无匹配的执行记录" />}</div> : <TaskTable runs={runs} cancellingId={cancellingId} onCancel={cancel} onOpenCandidates={openCandidates} onOpenRun={openRun} loading={loading} />}
      <div className="processing-task-pagination"><Pagination current={Number(query.page) || 1} pageSize={20} total={count} showSizeChanger={false} showTotal={(total) => `共 ${total} 条`} onChange={(page) => updateQuery({ page })} /></div>
    </> : null}
    <Drawer title={`执行详情 #${detailId}`} open={Boolean(detailId)} width={960} onClose={() => updateQuery({ run_id: '' })}>
      <Typography.Paragraph type="secondary">此详情可通过当前地址直接访问，运行中自动更新。</Typography.Paragraph>
      {detail.error && <Alert type="error" message="无法获取执行记录" action={<Button onClick={detail.refresh}>重试</Button>} />}
      {selectedRun ? <TaskCard run={selectedRun} cancellingId={cancellingId} onCancel={cancel} onOpenCandidates={openCandidates} /> : detail.loading && <Spin />}
    </Drawer>
    <ResumeProcessModal open={createOpen} processing={creating} error={createError} processCurrentSelected={false} processCandidateCount={0} processStatusSelection={scopeStatuses} statusOptions={RESUME_STATUSES} onCurrentSelectedChange={() => {}} onStatusChange={setScopeStatuses} onConfirm={create} onCancel={() => { if (!creating) setCreateOpen(false) }} />
  </section>
}
