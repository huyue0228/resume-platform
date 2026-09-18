import { render, screen } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ResumeWorkspace from './ResumeWorkspace'
import { allRoute } from '../layouts/menuRoutes'

const permissions = vi.hoisted(() => ({ pool: true }))
vi.mock('../contexts/roleState', () => ({ useRole: () => ({ hasPermission: () => permissions.pool }) }))
vi.mock('./ResumesPage', () => ({ default: () => <TestPage /> }))
function TestPage() {
  const location = useLocation()
  return <><p>简历库内容</p><output>{location.search}</output></>
}

describe('ResumeWorkspace', () => {
  beforeEach(() => { permissions.pool = true })
  it('keeps a single resume list and preserves task drilldown scope', async () => {
    const menu = allRoute.routes.flatMap((route) => route.routes || [route])
    expect(menu.filter((route) => route.path === '/resumes')).toHaveLength(1)
    expect(menu.some((route) => route.path === '/position-pools')).toBe(false)
    render(<MemoryRouter initialEntries={['/resumes?processing_run_id=7&processing_result=failed']}><ResumeWorkspace /></MemoryRouter>)
    await screen.findByText('简历库内容')
    expect(screen.queryByRole('tab')).toBeNull()
    expect(screen.getByRole('status').textContent).toBe('?processing_run_id=7&processing_result=failed')
  })
  it('turns an old pool link into a resume filter without dropping its scope', async () => {
    render(<MemoryRouter initialEntries={['/resumes?tab=pool&processing_run_id=7']}><ResumeWorkspace /></MemoryRouter>)
    await screen.findByText('简历库内容')
    const query = new URLSearchParams(screen.getByRole('status').textContent)
    expect(query.get('pool_status')).toBe('admitted')
    expect(query.get('processing_run_id')).toBe('7')
    expect(query.has('tab')).toBe(false)
  })
  it('does not add a restricted pool filter for a department viewer', async () => {
    permissions.pool = false
    render(<MemoryRouter initialEntries={['/resumes?tab=pool']}><ResumeWorkspace /></MemoryRouter>)
    await screen.findByText('简历库内容')
    expect(screen.getByRole('status').textContent).toBe('')
  })
})
