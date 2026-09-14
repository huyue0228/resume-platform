import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AllocationWorkspace, { AllocationTaskDetail } from './AllocationWorkspace'
import * as api from '../api/pools'

vi.mock('../contexts/roleState', () => ({ useRole: () => ({ hasPermission: () => true }) }))
vi.mock('../api/pools', () => ({ fetchAllocationScopes: vi.fn(), fetchAllocationSupply: vi.fn(), fetchAllocationTasks: vi.fn(), fetchAllocationTask: vi.fn(), createAllocationTask: vi.fn(), retryAllocationTask: vi.fn(), cancelAllocationTask: vi.fn(), updateAllocationScope: vi.fn(), updateDemandReception: vi.fn() }))

beforeEach(() => {
  vi.clearAllMocks()
  api.fetchAllocationScopes.mockResolvedValue({ data: { results: [{ id: 4, entity: 'YLS', pool_code: 'mechanical', revision: 2, allocation_mode: 'execute_v1' }] } })
  api.fetchAllocationSupply.mockResolvedValue({ data: { results: [{ demand_id: 8, position_name: '内部结构岗位', department_name: '产品部门', is_public: false, headcount: 0, reception_state: 'receiving', revision: 3, recent_supply_count: 0 }] } })
  api.fetchAllocationTasks.mockResolvedValue({ data: { results: [] } })
  api.fetchAllocationTask.mockResolvedValue({ data: { id: 12, status: 'completed', mode: 'simulate', progress: { assigned: 1, waiting: 2 }, plans: [] } })
  api.createAllocationTask.mockResolvedValue({ data: { task_id: 12 } })
})

describe('allocation workflow', () => {
  it('makes simulation status explicit', async () => {
    render(<AllocationTaskDetail taskId={12} />)
    expect(await screen.findByText('试算结果不代表已经分配')).toBeTruthy()
    expect(screen.getByText('已拟分配 1，等待 2')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '取消未执行任务' })).toBeNull()
  })

  it('submits a simulation for the selected pool', async () => {
    render(<AllocationWorkspace />)
    expect(await screen.findByText('内部结构岗位')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: '试算待分配候选人' }))
    await waitFor(() => expect(api.createAllocationTask).toHaveBeenCalledWith(expect.objectContaining({ scope_id: 4, mode: 'simulate', idempotency_key: expect.any(String) })))
    expect(await screen.findByText('试算结果不代表已经分配')).toBeTruthy()
  })

  it('requires a reason and the current demand revision', async () => {
    api.updateDemandReception.mockResolvedValue({ data: {} })
    render(<AllocationWorkspace />)
    await userEvent.click(await screen.findByRole('button', { name: '调整接收状态' }))
    expect((await screen.findByRole('button', { name: /保\s*存/ })).disabled).toBe(true)
    await userEvent.type(screen.getByRole('textbox', { name: '调整原因' }), '部门本周暂停接收')
    await userEvent.click(screen.getByRole('combobox', { name: '目标状态' }))
    await userEvent.click(await screen.findByText('暂停接收'))
    await userEvent.click(await screen.findByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(api.updateDemandReception).toHaveBeenCalledWith(8, { reception_state: 'paused', expected_revision: 3, reason: '部门本周暂停接收' }))
  })

  it('opens the new task after an independent retry', async () => {
    api.fetchAllocationTask.mockImplementation((id) => Promise.resolve({ data: { id, status: id === 12 ? 'failed' : 'completed', mode: 'execute', progress: {}, plans: [] } }))
    api.retryAllocationTask.mockResolvedValue({ data: { task_id: 13 } })
    const changed = vi.fn()
    render(<AllocationTaskDetail taskId={12} onTaskChange={changed} />)
    await userEvent.click(await screen.findByRole('button', { name: '用新快照重试' }))
    await waitFor(() => expect(changed).toHaveBeenCalledWith(13))
    expect(api.retryAllocationTask).toHaveBeenCalledWith(12, { idempotency_key: expect.any(String) })
    await waitFor(() => expect(api.fetchAllocationTask).toHaveBeenCalledWith(13))
  })
})
