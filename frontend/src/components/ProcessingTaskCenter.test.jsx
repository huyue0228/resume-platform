vi.mock('./checkProcessingConfiguration', () => ({ confirmProcessingConfiguration: vi.fn().mockResolvedValue(true) }))
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProcessingTaskCenter from './ProcessingTaskCenter'

const fetchPipelineRuns = vi.hoisted(() => vi.fn())
const fetchPipelineRun = vi.hoisted(() => vi.fn())
const querySchedules = vi.hoisted(() => vi.fn().mockResolvedValue({ data: { count: 0, results: [] } }))
const canCreate = vi.hoisted(() => ({ value: false }))
const runPipeline = vi.hoisted(() => vi.fn())
vi.mock('../contexts/roleState', () => ({ useRole: () => ({ hasPermission: () => canCreate.value }) }))

vi.mock('../api/services', () => ({
  fetchPipelineRuns,
  fetchPipelineRun,
  fetchProcessingSchedules: querySchedules,
  runPipeline,
  cancelProcessingSchedule: vi.fn(),
  cancelPipelineRun: vi.fn(),
}))

function CurrentLocation() {
  const location = useLocation()
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>
}

function taskWithNodes(current = 'queued', status = 'pending') {
  const plan = [
    ['queued', '等待后台处理'], ['initialize', '检查处理服务'], ['preparing', '准备候选人材料'],
    ['step1', '整理简历与志愿'], ['step2', '核验学历与院校'], ['step3', '匹配可选岗位'],
    ['step4', 'AI 分析与保存结果'], ['finalize', '汇总处理结果'],
  ]
  const position = plan.findIndex(([step]) => step === current)
  return {
    id: 42, step: 'all', mode: 'ai', status, current_stage: current,
    total_count: 100, processed_count: 0, created_at: '2026-09-09T10:00:00Z',
    stages: plan.map(([step, label], index) => ({
      step, label, sequence: index + 1,
      status: index < position ? 'success' : index === position ? 'running' : 'pending',
      total_count: ['queued', 'initialize', 'finalize'].includes(step) ? 1 : 100,
      processed_count: index < position ? 1 : 0,
      started_at: index <= position ? '2026-09-09T10:00:00Z' : null,
      elapsed_seconds: index <= position ? 6 : null,
    })),
  }
}

function showTask(run) {
  fetchPipelineRuns.mockResolvedValue({ data: { results: [run] } })
  return render(<MemoryRouter initialEntries={['/processing-tasks?view=card']}><ProcessingTaskCenter /></MemoryRouter>)
}

