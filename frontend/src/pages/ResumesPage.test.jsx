import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import ResumesPage from './ResumesPage'
import {
  exportAllocations,
  exportCandidates,
  fetchCandidates,
  fetchCandidateExportFields,
  fetchCandidateFilterOptions,
  bulkDispatchCandidates,
  bulkTransferCandidates,
  fetchFeedbackReasons,
  fetchManualAssignmentOptions,
  fetchTransferOptions,
  manualAssignResume,
  submitAllocationFeedback,
  transferAllocation,
} from '../api/services'
import { downloadBlobFromResponse } from '../utils/download'

const candidate = vi.hoisted(() => ({
  id: 1,
  name: '张三',
  phone: '13800000000',
  highest_major: '计算机',
  first_degree_school: '南京大学',
  first_degree_platform: '平台A',
  highest_degree_school: '浙江大学',
  highest_degree_platform: '平台B',
  school_tags: [
    { id: 1, code: 'PLATFORM_A', name: '平台A' },
    { id: 2, code: 'PLATFORM_B', name: '平台B' },
  ],
  current_rank: 1,
  current_apply_id: 'A001',
  current_apply_date: '2026-07-15',
  current_primary_department_id: 1,
  current_primary_department_name: '研发中心',
  current_department_id: 2,
  current_department_name: '平台研发部',
  workflow_status: 'in_progress',
  system_status_label: '待处理',
  reason_type: 'none',
  reason_text: '',
  workflow_id: 9,
  current_attempt: null,
  current_resume: {
    id: 11,
    apply_id: 'A001',
    apply_date: '2026-07-15',
    volunteer_rank: 1,
    entity: '主体A',
    position_name: '后端工程师',
    job_category: '技术类',
    status: '待处理',
    resume_file: 'A001.pdf',
  },
  preview_resume: {
    id: 11,
    apply_id: 'A001',
    volunteer_rank: 1,
    entity: '主体A',
    position_name: '后端工程师',
    job_category: '技术类',
    status: '待处理',
    resume_file: 'A001.pdf',
  },
  resumes: [
    {
      id: 11,
      apply_id: 'A001',
      volunteer_rank: 1,
      entity: '主体A',
      position_name: '后端工程师',
      job_category: '技术类',
      status: '待处理',
      resume_file: 'A001.pdf',
    },
    {
      id: 12,
      apply_id: 'A002',
      volunteer_rank: 2,
      entity: '主体B',
      position_name: '产品经理',
      job_category: '产品类',
      status: '待处理',
      resume_file: '',
    },
  ],
  attempts: [],
}))

const roleState = vi.hoisted(() => ({
  permissions: new Set(['attempt.view_all']),
  user: { id: 1, username: 'tester' },
  contact: null,
  isContact: false,
  isSecondaryContact: false,
}))
const runProcess = vi.hoisted(() => vi.fn())

vi.mock('@ant-design/pro-components', () => ({
  PageContainer: ({ children }) => <div>{children}</div>,
}))

vi.mock('../contexts/roleState', () => ({
  useRole: () => ({
    hasPermission: (code) => roleState.permissions.has(code),
    user: roleState.user,
    contact: roleState.contact,
    isContact: roleState.isContact,
    isSecondaryContact: roleState.isSecondaryContact,
  }),
}))

vi.mock('../components/useProcessRunner', () => ({
  useProcessRunner: () => ({ run: runProcess }),
}))

vi.mock('../components/ImportButton', () => ({ default: () => null }))

vi.mock('../components/ResumePreview', () => ({
  default: ({ resume }) => (
    <div data-testid="resume-preview">
      {resume?.resume_file ? `preview:${resume.apply_id}` : `missing:${resume?.apply_id || 'none'}`}
    </div>
  ),
}))

