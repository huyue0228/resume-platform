import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import PositionPoolsPage from './PositionPoolsPage'
import { actOnPoolMember, fetchPoolMember } from '../api/pools'

vi.mock('../api/pools', () => ({ fetchPoolMembers: vi.fn(), fetchPoolMember: vi.fn(), actOnPoolMember: vi.fn() }))
vi.mock('../api/services', () => ({ retryAgentDecision: vi.fn() }))
vi.mock('../contexts/roleState', () => ({ useRole: () => ({ hasPermission: () => true }) }))
vi.mock('../components/CandidateAnalysis', () => ({ default: () => null }))
vi.mock('../components/SmartDataTable', () => ({ default: ({ columns }) => <div>{columns.at(-1).render(null, { id: 7 })}</div> }))
const membership = { id: 7, revision: 4, status: 'pending_allocation', tags: [], assessment: { standard: { name: '机械工程师投递' }, pool: { name: '机械工程师池' }, tag_catalog: [] }, events: [] }

describe('PositionPoolsPage', () => {
  beforeEach(() => { vi.clearAllMocks(); fetchPoolMember.mockResolvedValue({ data: membership }); actOnPoolMember.mockResolvedValue({ data: { detail: '已入池，等待名额' } }) })
  it('requires evidence and records the revision when editing admitted tags', async () => {
    render(<PositionPoolsPage />)
    await userEvent.click(screen.getByRole('button', { name: '查看详情' }))
    await userEvent.click(await screen.findByRole('button', { name: '修订能力标签' }))
    expect(screen.getByRole('button', { name: /确.*定/ }).disabled).toBe(true)
    await userEvent.type(screen.getByRole('textbox', { name: '操作依据' }), '原文证明符合机械设计要求')
    await userEvent.click(screen.getByRole('button', { name: /确.*定/ }))
    await waitFor(() => expect(actOnPoolMember).toHaveBeenCalledWith(7, 'tags', { revision: 4, tags: [], note: '原文证明符合机械设计要求' }))
  })
  it('reallocates an admitted candidate without requesting another assessment', async () => {
    fetchPoolMember.mockResolvedValue({ data: { ...membership, status: 'pending_allocation' } })
    render(<PositionPoolsPage />)
    await userEvent.click(screen.getByRole('button', { name: '查看详情' }))
    await userEvent.click(await screen.findByRole('button', { name: '重新执行分配' }))
    await waitFor(() => expect(actOnPoolMember).toHaveBeenCalledWith(7, 'allocate', { revision: 4 }))
    expect(screen.queryByRole('button', { name: '复核通过并分配' })).toBeNull()
  })
})
