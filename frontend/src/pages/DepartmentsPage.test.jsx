import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import DepartmentsPage from './DepartmentsPage'

const mocks = vi.hoisted(() => ({
  createContact: vi.fn(),
  deleteContact: vi.fn(),
  fetchDepartments: vi.fn(),
  updateContact: vi.fn(),
  hasPermission: vi.fn(),
  reload: vi.fn(),
  reloadOptions: vi.fn(),
  success: vi.fn(),
}))

vi.mock('@ant-design/pro-components', () => ({
  PageContainer: ({ children }) => <div>{children}</div>,
  ModalForm: ({ children, onFinish, open }) => (
    <div>
      {children}
      {open && (
        <button
          type="button"
          onClick={() => onFinish({
            name: '新增接口人',
            employee_no: 'E1001',
            email: 'E1001@EXAMPLE.COM',
            department: 12,
            can_delegate: true,
            is_active: true,
          })}
        >
          保存接口人
        </button>
      )}
    </div>
  ),
  ProFormSelect: () => null,
  ProFormSwitch: () => null,
  ProFormText: () => null,
}))

vi.mock('antd', () => ({
  Button: ({ children, onClick }) => <button type="button" onClick={onClick}>{children}</button>,
  message: { success: mocks.success, error: vi.fn() },
  Popconfirm: ({ children, onConfirm }) => (
    <div>
      {children}
      <button type="button" onClick={onConfirm}>确认删除</button>
    </div>
  ),
  Space: ({ children }) => <div>{children}</div>,
  Tag: ({ children }) => <span>{children}</span>,
}))

vi.mock('../contexts/roleState', () => ({
  useRole: () => ({ hasPermission: mocks.hasPermission }),
}))

vi.mock('../api/services', () => ({
  createContact: mocks.createContact,
  deleteContact: mocks.deleteContact,
  fetchContactFilterOptions: vi.fn(),
  fetchContacts: vi.fn(),
  fetchDepartments: mocks.fetchDepartments,
  updateContact: mocks.updateContact,
}))

vi.mock('../components/ImportButton', () => ({ default: () => null }))

vi.mock('../components/SmartDataTable', () => ({
  default: ({ actionRef, columns, toolBarRender }) => {
    actionRef.current = {
      reload: mocks.reload,
      reloadOptions: mocks.reloadOptions,
    }
    const actionColumn = columns.find((column) => column.valueType === 'option')
    return (
      <div>
        {toolBarRender?.()}
        {actionColumn?.render(null, {
          id: 7,
          name: '原接口人',
          employee_no: 'E0007',
          email: 'e0007@example.com',
          department: 11,
          can_delegate: true,
          is_active: true,
        })}
      </div>
    )
  },
}))

describe('DepartmentsPage', () => {
  beforeEach(() => {
    mocks.createContact.mockReset()
    mocks.createContact.mockResolvedValue({})
    mocks.deleteContact.mockReset()
    mocks.deleteContact.mockResolvedValue({})
    mocks.fetchDepartments.mockReset()
    mocks.fetchDepartments.mockResolvedValue({
      data: {
        results: [
          { id: 11, name: '二级部门', level: 2 },
          { id: 12, name: '三级部门', level: 3 },
        ],
      },
    })
    mocks.updateContact.mockReset()
    mocks.updateContact.mockResolvedValue({})
    mocks.hasPermission.mockReset()
    mocks.hasPermission.mockImplementation((code) => code === 'department.manage')
    mocks.reload.mockReset()
    mocks.reloadOptions.mockReset()
    mocks.success.mockReset()
  })

  it('refreshes table data and filter options after deleting a contact', async () => {
    render(<DepartmentsPage />)

    await userEvent.click(screen.getByRole('button', { name: '确认删除' }))

    await waitFor(() => expect(mocks.deleteContact).toHaveBeenCalledWith(7))
    expect(mocks.reload).toHaveBeenCalledOnce()
    expect(mocks.reloadOptions).toHaveBeenCalledOnce()
    expect(mocks.success).toHaveBeenCalledWith('已删除')
  })

  it('keeps the WeLink notification switch hidden', () => {
    render(<DepartmentsPage />)

    expect(screen.queryByRole('switch', { name: 'WeLink 通知' })).toBeNull()
  })

  it('creates a contact and derives its level from the selected department', async () => {
    render(<DepartmentsPage />)

    await waitFor(() => expect(mocks.fetchDepartments).toHaveBeenCalledWith({ page_size: 500 }))
    await userEvent.click(screen.getByRole('button', { name: '新增接口人' }))
    await userEvent.click(screen.getByRole('button', { name: '保存接口人' }))

    await waitFor(() => expect(mocks.createContact).toHaveBeenCalledWith({
      name: '新增接口人',
      employee_no: 'E1001',
      email: 'e1001@example.com',
      department: 12,
      contact_level: 'tertiary',
      can_delegate: false,
      is_active: true,
    }))
    expect(mocks.reload).toHaveBeenCalled()
    expect(mocks.reloadOptions).toHaveBeenCalled()
  })

  it('updates an existing contact from the row action', async () => {
    render(<DepartmentsPage />)

    await waitFor(() => expect(mocks.fetchDepartments).toHaveBeenCalled())
    await userEvent.click(screen.getByText('编辑'))
    await userEvent.click(screen.getByRole('button', { name: '保存接口人' }))

    await waitFor(() => expect(mocks.updateContact).toHaveBeenCalledWith(7, {
      name: '新增接口人',
      employee_no: 'E1001',
      email: 'e1001@example.com',
      department: 12,
      contact_level: 'tertiary',
      can_delegate: false,
      is_active: true,
    }))
  })

  it('still hides the WeLink switch without contact management permission', () => {
    mocks.hasPermission.mockReturnValue(false)

    render(<DepartmentsPage />)

    expect(screen.queryByRole('switch', { name: 'WeLink 通知' })).toBeNull()
  })
})
