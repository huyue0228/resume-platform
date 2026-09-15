import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SchoolsPage from './SchoolsPage'

const mocks = vi.hoisted(() => ({
  createSchool: vi.fn(),
  columns: [],
  hasPermission: vi.fn(),
  reload: vi.fn(),
  reloadOptions: vi.fn(),
  updateSchool: vi.fn(),
}))

vi.mock('@ant-design/pro-components', () => ({
  PageContainer: ({ children }) => <div>{children}</div>,
  DrawerForm: ({ children, onFinish, open }) => (
    <div>
      {children}
      {open && (
        <button
          type="button"
          onClick={() => onFinish({
            name: '测试大学',
            school_tag: 5,
          })}
        >
          保存院校
        </button>
      )}
    </div>
  ),
  ProFormSelect: () => null,
  ProFormText: () => null,
}))

vi.mock('antd', () => ({
  Button: ({ children, onClick }) => <button type="button" onClick={onClick}>{children}</button>,
  Space: ({ children }) => <div>{children}</div>,
  message: { success: vi.fn() },
}))

vi.mock('../contexts/roleState', () => ({
  useRole: () => ({ hasPermission: mocks.hasPermission }),
}))

vi.mock('../api/services', () => ({
  createSchool: mocks.createSchool,
  fetchSchoolFilterOptions: vi.fn().mockResolvedValue({ data: { school_tag: [] } }),
  fetchSchools: vi.fn(),
  updateSchool: mocks.updateSchool,
}))

vi.mock('../components/ImportButton', () => ({ default: () => null }))
vi.mock('../components/SchoolTagBadge', () => ({ default: ({ value }) => <span>{value}</span> }))
vi.mock('../components/SmartDataTable', () => ({
  default: ({ actionRef, columns, toolBarRender }) => {
    mocks.columns = columns
    actionRef.current = {
      reload: mocks.reload,
      reloadOptions: mocks.reloadOptions,
    }
    const actionColumn = columns.find((column) => column.valueType === 'option')
    return (
      <div>
        {toolBarRender?.()}
        {actionColumn?.render(null, {
          id: 9,
          name: '原大学',
          school_tag: 3,
        })}
      </div>
    )
  },
}))

describe('SchoolsPage', () => {
  beforeEach(() => {
    mocks.columns = []
    mocks.createSchool.mockReset()
    mocks.createSchool.mockResolvedValue({})
    mocks.updateSchool.mockReset()
    mocks.updateSchool.mockResolvedValue({})
    mocks.hasPermission.mockReset()
    mocks.hasPermission.mockImplementation((code) => code === 'school.manage')
    mocks.reload.mockReset()
    mocks.reloadOptions.mockReset()
  })

  it('does not expose province in the school list', () => {
    render(<SchoolsPage />)

    expect(mocks.columns.map((column) => column.dataIndex)).not.toContain('province')
  })

  it('creates a school and refreshes table data and filters', async () => {
    render(<SchoolsPage />)

    await userEvent.click(screen.getByRole('button', { name: '新增院校' }))
    await userEvent.click(screen.getByRole('button', { name: '保存院校' }))

    await waitFor(() => expect(mocks.createSchool).toHaveBeenCalledWith({
      name: '测试大学',
      school_tag: 5,
    }))
    expect(mocks.reload).toHaveBeenCalledOnce()
    expect(mocks.reloadOptions).toHaveBeenCalledOnce()
  })

  it('updates an existing school from the row action', async () => {
    render(<SchoolsPage />)

    await userEvent.click(screen.getByText('编辑'))
    await userEvent.click(screen.getByRole('button', { name: '保存院校' }))

    await waitFor(() => expect(mocks.updateSchool).toHaveBeenCalledWith(9, {
      name: '测试大学',
      school_tag: 5,
    }))
  })

  it('hides maintenance actions without school management permission', () => {
    mocks.hasPermission.mockReturnValue(false)

    render(<SchoolsPage />)

    expect(screen.queryByRole('button', { name: '新增院校' })).toBeNull()
    expect(screen.queryByText('编辑')).toBeNull()
  })
})
