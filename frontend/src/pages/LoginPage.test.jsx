import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import LoginPage from './LoginPage'
import { fetchW3OAuth2Status } from '../api/services'

const roleMocks = vi.hoisted(() => ({
  completeW3OAuth2Login: vi.fn(),
  loginWithDevToken: vi.fn(),
}))

vi.mock('../api/services', () => ({
  fetchW3OAuth2Status: vi.fn(),
}))

vi.mock('../contexts/roleState', () => ({
  useRole: () => roleMocks,
}))

function renderLogin(initialEntry = '/login', redirectToW3 = vi.fn()) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/login" element={<LoginPage redirectToW3={redirectToW3} />} />
        <Route path="/" element={<div>已登录首页</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('LoginPage W3 OAuth2', () => {
  beforeEach(() => {
    roleMocks.completeW3OAuth2Login.mockReset()
    roleMocks.completeW3OAuth2Login.mockResolvedValue({ username: 'E10001' })
    roleMocks.loginWithDevToken.mockReset()
    roleMocks.loginWithDevToken.mockResolvedValue({ username: 'DEV100' })
    fetchW3OAuth2Status.mockReset()
    fetchW3OAuth2Status.mockResolvedValue({
      data: {
        enabled: true,
        ready: true,
        debug_token_login_enabled: false,
        start_url: '/api/auth/w3/start/',
      },
    })
  })

  it('redirects directly to W3 without rendering a password form', async () => {
    const redirectToW3 = vi.fn()
    renderLogin('/login', redirectToW3)

    await waitFor(() => {
      expect(redirectToW3).toHaveBeenCalledWith('/api/auth/w3/start/')
    })
    expect(screen.queryByPlaceholderText('用户名')).toBeNull()
    expect(screen.queryByPlaceholderText('密码')).toBeNull()
  })

  it('shows a configuration error instead of falling back to password login', async () => {
    fetchW3OAuth2Status.mockResolvedValue({
      data: {
        enabled: true,
        ready: false,
        debug_token_login_enabled: false,
        start_url: null,
      },
    })
    const redirectToW3 = vi.fn()

    renderLogin('/login', redirectToW3)

    expect(await screen.findByText('W3 登录尚未正确配置，请联系管理员')).toBeTruthy()
    expect(screen.getByRole('button', { name: /重新检查 W3 登录/ })).toBeTruthy()
    expect(redirectToW3).not.toHaveBeenCalled()
    expect(screen.queryByPlaceholderText('用户名')).toBeNull()
    expect(screen.queryByPlaceholderText('密码')).toBeNull()
  })

  it('completes the one-time handoff and enters the application', async () => {
    renderLogin('/login?oauth2=success')

    expect(await screen.findByText('已登录首页')).toBeTruthy()
    expect(roleMocks.completeW3OAuth2Login).toHaveBeenCalledTimes(1)
  })

  it('shows a stable message for callback errors', async () => {
    const redirectToW3 = vi.fn()
    renderLogin('/login?oauth2_error=account_not_found', redirectToW3)

    await waitFor(() => {
      expect(
        screen.getByText('该 W3 工号和邮箱尚未绑定同一系统账号，请联系管理员'),
      ).toBeTruthy()
    })
    expect(roleMocks.completeW3OAuth2Login).not.toHaveBeenCalled()
    expect(redirectToW3).not.toHaveBeenCalled()
    expect(await screen.findByRole('button', { name: /重新发起 W3 登录/ })).toBeTruthy()
  })

  it('validates a development token before entering the application', async () => {
    const user = userEvent.setup()
    fetchW3OAuth2Status.mockResolvedValue({
      data: {
        enabled: false,
        ready: false,
        debug_token_login_enabled: true,
        start_url: null,
      },
    })

    renderLogin()

    const input = await screen.findByPlaceholderText('开发令牌')
    await user.type(input, ' dev-token ')
    await user.click(screen.getByRole('button', { name: '使用开发令牌登录' }))

    await waitFor(() => {
      expect(roleMocks.loginWithDevToken).toHaveBeenCalledWith('dev-token')
    })
    expect(await screen.findByText('已登录首页')).toBeTruthy()
  })

  it('keeps the development token out of login state when validation fails', async () => {
    const user = userEvent.setup()
    roleMocks.loginWithDevToken.mockRejectedValue({
      response: { data: { detail: '无效令牌' } },
    })
    fetchW3OAuth2Status.mockResolvedValue({
      data: {
        enabled: false,
        ready: false,
        debug_token_login_enabled: true,
        start_url: null,
      },
    })

    renderLogin()

    await user.type(await screen.findByPlaceholderText('开发令牌'), 'bad-token')
    await user.click(screen.getByRole('button', { name: '使用开发令牌登录' }))

    expect(await screen.findByText('无效令牌')).toBeTruthy()
    expect(screen.queryByText('已登录首页')).toBeNull()
  })
})
