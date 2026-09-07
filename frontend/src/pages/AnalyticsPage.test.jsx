import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import AnalyticsPage from './AnalyticsPage'
import { exportResumeResultReport, fetchRecruitmentOverview } from '../api/services'

const roleState = vi.hoisted(() => ({ permissions: new Set(['resume.view']) }))

vi.mock('../api/services', () => ({
  fetchRecruitmentOverview: vi.fn(),
  exportResumeResultReport: vi.fn(),
}))

vi.mock('../contexts/roleState', () => ({
  useRole: () => ({
    hasPermission: (code) => roleState.permissions.has(code),
  }),
}))

vi.mock('../utils/download', () => ({
  downloadBlobFromResponse: vi.fn(),
}))

vi.mock('react-chartjs-2', () => ({
  Doughnut: ({ 'aria-label': ariaLabel }) => <canvas data-testid="doughnut-chart" aria-label={ariaLabel} />,
  Bar: ({ 'aria-label': ariaLabel }) => <canvas data-testid="bar-chart" aria-label={ariaLabel} />,
}))

const payload = {
  data_as_of: '2026-07-16T10:00:00+08:00',
  filters: { date_from: '2026-06-17', date_to: '2026-07-16' },
  summary: {
    resume_count: 12,
    candidate_count: 10,
    classified_count: 9,
    allocated_count: 8,
    dispatched_count: 7,
    feedback_count: 6,
    passed_count: 4,
    archived_count: 2,
  },
  conversion: { allocated_rate: 80, dispatched_rate: 70, feedback_rate: 60, passed_rate: 40 },
  average_hours: { to_allocation: 2.5, to_dispatch: 8, to_feedback: 26 },
  trend: [{ date: '2026-07-16', resumes: 12, allocated: 8, dispatched: 7, feedback: 6, passed: 4 }],
  source_distribution: [
    { key: 'rule', label: '规则分配', count: 6 },
    { key: 'ai', label: 'AI 分配', count: 2 },
  ],
  ai_recommendation_distribution: [
    { key: 'review', label: '人工复核', count: 1 },
  ],
  ai_error_distribution: [],
  job_ranking: [{ key: 1, label: '软件工程师', count: 5 }],
  primary_department_ranking: [{ key: 10, label: '科技中心', count: 5 }],
  department_ranking: [{ key: 1, label: '产品研发', count: 5 }],
  school_tag_ranking: [],
  education_distribution: [],
  archive_reason_distribution: [],
  rejection_reason_distribution: [
    { key: 'major_background_mismatch', label: '专业背景不匹配', count: 2 },
  ],
  handling_speed: {
    overall: {
      hr_dispatch_hours: { avg: 2, median: 1.5, p90: 3, sample_count: 7 },
      department_processing_hours: { avg: 8, median: 6, p90: 14, sample_count: 6 },
      total_feedback_hours: { avg: 26, median: 24, p90: 40, sample_count: 6 },
      pending_count: 1,
      max_pending_age_hours: 12,
    },
    departments: [
      {
        department_id: 1,
        department_name: '算法平台部',
        primary_department_id: 10,
        primary_department_name: '研究院',
        processing_hours: { avg: 8, median: 6, p90: 14, sample_count: 6 },
        pending_count: 1,
        max_pending_age_hours: 12,
      },
    ],
  },
  filter_options: {
    entities: ['GW'],
    jobs: [],
    primary_departments: [
      { value: 10, label: '科技中心' },
      { value: 20, label: '市场中心' },
    ],
    departments: [
      { value: 1, label: '研发二部', parent_id: 10 },
      { value: 2, label: '市场二部', parent_id: 20 },
    ],
    school_tags: [],
    educations: [],
    sources: [],
  },
}

function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>
}

