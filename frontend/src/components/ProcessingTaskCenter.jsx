import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Alert, Button, Card, Empty, Popconfirm, Progress, Segmented, Space, Table, Tag, Typography, message } from 'antd'
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
import { cancelPipelineRun } from '../api/services'
import useProcessingRuns from './useProcessingRuns'
import './ProcessingTaskCenter.css'

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
  if (run.step === 'resume_process') return '上传后候选人处理'
  if (run.step === 'step2') return '准入核验与岗位分析'
  if (run.step === 'all') return '候选人完整处理'
  return run.step || '处理任务'
}

function scopeSummaryText(run) {
  const summary = run.scope_summary || {}
  const parts = []
  if (summary.candidate_count != null) parts.push(`候选人 ${summary.candidate_count} 名`)
  if (summary.system_statuses?.length) parts.push(`状态 ${summary.system_statuses.join('、')}`)
  if (summary.source) parts.push(`来源 ${summary.source}`)
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
  { key: 'review', label: '待复核' },
  { key: 'dispatch', label: '待下发' },
  { key: 'archive', label: '建议归档' },
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

function TaskTable({ runs, cancellingId, onCancel, onOpenCandidates }) {
  const columns = [
    {
      title: '任务',
      key: 'task',
      width: 220,
      render: (_, run) => (
        <div className="processing-task-table-task">
          <div>
            <Typography.Text strong>{taskTitle(run)}</Typography.Text>
            <Typography.Text type="secondary">#{run.id}</Typography.Text>
          </div>
          <Typography.Text type="secondary">
            {run.mode === 'ai' ? 'Agent 分配' : '历史规则分配'} · {run.created_by_username_snapshot || '系统'}
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
      title: '操作',
      key: 'action',
      fixed: 'right',
      width: 110,
      render: (_, run) => (
        <TaskCancelAction run={run} cancellingId={cancellingId} onCancel={onCancel} />
      ),
    },
  ]

  return (
    <div className="processing-task-table">
      <Table
        rowKey="id"
        columns={columns}
        dataSource={runs}
        pagination={false}
        expandable={{
          rowExpandable: (run) => Boolean(run.stages?.length),
          expandedRowRender: (run) => <TaskExecutionNodes run={run} />,
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
          <span><RobotOutlined /> {run.mode === 'ai' ? 'Agent 分配' : '历史规则分配'}</span>
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
            正在分析 {run.activity.processing} 名 · 待分析 {run.activity.queued} 名
            {run.activity.waiting_conflict ? ` · 等待其他任务释放 ${run.activity.waiting_conflict} 名` : ''}
          </Typography.Text>
        ) : null}
        <TaskExecutionNodes run={run} />

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
        <TaskCancelAction run={run} cancellingId={cancellingId} onCancel={onCancel} />
      </Space>
    </Card>
  )
}

// 处理任务独立页主体：所有有 pipeline.view 权限的 HR/管理员看到同一份服务端任务状态。
export default function ProcessingTaskCenter() {
  const navigate = useNavigate()
  const { runs, loading, error: refreshError, refresh } = useProcessingRuns()
  const [cancellingId, setCancellingId] = useState(null)
  const [viewMode, setViewMode] = useState('card')

  const activeRuns = useMemo(() => runs.filter((run) => ACTIVE_STATUSES.has(run.status)), [runs])
  const visibleRuns = useMemo(() => [...activeRuns, ...runs.filter((run) => !ACTIVE_STATUSES.has(run.status))], [activeRuns, runs])
  const finishedCount = useMemo(() => runs.filter((run) => FINISHED_STATUSES.has(run.status)).length, [runs])
  const attentionCount = useMemo(() => runs.filter((run) => ATTENTION_STATUSES.has(run.status)).length, [runs])

  const cancel = async (run) => {
    setCancellingId(run.id)
    try {
      const { data } = await cancelPipelineRun(run.id)
      message.success(data.status === 'cancelled' ? '任务已取消' : '已请求取消任务')
      await refresh()
    } finally {
      setCancellingId(null)
    }
  }

  const openCandidates = (run, result) => {
    navigate({
      pathname: '/resumes',
      search: `?processing_run_id=${run.id}&processing_result=${result}`,
    })
  }

  return (
    <section className="processing-task-center">
      <div className="processing-task-toolbar">
        <div>
          <Typography.Title level={4}>任务概览</Typography.Title>
          <Typography.Text type="secondary">最近 {runs.length} 条任务 · 处理中每 2 秒更新，空闲时降低刷新频率</Typography.Text>
        </div>
        <div className="processing-task-toolbar-actions">
          <Segmented
            aria-label="任务展示方式"
            value={viewMode}
            onChange={setViewMode}
            options={[
              { label: '卡片', value: 'card', icon: <AppstoreOutlined /> },
              { label: '列表', value: 'list', icon: <UnorderedListOutlined /> },
            ]}
          />
          <Button
            aria-label="刷新任务"
            icon={<ReloadOutlined />}
            onClick={refresh}
            loading={loading}
          >
            刷新
          </Button>
        </div>
      </div>
      {refreshError && <Alert banner showIcon type="warning" message={runs.length ? '更新暂时失败，当前显示上次获取的状态' : '暂时无法获取任务，请点击刷新重试'} />}
      <div className="processing-task-summary" aria-label="最近任务概览">
        <div>
          <SyncOutlined spin={Boolean(activeRuns.length)} />
          <span>进行中</span>
          <strong>{activeRuns.length}</strong>
        </div>
        <div>
          <CheckCircleOutlined />
          <span>已结束</span>
          <strong>{finishedCount}</strong>
        </div>
        <div className={attentionCount ? 'has-attention' : ''}>
          <ExclamationCircleOutlined />
          <span>需关注</span>
          <strong>{attentionCount}</strong>
        </div>
      </div>
      {viewMode === 'card' ? (
        <div className="processing-task-list">
          {visibleRuns.length ? visibleRuns.map((run) => (
            <TaskCard
              key={run.id}
              run={run}
              cancellingId={cancellingId}
              onCancel={cancel}
              onOpenCandidates={openCandidates}
            />
          )) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无处理任务" />}
        </div>
      ) : (
        <TaskTable
          runs={visibleRuns}
          cancellingId={cancellingId}
          onCancel={cancel}
          onOpenCandidates={openCandidates}
        />
      )}
    </section>
  )
}
