import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProcessingTaskCenter from './ProcessingTaskCenter'

const fetchPipelineRuns = vi.hoisted(() => vi.fn())

vi.mock('../api/services', () => ({
  fetchPipelineRuns,
  cancelPipelineRun: vi.fn(),
}))

function CurrentLocation() {
  const location = useLocation()
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>
}

describe('ProcessingTaskCenter', () => {
  beforeEach(() => {
    fetchPipelineRuns.mockReset()
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
      <MemoryRouter>
        <ProcessingTaskCenter />
        <CurrentLocation />
      </MemoryRouter>,
    )

    await userEvent.click(await screen.findByRole('button', { name: '展开成功子项' }))
    const reviewButton = await screen.findByRole('button', {
      name: '筛选本任务待复核简历 1 名',
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

    render(<MemoryRouter><ProcessingTaskCenter /></MemoryRouter>)
    const expandButtons = await screen.findAllByRole('button', { name: '展开成功子项' })
    await userEvent.click(expandButtons[0])
    await userEvent.click(expandButtons[1])

    expect(screen.getAllByRole('button', { name: '收起成功子项' })).toHaveLength(2)
    expect(screen.getAllByText(/Agent 子项合计可小于处理完成总数/)).toHaveLength(2)
    const disabledDispatch = screen.getAllByRole('button', {
      name: '筛选本任务待下发简历 0 名',
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
      <MemoryRouter>
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
      <MemoryRouter>
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
