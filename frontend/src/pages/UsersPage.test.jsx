import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UsersPage from './UsersPage'
import { fetchPermissionTree, fetchRoles, updateRole } from '../api/services'

const roleRecord = vi.hoisted(() => ({
  id: 1,
  name: 'HR',
  permissions: ['resume.view'],
}))

const protectedUserRecord = vi.hoisted(() => ({
  id: 12358,
  username: '012358',
  email: 'huyue2@ueascend.com',
  is_active: true,
  is_protected: true,
  roles: ['管理员'],
}))

const apiMocks = vi.hoisted(() => ({
  createRole: vi.fn(),
  createUser: vi.fn(),
  deleteUser: vi.fn(),
  fetchContacts: vi.fn(),
  fetchPermissionTree: vi.fn(),
  fetchRoles: vi.fn(),
  fetchUsers: vi.fn(),
  updateRole: vi.fn(),
  updateUser: vi.fn(),
}))
const roleMocks = vi.hoisted(() => ({
  refreshMe: vi.fn(),
}))

vi.mock('../api/services', () => apiMocks)
vi.mock('../contexts/roleState', () => ({
  useRole: () => roleMocks,
}))

vi.mock('@ant-design/pro-components', () => ({
  PageContainer: ({ children }) => <div>{children}</div>,
  ModalForm: ({ open, title, children }) => open ? (
    <div role="dialog" aria-label={title}>{children}</div>
  ) : null,
  ProFormSelect: () => null,
  ProFormSwitch: () => null,
  ProFormText: ({ label }) => <span>{label}</span>,
}))

vi.mock('antd', async () => {
  const actual = await vi.importActual('antd')
  return {
    ...actual,
    message: { success: vi.fn() },
    Modal: ({ open, title, children, footer }) => open ? (
      <div role="dialog" aria-label={title}>
        <div>{title}</div>
        {children}
        <div>{footer}</div>
      </div>
    ) : null,
    Tree: ({ treeData, checkedKeys = [], onCheck }) => (
      <div>
        {treeData.flatMap((module) => module.children || []).map((item) => (
          <label key={item.key}>
            <input
              type="checkbox"
              checked={checkedKeys.includes(item.key)}
              onChange={(event) => onCheck(
                event.target.checked
                  ? [...checkedKeys, item.key]
                  : checkedKeys.filter((key) => key !== item.key),
              )}
            />
            {item.title}
          </label>
        ))}
      </div>
    ),
  }
})

vi.mock('../components/SmartDataTable', () => ({
  default: ({ tableId, columns, toolBarRender }) => {
    if (tableId === 'users') {
      const actionColumn = columns.find((column) => column.title === '操作')
      return (
        <div data-testid="table-users">
          {toolBarRender?.()}
          <span>{protectedUserRecord.username}</span>
          {actionColumn.render(null, protectedUserRecord)}
        </div>
      )
    }
    const actionColumn = columns.find((column) => column.title === '操作')
    return (
      <div data-testid="table-roles">
        <span>{roleRecord.name}</span>
        {actionColumn.render(null, roleRecord)}
      </div>
    )
  },
}))

describe('UsersPage role permissions', () => {
  beforeEach(() => {
    apiMocks.fetchRoles.mockReset()
    apiMocks.fetchRoles.mockResolvedValue({ data: { results: [roleRecord] } })
    apiMocks.fetchContacts.mockReset()
    apiMocks.fetchContacts.mockResolvedValue({ data: { results: [] } })
    apiMocks.fetchPermissionTree.mockReset()
    apiMocks.fetchPermissionTree.mockResolvedValue({
      data: [
        {
          code: 'resume',
          name: '简历库',
          children: [
            { code: 'resume.view', name: '查看简历' },
            { code: 'resume.export', name: '导出简历' },
          ],
        },
      ],
    })
    apiMocks.updateRole.mockReset()
    apiMocks.updateRole.mockResolvedValue({
      data: { ...roleRecord, permissions: ['resume.view', 'resume.export'] },
    })
    roleMocks.refreshMe.mockReset()
    roleMocks.refreshMe.mockResolvedValue({ permissions: ['resume.view', 'resume.export'] })
  })

  it('merges permission configuration into the role action', async () => {
    const user = userEvent.setup()
    render(<UsersPage />)

    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual([
      '用户管理',
      '角色管理',
    ])
    expect(screen.queryByRole('tab', { name: '权限配置' })).toBeNull()

    await user.click(screen.getByRole('tab', { name: '角色管理' }))
    await user.click(screen.getByText('配置权限'))

    expect(await screen.findByText('配置权限：HR')).toBeTruthy()
    const viewPermission = screen.getByRole('checkbox', {
      name: '查看简历（resume.view）',
    })
    const exportPermission = screen.getByRole('checkbox', {
      name: '导出简历（resume.export）',
    })
    expect(viewPermission.checked).toBe(true)
    expect(exportPermission.checked).toBe(false)

    await user.click(exportPermission)
    await user.click(screen.getByRole('button', { name: '保存权限' }))

    await waitFor(() => expect(updateRole).toHaveBeenCalledWith(1, {
      permission_codes: ['resume.view', 'resume.export'],
    }))
    await waitFor(() => expect(screen.queryByRole('dialog', {
      name: '配置权限：HR',
    })).toBeNull())
    expect(fetchRoles).toHaveBeenCalledTimes(2)
    expect(fetchPermissionTree).toHaveBeenCalledTimes(2)
    expect(roleMocks.refreshMe).toHaveBeenCalledTimes(1)
  })

  it('does not expose mutation actions for the protected administrator', async () => {
    render(<UsersPage />)

    expect(await screen.findByText('012358')).toBeTruthy()
    expect(screen.getByText('内置保护')).toBeTruthy()
    expect(screen.queryByText('编辑')).toBeNull()
    expect(screen.queryByText('停用')).toBeNull()
    expect(screen.queryByText('删除')).toBeNull()
  })

  it('does not render initial-password or reset-password controls', async () => {
    const user = userEvent.setup()
    render(<UsersPage />)

    await user.click(await screen.findByRole('button', { name: '新增用户' }))

    expect(await screen.findByRole('dialog', { name: '新增用户' })).toBeTruthy()
    expect(screen.queryByText('密码')).toBeNull()
    expect(screen.queryByText('重置密码')).toBeNull()
    expect(screen.queryByPlaceholderText('请输入初始密码')).toBeNull()
  })
})
