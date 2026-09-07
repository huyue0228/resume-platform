import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { RoleProvider } from './RoleContext'
import { useRole } from './roleState'
import { completeW3OAuth2Login, fetchMe, validateDevToken } from '../api/services'

vi.mock('../api/services', () => ({
  completeW3OAuth2Login: vi.fn(),
  fetchMe: vi.fn(),
  logout: vi.fn(),
  validateDevToken: vi.fn(),
}))

function Consumer() {
  const {
    completeW3OAuth2Login: complete,
    loginWithDevToken,
    user,
    dataScope,
    isContact,
    isSecondaryContact,
    isTertiaryContact,
  } = useRole()
  return (
    <>
      <button type="button" onClick={() => complete()}>
        完成 W3 登录
      </button>
      <button
        type="button"
        onClick={() => loginWithDevToken(' dev-token ').catch(() => {})}
      >
        使用开发令牌
      </button>
      <span>{user?.username || '未登录'}</span>
      <span data-testid="department-scope">
        {`${dataScope?.type || 'none'}:${isContact}:${isSecondaryContact}:${isTertiaryContact}`}
      </span>
    </>
  )
}

describe('RoleProvider W3 OAuth2', () => {
  beforeEach(() => {
    completeW3OAuth2Login.mockReset()
    fetchMe.mockReset()
    validateDevToken.mockReset()
    localStorage.clear()
  })

  it('stores the one-time project token and reuses the normal user context', async () => {
    const user = { username: 'E10001', permissions: ['resume.view'] }
    completeW3OAuth2Login.mockResolvedValue({
      data: { token: 'project-token', user },
    })
    fetchMe.mockResolvedValue({ data: user })
    render(
      <RoleProvider>
        <Consumer />
      </RoleProvider>,
    )

    await userEvent.click(screen.getByRole('button', { name: '完成 W3 登录' }))

    expect(await screen.findByText('E10001')).toBeTruthy()
    expect(localStorage.getItem('srf_token')).toBe('project-token')
    await waitFor(() => expect(fetchMe).toHaveBeenCalledTimes(1))
  })

  it('stores a development token only after /me validates it', async () => {
    const user = { username: 'DEV100', permissions: [] }
    validateDevToken.mockResolvedValue({ data: user })
    fetchMe.mockResolvedValue({ data: user })
    render(
      <RoleProvider>
        <Consumer />
      </RoleProvider>,
    )

    await userEvent.click(screen.getByRole('button', { name: '使用开发令牌' }))

    await waitFor(() => {
      expect(validateDevToken).toHaveBeenCalledWith('dev-token')
    })
    expect(localStorage.getItem('srf_token')).toBe('dev-token')
    expect(await screen.findByText('DEV100')).toBeTruthy()
  })

  it('does not store a development token when /me rejects it', async () => {
    validateDevToken.mockRejectedValue(new Error('invalid token'))
    render(
      <RoleProvider>
        <Consumer />
      </RoleProvider>,
    )

    await userEvent.click(screen.getByRole('button', { name: '使用开发令牌' }))

    await waitFor(() => {
      expect(validateDevToken).toHaveBeenCalledWith('dev-token')
    })
    expect(localStorage.getItem('srf_token')).toBeNull()
    expect(screen.getByText('未登录')).toBeTruthy()
  })

  it('derives the contact level from the department data scope', async () => {
    localStorage.setItem('srf_token', 'department-token')
    fetchMe.mockResolvedValue({
      data: {
        username: 'E20001',
        permissions: ['attempt.view_department'],
        contact: { id: 5, department: 20, department_level: 2 },
        data_scope: {
          type: 'department',
          department_id: 20,
          department_level: 2,
          include_descendants: true,
        },
      },
    })

    render(
      <RoleProvider>
        <Consumer />
      </RoleProvider>,
    )

    expect(await screen.findByText('department:true:true:false')).toBeTruthy()
  })
})
