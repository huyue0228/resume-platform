import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import JobsPage from './JobsPage'

const mocks = vi.hoisted(() => ({
  columns: [],
  fetchDepartments: vi.fn(),
  exportJobs: vi.fn(),
  downloadBlobFromResponse: vi.fn(),
  departmentSelectProps: null,
}))

vi.mock('@ant-design/pro-components', () => ({
  PageContainer: ({ children }) => <div>{children}</div>,
  ModalForm: ({ children }) => <form>{children}</form>,
  ProFormDigit: () => null,
  ProFormSelect: (props) => {
    if (props.name === 'department') mocks.departmentSelectProps = props
    return null
  },
  ProFormSwitch: () => null,
  ProFormText: () => null,
  ProFormTextArea: ({ label, rules }) => (
    <textarea
      aria-label={label}
      data-required={rules?.some((rule) => rule.required) ? 'true' : 'false'}
    />
  ),
}))

vi.mock('antd', () => ({
  Button: ({ children, loading, onClick }) => (
    <button type="button" aria-busy={loading ? 'true' : 'false'} onClick={onClick}>
      {children}
    </button>
  ),
  Popconfirm: ({ children }) => <>{children}</>,
  Space: ({ children }) => <div>{children}</div>,
  Tag: ({ children }) => <span>{children}</span>,
  message: { success: vi.fn() },
}))

vi.mock('../contexts/roleState', () => ({
  useRole: () => ({ hasPermission: () => false }),
}))

vi.mock('../api/services', () => ({
  createJob: vi.fn(),
  deleteJob: vi.fn(),
  exportJobs: mocks.exportJobs,
  fetchDepartments: mocks.fetchDepartments,
  fetchJobFilterOptions: vi.fn(),
  fetchJobs: vi.fn(),
  updateJob: vi.fn(),
}))

vi.mock('../utils/download', () => ({
  downloadBlobFromResponse: mocks.downloadBlobFromResponse,
}))

vi.mock('../components/ImportButton', () => ({ default: () => null }))

vi.mock('../components/SmartDataTable', () => ({
  default: ({ actionRef, columns, toolBarRender }) => {
    mocks.columns = columns
    actionRef.current = {
      getFilters: () => ({ secondary_department_name_in: '平台部' }),
    }
    return (
      <div>
        {columns.map((column) => column.title).join('、')}
        {toolBarRender?.()}
      </div>
    )
  },
}))

describe('JobsPage', () => {
  beforeEach(() => {
    mocks.columns = []
    mocks.departmentSelectProps = null
    mocks.fetchDepartments.mockReset()
    mocks.fetchDepartments.mockResolvedValue({ data: { results: [] } })
    mocks.exportJobs.mockReset()
    mocks.exportJobs.mockResolvedValue({
      data: new Blob(['jobs']),
      headers: { 'x-export-count': '2' },
    })
    mocks.downloadBlobFromResponse.mockReset()
  })

  it('shows, filters and requires job responsibilities', () => {
    render(<JobsPage />)

    const responsibilitiesColumn = mocks.columns.find(
      (column) => column.dataIndex === 'responsibilities',
    )
    expect(responsibilitiesColumn?.title).toBe('工作职责')
    expect(responsibilitiesColumn?.filter).toMatchObject({
      type: 'text',
      param: 'responsibilities',
    })
    expect(
      screen.getByRole('textbox', { name: '工作职责' }).getAttribute('data-required'),
    ).toBe('true')
  })

  it('shows and filters the primary and secondary department levels', () => {
    render(<JobsPage />)

    expect(
      mocks.columns
        .filter((column) => column.dataIndex?.endsWith('_department_name'))
        .map((column) => [column.title, column.filter.param]),
    ).toEqual([
      ['一级部门', 'primary_department_name_in'],
      ['二级部门', 'secondary_department_name_in'],
    ])
  })

  it('offers only secondary departments with full paths', async () => {
    mocks.fetchDepartments.mockResolvedValue({
      data: {
        results: [
          { id: 1, name: '技术中心', level: 1, parent: null },
          { id: 2, name: '平台部', level: 2, parent: 1 },
          { id: 3, name: '平台研发组', level: 3, parent: 2 },
          { id: 4, name: '无效三级', level: 3, parent: null },
        ],
      },
    })

    render(<JobsPage />)

    await waitFor(() => expect(mocks.departmentSelectProps?.options).toEqual([
      { label: '技术中心 / 平台部', value: 2 },
    ]))
    expect(mocks.departmentSelectProps.label).toBe('所属二级部门')
    expect(mocks.departmentSelectProps.rules).toEqual([
      { required: true, message: '请选择所属二级部门' },
    ])
  })

  it('downloads all jobs matching the current table filters', async () => {
    render(<JobsPage />)

    await userEvent.click(screen.getByRole('button', { name: '下载职位清单' }))

    await waitFor(() => expect(mocks.exportJobs).toHaveBeenCalledWith({
      secondary_department_name_in: '平台部',
    }))
    expect(mocks.downloadBlobFromResponse).toHaveBeenCalledWith(
      expect.objectContaining({ data: expect.any(Blob) }),
      '职位清单.xlsx',
    )
    expect(screen.getByRole('button', { name: '下载职位清单' }).getAttribute('aria-busy'))
      .toBe('false')
  })

  it('restores the download button after an export error', async () => {
    mocks.exportJobs.mockRejectedValueOnce(new Error('download failed'))
    render(<JobsPage />)

    await userEvent.click(screen.getByRole('button', { name: '下载职位清单' }))

    await waitFor(() => expect(
      screen.getByRole('button', { name: '下载职位清单' }).getAttribute('aria-busy'),
    ).toBe('false'))
    expect(mocks.downloadBlobFromResponse).not.toHaveBeenCalled()
  })
})
