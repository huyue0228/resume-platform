import { StrictMode } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import AuthenticationPage from './AuthenticationPage'
import { fetchW3OAuth2Status } from '../api/services'

const roleMocks = vi.hoisted(() => ({ completeW3OAuth2Login: vi.fn() }))
vi.mock('../api/services', () => ({ fetchW3OAuth2Status: vi.fn() }))
vi.mock('../contexts/roleState', () => ({ useRole: () => roleMocks }))

function renderAuth(entry = '/login', redirectToW3 = vi.fn()) {
  return render(<StrictMode><MemoryRouter initialEntries={[entry]}><Routes>
    <Route path="/login" element={<AuthenticationPage redirectToW3={redirectToW3} />} />
    <Route path="/" element={<div>工作空间</div>} />
  </Routes></MemoryRouter></StrictMode>)
}
function expectNoLoginActions() {
  expect(screen.queryByRole('button')).toBeNull()
  expect(screen.queryByRole('textbox')).toBeNull()
  expect(screen.queryByPlaceholderText('开发令牌')).toBeNull()
}

describe('W3 authentication gateway', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    roleMocks.completeW3OAuth2Login.mockResolvedValue({ username: 'E10001' })
    fetchW3OAuth2Status.mockResolvedValue({ data: { ready: true, start_url: '/api/auth/w3/start/' } })
  })
  it('automatically enters W3 without a login screen', async () => {
    const redirect = vi.fn()
    renderAuth('/login', redirect)
    await waitFor(() => expect(redirect).toHaveBeenCalledTimes(1))
    expect(redirect).toHaveBeenCalledWith('/api/auth/w3/start/')
    expectNoLoginActions()
  })
  it.each([false, true])('shows administrator guidance when W3 is unavailable, including debug=%s', async (debug) => {
    fetchW3OAuth2Status.mockResolvedValue({ data: { ready: false, debug_token_login_enabled: debug } })
    const redirect = vi.fn()
    renderAuth('/login', redirect)
    expect(await screen.findByText('暂时无法访问')).toBeTruthy()
    expect(screen.getByText(/请联系管理员/)).toBeTruthy()
    expect(redirect).not.toHaveBeenCalled()
    expectNoLoginActions()
  })
  it('completes the one-time handoff once under StrictMode', async () => {
    renderAuth('/login?oauth2=success')
    expect(await screen.findByText('工作空间')).toBeTruthy()
    expect(roleMocks.completeW3OAuth2Login).toHaveBeenCalledTimes(1)
    expect(fetchW3OAuth2Status).not.toHaveBeenCalled()
  })
  it.each(['account_not_found', 'state_invalid', 'token_exchange_failed', 'unknown'])('keeps callback failure %s stable without redirect or retry', async (code) => {
    const redirect = vi.fn()
    renderAuth(`/login?oauth2_error=${code}`, redirect)
    expect(await screen.findByText('暂时无法访问')).toBeTruthy()
    expect(screen.getByText(/请联系管理员/)).toBeTruthy()
    expect(fetchW3OAuth2Status).not.toHaveBeenCalled()
    expect(roleMocks.completeW3OAuth2Login).not.toHaveBeenCalled()
    expect(redirect).not.toHaveBeenCalled()
    expectNoLoginActions()
  })
  it('shows the same contact-only page for connection failures', async () => {
    fetchW3OAuth2Status.mockRejectedValue(new Error('offline'))
    renderAuth()
    expect(await screen.findByText(/无法连接 W3 认证服务/)).toBeTruthy()
    expectNoLoginActions()
  })
  it('does not replay a failed handoff or expose backend details', async () => {
    roleMocks.completeW3OAuth2Login.mockRejectedValue(new Error('internal detail'))
    renderAuth('/login?oauth2=success')
    expect(await screen.findByText(/无法完成 W3 认证/)).toBeTruthy()
    expect(roleMocks.completeW3OAuth2Login).toHaveBeenCalledTimes(1)
    expectNoLoginActions()
  })
})
