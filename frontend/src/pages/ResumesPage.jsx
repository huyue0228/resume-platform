import { useRef, useEffect, useState, useCallback, useMemo } from 'react'
import dayjs from 'dayjs'
import { useSearchParams } from 'react-router-dom'
import { PageContainer } from '@ant-design/pro-components'
import {
  Button,
  Tag,
  Space,
  Modal,
  message,
  Drawer,
  Descriptions,
  Typography,
  Tooltip,
  Select,
  Radio,
  Input,
  Popconfirm,
  Alert,
  Timeline,
} from 'antd'
import {
  DeleteOutlined,
  DownloadOutlined,
  PlayCircleOutlined,
  SendOutlined,
} from '@ant-design/icons'
import {
  deleteCandidate,
  bulkDeleteCandidates,
  exportCandidates,
  fetchCandidates,
  fetchCandidateFilterOptions,
  fetchManualAssignmentOptions,
  manualAssignResume,
  fetchAgentDecisions,
  retryAgentDecision,
  dispatchAllocation,
  cancelAllocation,
  transferAllocationToManual,
  bulkDispatchCandidates,
  bulkTransferCandidates,
  transferAllocation,
  fetchTransferOptions,
  fetchFeedbackReasons,
  submitAllocationFeedback,
  exportAllocations,
  createProcessingSchedule,
} from '../api/services'
import ImportButton from '../components/ImportButton'
import ResumePreview from '../components/ResumePreview'
import CandidateAnalysis from '../components/CandidateAnalysis'
import SchoolTagBadge from '../components/SchoolTagBadge'
import SmartDataTable from '../components/SmartDataTable'
import ResumeExportModal from '../components/ResumeExportModal'
import { useProcessRunner } from '../components/useProcessRunner'
import { useRole } from '../contexts/roleState'
import { downloadBlobFromResponse } from '../utils/download'
import ResumeProcessModal from './resumes/ResumeProcessModal'
import { buildResumeProcessingScope, describeProcessingFilters } from './resumes/resumeProcessing'
import './ResumesPage.css'

const RESUME_IMPORT_FIELDS = [
  { key: 'resume_list', label: '① 简历信息列表 (.xlsx/.xls/.csv)', accept: '.xlsx,.xls,.csv' },
  { key: 'resume_package', label: '② 简历包 (.zip，文件名含应聘ID)', accept: '.zip' },
]

const PROCESSING_RESULT_LABELS = {
  success: '处理完成',
  completed: '处理完成',
  needs_attention: '需处理',
  failed: '失败',
  review: '历史复核',
  dispatch: '待下发',
  archive: '归档',
  skipped: '跳过',
  cancelled: '取消',
}

const REASON_CODE_OPTIONS = {
  education_not_eligible: '学历不符合',
  school_not_eligible: '院校不符合',
  job_not_found: '缺少匹配岗位',
  job_responsibility_missing: '岗位缺少工作职责',
  secondary_department_missing: '缺少岗位二级部门',
  department_missing: '缺少接收部门',
  major_not_matched: '专业不匹配',
  job_mapping_ambiguous: '职位映射歧义',
  internal_position_name_missing: '内部职位缺失',
  job_hc_exhausted: '岗位 HC 已满',
  llm_timeout: '模型超时',
  resume_text_unavailable: '简历正文不可用',
  ai_connection_error: 'AI 连接异常',
  ai_rate_limited: 'AI 限流',
  ai_invalid_output: 'AI 输出不合法',
  agent_incomplete: 'AI 分析未完成',
  agent_budget_exhausted: 'AI 分析预算耗尽',
  ai_reference_invalidated: 'AI 引用已失效',
  rule_assigned: '历史规则分配成功',
  ai_dispatched: 'AI 建议下发',
  ai_review: '历史 AI 复核',
  ai_archived: 'AI 建议归档',
  ai_rejected: '当前志愿 AI 不通过',
  ai_next_volunteer: '继续评估下一志愿',
  talent_pool: '志愿已耗尽，进入人才库',
  awaiting_department: '保留部门处理流程',
  terminal_workflow: '已处于终态',
  no_resume_available: '无可处理志愿',
  cancelled: '任务已取消',
}

const ENTITY_TAG_COLORS = [
  'blue',
  'geekblue',
  'purple',
  'magenta',
  'volcano',
  'orange',
  'gold',
  'green',
  'cyan',
  'lime',
]
const ENTITY_TAG_COLOR_MAP = new Map()

function entityTagColor(entity) {
  if (!ENTITY_TAG_COLOR_MAP.has(entity)) {
    const color = ENTITY_TAG_COLORS[ENTITY_TAG_COLOR_MAP.size % ENTITY_TAG_COLORS.length]
    ENTITY_TAG_COLOR_MAP.set(entity, color)
  }
  return ENTITY_TAG_COLOR_MAP.get(entity)
}

function renderEntityTag(entity) {
  const label = String(entity || '').trim()
  return label ? <Tag color={entityTagColor(label)}>{label}</Tag> : '-'
}

const SYSTEM_STATUS_OPTIONS = {
  pending_allocation: { text: '入池待分配', color: 'processing' },
  raw: {
    text: '待处理',
    color: 'default',
    status: 'Default',
    description: '首次处理，或部门不通过后等待下一轮处理剩余志愿',
  },
  talent_pool: { text: '人才库', color: 'purple', description: '全部志愿均未通过，保留历史，等待后续筛选策略' },
  archived: {
    text: '已归档',
    color: 'default',
    status: 'Default',
    description: '已有处理证据，但当前志愿没有有效分配状态',
  },
  pending_reallocation: {
    text: '待重新分配',
    color: 'orange',
    status: 'Warning',
    description: '岗位已匹配，但本任务 HC 容量不足，等待新任务重新分配',
  },
  pending_dispatch: {
    text: '待下发',
    color: 'blue',
    status: 'Processing',
    description: '存在当前志愿的待下发尝试',
  },
  pending_screening: {
    text: '待业务反馈',
    color: 'processing',
    status: 'Processing',
    description: '存在已下发部门的尝试，业务部门尚未反馈',
  },
  screening_passed: {
    text: '通过',
    color: 'success',
    status: 'Success',
    description: '当前流程或最近有效尝试已由业务部门反馈通过',
  },
  screening_rejected: {
    text: '不通过',
    color: 'error',
    status: 'Error',
    description: '最近有效尝试已反馈未通过，或全部志愿未通过导致归档',
  },
}

const ATTEMPT_STATUS = {
  pending_review: { color: 'default', text: '历史复核' },
  pending_dispatch: { color: 'default', text: '待下发' },
  dispatched: { color: 'processing', text: '部门处理中' },
  passed: { color: 'success', text: '已通过' },
  rejected: { color: 'error', text: '未通过' },
  cancelled: { color: 'default', text: '已取消' },
}

const SOURCE_TEXT = {
  rule: '历史规则',
  ai: 'AI',
  manual: '手动',
}

const FEEDBACK_REASON_OPTIONS = [
  { value: 'major_background_mismatch', label: '专业背景不匹配' },
  { value: 'research_experience_mismatch', label: '科研经历不符合' },
  { value: 'key_capability_mismatch', label: '关键能力不匹配' },
  { value: 'project_internship_mismatch', label: '项目/实习经历不匹配' },
  { value: 'position_direction_mismatch', label: '岗位方向不匹配' },
  { value: 'other', label: '其他' },
]

const HANDLING_EVENT_TEXT = {
  attempt_created: '创建分配尝试',
  review_confirmed: 'HR 确认复核',
  department_dispatched: '下发部门',
  department_transferred: '部门转派',
  screener_assigned: '转派简历筛选人',
  feedback_passed: '反馈通过',
  feedback_rejected: '反馈不通过',
  cancelled: '取消处理',
}

function departmentLabel(department) {
  if (!department) return '-'
  const name = department.name || department.label || '-'
  const parent = department.parent_name || department.primary_department_name
  return parent && parent !== name ? `${parent} / ${name}` : name
}

function formatDuration(seconds) {
  const value = Number(seconds)
  if (!Number.isFinite(value) || value < 0) return ''
  if (value < 60) return `${Math.round(value)} 秒`
  if (value < 3600) return `${Math.round(value / 60)} 分钟`
  if (value < 86400) return `${(value / 3600).toFixed(value < 36000 ? 1 : 0)} 小时`
  return `${(value / 86400).toFixed(value < 864000 ? 1 : 0)} 天`
}