vi.mock('../components/SmartDataTable', () => ({
  default: ({
    tableId,
    columns = [],
    dataSource = [],
    onRowClick,
    rowSelection,
    toolBarRender,
    request,
    params,
    actionRef,
    batchActions,
    defaultColumnsState,
  }) => {
    if (actionRef) {
      actionRef.current = {
        clearSelected: () => rowSelection?.onChange?.([]),
      }
    }
    return (
      <section
        data-testid={`table-${tableId}`}
        data-filter-count={columns.filter((column) => column.filter).length}
        data-columns={columns.map((column) => column.title).join(',')}
        data-params={JSON.stringify(params || {})}
        data-default-columns-state={JSON.stringify(defaultColumnsState || {})}
        data-selectable={Boolean(rowSelection)}
      >
        {tableId === 'candidates' && (
          <>
            <div>{toolBarRender?.()}</div>
            <output data-testid="selected-count">{rowSelection?.selectedRowKeys?.length || 0}</output>
            <button type="button" onClick={() => onRowClick?.(candidate)}>
              打开候选人
            </button>
            <span data-testid="current-apply-date-cell">
              {columns.find((column) => column.dataIndex === 'current_apply_date')
                ?.render?.(candidate.current_apply_date, candidate)}
            </span>
            <span data-testid="school-tags-cell">
              {columns.find((column) => column.dataIndex === 'school_tag')
                ?.render?.(candidate.school_tag, candidate)}
            </span>
            <button type="button" onClick={() => rowSelection?.onChange?.([1, 2])}>
              选择两名候选人
            </button>
            <button type="button" onClick={() => rowSelection?.onChange?.(
              [1, 2],
              [candidate, { ...candidate, id: 2 }],
            )}>
              选择两名可转派候选人
            </button>
            <button type="button" onClick={() => rowSelection?.onChange?.([3], [{ ...candidate, id: 3 }])}>
              改选一名候选人
            </button>
            {rowSelection?.selectedRowKeys?.length ? batchActions?.({
              clearSelection: () => rowSelection.onChange?.([]),
            }) : null}
            <button
              type="button"
              onClick={() => request?.({
                ...params,
                page: 1,
                page_size: 10,
                name: '张三',
                system_status: 'raw',
              })}
            >
              加载候选人
            </button>
            <button
              type="button"
              onClick={() => request?.({
                ...params,
                page: 1,
                page_size: 10,
                current_apply_date_from: '2026-07-01',
                current_apply_date_to: '2026-07-31',
              })}
            >
              加载日期筛选
            </button>
          </>
        )}
        {dataSource.map((record) => (
          <div key={record.id}>
            <button type="button" onClick={() => onRowClick?.(record)}>
              {record.apply_id || record.id}
            </button>
            {tableId === 'candidate-resumes' && (
              <span data-testid={`entity-${record.id}`}>
                {columns.find((column) => column.dataIndex === 'entity')?.render?.(record.entity, record)}
              </span>
            )}
          </div>
        ))}
      </section>
    )
  },
}))

vi.mock('../api/services', () => ({
  deleteCandidate: vi.fn(),
  bulkDeleteCandidates: vi.fn(),
  exportCandidates: vi.fn(),
  fetchCandidates: vi.fn(),
  fetchCandidateFilterOptions: vi.fn(),
  fetchCandidateExportFields: vi.fn(),
  fetchContacts: vi.fn(),
  fetchManualAssignmentOptions: vi.fn(),
  manualAssignResume: vi.fn(),
  fetchAgentDecisions: vi.fn().mockResolvedValue({ data: { results: [] } }),
  fetchAgentDecision: vi.fn().mockResolvedValue({ data: {} }),
  retryAgentDecision: vi.fn(),
  dispatchAllocation: vi.fn(),
  confirmReviewAllocation: vi.fn(),
  cancelAllocation: vi.fn(),
  cancelReviewAllocation: vi.fn(),
  transferAllocationToManual: vi.fn(),
  bulkDispatchCandidates: vi.fn(),
  bulkTransferCandidates: vi.fn(),
  transferAllocation: vi.fn(),
  fetchTransferOptions: vi.fn(),
  fetchFeedbackReasons: vi.fn(),
  submitAllocationFeedback: vi.fn(),
  exportAllocations: vi.fn(),
  exportResumeResultReport: vi.fn(),
}))

vi.mock('../utils/download', () => ({
  downloadBlobFromResponse: vi.fn(),
}))

const exportCatalog = {
  version: 1,
  groups: [
    {
      key: 'candidate',
      label: '候选人',
      fields: [
        { key: 'candidate_name', label: '姓名', default_selected: true },
        { key: 'candidate_phone', label: '手机号', default_selected: true },
      ],
    },
    {
      key: 'application',
      label: '当前投递',
      fields: [
        { key: 'current_apply_id', label: '应聘ID', default_selected: true },
      ],
    },
  ],
}