describe('ProcessingTaskCenter', () => {
  beforeEach(() => {
    fetchPipelineRuns.mockReset()
    fetchPipelineRun.mockReset()
    querySchedules.mockClear()
    canCreate.value = false
  })

  it('restores query filters from the URL and uses totals outside the visible page', async () => {
    fetchPipelineRuns.mockResolvedValue({ data: { results: [], count: 25, summary: { total: 80, active: 35, attention: 25, finished: 45 } } })
    render(<MemoryRouter initialEntries={['/processing-tasks?search=Night&state=attention&source=schedule&page=2']}><ProcessingTaskCenter /><CurrentLocation /></MemoryRouter>)
    await waitFor(() => expect(fetchPipelineRuns).toHaveBeenCalledWith(expect.objectContaining({ search: 'Night', source: 'schedule', state: 'attention', page: '2', include_summary: 'true' }), expect.anything()))
    expect(screen.getByRole('button', { name: /进行中.*35/ })).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: /进行中.*35/ }))
    await waitFor(() => expect(screen.getByTestId('location').textContent).toContain('state=active'))
    expect(screen.getByTestId('location').textContent).not.toContain('page=2')
    expect(screen.getByTestId('location').textContent).toContain('search=Night')
  })

  it('queries the complete history of a schedule and opens an older execution by URL', async () => {
    querySchedules.mockResolvedValueOnce({ data: { count: 1, results: [{ id: 9, name: '每日计划', scope_label: '待处理', repeat: 'daily', status: 'active' }] } })
    fetchPipelineRuns.mockResolvedValue({ data: { results: [taskWithNodes()], count: 1 } })
    render(<MemoryRouter initialEntries={['/processing-tasks?tab=schedules']}><ProcessingTaskCenter /><CurrentLocation /></MemoryRouter>)
    await userEvent.click(await screen.findByRole('button', { name: '更多操作' }))
    await userEvent.click(await screen.findByRole('button', { name: '执行历史' }))
    await waitFor(() => expect(fetchPipelineRuns).toHaveBeenCalledWith(expect.objectContaining({ schedule_id: '9' }), expect.anything()))
    expect(screen.getByText('计划「每日计划」的执行历史')).toBeTruthy()
    fetchPipelineRun.mockResolvedValue({ data: taskWithNodes() })
    await userEvent.click(await screen.findByRole('button', { name: '候选人完整处理' }))
    expect(await screen.findByText('执行详情 #42')).toBeTruthy()
    expect(screen.getByTestId('location').textContent).toContain('run_id=42')
    expect(screen.getByTestId('location').textContent).toContain('schedule_id=9')
  })

  it('creates a named immediate task from the same processing dialog', async () => {
    canCreate.value = true
    fetchPipelineRuns.mockResolvedValue({ data: { results: [], count: 0 } })
    fetchPipelineRun.mockResolvedValue({ data: taskWithNodes() })
    runPipeline.mockResolvedValue({ data: { processing_runs: [{ id: 42 }] } })
    render(<MemoryRouter><ProcessingTaskCenter /></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: '处理简历' }))
    await userEvent.type(screen.getByLabelText('任务名称'), '本周简历处理')
    expect(screen.getByRole('radio', { name: '立即执行' }).checked).toBe(true)
    expect(screen.getByRole('radio', { name: '指定时间' })).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: '开始处理' }))
    await waitFor(() => expect(runPipeline).toHaveBeenCalledWith({ name: '本周简历处理', step: 'step2', scope: { system_statuses: ['raw'] } }))
  })

  it('shows the entire ordered plan as soon as the task is queued', async () => {
    const run = taskWithNodes()
    showTask(run)
    const region = await screen.findByRole('region', { name: '任务 42 的执行节点' })
    const nodes = within(region).getAllByRole('listitem')
    expect(nodes).toHaveLength(8)
    nodes.forEach((node, index) => expect(node.textContent).toContain(run.stages[index].label))
    expect(nodes[0].getAttribute('aria-current')).toBe('step')
    expect(within(region).getAllByText('待执行')).toHaveLength(7)
    expect(within(region).getByText('下一步：检查处理服务')).toBeTruthy()
    expect(screen.getByText('0%')).toBeTruthy()
  })

  it('shows current material preparation counts, elapsed time and the next node in both views', async () => {
    const run = taskWithNodes('preparing', 'running')
    run.stages[2].processed_count = 12
    showTask(run)
    let region = await screen.findByRole('region', { name: '任务 42 的执行节点' })
    expect(screen.getByText('当前：准备候选人材料')).toBeTruthy()
    expect(within(region).getByText('已处理 12 / 100')).toBeTruthy()
    expect(within(region).getAllByRole('listitem')[2].textContent).toContain('耗时 6秒')
    expect(within(region).getByText('下一步：整理简历与志愿')).toBeTruthy()
    await userEvent.click(screen.getByText('列表'))
    expect(screen.queryByRole('region', { name: '任务 42 的执行节点' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '展开任务 42 的执行节点' }))
    region = await screen.findByRole('region', { name: '任务 42 的执行节点' })
    expect(within(region).getAllByRole('listitem')).toHaveLength(8)
    expect(within(region).getByText('已处理 12 / 100')).toBeTruthy()
  })

  it.each(['failed', 'cancelled'])('keeps %s tasks below 100 percent and marks future nodes unexecuted', async (status) => {
    const run = taskWithNodes('preparing', status)
    run.processed_count = 100
    run.stages[2].status = status
    run.stages.slice(3).forEach((stage) => { stage.status = 'skipped' })
    run.stages[2].error = status === 'failed' ? '准备候选人材料失败' : ''
    showTask(run)
    const region = await screen.findByRole('region', { name: '任务 42 的执行节点' })
    expect(within(region).getByText('任务已停止')).toBeTruthy()
    expect(within(region).getAllByText('未执行')).toHaveLength(5)
    expect(within(region).queryByText(/下一步/)).toBeNull()
    expect(screen.getByText('25%')).toBeTruthy()
    if (status === 'failed') expect(within(region).getByText('准备候选人材料失败')).toBeTruthy()
  })

  it('shows actual candidate activity during AI analysis', async () => {
    const run = taskWithNodes('step4', 'running')
    run.activity = { processing: 3, queued: 12, waiting_conflict: 1 }
    showTask(run)
    expect(await screen.findByText(/正在处理 3 名 · 待处理 12 名/)).toBeTruthy()
    expect(screen.getByText(/等待其他任务释放 1 名/)).toBeTruthy()
    expect(screen.getByText('下一步：汇总处理结果')).toBeTruthy()
  })

  it('opens the corresponding AI result in the candidate list', async () => {
    fetchPipelineRuns.mockResolvedValue({
      data: {
        results: [{
          id: 18,
          step: 'step2',
          mode: 'ai',
          status: 'success',
          created_at: '2026-07-14T10:00:00Z',
          elapsed_seconds: 12,
          total_count: 2,
          processed_count: 2,
          success_count: 2,
          completed_count: 2,
          review_count: 1,
          dispatch_count: 1,
        }],
      },
    })

    render(
      <MemoryRouter initialEntries={['/processing-tasks?view=card']}>
        <ProcessingTaskCenter />
        <CurrentLocation />
      </MemoryRouter>,
    )

    await userEvent.click(await screen.findByRole('button', { name: '展开成功子项' }))
    const reviewButton = await screen.findByRole('button', {
      name: '筛选本任务历史复核简历 1 名',
    })
    await userEvent.click(reviewButton)

    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe(
      '/resumes?processing_run_id=18&processing_result=review',
    ))
  })

  it('allows multiple AI task cards to expand and keeps zero subitems disabled', async () => {
    fetchPipelineRuns.mockResolvedValue({
      data: {
        results: [18, 19].map((id) => ({
          id,
          step: 'step2',
          mode: 'ai',
          status: 'success',
          created_at: '2026-07-14T10:00:00Z',
          elapsed_seconds: 12,
          total_count: 2,
          processed_count: 2,
          success_count: 2,
          completed_count: 2,
          review_count: 1,
          dispatch_count: 0,
          archive_count: 0,
        })),
      },
    })

    render(<MemoryRouter initialEntries={['/processing-tasks?view=card']}><ProcessingTaskCenter /></MemoryRouter>)
    const expandButtons = await screen.findAllByRole('button', { name: '展开成功子项' })
    await userEvent.click(expandButtons[0])
    await userEvent.click(expandButtons[1])

    expect(screen.getAllByRole('button', { name: '收起成功子项' })).toHaveLength(2)
    expect(screen.getAllByText(/Agent 子项合计可小于处理完成总数/)).toHaveLength(2)
    const disabledDispatch = screen.getAllByRole('button', {
      name: '筛选本任务达标入池简历 0 名',
    })
    expect(disabledDispatch).toHaveLength(2)
    disabledDispatch.forEach((button) => expect(button.disabled).toBe(true))
  })

  it('opens nonzero Rule results and disables zero main results', async () => {
    fetchPipelineRuns.mockResolvedValue({
      data: {
        results: [{
          id: 20,
          step: 'step2',
          mode: 'rule',
          status: 'success',
          created_at: '2026-07-14T10:00:00Z',
          elapsed_seconds: 3,
          total_count: 2,
          processed_count: 2,
          success_count: 1,
          completed_count: 1,
          failed_count: 0,
          skipped_count: 1,
          cancelled_count: 0,
        }],
      },
    })

    render(
      <MemoryRouter initialEntries={['/processing-tasks?view=card']}>
        <ProcessingTaskCenter />
        <CurrentLocation />
      </MemoryRouter>,
    )
    const failedButton = await screen.findByRole('button', {
      name: '筛选本任务失败简历 0 名',
    })
    expect(failedButton.disabled).toBe(true)
    await userEvent.click(screen.getByRole('button', {
      name: '筛选本任务跳过简历 1 名',
    }))

    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe(
      '/resumes?processing_run_id=20&processing_result=skipped',
    ))
  })

  it('switches between card and list views while keeping result navigation', async () => {
    fetchPipelineRuns.mockResolvedValue({
      data: {
        results: [{
          id: 21,
          step: 'step2',
          mode: 'rule',
          status: 'success',
          created_at: '2026-07-14T10:00:00Z',
          elapsed_seconds: 5,
          total_count: 1,
          processed_count: 1,
          completed_count: 1,
        }],
      },
    })

    render(
      <MemoryRouter initialEntries={['/processing-tasks?view=card']}>
        <ProcessingTaskCenter />
        <CurrentLocation />
      </MemoryRouter>,
    )

    expect(await screen.findByText('任务耗时')).toBeTruthy()
    expect(screen.queryByRole('columnheader', { name: '任务' })).toBeNull()

    await userEvent.click(screen.getByText('列表'))
    expect(await screen.findByRole('columnheader', { name: '任务' })).toBeTruthy()
    expect(screen.getByRole('columnheader', { name: '处理结果' })).toBeTruthy()

    await userEvent.click(screen.getByRole('button', {
      name: '筛选本任务处理完成简历 1 名',
    }))
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe(
      '/resumes?processing_run_id=21&processing_result=completed',
    ))

    await userEvent.click(screen.getByText('卡片'))
    expect(await screen.findByText('处理进度')).toBeTruthy()
    expect(screen.queryByRole('columnheader', { name: '任务' })).toBeNull()
  })
})