function eventDepartmentName(event, direction) {
  const key = direction === 'from' ? 'from_department' : 'to_department'
  return event?.[`${key}_name`] || event?.[key]?.name || ''
}

function formatEventTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(date)
}

function candidateHandlingEvents(record) {
  const events = (record?.attempts || [])
    .flatMap((attempt) => (attempt.handling_events || []).map((event) => ({
      ...event,
      attemptNo: attempt.attempt_no,
    })))
    .sort((left, right) => new Date(left.occurred_at) - new Date(right.occurred_at))
  return events.map((event, index) => {
    if (index === 0) return { ...event, duration_since_previous_seconds: null }
    const occurredAt = new Date(event.occurred_at).getTime()
    const previousAt = new Date(events[index - 1].occurred_at).getTime()
    return {
      ...event,
      duration_since_previous_seconds: Number.isFinite(occurredAt) && Number.isFinite(previousAt)
        ? Math.max(0, Math.round((occurredAt - previousAt) / 1000))
        : null,
    }
  })
}

const WORKFLOW_STATUS = {
  pending: { text: '待分配', color: 'default' },
  in_progress: { text: '进行中', color: 'processing' },
  passed: { text: '已通过', color: 'success' },
  archived: { text: '已归档', color: 'default' },
  waiting_next: { text: '待处理（继续下一志愿）', color: 'default' },
  talent_pool: { text: '人才库', color: 'purple' },
}

const REASON_TYPE = {
  assignment: { text: '分配理由', color: 'blue' },
  archive: { text: '归档理由', color: 'default' },
  waiting_next: { text: '等待下一轮', color: 'default' },
  talent_pool: { text: '人才库说明', color: 'purple' },
  block: { text: '阻塞原因', color: 'orange' },
  classification: { text: '分类理由', color: 'purple' },
  none: { text: '无', color: 'default' },
}

const ANALYTICS_REQUEST_PARAM_KEYS = [
  'analytics_date_from',
  'analytics_date_to',
  'analytics_entity',
  'analytics_job_id',
  'analytics_primary_department_id',
  'analytics_department_id',
  'analytics_school_tag_id',
  'analytics_education',
  'analytics_source',
  'analytics_dimension',
  'analytics_values',
  'analytics_value_labels',
]

function transferTargets(data) {
  return [
    ...(data?.screeners || []).map((person) => ({ value: `screener:${person.id}`, label: `${person.name}（${person.employee_no}） · 简历筛选人` })),
    ...(data?.results || []).filter((department) => [1, 2].includes(Number(department.level))).map((department) => ({ value: `department:${department.id}`, label: departmentLabel(department) })),
  ]
}
function transferTargetBody(selected) {
  const [kind, id] = selected.split(':')
  return { [kind === 'screener' ? 'target_screener_id' : 'target_department_id']: Number(id) }
}