describe('ResumesPage detail', () => {
  beforeEach(() => {
    candidate.current_attempt = null
    candidate.attempts = []
    roleState.permissions = new Set(['attempt.view_all'])
    roleState.user = { id: 1, username: 'tester' }
    roleState.contact = null
    roleState.isContact = false
    roleState.isSecondaryContact = false
    runProcess.mockReset()
    runProcess.mockResolvedValue({ success: true })
    fetchCandidateExportFields.mockReset()
    fetchCandidateExportFields.mockResolvedValue({ data: exportCatalog })
    exportCandidates.mockReset()
    exportCandidates.mockResolvedValue({
      data: new Blob(['xlsx']),
      headers: {
        'x-export-mode': 'excel',
        'x-export-count': '0',
        'x-export-missing': '0',
        'x-export-candidate-count': '2',
      },
    })
    exportAllocations.mockReset()
    exportAllocations.mockResolvedValue({
      data: new Blob(['zip']),
      headers: {
        'x-export-mode': 'zip',
        'x-export-count': '1',
        'x-export-missing': '0',
        'x-export-candidate-count': '1',
      },
    })
    downloadBlobFromResponse.mockReset()
    fetchCandidateFilterOptions.mockReset()
    fetchCandidateFilterOptions.mockResolvedValue({ data: {} })
    bulkDispatchCandidates.mockReset()
    bulkDispatchCandidates.mockResolvedValue({
      data: { detail: '已下发 2 条，跳过 0 条，失败 0 条' },
    })
    bulkTransferCandidates.mockReset()
    bulkTransferCandidates.mockResolvedValue({
      data: { transferred: 2, skipped: 0, failed: 0, batch_operation_id: 'batch-1' },
    })
    fetchTransferOptions.mockReset()
    fetchTransferOptions.mockResolvedValue({
      data: {
        results: [
          { id: 2, name: '平台研发部', level: 2, primary_department_name: '研发中心' },
          { id: 3, name: '后端开发组', level: 3, primary_department_name: '研发中心' },
        ],
      },
    })
    fetchFeedbackReasons.mockReset()
    fetchFeedbackReasons.mockResolvedValue({ data: { results: [
      { value: 'major_background_mismatch', label: '专业背景不匹配' },
      { value: 'other', label: '其他' },
    ] } })
    submitAllocationFeedback.mockReset()
    submitAllocationFeedback.mockResolvedValue({ data: {} })
    fetchManualAssignmentOptions.mockReset()
    fetchManualAssignmentOptions.mockResolvedValue({ data: { results: [
      { id: 2, name: '平台研发部', level: 2, parent_name: '研发中心' },
      { id: 3, name: '后端开发组', level: 3, parent_name: '平台研发部' },
    ] } })
    manualAssignResume.mockReset()
    manualAssignResume.mockResolvedValue({ data: {} })
    transferAllocation.mockReset()
    transferAllocation.mockResolvedValue({ data: {} })
    fetchCandidates.mockReset()
    fetchCandidates.mockResolvedValue({ data: { count: 0, results: [] } })
  })

  it('removes detail filters and switches preview by volunteer row', async () => {
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: '打开候选人' }))

    await waitFor(() => expect(screen.getByTestId('resume-preview').textContent).toBe('preview:A001'))

    for (const tableId of ['candidate-resumes', 'candidate-attempts', 'candidate-ai-decisions']) {
      expect(screen.getByTestId(`table-${tableId}`).dataset.filterCount).toBe('0')
    }
    expect(screen.getByTestId('table-candidate-resumes').dataset.columns).not.toContain('预览')
    expect(screen.getByTestId('table-candidates').dataset.columns).not.toContain('处理结果')
    const candidateColumns = screen.getByTestId('table-candidates').dataset.columns
    expect(candidateColumns).not.toContain('当前志愿')
    expect(candidateColumns).toContain('投递时间,当前应聘ID,当前主体,当前投递岗位,岗位部门,当前接收一级部门,当前接收部门')
    expect(screen.getByTestId('current-apply-date-cell').textContent).toBe('2026-07-15')
    expect(JSON.parse(screen.getByTestId('table-candidates').dataset.defaultColumnsState)).toEqual({
      current_apply_id: { show: false },
      current_entity: { show: false },
      allocation_source: { show: false },
    })
    expect(screen.queryByText('第一学历标签')).toBeNull()
    expect(screen.queryByText('最高学历标签')).toBeNull()
    expect(screen.getAllByText('平台A').every((item) => item.classList.contains('ant-tag'))).toBe(true)
    expect(screen.getAllByText('平台B').every((item) => item.classList.contains('ant-tag'))).toBe(true)
    expect(screen.getByTestId('school-tags-cell').textContent).toBe('平台A平台B')
    const entityATag = screen.getByTestId('entity-11').querySelector('.ant-tag')
    const entityBTag = screen.getByTestId('entity-12').querySelector('.ant-tag')
    expect(entityATag.textContent).toBe('主体A')
    expect(entityBTag.textContent).toBe('主体B')
    expect(entityATag.className).not.toBe(entityBTag.className)

    await userEvent.click(screen.getByRole('button', { name: 'A002' }))
    expect(screen.getByTestId('resume-preview').textContent).toBe('missing:A002')
  })

  it('shows feedback to a contact in the current receiving department', async () => {
    candidate.current_attempt = {
      id: 21,
      status: 'dispatched',
      current_department: 2,
      current_department_name: '平台研发部',
      feedback_at: null,
    }
    candidate.attempts = [candidate.current_attempt]
    roleState.permissions = new Set(['attempt.feedback', 'attempt.view_department'])
    roleState.contact = { id: 10, department: 2, department_level: 2 }
    roleState.isContact = true
    roleState.isSecondaryContact = true

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: '打开候选人' }))

    expect(screen.getByRole('button', { name: '提交反馈' })).not.toBeNull()
  })

  it('loads manual assignment targets without the generic department API', async () => {
    roleState.permissions = new Set(['resume.view', 'resume.manual_assign'])
    const user = userEvent.setup()

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await user.click(screen.getByRole('button', { name: '打开候选人' }))
    await user.click(screen.getByRole('button', { name: '手动强制分配当前志愿' }))

    await waitFor(() => expect(fetchManualAssignmentOptions).toHaveBeenCalledTimes(1))
    await user.click(screen.getByRole('combobox', { name: '手动分配目标部门' }))
    await user.click(await screen.findByText('研发中心 / 平台研发部'))
    await user.click(screen.getByRole('button', { name: /确认分配/ }))

    await waitFor(() => expect(manualAssignResume).toHaveBeenCalledWith(11, {
      target_department_id: 2,
      manual_reason: 'HR 手动强制分配',
    }))
  })

  it('hides feedback from a parent department contact after transfer to a child department', async () => {
    candidate.current_attempt = {
      id: 22,
      status: 'dispatched',
      current_department: 3,
      current_department_name: '后端开发组',
      feedback_at: null,
    }
    candidate.attempts = [candidate.current_attempt]
    roleState.permissions = new Set(['attempt.feedback', 'attempt.view_department'])
    roleState.contact = { id: 10, department: 2, department_level: 2 }
    roleState.isContact = true
    roleState.isSecondaryContact = true

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: '打开候选人' }))

    expect(screen.queryByRole('button', { name: '提交反馈' })).toBeNull()
  })

  it('submits a structured rejection reason for the current department', async () => {
    candidate.current_attempt = {
      id: 23,
      status: 'dispatched',
      current_department: 2,
      current_department_name: '平台研发部',
      feedback_at: null,
    }
    candidate.attempts = [candidate.current_attempt]
    roleState.permissions = new Set(['attempt.feedback', 'attempt.view_department'])
    roleState.contact = { id: 10, department: 2, department_level: 2 }
    roleState.isContact = true
    const user = userEvent.setup()

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await user.click(screen.getByRole('button', { name: '打开候选人' }))
    await user.click(screen.getAllByRole('button', { name: /提交反馈/ }).at(-1))
    await waitFor(() => expect(fetchFeedbackReasons).toHaveBeenCalled())

    const resultSelect = screen.getAllByRole('combobox')[0]
    await user.click(resultSelect)
    await user.click(await screen.findByText('不通过'))
    const reasonSelect = screen.getAllByRole('combobox')[1]
    await user.click(reasonSelect)
    await user.click(await screen.findByText('专业背景不匹配'))
    await user.click(screen.getAllByRole('button', { name: /提交反馈/ }).at(-1))

    await waitFor(() => expect(submitAllocationFeedback).toHaveBeenCalledWith(23, {
      result: 'rejected',
      reason_code: 'major_background_mismatch',
      note: '',
    }))
  })

  it('freezes selected candidates and only offers secondary departments for bulk transfer', async () => {
    candidate.current_attempt = {
      id: 24,
      status: 'dispatched',
      current_department: 2,
      current_department_name: '平台研发部',
      feedback_at: null,
    }
    candidate.attempts = [candidate.current_attempt]
    roleState.permissions = new Set(['attempt.transfer_department', 'attempt.view_department'])
    roleState.contact = { id: 10, department: 2, department_level: 2, can_delegate: true }
    roleState.isContact = true
    const user = userEvent.setup()

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await user.click(screen.getByRole('button', { name: '选择两名可转派候选人' }))
    await user.click(screen.getByRole('button', { name: '批量转派' }))

    expect(await screen.findByText('批量转派（冻结 2 人）')).toBeTruthy()
    await waitFor(() => expect(fetchTransferOptions).toHaveBeenCalledWith(24))
    const targetSelect = screen.getByRole('combobox')
    await user.click(targetSelect)
    expect(screen.queryByText(/后端开发组/)).toBeNull()
    await user.click(await screen.findByText('研发中心 / 平台研发部'))
    await user.click(screen.getByRole('button', { name: /确认转派/ }))

    await waitFor(() => expect(bulkTransferCandidates).toHaveBeenCalledWith({
      candidate_ids: [1, 2],
      target_department_id: 2,
      note: '',
    }))
  })

  it('allows a secondary contact to transfer one resume to an eligible tertiary department', async () => {
    candidate.current_attempt = {
      id: 26,
      status: 'dispatched',
      current_department: 2,
      current_department_name: '平台研发部',
      feedback_at: null,
    }
    candidate.attempts = [candidate.current_attempt]
    roleState.permissions = new Set(['attempt.transfer_department', 'attempt.view_department'])
    roleState.contact = { id: 10, department: 2, department_level: 2, can_delegate: true }
    roleState.isContact = true
    const user = userEvent.setup()

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await user.click(screen.getByRole('button', { name: '打开候选人' }))
    await user.click(screen.getByRole('button', { name: '转派部门' }))
    await waitFor(() => expect(fetchTransferOptions).toHaveBeenCalledWith(26))
    await user.click(screen.getByRole('combobox'))
    await user.click(await screen.findByText('研发中心 / 后端开发组'))
    await user.click(screen.getAllByRole('button', { name: /转.?派/ }).at(-1))

    await waitFor(() => expect(transferAllocation).toHaveBeenCalledWith(26, {
      target_department_id: 3,
      note: '',
    }))
  })

  it('hides transfer selection and bulk transfer when the secondary contact cannot delegate', () => {
    roleState.permissions = new Set(['attempt.transfer_department', 'attempt.view_department'])
    roleState.contact = { id: 10, department: 2, department_level: 2, can_delegate: false }
    roleState.isContact = true

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    expect(screen.queryByRole('button', { name: '批量转派' })).toBeNull()
    expect(screen.getByTestId('table-candidates').dataset.selectable).toBe('false')
  })

  it('shows department handling events and elapsed time in the detail timeline', async () => {
    candidate.current_attempt = {
      id: 25,
      status: 'dispatched',
      current_department: 2,
      current_department_name: '平台研发部',
      handling_events: [
        {
          id: 100,
          event_type: 'attempt_created',
          duration_since_previous_seconds: null,
          occurred_at: '2026-08-23T23:00:00Z',
        },
        {
          id: 101,
          event_type: 'department_dispatched',
          to_department_name: '平台研发部',
          actor_username_snapshot: 'HR001',
          duration_since_previous_seconds: 7200,
          occurred_at: '2026-08-24T01:00:00Z',
        },
      ],
    }
    candidate.attempts = [candidate.current_attempt]

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: '打开候选人' }))

    expect(screen.getByText('处理时间线')).toBeTruthy()
    expect(screen.getByText('下发部门')).toBeTruthy()
    expect(screen.getByText('距上一步 2.0 小时')).toBeTruthy()
    expect(screen.getByText(/操作人 HR001/)).toBeTruthy()
  })

  it('recalculates elapsed time across assignment attempts in the candidate timeline', async () => {
    candidate.current_attempt = {
      id: 28,
      attempt_no: 2,
      status: 'dispatched',
      current_department: 2,
      current_department_name: '平台研发部',
    }
    candidate.attempts = [
      {
        id: 27,
        attempt_no: 1,
        handling_events: [
          {
            id: 201,
            event_type: 'feedback_rejected',
            duration_since_previous_seconds: null,
            occurred_at: '2026-08-24T01:00:00Z',
          },
        ],
      },
      {
        ...candidate.current_attempt,
        handling_events: [
          {
            id: 202,
            event_type: 'attempt_created',
            duration_since_previous_seconds: null,
            occurred_at: '2026-08-24T04:00:00Z',
          },
        ],
      },
    ]

    render(<MemoryRouter><ResumesPage /></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: '打开候选人' }))

    expect(screen.getByText('距上一步 3.0 小时')).toBeTruthy()
    expect(screen.getByText('第 2 次尝试')).toBeTruthy()
  })

  it('always shows the fixed processing choices and disables an empty current selection', async () => {
    roleState.permissions = new Set(['pipeline.run'])
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    await userEvent.click(screen.getByRole('button', { name: /处理简历/ }))

    const currentSelected = screen.getByRole('checkbox', { name: '当前选中（0）' })
    expect(currentSelected.disabled).toBe(true)
    expect(screen.getAllByRole('checkbox')).toHaveLength(9)
    for (const checkbox of screen.getAllByRole('checkbox')) {
      expect(checkbox.checked).toBe(false)
    }
  })

  it('freezes selected candidate ids, keeps choices exclusive, and clears selection on success', async () => {
    roleState.permissions = new Set(['pipeline.run', 'resume.import'])
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    await userEvent.click(screen.getByRole('button', { name: '选择两名候选人' }))
    expect(screen.getByTestId('selected-count').textContent).toBe('2')
    await userEvent.click(screen.getByRole('button', { name: /处理简历/ }))

    const currentSelected = screen.getByRole('checkbox', { name: '当前选中（2）' })
    expect(currentSelected.checked).toBe(false)
    await userEvent.click(currentSelected)
    expect(currentSelected.checked).toBe(true)

    const rawStatus = screen.getByRole('checkbox', { name: '待处理' })
    const archivedStatus = screen.getByRole('checkbox', { name: '已归档' })
    await userEvent.click(rawStatus)
    await userEvent.click(archivedStatus)
    expect(currentSelected.checked).toBe(false)
    expect(rawStatus.checked).toBe(true)
    expect(archivedStatus.checked).toBe(true)

    await userEvent.click(currentSelected)
    expect(rawStatus.checked).toBe(false)
    expect(archivedStatus.checked).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: '改选一名候选人' }))
    await userEvent.click(screen.getByRole('button', { name: '开始处理' }))

    await waitFor(() => expect(runProcess).toHaveBeenCalledWith(
      [{ step: 'step2', label: '院校准入 → 固定业务引用 → Agent 筛选' }],
      '正在提交 Agent 简历处理任务',
      {
        scope: {
          candidate_ids: [1, 2],
          force_reprocess: true,
        },
      },
    ))
    expect(screen.getByTestId('selected-count').textContent).toBe('0')
  })

  it('submits status processing with the current table and task filters', async () => {
    roleState.permissions = new Set(['pipeline.run'])
    render(
      <MemoryRouter initialEntries={['/resumes?processing_run_id=18&processing_result=completed']}>
        <ResumesPage />
      </MemoryRouter>,
    )

    await userEvent.click(screen.getByRole('button', { name: '加载候选人' }))
    await userEvent.click(screen.getByRole('button', { name: /处理简历/ }))
    await userEvent.click(screen.getByRole('checkbox', { name: '通过' }))
    await userEvent.click(screen.getByRole('button', { name: '开始处理' }))

    await waitFor(() => expect(runProcess).toHaveBeenCalledWith(
      [{ step: 'step2', label: '院校准入 → 固定业务引用 → Agent 筛选' }],
      '正在提交 Agent 简历处理任务',
      {
        scope: {
          system_statuses: ['screening_passed'],
          candidate_filters: {
            name: '张三',
            processing_run_id: '18',
            processing_result: 'completed',
          },
        },
      },
    ))
  })

  it('passes the current apply date range into status reprocessing scope', async () => {
    roleState.permissions = new Set(['pipeline.run'])
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    await userEvent.click(screen.getByRole('button', { name: '加载日期筛选' }))
    await userEvent.click(screen.getByRole('button', { name: /处理简历/ }))
    await userEvent.click(screen.getByRole('checkbox', { name: '待处理' }))
    await userEvent.click(screen.getByRole('button', { name: '开始处理' }))

    await waitFor(() => expect(runProcess).toHaveBeenCalledWith(
      [{ step: 'step2', label: '院校准入 → 固定业务引用 → Agent 筛选' }],
      '正在提交 Agent 简历处理任务',
      {
        scope: {
          system_statuses: ['raw'],
          candidate_filters: {
            current_apply_date_from: '2026-07-01',
            current_apply_date_to: '2026-07-31',
          },
        },
      },
    ))
  })

  it('uses the fixed Agent Kernel without a client-side mode selector', async () => {
    roleState.permissions = new Set(['pipeline.run'])
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    await userEvent.click(screen.getByRole('button', { name: /处理简历/ }))
    expect(screen.getByText(/系统将使用 Agent Kernel/)).not.toBeNull()
    expect(screen.queryByRole('radio')).toBeNull()
    await userEvent.click(screen.getByRole('checkbox', { name: '待处理' }))
    await userEvent.click(screen.getByRole('button', { name: '开始处理' }))

    await waitFor(() => expect(runProcess).toHaveBeenCalledWith(
      [{ step: 'step2', label: '院校准入 → 固定业务引用 → Agent 筛选' }],
      '正在提交 Agent 简历处理任务',
      {
        scope: {
          system_statuses: ['raw'],
          candidate_filters: {},
        },
      },
    ))
  })

  it('keeps selection but closes old detail when clearing a task result filter', async () => {
    roleState.permissions = new Set(['pipeline.run', 'resume.import'])
    render(
      <MemoryRouter initialEntries={['/resumes?processing_run_id=18&processing_result=completed']}>
        <ResumesPage />
      </MemoryRouter>,
    )

    expect(screen.getByTestId('table-candidates').dataset.params).toBe(
      '{"processing_run_id":"18","processing_result":"completed"}',
    )
    await userEvent.click(screen.getByRole('button', { name: '选择两名候选人' }))
    await userEvent.click(screen.getByRole('button', { name: '打开候选人' }))
    await waitFor(() => expect(screen.getByTestId('resume-preview')).not.toBeNull())
    await userEvent.click(screen.getByRole('button', { name: '清除筛选' }))

    await waitFor(() => expect(screen.queryByTestId('resume-preview')).toBeNull())
    expect(screen.getByTestId('selected-count').textContent).toBe('2')
    expect(screen.getByTestId('table-candidates').dataset.params).toBe('{}')
  })

  it('restores dashboard drilldown parameters and exposes the active scope', async () => {
    const query = new URLSearchParams({
      analytics_date_from: '2026-06-17',
      analytics_date_to: '2026-07-16',
      analytics_primary_department_id: '10',
      analytics_dimension: 'source',
      analytics_values: JSON.stringify(['rule']),
      analytics_value_labels: JSON.stringify(['规则分配']),
      analytics_title: '分配来源 · 规则分配',
      analytics_context: '导入时间 2026-06-17 至 2026-07-16；一级部门 科技中心',
    }).toString()
    render(
      <MemoryRouter initialEntries={[`/resumes?${query}`]}>
        <ResumesPage />
      </MemoryRouter>,
    )

    expect(screen.getByText('看板下钻：分配来源 · 规则分配')).toBeTruthy()
    expect(screen.getByText(/一级部门 科技中心/)).toBeTruthy()
    expect(JSON.parse(screen.getByTestId('table-candidates').dataset.params)).toEqual({
      analytics_date_from: '2026-06-17',
      analytics_date_to: '2026-07-16',
      analytics_primary_department_id: '10',
      analytics_dimension: 'source',
      analytics_values: '["rule"]',
      analytics_value_labels: '["规则分配"]',
    })

    await userEvent.click(screen.getByRole('button', { name: '加载候选人' }))
    expect(fetchCandidates).toHaveBeenCalledWith(expect.objectContaining({
      analytics_dimension: 'source',
      analytics_values: '["rule"]',
      analytics_primary_department_id: '10',
      name: '张三',
      system_status: 'raw',
    }))
  })

  it('exports selected candidates as an Excel list by default', async () => {
    roleState.permissions = new Set(['resume.view'])
    const user = userEvent.setup()
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    expect(screen.queryByRole('button', { name: /导出结果报表/ })).toBeNull()
    await user.click(screen.getByRole('button', { name: '选择两名候选人' }))
    await user.click(screen.getByRole('button', { name: /导出选中/ }))
    expect(await screen.findByText('选择简历导出属性')).toBeTruthy()
    expect(exportCandidates).not.toHaveBeenCalled()

    await user.click(screen.getByRole('checkbox', { name: '手机号' }))
    await user.click(screen.getByRole('button', { name: '导出 Excel' }))

    await waitFor(() => expect(exportCandidates).toHaveBeenCalledWith([1, 2], {
      fields: 'candidate_name,current_apply_id',
      include_resume_files: false,
    }))
    expect(downloadBlobFromResponse).toHaveBeenCalledWith(
      expect.objectContaining({ headers: expect.objectContaining({ 'x-export-candidate-count': '2' }) }),
      '简历库清单.xlsx',
    )
    expect(await screen.findByText(/已导出 2 名候选人的 Excel 清单/)).toBeTruthy()
  })

  it('keeps dispatch in the toolbar and freezes selected candidate ids', async () => {
    roleState.permissions = new Set(['attempt.dispatch'])
    const user = userEvent.setup()
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    await user.click(screen.getByRole('button', { name: '选择两名候选人' }))
    await user.click(screen.getByRole('button', { name: /下发$/ }))
    expect(await screen.findByText('当前选中（冻结 2 人）')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: '确认下发' }))

    await waitFor(() => expect(bulkDispatchCandidates).toHaveBeenCalledWith({
      candidate_ids: [1, 2],
    }))
    expect(screen.getByTestId('selected-count').textContent).toBe('0')
  })

  it('uses the frozen table filters when opening current-filter export', async () => {
    roleState.permissions = new Set(['resume.view'])
    const user = userEvent.setup()
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    await user.click(screen.getByRole('button', { name: '加载候选人' }))
    await user.click(screen.getByRole('button', { name: '选择两名候选人' }))
    await user.click(screen.getByRole('button', { name: /导出当前筛选/ }))
    await screen.findByRole('checkbox', { name: '姓名' })
    await user.click(screen.getByRole('button', { name: '导出 Excel' }))

    await waitFor(() => expect(exportCandidates).toHaveBeenCalledWith(null, {
      name: '张三',
      system_status: 'raw',
      fields: 'candidate_name,candidate_phone,current_apply_id',
      include_resume_files: false,
    }))
  })

  it('uses the frozen current apply date range for current-filter export', async () => {
    roleState.permissions = new Set(['resume.view'])
    const user = userEvent.setup()
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    await user.click(screen.getByRole('button', { name: '加载日期筛选' }))
    await user.click(screen.getByRole('button', { name: '选择两名候选人' }))
    await user.click(screen.getByRole('button', { name: /导出当前筛选/ }))
    await screen.findByRole('checkbox', { name: '姓名' })
    await user.click(screen.getByRole('button', { name: '导出 Excel' }))

    await waitFor(() => expect(exportCandidates).toHaveBeenCalledWith(null, {
      current_apply_date_from: '2026-07-01',
      current_apply_date_to: '2026-07-31',
      fields: 'candidate_name,candidate_phone,current_apply_id',
      include_resume_files: false,
    }))
  })

  it('opens the same field chooser for an attempt-scoped detail export', async () => {
    candidate.current_attempt = {
      id: 31,
      status: 'dispatched',
      current_department: 2,
      feedback_at: null,
    }
    candidate.attempts = [candidate.current_attempt]
    roleState.permissions = new Set(['attempt.export'])
    const user = userEvent.setup()
    render(<MemoryRouter><ResumesPage /></MemoryRouter>)

    await user.click(screen.getByRole('button', { name: '打开候选人' }))
    await user.click(screen.getByRole('button', { name: /导出$/ }))
    await screen.findByRole('checkbox', { name: '姓名' })
    await user.click(screen.getByRole('checkbox', { name: '同时下载简历原件' }))
    await user.click(screen.getByRole('button', { name: '导出 ZIP' }))

    await waitFor(() => expect(exportAllocations).toHaveBeenCalledWith([31], {
      fields: 'candidate_name,candidate_phone,current_apply_id',
      include_resume_files: true,
    }))
    expect(downloadBlobFromResponse).toHaveBeenCalledWith(
      expect.objectContaining({ headers: expect.objectContaining({ 'x-export-mode': 'zip' }) }),
      '简历导出.zip',
    )
    expect(await screen.findByText(/已导出 1 名候选人，包含 1 份简历/)).toBeTruthy()
  })
})
