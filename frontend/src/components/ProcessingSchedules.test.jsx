import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import ProcessingSchedules from './ProcessingSchedules'
import { cancelProcessingSchedule, fetchProcessingSchedules } from '../api/services'

vi.mock('../api/services', () => ({ fetchProcessingSchedules: vi.fn(), cancelProcessingSchedule: vi.fn() }))

const record = {
  id: 8, name: '夜间处理', scope_label: '触发时匹配：待处理', repeat: 'daily', status: 'active',
  run_at: '2026-09-11T18:30:00Z', next_run_at: '2026-09-12T18:30:00Z',
  last_run_id: 42, last_run_status: 'success', can_cancel: true,
}

beforeEach(() => {
  vi.clearAllMocks()
  fetchProcessingSchedules.mockResolvedValue({ data: { count: 1, results: [record] } })
  cancelProcessingSchedule.mockResolvedValue({ data: { status: 'cancelled' } })
})

it('shows scope and linked results, and persists cancellation through the API', async () => {
  const openRun = vi.fn()
  render(<ProcessingSchedules onOpenRun={openRun} />)
  expect(await screen.findByText('触发时匹配：待处理')).toBeTruthy()
  await userEvent.click(screen.getByRole('button', { name: '处理任务 #42 · 已完成' }))
  expect(openRun).toHaveBeenCalledWith(42)
  await userEvent.click(await screen.findByRole('button', { name: '更多操作' }))
  await userEvent.click(screen.getByRole('button', { name: '取消定时' }))
  fetchProcessingSchedules.mockResolvedValue({ data: { count: 1, results: [{ ...record, status: 'cancelled', can_cancel: false }] } })
  await userEvent.click(screen.getByRole('button', { name: '确认取消' }))
  await waitFor(() => expect(cancelProcessingSchedule).toHaveBeenCalledWith(8))
  expect(await screen.findByText('已取消')).toBeTruthy()
  expect(screen.queryByRole('button', { name: '取消定时' })).toBeNull()
})

it('keeps read-only schedules visible without cancellation controls', async () => {
  fetchProcessingSchedules.mockResolvedValue({ data: { count: 1, results: [{ ...record, can_cancel: false }] } })
  render(<ProcessingSchedules onOpenRun={vi.fn()} />)
  expect(await screen.findByText('夜间处理')).toBeTruthy()
  expect(screen.queryByRole('button', { name: '取消定时' })).toBeNull()
})