function renderAnalytics() {
  return render(
    <MemoryRouter initialEntries={['/analytics']}>
      <Routes>
        <Route path="/analytics" element={<AnalyticsPage />} />
        <Route path="/resumes" element={<LocationProbe />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('AnalyticsPage', () => {
  beforeEach(() => {
    roleState.permissions = new Set(['resume.view'])
    fetchRecruitmentOverview.mockReset()
    fetchRecruitmentOverview.mockResolvedValue({ data: payload })
    exportResumeResultReport.mockReset()
    exportResumeResultReport.mockResolvedValue({ data: new Blob(['report']) })
  })

  it('renders the management overview, conversion, rankings and diagnosis sections', async () => {
    renderAnalytics()

    expect(await screen.findByText('招聘概览')).toBeTruthy()
    expect(screen.getByText('候选人数')).toBeTruthy()
    expect(screen.queryByText('去重候选人')).toBeNull()
    expect(screen.getByRole('button', { name: '投递 12' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '已分类 9' })).toBeTruthy()
    expect(screen.getByText('招聘流程')).toBeTruthy()
    expect(screen.queryByText('按日趋势')).toBeNull()
    expect(screen.queryByText('最近候选人')).toBeNull()
    expect(screen.getByText('处理效率')).toBeTruthy()
    expect(screen.getByText('人工处理时效')).toBeTruthy()
    expect(screen.getByText('HR 下发时长')).toBeTruthy()
    expect(screen.getByText('部门处理时效')).toBeTruthy()
    expect(screen.getByText('算法平台部')).toBeTruthy()
    expect(screen.getByText('人工复核')).toBeTruthy()
    expect(screen.getByRole('img', { name: /分配来源：规则分配 6/ })).toBeTruthy()
    expect(screen.getByRole('img', { name: /AI 建议分布：人工复核 1/ })).toBeTruthy()
    expect(screen.getByRole('img', { name: /岗位排行：软件工程师 5/ })).toBeTruthy()
    expect(screen.getByRole('img', { name: /一级部门排行：科技中心 5/ })).toBeTruthy()
    expect(screen.getByRole('img', { name: /二级部门排行：产品研发 5/ })).toBeTruthy()
    expect(screen.getByText('暂无 AI 错误记录')).toBeTruthy()
    expect(fetchRecruitmentOverview).toHaveBeenCalledWith({})
  })

  it('keeps only the time range and level-1/level-2 department filters in the data area', async () => {
    renderAnalytics()
    await screen.findByText('招聘概览')

    expect(screen.getByText('时间区间')).toBeTruthy()
    expect(screen.getAllByText('一级部门').length).toBeGreaterThan(0)
    expect(screen.getAllByText('二级部门').length).toBeGreaterThan(0)
    expect(screen.queryByText('招聘主体')).toBeNull()
    expect(screen.queryByText('岗位', { selector: 'label *' })).toBeNull()
    expect(screen.queryByText('分配来源', { selector: 'label *' })).toBeNull()
    expect(screen.queryByText('院校标签', { selector: 'label *' })).toBeNull()
    expect(screen.queryByText('最高学历', { selector: 'label *' })).toBeNull()
  })

  it('applies the selected level-1 department and narrows level-2 options', async () => {
    const user = userEvent.setup()
    renderAnalytics()
    await screen.findByText('招聘概览')

    const [primarySelect, secondarySelect] = screen.getAllByRole('combobox')
    await user.click(primarySelect)
    await user.click(await screen.findByText('科技中心'))
    await user.click(secondarySelect)
    expect(await screen.findByText('研发二部')).toBeTruthy()
    expect(screen.queryByText('市场二部')).toBeNull()
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('button', { name: /查询/ }))

    await waitFor(() => expect(fetchRecruitmentOverview).toHaveBeenCalledTimes(2))
    expect(fetchRecruitmentOverview).toHaveBeenLastCalledWith({ primary_department_id: 10 })
  })

  it('applies the selected level-2 department when querying', async () => {
    const user = userEvent.setup()
    renderAnalytics()
    await screen.findByText('招聘概览')

    await user.click(screen.getAllByRole('combobox')[1])
    await user.click(await screen.findByText('研发二部'))
    await user.click(screen.getByRole('button', { name: /查询/ }))
    await waitFor(() => expect(fetchRecruitmentOverview).toHaveBeenCalledTimes(2))
    expect(fetchRecruitmentOverview).toHaveBeenLastCalledWith({ department_id: 1 })
  })

  it('resets filters and reloads the default 30-day cohort', async () => {
    const user = userEvent.setup()
    renderAnalytics()
    await screen.findByRole('button', { name: '投递 12' })

    await user.click(screen.getByRole('button', { name: /重置/ }))
    await waitFor(() => expect(fetchRecruitmentOverview).toHaveBeenCalledTimes(2))
    expect(fetchRecruitmentOverview).toHaveBeenLastCalledWith({})
  })

  it('exports the result report with the filters already applied to the dashboard', async () => {
    const user = userEvent.setup()
    renderAnalytics()
    await screen.findByText('招聘概览')

    await user.click(screen.getAllByRole('combobox')[1])
    await user.click(await screen.findByText('研发二部'))
    await user.click(screen.getByRole('button', { name: /导出结果报表/ }))

    await waitFor(() => expect(exportResumeResultReport).toHaveBeenCalledWith({
      imported_after: '2026-06-17',
      imported_before: '2026-07-16',
    }))
  })

  it('includes the applied level-1 department in result report export', async () => {
    const user = userEvent.setup()
    fetchRecruitmentOverview
      .mockResolvedValueOnce({ data: payload })
      .mockResolvedValueOnce({
        data: {
          ...payload,
          filters: { ...payload.filters, primary_department_id: 10 },
        },
      })
    renderAnalytics()
    await screen.findByText('招聘概览')

    await user.click(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('科技中心'))
    await user.click(screen.getByRole('button', { name: /查询/ }))
    await waitFor(() => expect(fetchRecruitmentOverview).toHaveBeenCalledTimes(2))
    await user.click(screen.getByRole('button', { name: /导出结果报表/ }))

    await waitFor(() => expect(exportResumeResultReport).toHaveBeenCalledWith({
      imported_after: '2026-06-17',
      imported_before: '2026-07-16',
      primary_department_id: 10,
    }))
  })

  it('includes the applied level-2 department in result report export', async () => {
    const user = userEvent.setup()
    fetchRecruitmentOverview
      .mockResolvedValueOnce({ data: payload })
      .mockResolvedValueOnce({
        data: {
          ...payload,
          filters: { ...payload.filters, department_id: 1 },
        },
      })
    renderAnalytics()
    await screen.findByText('招聘概览')

    await user.click(screen.getAllByRole('combobox')[1])
    await user.click(await screen.findByText('研发二部'))
    await user.click(screen.getByRole('button', { name: /查询/ }))
    await waitFor(() => expect(fetchRecruitmentOverview).toHaveBeenCalledTimes(2))
    await user.click(screen.getByRole('button', { name: /导出结果报表/ }))

    await waitFor(() => expect(exportResumeResultReport).toHaveBeenCalledWith({
      imported_after: '2026-06-17',
      imported_before: '2026-07-16',
      department_id: 1,
    }))
  })

  it('drills a KPI into the resume library with the applied dashboard cohort', async () => {
    const user = userEvent.setup()
    renderAnalytics()
    await screen.findByText('招聘概览')

    await user.click(screen.getByRole('button', { name: /已生成分配/ }))

    const location = await screen.findByTestId('location')
    const [, query = ''] = location.textContent.split('?')
    const params = new URLSearchParams(query)
    expect(location.textContent.startsWith('/resumes?')).toBe(true)
    expect(params.get('analytics_dimension')).toBe('allocated')
    expect(params.get('analytics_date_from')).toBe('2026-06-17')
    expect(params.get('analytics_date_to')).toBe('2026-07-16')
    expect(params.get('analytics_title')).toBe('已生成分配')
  })

  it('drills a chart item with its category value', async () => {
    const user = userEvent.setup()
    renderAnalytics()
    await screen.findByText('招聘概览')

    await user.click(screen.getByRole('button', { name: /规则分配/ }))

    const location = await screen.findByTestId('location')
    const [, query = ''] = location.textContent.split('?')
    const params = new URLSearchParams(query)
    expect(params.get('analytics_dimension')).toBe('source')
    expect(JSON.parse(params.get('analytics_values'))).toEqual(['rule'])
    expect(params.get('analytics_title')).toBe('分配来源 · 规则分配')
  })

  it('drills a rejection reason with its stable reason code', async () => {
    const user = userEvent.setup()
    renderAnalytics()
    await screen.findByText('招聘概览')

    await user.click(screen.getByRole('button', { name: /专业背景不匹配/ }))

    const location = await screen.findByTestId('location')
    const [, query = ''] = location.textContent.split('?')
    const params = new URLSearchParams(query)
    expect(params.get('analytics_dimension')).toBe('rejection_reason')
    expect(JSON.parse(params.get('analytics_values'))).toEqual(['major_background_mismatch'])
  })

  it('hides result report export without resume.view', async () => {
    roleState.permissions = new Set()
    renderAnalytics()
    await screen.findByText('招聘概览')

    expect(screen.queryByRole('button', { name: /导出结果报表/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /已生成分配，查看对应简历/ })).toBeNull()
    expect(screen.queryByRole('link', { name: /导入至分配，查看参与统计的简历/ })).toBeNull()
    expect(screen.queryByText('点击数据项查看简历')).toBeNull()
    expect(screen.queryByText('点击柱形查看简历')).toBeNull()
    expect(screen.getByRole('button', { name: /规则分配/ }).disabled).toBe(true)
  })

  it('shows an API failure and allows retry', async () => {
    const user = userEvent.setup()
    fetchRecruitmentOverview.mockRejectedValueOnce({ response: { data: { detail: '统计失败' } } })
    renderAnalytics()

    expect(await screen.findByText('统计失败')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: /重\s*试/ }))
    expect(await screen.findByText('招聘概览')).toBeTruthy()
    expect(fetchRecruitmentOverview).toHaveBeenCalledTimes(2)
  })

  it('renders recorded AI errors instead of the healthy empty state', async () => {
    fetchRecruitmentOverview.mockResolvedValue({
      data: {
        ...payload,
        ai_error_distribution: [
          { key: 'llm_timeout', label: 'llm_timeout', count: 2 },
        ],
      },
    })
    renderAnalytics()

    expect(await screen.findByText('llm_timeout')).toBeTruthy()
    expect(screen.queryByText('暂无 AI 错误记录')).toBeNull()
  })
})