export default function ResumesPage() {
  const actionRef = useRef()
  const [searchParams, setSearchParams] = useSearchParams()
  const { hasPermission, contact, contacts, isContact, user } = useRole()
  const canViewAgentDecisions = hasPermission('attempt.view_all')
  const canRunPipeline = hasPermission('pipeline.run')
  const canImport = hasPermission('resume.import')
  const canDispatch = hasPermission('attempt.dispatch')
  const canTransfer = hasPermission('attempt.transfer_department')
    && (hasPermission('attempt.view_all') || (contacts || (contact ? [contact] : [])).some(
      (grant) => grant.is_active !== false && ['primary_hr', 'secondary_hr', 'secondary'].includes(grant.contact_level) && grant.can_delegate,
    ))
  const canExport = hasPermission('resume.view') || hasPermission('attempt.export')
  const canSelectCandidates = canRunPipeline || canDispatch || canTransfer || canExport || canImport
  const { run } = useProcessRunner()
  const [detailRecord, setDetailRecord] = useState(null)
  const [previewRecord, setPreviewRecord] = useState(null)
  const [agentDecisions, setAgentDecisions] = useState([])
  const [agentDecisionsLoading, setAgentDecisionsLoading] = useState(false)
  const [agentDecisionDetail, setAgentDecisionDetail] = useState(null)
  const [retryingDecisionId, setRetryingDecisionId] = useState(null)
  const [exporting, setExporting] = useState(false)
  const [exportTarget, setExportTarget] = useState(null)
  const [processing, setProcessing] = useState(false)
  const [processModalOpen, setProcessModalOpen] = useState(false)
  const [processError, setProcessError] = useState('')
  const [processStatusSelection, setProcessStatusSelection] = useState([])
  const [processCurrentSelected, setProcessCurrentSelected] = useState(false)
  const [processCandidateSnapshot, setProcessCandidateSnapshot] = useState([])
  const [processFilterSnapshot, setProcessFilterSnapshot] = useState({})
  const [lastQuery, setLastQuery] = useState({})
  const [selectedRowKeys, setSelectedRowKeys] = useState([])
  const [selectedCandidates, setSelectedCandidates] = useState([])
  const [bulkDispatching, setBulkDispatching] = useState(false)
  const [bulkDispatchModal, setBulkDispatchModal] = useState({
    open: false,
    scope: 'filters',
    candidateIds: [],
    filters: { system_statuses: ['pending_dispatch'] },
    options: {},
  })
  const [bulkDeleting, setBulkDeleting] = useState(false)
  const [dispatchingId, setDispatchingId] = useState(null)
  const [manualModal, setManualModal] = useState({
    open: false,
    resume: null,
    attempt: null,
    departments: [],
    departmentId: undefined,
    reason: '',
    loading: false,
  })
  const [transferModal, setTransferModal] = useState({
    open: false,
    record: null,
    departments: [],
    selected: undefined,
    note: '',
    loading: false,
  })
  const [bulkTransferModal, setBulkTransferModal] = useState({
    open: false,
    candidateIds: [],
    departments: [],
    selected: undefined,
    note: '',
    loading: false,
  })
  const [feedbackModal, setFeedbackModal] = useState({
    open: false,
    record: null,
    result: 'passed',
    reasonCode: undefined,
    reasonOptions: FEEDBACK_REASON_OPTIONS,
    note: '',
    loading: false,
  })
  const openManualAssign = async (resume, attempt = null) => {
    setManualModal((prev) => ({ ...prev, open: true, resume, attempt, loading: true }))
    try {
      const { data } = await fetchManualAssignmentOptions()
      const departments = data?.results || []
      setManualModal((prev) => ({ ...prev, departments, loading: false }))
    } catch {
      setManualModal((prev) => ({ ...prev, loading: false }))
    }
  }

  const handleManualAssign = async () => {
    if (!manualModal.departmentId) {
      message.warning('请选择简历筛选人或目标部门')
      return
    }
    setManualModal((prev) => ({ ...prev, loading: true }))
    try {
      const payload = {
        target_department_id: manualModal.departmentId,
        manual_reason: manualModal.reason || 'HR 手动强制分配',
      }
      if (manualModal.attempt) {
        await transferAllocationToManual(manualModal.attempt.id, payload)
      } else {
        await manualAssignResume(manualModal.resume.id, payload)
      }
      message.success('已创建人工分配尝试')
      setManualModal({ open: false, resume: null, attempt: null, departments: [], departmentId: undefined, reason: '', loading: false })
      setDetailRecord(null)
      actionRef.current?.reload?.()
    } catch {
      setManualModal((prev) => ({ ...prev, loading: false }))
    }
  }

  useEffect(() => {
    if (!detailRecord) {
      setPreviewRecord(null)
      return
    }
    setPreviewRecord(
      detailRecord.preview_resume || detailRecord.current_resume || detailRecord.resumes?.[0] || null,
    )
  }, [detailRecord])

  const decisionsRequest = useRef(0)
  const loadAgentDecisions = useCallback(async (workflowId) => {
    const requestId = ++decisionsRequest.current
    setAgentDecisions([])
    setAgentDecisionDetail(null)
    if (!workflowId || !canViewAgentDecisions) {
      setAgentDecisions([])
      return
    }
    setAgentDecisionsLoading(true)
    try {
      const { data } = await fetchAgentDecisions({ workflow: workflowId, page_size: 100 })
      if (requestId === decisionsRequest.current) setAgentDecisions(data?.results || [])
    } catch {
      if (requestId === decisionsRequest.current) setAgentDecisions([])
    } finally {
      if (requestId === decisionsRequest.current) setAgentDecisionsLoading(false)
    }
  }, [canViewAgentDecisions])

  useEffect(() => {
    const workflowId = detailRecord?.workflow_id
    if (!workflowId) {
      ++decisionsRequest.current
      setAgentDecisionsLoading(false)
      setAgentDecisions([])
      setAgentDecisionDetail(null)
      return
    }
    loadAgentDecisions(workflowId)
    const generation = decisionsRequest
    return () => { ++generation.current }
  }, [detailRecord?.workflow_id, loadAgentDecisions])

  const handleRetryAgentDecision = async (decision) => {
    setRetryingDecisionId(decision.id)
    try {
      const { data } = await retryAgentDecision(decision.id)
      setAgentDecisionDetail(null)
      await loadAgentDecisions(decision.workflow)
      actionRef.current?.reload()
      window.dispatchEvent(new Event('srf:processing-run-created'))
      message.success(data?.detail || '已创建 AI 重试任务，请在处理任务中心查看进度')
    } finally {
      setRetryingDecisionId(null)
    }
  }

  const reloadCandidates = () => {
    setDetailRecord(null)
    actionRef.current?.reload()
  }

  const handleDispatch = async (attempt) => {
    setDispatchingId(attempt.id)
    try {
      const { data } = await dispatchAllocation(attempt.id)
      message.success(data?.detail || '下发成功')
      reloadCandidates()
    } finally {
      setDispatchingId(null)
    }
  }

  const handleCancelAttempt = async (attempt) => {
    setDispatchingId(attempt.id)
    try {
      await cancelAllocation(attempt.id, { reason: 'hr_cancelled_dispatch' })
      message.success('已取消待下发尝试')
      reloadCandidates()
    } finally {
      setDispatchingId(null)
    }
  }

  const openBulkDispatch = () => {
    const candidateIds = [...selectedRowKeys]
    const toArray = (value) => value == null || value === ''
      ? []
      : Array.isArray(value) ? value : String(value).split(',').filter(Boolean)
    setBulkDispatchModal({
      open: true,
      scope: candidateIds.length ? 'selected' : 'filters',
      candidateIds,
      filters: {
        system_statuses: toArray(lastQuery.system_status).length
          ? toArray(lastQuery.system_status)
          : ['pending_dispatch'],
        current_entity_in: toArray(lastQuery.current_entity_in),
        current_position_name_in: toArray(lastQuery.current_position_name_in),
        job_department_name_in: toArray(lastQuery.job_department_name_in),
        current_job_category_in: toArray(lastQuery.current_job_category_in),
        school_tag_in: toArray(lastQuery.school_tag_in),
        allocation_source: toArray(lastQuery.allocation_source),
      },
      options: {},
    })
    fetchCandidateFilterOptions()
      .then(({ data }) => setBulkDispatchModal((previous) => ({
        ...previous,
        options: data || {},
      })))
      .catch(() => {})
  }

  const submitBulkDispatch = async () => {
    setBulkDispatching(true)
    try {
      const selected = bulkDispatchModal.scope === 'selected'
      const candidateFilters = Object.fromEntries(
        Object.entries(bulkDispatchModal.filters).filter(([, value]) => value?.length),
      )
      const { data } = await bulkDispatchCandidates(
        selected
          ? { candidate_ids: bulkDispatchModal.candidateIds }
          : { candidate_filters: candidateFilters },
      )
      message.success(data?.detail || '批量下发完成')
      setBulkDispatchModal((previous) => ({ ...previous, open: false }))
      setSelectedRowKeys([])
      setSelectedCandidates([])
      actionRef.current?.reload?.()
    } finally {
      setBulkDispatching(false)
    }
  }

  const updateBulkDispatchFilter = (key, value) => {
    setBulkDispatchModal((previous) => ({
      ...previous,
      filters: { ...previous.filters, [key]: value },
    }))
  }

  const openTransferModal = async (attempt) => {
    setTransferModal({
      open: true,
      record: attempt,
      departments: [],
      selected: undefined,
      note: '',
      loading: true,
    })
    try {
      const { data } = await fetchTransferOptions(attempt.id)
      setTransferModal((previous) => ({
        ...previous,
        departments: transferTargets(data),
        loading: false,
      }))
    } catch {
      setTransferModal((previous) => ({ ...previous, loading: false }))
    }
  }

  const handleTransfer = async () => {
    if (!transferModal.selected) {
      message.warning('请选择简历筛选人或目标部门')
      return
    }
    setTransferModal((previous) => ({ ...previous, loading: true }))
    try {
      await transferAllocation(transferModal.record.id, {
        ...transferTargetBody(transferModal.selected),
        note: transferModal.note.trim(),
      })
      message.success('简历已转派')
      setTransferModal({ open: false, record: null, departments: [], selected: undefined, note: '', loading: false })
      reloadCandidates()
    } catch {
      setTransferModal((previous) => ({ ...previous, loading: false }))
    }
  }

  const openBulkTransfer = async () => {
    if (!selectedRowKeys.length) {
      message.warning('请先选择候选人')
      return
    }
    const optionSource = selectedCandidates.find((candidate) => candidate.current_attempt?.id)
    if (!optionSource) {
      message.warning('当前选中项中没有可用于加载转派目标的处理记录')
      return
    }
    const candidateIds = [...selectedRowKeys]
    setBulkTransferModal({
      open: true,
      candidateIds,
      departments: [],
      selected: undefined,
      note: '',
      loading: true,
    })
    try {
      const { data } = await fetchTransferOptions(optionSource.current_attempt.id)
      const departments = transferTargets(data)
      setBulkTransferModal((previous) => ({ ...previous, departments, loading: false }))
    } catch {
      setBulkTransferModal((previous) => ({ ...previous, loading: false }))
    }
  }

  const handleBulkTransfer = async () => {
    if (!bulkTransferModal.selected) {
      message.warning('请选择简历筛选人或目标部门')
      return
    }
    setBulkTransferModal((previous) => ({ ...previous, loading: true }))
    try {
      const { data } = await bulkTransferCandidates({
        candidate_ids: bulkTransferModal.candidateIds,
        ...transferTargetBody(bulkTransferModal.selected),
        note: bulkTransferModal.note.trim(),
      })
      message.success(
        `批量转派完成：成功 ${data?.transferred || 0}，跳过 ${data?.skipped || 0}，失败 ${data?.failed || 0}`,
      )
      setBulkTransferModal({ open: false, candidateIds: [], departments: [], selected: undefined, note: '', loading: false })
      setSelectedRowKeys([])
      setSelectedCandidates([])
      actionRef.current?.reload?.()
    } catch {
      setBulkTransferModal((previous) => ({ ...previous, loading: false }))
    }
  }

  const openFeedbackModal = async (attempt) => {
    setFeedbackModal({
      open: true,
      record: attempt,
      result: 'passed',
      reasonCode: undefined,
      reasonOptions: FEEDBACK_REASON_OPTIONS,
      note: '',
      loading: true,
    })
    try {
      const { data } = await fetchFeedbackReasons()
      setFeedbackModal((previous) => ({
        ...previous,
        reasonOptions: data?.results?.length ? data.results : FEEDBACK_REASON_OPTIONS,
        loading: false,
      }))
    } catch {
      setFeedbackModal((previous) => ({ ...previous, loading: false }))
    }
  }

  const handleFeedback = async () => {
    const rejected = feedbackModal.result === 'rejected'
    if (rejected && !feedbackModal.reasonCode) {
      message.warning('请选择不通过原因')
      return
    }
    if (rejected && feedbackModal.reasonCode === 'other' && !feedbackModal.note.trim()) {
      message.warning('选择“其他”时必须填写反馈备注')
      return
    }
    setFeedbackModal((previous) => ({ ...previous, loading: true }))
    try {
      await submitAllocationFeedback(feedbackModal.record.id, {
        result: feedbackModal.result,
        ...(rejected ? { reason_code: feedbackModal.reasonCode } : {}),
        note: feedbackModal.note,
      })
      message.success('反馈已提交，简历状态已更新')
      setFeedbackModal({ open: false, record: null, result: 'passed', reasonCode: undefined, reasonOptions: FEEDBACK_REASON_OPTIONS, note: '', loading: false })
      reloadCandidates()
    } catch {
      setFeedbackModal((previous) => ({ ...previous, loading: false }))
    }
  }

  // 导入与 Agent 处理解耦；Agent 未就绪时数据仍会保留为待处理。
  const handleImported = async (data) => {
    await actionRef.current?.reloadOptions()
    actionRef.current?.reload()
    if (data?.processing_runs?.length) {
      message.success('简历已导入并提交后台处理，可继续操作并在任务中心查看进度')
    }
    else if (data?.agent_processing === 'pending') {
      message.info('简历已导入；Agent 就绪后可从简历库批量处理')
    }
  }

  const handleDelete = (record) => {
    Modal.confirm({
      title: '删除候选人',
      content: `将删除 ${record.name} 及其全部投递记录。若已产生分配历史，系统会阻止删除。确定继续？`,
      okText: '删除',
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await deleteCandidate(record.id)
          message.success('已删除')
          setDetailRecord(null)
          setSelectedRowKeys((keys) => keys.filter((key) => key !== record.id))
          setSelectedCandidates((rows) => rows.filter((row) => row.id !== record.id))
          await actionRef.current?.reloadOptions()
          actionRef.current?.reload()
        } catch (error) {
          message.error(error?.response?.data?.detail || '删除失败')
        }
      },
    })
  }

  const handleBulkDelete = () => {
    if (!selectedRowKeys.length) return
    const candidateIds = [...selectedRowKeys]
    Modal.confirm({
      title: `删除选中的 ${candidateIds.length} 名候选人？`,
      content: '系统将逐条校验；已产生分配、反馈、AI 决策或转派历史的候选人会保留并报告失败。',
      okText: '删除选中',
      okButtonProps: { danger: true },
      onOk: async () => {
        setBulkDeleting(true)
        try {
          const { data } = await bulkDeleteCandidates(candidateIds)
          const failedItems = (data?.results || []).filter((item) => item.status === 'failed')
          const failedIds = failedItems.map((item) => item.candidate_id)
          setSelectedRowKeys(failedIds)
          setSelectedCandidates((rows) => rows.filter((row) => failedIds.includes(row.id)))
          if (data?.deleted) {
            await actionRef.current?.reloadOptions()
            actionRef.current?.reload()
          }
          if (data?.failed) {
            message.warning(
              `已删除 ${data.deleted} 名，${data.failed} 名未删除：${failedItems[0]?.detail || '存在受保护历史'}`,
            )
          } else {
            message.success(`已删除 ${data?.deleted || 0} 名候选人`)
          }
        } finally {
          setBulkDeleting(false)
        }
      },
    })
  }

  const openCandidateExport = (candidateIds = []) => {
    const selected = candidateIds.length > 0
    setExportTarget({
      type: 'candidates',
      ids: selected ? [...candidateIds] : null,
      params: selected ? {} : { ...lastQuery },
    })
  }

  const openAttemptExport = (attempt) => {
    if (!attempt?.id) return
    setExportTarget({ type: 'attempts', ids: [attempt.id], params: {} })
  }

  const handleExport = async (fields, includeResumeFiles) => {
    if (!exportTarget) return
    setExporting(true)
    try {
      const params = {
        ...exportTarget.params,
        fields: fields.join(','),
        include_resume_files: includeResumeFiles,
      }
      const resp = exportTarget.type === 'attempts'
        ? await exportAllocations(exportTarget.ids, params)
        : await exportCandidates(exportTarget.ids, params)
      const count = Number(resp.headers?.['x-export-count'] ?? 0)
      const missing = Number(resp.headers?.['x-export-missing'] ?? 0)
      const candidateCount = Number(resp.headers?.['x-export-candidate-count'] ?? 0)
      const exportMode = String(resp.headers?.['x-export-mode'] || '').toLowerCase()
      const isZip = exportMode ? exportMode === 'zip' : includeResumeFiles
      downloadBlobFromResponse(resp, isZip ? '简历导出.zip' : '简历库清单.xlsx')
      if (isZip) {
        message.success(
          `已导出 ${candidateCount} 名候选人，包含 ${count} 份简历${missing ? `，${missing} 份缺文件（见压缩包内清单）` : ''}`,
        )
      } else {
        message.success(`已导出 ${candidateCount} 名候选人的 Excel 清单`)
      }
      setExportTarget(null)
    } catch {
      message.error('导出失败')
    } finally {
      setExporting(false)
    }
  }

  const handleProcessSelectedStatuses = () => {
    setProcessCandidateSnapshot([...selectedRowKeys])
    setProcessFilterSnapshot({ ...lastQuery })
    setProcessCurrentSelected(selectedRowKeys.length > 0)
    setProcessStatusSelection([])
    setProcessError('')
    setProcessModalOpen(true)
  }

  const handleCurrentSelectedChange = (event) => {
    const checked = event.target.checked
    setProcessCurrentSelected(checked)
    if (checked) setProcessStatusSelection([])
  }

  const handleProcessStatusChange = (statuses) => {
    setProcessStatusSelection(statuses)
    if (statuses.length) setProcessCurrentSelected(false)
  }

  const handleConfirmProcess = async (schedule) => {
    const statuses = processStatusSelection
    if (!processCurrentSelected && !statuses.length) {
      message.warning('请先勾选当前选中或需要处理的简历状态')
      return
    }
    setProcessError('')
    setProcessing(true)
    try {
      if (schedule?.repeat && schedule.repeat !== 'now') {
        const scope = buildResumeProcessingScope({ processCurrentSelected, processCandidateSnapshot, processStatusSelection: statuses, lastQuery: processFilterSnapshot })
        await createProcessingSchedule({ ...schedule, name: schedule.name || '定时简历处理', scope })
        message.success('定时任务已创建，可在任务中心查看或取消')
        window.dispatchEvent(new Event('srf:processing-schedule-created'))
        setProcessModalOpen(false)
        return
      }
      const scope = buildResumeProcessingScope({
        processCurrentSelected,
        processCandidateSnapshot,
        processStatusSelection: statuses,
        lastQuery: processFilterSnapshot,
      })
      const r = await run(
        [{ step: 'step2', label: '院校准入 → 固定业务引用 → Agent 筛选' }],
        '正在提交 Agent 简历处理任务',
        { scope, ...(schedule?.name ? { name: schedule.name } : {}) },
      )
      if (r.success) {
        message.success('已提交重新处理任务，可继续操作并在任务中心查看进度')
        setProcessModalOpen(false)
        if (processCurrentSelected) {
          setSelectedRowKeys([])
          setSelectedCandidates([])
          actionRef.current?.clearSelected?.()
        }
      } else {
        setProcessError(r.error || '提交处理任务失败，请重试')
      }
    } catch (error) {
      setProcessError(['ECONNABORTED', 'ETIMEDOUT'].includes(error.code) || [502, 504].includes(error.response?.status)
        ? '提交请求超时，任务可能已创建。请先查看任务中心确认，避免重复提交。'
        : error.response?.data?.detail || '提交处理任务失败，请重试')
    } finally {
      setProcessing(false)
    }
  }

  const renderDetailActions = (record) => {
    const attempt = record.current_attempt
    const canDispatchAttempt = attempt?.can_dispatch === true && attempt?.status === 'pending_dispatch'
    const canTransferAttempt = attempt?.can_transfer === true
      && attempt?.status === 'dispatched'
      && !attempt.feedback_at
    const canFeedback = attempt?.can_feedback === true
      && attempt?.status === 'dispatched'
      && !attempt.feedback_at
    return (
      <Space wrap className="resume-detail-actions">
        {attempt?.assigned_screener_name && <Tag>简历筛选人：{attempt.assigned_screener_name}（{attempt.assigned_screener_employee_no}）</Tag>}
        {hasPermission('resume.manual_assign') && record.current_resume && (
          <Button onClick={() => openManualAssign(record.current_resume)}>
            手动强制分配当前志愿
          </Button>
        )}
        {canExport && (hasPermission('resume.view') || attempt?.can_export === true) && (
          <Button
            icon={<DownloadOutlined />}
            loading={exporting}
            onClick={() => (
              hasPermission('resume.view')
                ? openCandidateExport([record.id])
                : openAttemptExport(attempt)
            )}
          >
            导出
          </Button>
        )}
        {canDispatchAttempt && (
          <Popconfirm title="确认下发该简历到部门收件箱？" onConfirm={() => handleDispatch(attempt)}>
            <Button type="link" size="small" style={{ padding: 0 }} loading={dispatchingId === attempt.id}>下发部门</Button>
          </Popconfirm>
        )}
        {canDispatchAttempt && (
          <Popconfirm title="取消该待下发尝试？" onConfirm={() => handleCancelAttempt(attempt)}>
            <Button type="link" danger size="small" style={{ padding: 0 }}>取消</Button>
          </Popconfirm>
        )}
        {canTransferAttempt && (
          <Button type="link" size="small" style={{ padding: 0 }} onClick={() => openTransferModal(attempt)}>
            转派简历
          </Button>
        )}
        {canFeedback && (
          <Button
            type="link"
            size="small"
            style={{ padding: 0 }}
            onClick={() => openFeedbackModal(attempt)}
          >
            提交反馈
          </Button>
        )}
        {hasPermission('resume.import') && (
          <Button danger icon={<DeleteOutlined />} onClick={() => handleDelete(record)}>
            删除
          </Button>
        )}
      </Space>
    )
  }

  const processingResultFilter = useMemo(() => {
    const runId = searchParams.get('processing_run_id')
    const result = searchParams.get('processing_result')
    if (!runId || !PROCESSING_RESULT_LABELS[result]) return null
    return { runId, result }
  }, [searchParams])

  const processingRequestParams = useMemo(() => ({
    processing_run_id: processingResultFilter?.runId,
    processing_result: processingResultFilter?.result,
  }), [processingResultFilter?.runId, processingResultFilter?.result])

  const analyticsDrilldown = useMemo(() => {
    const dimension = searchParams.get('analytics_dimension')
    if (!dimension) return null
    const params = Object.fromEntries(
      ANALYTICS_REQUEST_PARAM_KEYS
        .map((key) => [key, searchParams.get(key)])
        .filter(([, value]) => value !== null && value !== ''),
    )
    return {
      params,
      title: searchParams.get('analytics_title') || '数据看板明细',
      context: searchParams.get('analytics_context') || '',
    }
  }, [searchParams])

  const externalRequestParams = useMemo(() => ({
    ...processingRequestParams,
    ...(analyticsDrilldown?.params || {}),
  }), [analyticsDrilldown?.params, processingRequestParams])

  useEffect(() => {
    setDetailRecord(null)
  }, [
    analyticsDrilldown?.params,
    processingResultFilter?.runId,
    processingResultFilter?.result,
  ])

  const requestCandidates = useCallback((params) => {
    const { page: _page, page_size: _pageSize, ...query } = params
    setLastQuery(Object.fromEntries(
      Object.entries(query).filter(([, value]) => value !== undefined),
    ))
    return fetchCandidates(params)
  }, [])

  const baseColumns = [
    {
      title: '姓名',
      dataIndex: 'name',
      fixed: 'left',
      width: 190,
      filter: { type: 'text', param: 'name', pinyin: true, placeholder: '筛选姓名/拼音' },
      render: (_, record) => (
        <button type="button" className="resume-candidate-link" onClick={() => setDetailRecord(record)} aria-label={`查看 ${record.name || '未命名候选人'} 的简历`}>
          <span className="resume-candidate-avatar" aria-hidden="true">{(record.name || '?').slice(0, 1)}</span>
          <span className="resume-candidate-name">{record.name || '未命名候选人'}</span>
        </button>
      ),
    },
    {
      title: '简历状态',
      dataIndex: 'system_status',
      width: 120,
      valueType: 'select',
      valueEnum: Object.fromEntries(
        Object.entries(SYSTEM_STATUS_OPTIONS).map(([value, item]) => [
          value,
          { text: item.text, status: item.status },
        ]),
      ),
      filter: { type: 'select', param: 'system_status', multiple: true, options: Object.entries(SYSTEM_STATUS_OPTIONS).map(([value, item]) => ({ value, label: item.text })) },
      render: (_, record) => {
        const item = SYSTEM_STATUS_OPTIONS[record.system_status]
        return item ? <Tooltip title={item.description}><Tag color={item.color}>{item.text}</Tag></Tooltip> : '-'
      },
    },
    {
      title: '最高学历专业',
      dataIndex: 'highest_major',
      width: 130,
      ellipsis: true,
      filter: { type: 'select', param: 'highest_major_in', multiple: true, options: 'highest_major' },
      render: (_, record) => record.highest_major || '-',
    },
    {
      title: '投递时间',
      dataIndex: 'current_apply_date',
      width: 120,
      filter: {
        type: 'dateRange',
        params: ['current_apply_date_from', 'current_apply_date_to'],
        placeholders: ['投递开始日期', '投递结束日期'],
      },
      render: (value) => value || '-',
    },
    {
      title: '当前应聘ID',
      dataIndex: 'current_apply_id',
      width: 130,
      filter: { type: 'text', param: 'current_apply_id', placeholder: '筛选应聘ID' },
      render: (value) => value || '-',
    },
    {
      title: '当前主体',
      dataIndex: 'current_entity',
      width: 120,
      filter: { type: 'select', param: 'current_entity_in', multiple: true, options: 'current_entity' },
      render: (_, record) => renderEntityTag(record.current_resume?.entity),
    },
    {
      title: '当前投递岗位',
      dataIndex: 'current_position_name',
      width: 180,
      ellipsis: true,
      filter: { type: 'select', param: 'current_position_name_in', multiple: true, options: 'current_position_name' },
      render: (_, record) => record.current_resume?.position_name || '-',
    },
    {
      title: '岗位部门',
      dataIndex: 'job_department_name',
      width: 130,
      ellipsis: true,
      filter: { type: 'select', param: 'job_department_name_in', multiple: true, options: 'job_department_name' },
      render: (value) => value || '-',
    },
    {
      title: '当前接收一级部门',
      dataIndex: 'current_primary_department_name',
      width: 150,
      ellipsis: true,
      filter: {
        type: 'select',
        param: 'current_primary_department_id',
        options: 'current_primary_department',
      },
      render: (value) => value || '-',
    },
    {
      title: '当前接收部门',
      dataIndex: 'current_department_name',
      width: 150,
      ellipsis: true,
      filter: {
        type: 'select',
        param: 'current_department_id',
        options: 'current_department',
      },
      render: (value) => value || '-',
    },
    {
      title: '岗位类别',
      dataIndex: 'current_job_category',
      width: 110,
      filter: { type: 'select', param: 'current_job_category_in', multiple: true, options: 'current_job_category' },
      render: (_, record) => record.current_resume?.job_category || '-',
    },
    {
      title: '分配来源',
      dataIndex: 'allocation_source',
      width: 100,
      filter: { type: 'select', param: 'allocation_source', multiple: true, options: Object.entries(SOURCE_TEXT).map(([value, label]) => ({ value, label })) },
      render: (_, record) => SOURCE_TEXT[record.allocation_source] || '-',
    },
    {
      title: '院校标签',
      dataIndex: 'school_tag',
      width: 110,
      filter: { type: 'select', param: 'school_tag_in', multiple: true, options: 'school_tag' },
      render: (_, record) =>
        record.school_tags?.length
          ? record.school_tags.map((tag) => (
              <SchoolTagBadge key={tag.id || tag.code || tag.name} value={tag.name || tag} />
            ))
          : (record.school_tag ? <SchoolTagBadge value={record.school_tag} /> : '-'),
    },
    {
      title: '原因',
      dataIndex: 'reason_type',
      width: 240,
      ellipsis: true,
      valueType: 'select',
      valueEnum: Object.fromEntries(
        Object.entries(REASON_TYPE).map(([value, item]) => [
          value,
          { text: item.text },
        ]),
      ),
      filter: {
        type: 'select',
        param: 'feedback_reason_code',
        options: FEEDBACK_REASON_OPTIONS,
      },
      render: (_, record) => {
        const type = record.reason_type || 'none'
        const item = REASON_TYPE[type] || REASON_TYPE.none
        const reasonText = record.reason_text || '-'
        const feedbackReasonLabel = record.current_attempt?.feedback_reason_label_snapshot
        return (
          <Tooltip title={reasonText}>
            <Space size={4}>
              <Tag color={item.color}>
                {feedbackReasonLabel || REASON_CODE_OPTIONS[record.reason_code] || item.text}
              </Tag>
              <Typography.Text ellipsis style={{ maxWidth: 150 }}>
                {reasonText}
              </Typography.Text>
            </Space>
          </Tooltip>
        )
      },
    },
  ]
  return (
    <PageContainer
      className="resumes-page"
      title="简历库"
      content="一位候选人，一份完整视图。查看当前志愿，协同完成分析、下发与反馈。"
      extra={canImport ? <ImportButton
        buttonText="上传简历"
        title="上传简历（简历列表 + 简历包），上传后自动处理"
        fields={RESUME_IMPORT_FIELDS}
        templateType="resume_list"
        templateFilename="简历信息列表标准模板.xlsx"
        onDone={handleImported}
      /> : null}
    >
      {processingResultFilter && (
        <Alert
          showIcon
          type="info"
          style={{ marginBottom: 16 }}
          message={`处理任务 #${processingResultFilter.runId}：${PROCESSING_RESULT_LABELS[processingResultFilter.result]}简历`}
          description="当前列表、导出和批量操作均限定为该任务的对应候选人结果。"
          action={<Button size="small" onClick={() => setSearchParams({})}>清除筛选</Button>}
        />
      )}
      {analyticsDrilldown && (
        <Alert
          showIcon
          type="info"
          style={{ marginBottom: 16 }}
          message={`看板下钻：${analyticsDrilldown.title}`}
          description={`${analyticsDrilldown.context || '沿用数据看板当前已生效的统计范围'}。简历库按候选人一人一行展示；涉及多投递或多分组的指标，列表行数可能小于看板计数。`}
          action={<Button size="small" onClick={() => setSearchParams({})}>返回全部简历</Button>}
        />
      )}
      <SmartDataTable
        tableId="candidates"
        headerTitle={<div className="resume-table-heading"><span>候选人清单</span><span>点击姓名查看完整档案</span></div>}
        stickyPagination
        actionRef={actionRef}
        rowKey="id"
        columns={baseColumns}
        defaultColumnsState={{
          current_apply_id: { show: false },
          current_entity: { show: false },
          allocation_source: { show: false },
          current_primary_department_name: { show: false },
          job_department_name: { show: false },
          current_job_category: { show: false },
          reason_type: { show: false },
        }}
        filterOptionsRequest={fetchCandidateFilterOptions}
        rowSelection={
          canSelectCandidates
            ? {
                selectedRowKeys,
                onChange: (keys, rows = []) => {
                  setSelectedRowKeys(keys)
                  setSelectedCandidates(rows)
                },
                preserveSelectedRowKeys: true,
              }
            : false
        }
        rowClassName="resume-library-row"
        onRowClick={(record) => setDetailRecord(record)}
        batchActions={({ clearSelection }) => (
          <Space wrap className="resume-selection-actions">
            {canExport && (
              <Button
                type="link"
                size="small"
                icon={<DownloadOutlined />}
                loading={exporting}
                onClick={() => openCandidateExport(selectedRowKeys)}
              >
                导出选中
              </Button>
            )}
            {canExport && (
              <Button
                type="link"
                size="small"
                loading={exporting}
                onClick={() => openCandidateExport([])}
              >
                导出当前筛选
              </Button>
            )}
            {canImport && (
              <Button
                type="link"
                danger
                size="small"
                icon={<DeleteOutlined />}
                loading={bulkDeleting}
                onClick={handleBulkDelete}
              >
                删除选中
              </Button>
            )}
            <Button type="link" size="small" onClick={clearSelection}>
              取消选择
            </Button>
          </Space>
        )}
        toolBarRender={() => [
          canRunPipeline && <Button
            key="process"
            type="primary"
            icon={<PlayCircleOutlined />}
            loading={processing}
            onClick={handleProcessSelectedStatuses}
          >
            处理简历
          </Button>,
          canDispatch && <Button
            key="dispatch"
            icon={<SendOutlined />}
            loading={bulkDispatching}
            onClick={openBulkDispatch}
          >
            下发
          </Button>,
          canTransfer && <Button
            key="bulk-transfer"
            disabled={!selectedRowKeys.length}
            onClick={openBulkTransfer}
          >
            批量转派
          </Button>,
        ].filter(Boolean)}
        params={externalRequestParams}
        request={requestCandidates}
      />
      <Modal
        title="批量下发"
        open={bulkDispatchModal.open}
        okText="确认下发"
        cancelText="取消"
        confirmLoading={bulkDispatching}
        okButtonProps={{
          disabled: bulkDispatchModal.scope === 'selected'
            ? bulkDispatchModal.candidateIds.length === 0
            : !Object.values(bulkDispatchModal.filters).some((value) => value?.length),
        }}
        onOk={submitBulkDispatch}
        onCancel={() => {
          if (!bulkDispatching) {
            setBulkDispatchModal((previous) => ({ ...previous, open: false }))
          }
        }}
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Alert
            type="info"
            showIcon
            message="只处理当前志愿下状态为“待下发”的有效记录，其他候选人会计入跳过。"
          />
          <Radio.Group
            value={bulkDispatchModal.scope}
            onChange={(event) => setBulkDispatchModal((previous) => ({
              ...previous,
              scope: event.target.value,
            }))}
          >
            <Space direction="vertical">
              <Radio
                value="selected"
                disabled={!bulkDispatchModal.candidateIds.length}
              >
                当前选中（冻结 {bulkDispatchModal.candidateIds.length} 人）
              </Radio>
              <Radio value="filters">按业务枚举筛选</Radio>
            </Space>
          </Radio.Group>
          {bulkDispatchModal.scope === 'filters' && (
            <Space direction="vertical" size="small" style={{ width: '100%' }}>
              {[
                ['system_statuses', '简历状态', Object.entries(SYSTEM_STATUS_OPTIONS).map(([value, item]) => ({ value, label: item.text }))],
                ['current_entity_in', '招聘主体', bulkDispatchModal.options.current_entity || []],
                ['current_position_name_in', '投递岗位', bulkDispatchModal.options.current_position_name || []],
                ['job_department_name_in', '岗位部门', bulkDispatchModal.options.job_department_name || []],
                ['current_job_category_in', '岗位类别', bulkDispatchModal.options.current_job_category || []],
                ['school_tag_in', '院校标签', bulkDispatchModal.options.school_tag || []],
                ['allocation_source', '分配来源', Object.entries(SOURCE_TEXT).map(([value, label]) => ({ value, label }))],
              ].map(([key, label, options]) => (
                <div key={key}>
                  <Typography.Text type="secondary">{label}</Typography.Text>
                  <Select
                    mode="multiple"
                    allowClear
                    value={bulkDispatchModal.filters[key] || []}
                    options={options}
                    onChange={(value) => updateBulkDispatchFilter(key, value)}
                    style={{ width: '100%', marginTop: 4 }}
                  />
                </div>
              ))}
            </Space>
          )}
        </Space>
      </Modal>
      <Modal
        title={`批量转派（冻结 ${bulkTransferModal.candidateIds.length} 人）`}
        open={bulkTransferModal.open}
        okText="确认转派"
        cancelText="取消"
        confirmLoading={bulkTransferModal.loading}
        okButtonProps={{ disabled: !bulkTransferModal.selected }}
        onOk={handleBulkTransfer}
        onCancel={() => {
          if (!bulkTransferModal.loading) {
            setBulkTransferModal({ open: false, candidateIds: [], departments: [], selected: undefined, note: '', loading: false })
          }
        }}
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Alert
            type="info"
            showIcon
            message="仅转派打开弹窗时冻结的候选人；已结束或状态已变化的记录将被跳过。"
          />
          <Select
            showSearch
            optionFilterProp="label"
            style={{ width: '100%' }}
            placeholder="选择简历筛选人或目标部门"
            value={bulkTransferModal.selected}
            options={bulkTransferModal.departments}
            onChange={(value) => setBulkTransferModal((previous) => ({ ...previous, selected: value }))}
          />
          <Input.TextArea
            rows={3}
            placeholder="转派备注（可选）"
            value={bulkTransferModal.note}
            onChange={(event) => setBulkTransferModal((previous) => ({ ...previous, note: event.target.value }))}
          />
        </Space>
      </Modal>
      <ResumeExportModal
        open={Boolean(exportTarget)}
        userKey={user?.id || user?.username}
        exporting={exporting}
        onCancel={() => setExportTarget(null)}
        onExport={handleExport}
      />
      <ResumeProcessModal
        open={processModalOpen}
        allowScheduling={hasPermission('resume.view')}
        filterSummary={describeProcessingFilters(processFilterSnapshot)}
        processing={processing}
        error={processError}
        processCurrentSelected={processCurrentSelected}
        processCandidateCount={processCandidateSnapshot.length}
        processStatusSelection={processStatusSelection}
        statusOptions={SYSTEM_STATUS_OPTIONS}
        onCurrentSelectedChange={handleCurrentSelectedChange}
        onStatusChange={handleProcessStatusChange}
        onConfirm={handleConfirmProcess}
        onCancel={() => {
          if (!processing) setProcessModalOpen(false)
        }}
      />
      <Drawer
        title={detailRecord ? `${detailRecord.name} 的简历详情` : '简历详情'}
        width={1100}
        open={!!detailRecord}
        onClose={() => setDetailRecord(null)}
      >
        {detailRecord && (
          <>
            <Descriptions column={2} size="small" bordered>
              <Descriptions.Item label="姓名">{detailRecord.name}</Descriptions.Item>
              <Descriptions.Item label="手机">{detailRecord.phone || '-'}</Descriptions.Item>
              <Descriptions.Item label="最高学历专业">
                {detailRecord.highest_major || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="最高学历">
                {detailRecord.highest_education_label || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="第一学历院校">
                <Space size={6} wrap>
                  <span>{detailRecord.first_degree_school || '-'}</span>
                  <SchoolTagBadge value={detailRecord.first_degree_platform} />
                </Space>
              </Descriptions.Item>
              <Descriptions.Item label="最高学历院校">
                <Space size={6} wrap>
                  <span>{detailRecord.highest_degree_school || '-'}</span>
                  <SchoolTagBadge value={detailRecord.highest_degree_platform} />
                </Space>
              </Descriptions.Item>
              <Descriptions.Item label="全部院校标签" span={2}>
                <Space size={6} wrap>
                  {detailRecord.school_tags?.length
                    ? detailRecord.school_tags.map((tag) => (
                        <SchoolTagBadge
                          key={tag.id || tag.code || tag.name}
                          value={tag.name || tag}
                        />
                      ))
                    : '-'}
                </Space>
              </Descriptions.Item>
              <Descriptions.Item label="当前志愿">
                {detailRecord.current_rank || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="当前应聘ID">
                {detailRecord.current_apply_id || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="岗位部门">
                {detailRecord.job_department_name || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="当前接收一级部门">
                {detailRecord.current_primary_department_name || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="当前接收部门">
                {detailRecord.current_department_name || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="简历状态">
                {detailRecord.system_status_label || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="流程状态">
                {WORKFLOW_STATUS[detailRecord.workflow_status]?.text || detailRecord.workflow_status || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="分配来源">
                {SOURCE_TEXT[detailRecord.allocation_source] || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="原因" span={2}>
                <Space>
                  <Tag color={REASON_TYPE[detailRecord.reason_type || 'none']?.color || 'default'}>
                    {REASON_TYPE[detailRecord.reason_type || 'none']?.text || '无'}
                  </Tag>
                  <span>{detailRecord.reason_text || '-'}</span>
                </Space>
              </Descriptions.Item>
            </Descriptions>

            {renderDetailActions(detailRecord)}

            <SmartDataTable
              tableId="candidate-resumes"
              style={{ marginTop: 16 }}
              title={() => '投递志愿'}
              rowKey="id"
              size="small"
              pagination={false}
              dataSource={detailRecord.resumes || []}
              rowClassName={(resume) => [
                'resume-volunteer-row',
                previewRecord?.id === resume.id ? 'resume-volunteer-row-active' : '',
              ].filter(Boolean).join(' ')}
              onRowClick={(resume) => setPreviewRecord(resume)}
              columns={[
                {
                  title: '志愿',
                  dataIndex: 'volunteer_rank',
                  width: 70,
                },
                {
                  title: '应聘ID',
                  dataIndex: 'apply_id',
                  width: 110,
                },
                {
                  title: '主体',
                  dataIndex: 'entity',
                  width: 100,
                  render: (value) => renderEntityTag(value),
                },
                {
                  title: '投递岗位',
                  dataIndex: 'position_name',
                  ellipsis: true,
                },
                {
                  title: '岗位类别',
                  dataIndex: 'job_category',
                  width: 110,
                },
                {
                  title: '应聘状态',
                  dataIndex: 'status',
                  width: 100,
                },
                {
                  title: '志愿处理状态',
                  dataIndex: 'lifecycle_status_label',
                  width: 150,
                  render: (_, resume) => <Tooltip title={resume.lifecycle_reason}><Tag>{resume.lifecycle_status_label || '未处理'}</Tag></Tooltip>,
                },
              ]}
            />

            {!isContact && <SmartDataTable
              tableId="candidate-application-history"
              title={() => '志愿流转历史'}
              style={{ marginTop: 16 }}
              rowKey="id"
              size="small"
              pagination={{ pageSize: 5, showSizeChanger: false, hideOnSinglePage: true }}
              dataSource={detailRecord.application_history || []}
              locale={{ emptyText: '暂无志愿流转记录' }}
              columns={[
                { title: '时间', dataIndex: 'created_at', width: 160, render: (_, event) => event.created_at ? dayjs(event.created_at).format('YYYY-MM-DD HH:mm:ss') : '-' },
                { title: '应聘ID', dataIndex: 'apply_id', width: 110 },
                { title: '状态', dataIndex: 'status_label', width: 140 },
                { title: '原因', dataIndex: 'reason' },
              ]}
            />}

            <div style={{ marginTop: 16 }}>
              <Typography.Title level={5} style={{ marginTop: 0 }}>
                简历预览
              </Typography.Title>
              <ResumePreview
                resume={previewRecord}
                attemptId={
                  isContact && previewRecord?.resume_file
                    ? detailRecord.current_attempt?.id
                    : undefined
                }
              />
            </div>

            <SmartDataTable
              tableId="candidate-attempts"
              style={{ marginTop: 16 }}
              title={() => '分配尝试'}
              rowKey="id"
              size="small"
              pagination={false}
              dataSource={detailRecord.attempts || []}
              locale={{ emptyText: '暂无分配尝试' }}
              columns={[
                { title: '次序', dataIndex: 'attempt_no', width: 70 },
                {
                  title: '来源',
                  dataIndex: 'source',
                  width: 80,
                  render: (value) => SOURCE_TEXT[value] || value || '-',
                },
                {
                  title: '投递岗位',
                  dataIndex: 'position_name',
                  ellipsis: true,
                },
                {
                  title: '首次接收部门',
                  dataIndex: 'initial_department_name',
                  width: 130,
                },
                {
                  title: '当前接收部门',
                  dataIndex: 'current_department_name',
                  width: 130,
                },
                {
                  title: '原因',
                  key: 'reason',
                  width: 220,
                  ellipsis: true,
                  render: (_, attempt) =>
                    attempt.manual_reason || attempt.match_reason || attempt.feedback_note || '-',
                },
                {
                  title: '状态',
                  dataIndex: 'status',
                  width: 100,
                  render: (value) => (
                    <Tag color={ATTEMPT_STATUS[value]?.color || 'default'}>
                      {ATTEMPT_STATUS[value]?.text || value || '-'}
                    </Tag>
                  ),
                },
                {
                  title: '反馈',
                  dataIndex: 'feedback_result',
                  width: 100,
                  render: (value) =>
                    value === 'passed' ? '通过' : value === 'rejected' ? '未通过' : '-',
                },
                {
                  title: '不通过原因',
                  dataIndex: 'feedback_reason_label_snapshot',
                  width: 140,
                  render: (value) => value || '-',
                },
                {
                  title: '备注',
                  dataIndex: 'feedback_note',
                  ellipsis: true,
                },
              ]}
            />

            <section className="resume-handling-timeline">
              <Typography.Title level={5} style={{ marginTop: 0 }}>
                处理时间线
              </Typography.Title>
              {candidateHandlingEvents(detailRecord).length ? (
                <Timeline
                  items={candidateHandlingEvents(detailRecord).map((event, index) => {
                    const fromName = eventDepartmentName(event, 'from')
                    const toName = eventDepartmentName(event, 'to')
                    const route = [fromName, toName].filter(Boolean).join(' → ')
                    const duration = formatDuration(event.duration_since_previous_seconds)
                    return {
                      key: event.id || `${event.attemptNo}-${event.event_type}-${event.occurred_at}-${index}`,
                      color: event.event_type === 'feedback_rejected'
                        ? 'red'
                        : event.event_type === 'feedback_passed'
                          ? 'green'
                          : 'blue',
                      children: (
                        <div>
                          <Space size={8} wrap>
                            <Typography.Text strong>
                              {HANDLING_EVENT_TEXT[event.event_type] || event.event_type}
                              {event.metadata?.to_screener_name && `：${event.metadata.to_screener_name}（${event.metadata.to_screener_employee_no}）`}
                            </Typography.Text>
                            {event.attemptNo ? <Tag>第 {event.attemptNo} 次尝试</Tag> : null}
                            {event.is_system_auto ? <Tag color="purple">系统自动</Tag> : null}
                            {duration ? <Tag color="blue">距上一步 {duration}</Tag> : null}
                          </Space>
                          <div className="resume-handling-timeline-meta">
                            {formatEventTime(event.occurred_at)}
                            {route ? ` · ${route}` : ''}
                            {event.actor_username_snapshot ? ` · 操作人 ${event.actor_username_snapshot}` : ''}
                          </div>
                          {event.note ? <div>{event.note}</div> : null}
                        </div>
                      ),
                    }
                  })}
                />
              ) : (
                <Typography.Text type="secondary">暂无处理日志</Typography.Text>
              )}
            </section>

            {!canViewAgentDecisions && detailRecord.current_attempt?.agent_decision_summary && (
              <section style={{ marginTop: 16 }}>
                <Typography.Title level={5}>当前简历判定依据</Typography.Title>
                <Typography.Paragraph>{detailRecord.current_attempt.agent_decision_summary.summary || detailRecord.current_attempt.agent_decision_summary.reason || '未保留分析摘要'}</Typography.Paragraph>
                {(detailRecord.current_attempt.agent_decision_summary.evidence || []).map((item, index) => (
                  <Typography.Paragraph key={index}>{typeof item === 'string' ? item : item.quote}</Typography.Paragraph>
                ))}
              </section>
            )}
            {canViewAgentDecisions && (
              <SmartDataTable
                tableId="candidate-ai-decisions"
                style={{ marginTop: 16 }}
                title={() => 'AI 筛选决策'}
                rowKey="id"
                size="small"
                loading={agentDecisionsLoading}
                pagination={false}
                dataSource={agentDecisions}
                locale={{ emptyText: '暂无 AI 筛选决策' }}
                columns={[
                  { title: '应聘ID', dataIndex: 'apply_id', width: 110 },
                  {
                    title: '结论',
                    key: 'recommendation',
                    width: 100,
                    render: (_, decision) =>
                      decision.error_code ? (
                        <Tag color="error">处理失败</Tag>
                      ) : (
                        <Tag color={decision.recommendation === 'dispatch' ? 'success' : decision.recommendation === 'review' ? 'warning' : 'default'}>
                          {decision.recommendation === 'dispatch' ? '达标入池' : decision.recommendation === 'review' ? '历史复核' : decision.recommendation === 'archive' ? '当前志愿不通过' : '-'}
                        </Tag>
                      ),
                  },
                  {
                    title: '置信度',
                    dataIndex: 'confidence_score',
                    width: 90,
                    // ProTable 的第一个参数是已渲染节点，数值必须从原始记录读取。
                    render: (_, decision) => (
                      !decision.error_code && typeof decision.confidence_score === 'number'
                      && Number.isFinite(decision.confidence_score)
                        ? `${Math.round(decision.confidence_score * 100)}%`
                        : '-'
                    ),
                  },
                  {
                    title: '固定评估岗位',
                    dataIndex: 'evaluated_job_name',
                    ellipsis: true,
                  },
                  {
                    title: '摘要/失败原因',
                    key: 'summary',
                    width: 300,
                    ellipsis: true,
                    render: (_, decision) =>
                      decision.error_message || decision.summary || decision.reason || '-',
                  },
                  {
                    title: '操作',
                    width: 145,
                    render: (_, decision) => (
                      <Space>
                        <a onClick={() => setAgentDecisionDetail(decision)}>详情</a>
                        {decision.can_retry !== false && (decision.error_code || decision.recommendation === 'archive') && hasPermission('attempt.dispatch') && (
                          <Button
                            type="link"
                            size="small"
                            loading={retryingDecisionId === decision.id}
                            onClick={() => handleRetryAgentDecision(decision)}
                          >
                            重试 Agent
                          </Button>
                        )}
                      </Space>
                    ),
                  },
                ]}
              />
            )}
          </>
        )}
      </Drawer>
      <Modal
        title="简历与岗位匹配"
        open={Boolean(agentDecisionDetail)}
        width={1080}
        footer={null}
        onCancel={() => setAgentDecisionDetail(null)}
      >
        {agentDecisionDetail && (
          <CandidateAnalysis
            key={agentDecisionDetail.id}
            decision={agentDecisionDetail}
            onRetry={hasPermission('attempt.dispatch') ? handleRetryAgentDecision : undefined}
            retrying={retryingDecisionId === agentDecisionDetail.id}
          />
        )}
      </Modal>
      <Modal
        title={manualModal.attempt ? '调整人工分配' : '手动强制分配当前志愿'}
        open={manualModal.open}
        confirmLoading={manualModal.loading}
        onOk={handleManualAssign}
        onCancel={() => setManualModal({ open: false, resume: null, attempt: null, departments: [], departmentId: undefined, reason: '', loading: false })}
        okText="确认分配"
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Select
            aria-label="手动分配目标部门"
            showSearch
            optionFilterProp="label"
            style={{ width: '100%' }}
            placeholder="选择一级或二级部门"
            value={manualModal.departmentId}
            options={manualModal.departments.map((department) => ({
              value: department.id,
              label: departmentLabel(department),
            }))}
            onChange={(value) => setManualModal((prev) => ({ ...prev, departmentId: value }))}
          />
          <Input.TextArea
            rows={3}
            placeholder="人工分配原因"
            value={manualModal.reason}
            onChange={(event) => setManualModal((prev) => ({ ...prev, reason: event.target.value }))}
          />
        </Space>
      </Modal>
      <Modal
        title="转派简历"
        open={transferModal.open}
        confirmLoading={transferModal.loading}
        onOk={handleTransfer}
        onCancel={() => setTransferModal({ open: false, record: null, departments: [], selected: undefined, note: '', loading: false })}
        okText="转派"
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Select
            showSearch
            optionFilterProp="label"
            style={{ width: '100%' }}
            placeholder="选择简历筛选人或目标部门"
            value={transferModal.selected}
            options={transferModal.departments}
            onChange={(value) => setTransferModal((previous) => ({ ...previous, selected: value }))}
          />
          <Input.TextArea
            rows={3}
            placeholder="转派备注（可选）"
            value={transferModal.note}
            onChange={(event) => setTransferModal((previous) => ({ ...previous, note: event.target.value }))}
          />
        </Space>
      </Modal>
      <Modal
        title="提交筛选反馈"
        open={feedbackModal.open}
        confirmLoading={feedbackModal.loading}
        onOk={handleFeedback}
        onCancel={() => setFeedbackModal({ open: false, record: null, result: 'passed', reasonCode: undefined, reasonOptions: FEEDBACK_REASON_OPTIONS, note: '', loading: false })}
        okText="提交反馈"
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Select
            style={{ width: '100%' }}
            value={feedbackModal.result}
            options={[
              { value: 'passed', label: '通过' },
              { value: 'rejected', label: '不通过' },
            ]}
            onChange={(value) => setFeedbackModal((previous) => ({
              ...previous,
              result: value,
              reasonCode: value === 'rejected' ? previous.reasonCode : undefined,
            }))}
          />
          {feedbackModal.result === 'rejected' && (
            <Select
              style={{ width: '100%' }}
              placeholder="请选择不通过原因"
              value={feedbackModal.reasonCode}
              options={feedbackModal.reasonOptions}
              onChange={(value) => setFeedbackModal((previous) => ({ ...previous, reasonCode: value }))}
            />
          )}
          <Input.TextArea
            rows={4}
            placeholder={feedbackModal.reasonCode === 'other' ? '请填写其他原因（必填）' : '反馈备注（可选）'}
            value={feedbackModal.note}
            onChange={(event) => setFeedbackModal((previous) => ({ ...previous, note: event.target.value }))}
          />
        </Space>
      </Modal>
    </PageContainer>
  )
}
