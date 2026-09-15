import { createRef } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import SmartDataTable from './SmartDataTable'

const rows = [{ id: 1, name: '张三' }]

describe('SmartDataTable', () => {
  it('does not reserve horizontal space for hidden columns', async () => {
    const { container } = render(
      <SmartDataTable
        tableId="hidden-column-width"
        rowKey="id"
        columns={[
          { title: '姓名', dataIndex: 'name', width: 190 },
          { title: '内部资料', dataIndex: 'internal', width: 900 },
        ]}
        defaultColumnsState={{ internal: { show: false } }}
        dataSource={rows}
        options={false}
      />,
    )
    await screen.findByText('张三')
    expect(container.querySelector('table').style.width).toBe('190px')
    expect(screen.queryByRole('columnheader', { name: '内部资料' })).toBeNull()
  })

  it('requests the first page and applies a confirmed text filter', async () => {
    const request = vi.fn().mockResolvedValue({ data: { results: rows, count: 1 } })
    const { container } = render(
      <SmartDataTable
        tableId="request"
        rowKey="id"
        columns={[{
          title: '姓名',
          dataIndex: 'name',
          filter: { type: 'text', param: 'name', placeholder: '筛选姓名' },
        }]}
        request={request}
      />,
    )

    await waitFor(() => expect(request).toHaveBeenCalledWith({ page: 1, page_size: 10 }))
    expect(container.querySelector('.srf-table-pagination-sticky')).toBeTruthy()
    await userEvent.click(container.querySelector('.ant-table-filter-trigger'))
    await userEvent.type(screen.getByPlaceholderText('筛选姓名'), '张三')
    await userEvent.click(screen.getByRole('button', { name: /确认/ }))

    await waitFor(() => expect(request).toHaveBeenLastCalledWith({
      page: 1,
      page_size: 10,
      name: '张三',
    }))
    await userEvent.click(screen.getByRole('button', { name: '清除姓名筛选' }))
    await waitFor(() => expect(request).toHaveBeenLastCalledWith({ page: 1, page_size: 10 }))
  })

  it('distinguishes request failures from empty results and allows retry', async () => {
    const request = vi.fn().mockRejectedValueOnce(new Error('unavailable'))
      .mockResolvedValue({ data: { results: rows, count: 1 } })
    render(<SmartDataTable tableId="retry-failed" rowKey="id" columns={[{ title: '姓名', dataIndex: 'name' }]} request={request} />)
    expect(await screen.findByText('列表加载失败，请重试')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: '重新加载' }))
    expect(await screen.findByText('张三')).toBeTruthy()
    expect(screen.queryByText('列表加载失败，请重试')).toBeNull()
  })

  it('offers 500 rows per page and submits page_size=500', async () => {
    const request = vi.fn().mockResolvedValue({ data: { results: rows, count: 600 } })
    const { container } = render(
      <SmartDataTable
        tableId="page-size-500"
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name' }]}
        request={request}
      />,
    )

    await waitFor(() => expect(request).toHaveBeenCalledWith({ page: 1, page_size: 10 }))
    await userEvent.click(
      container.querySelector('.ant-pagination-options-size-changer .ant-select-selector'),
    )
    await userEvent.click(await screen.findByText(/500\s*(\/\s*page|条\s*\/\s*页)/i))

    await waitFor(() => expect(request).toHaveBeenLastCalledWith({
      page: 1,
      page_size: 500,
    }))
  })

  it('applies and resets a date range filter with separate server params', async () => {
    const request = vi.fn().mockResolvedValue({ data: { results: rows, count: 1 } })
    const { container } = render(
      <SmartDataTable
        tableId="date-range-request"
        rowKey="id"
        columns={[{
          title: '投递时间',
          dataIndex: 'current_apply_date',
          filter: {
            type: 'dateRange',
            params: ['current_apply_date_from', 'current_apply_date_to'],
            placeholders: ['投递开始日期', '投递结束日期'],
          },
        }]}
        request={request}
      />,
    )

    await waitFor(() => expect(request).toHaveBeenCalledWith({ page: 1, page_size: 10 }))
    await userEvent.click(container.querySelector('.ant-table-filter-trigger'))
    fireEvent.change(screen.getByLabelText('投递开始日期'), {
      target: { value: '2026-07-01' },
    })
    fireEvent.change(screen.getByLabelText('投递结束日期'), {
      target: { value: '2026-07-31' },
    })
    await userEvent.click(screen.getByRole('button', { name: /确认/ }))

    await waitFor(() => expect(request).toHaveBeenLastCalledWith({
      page: 1,
      page_size: 10,
      current_apply_date_from: '2026-07-01',
      current_apply_date_to: '2026-07-31',
    }))

    await userEvent.click(container.querySelector('.ant-table-filter-trigger'))
    await userEvent.click(screen.getByRole('button', { name: /重\s*置/ }))
    await waitFor(() => expect(request).toHaveBeenLastCalledWith({
      page: 1,
      page_size: 10,
    }))
  })

  it('fixes pagination to the visible table width and scrolls only the table body', async () => {
    const request = vi.fn().mockResolvedValue({ data: { results: rows, count: 20 } })
    const rectSpy = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
      bottom: 360,
      height: 260,
      left: 24,
      right: 824,
      top: 100,
      width: 800,
      x: 24,
      y: 100,
      toJSON: () => ({}),
    })
    const view = render(
      <SmartDataTable
        tableId="sticky-pagination"
        stickyPagination
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name' }]}
        request={request}
      />,
    )

    await waitFor(() => {
      const root = view.container.querySelector('.srf-smart-data-table')
      expect(view.container.querySelector('.srf-table-pagination-sticky')).toBeTruthy()
      expect(root.classList.contains('srf-smart-data-table--pagination-fixed')).toBe(true)
      expect(root.classList.contains('srf-smart-data-table--viewport-scroll')).toBe(true)
      expect(root.style.getPropertyValue('--srf-sticky-pagination-left')).toBe('24px')
      expect(root.style.getPropertyValue('--srf-sticky-pagination-width')).toBe('800px')
      expect(root.style.getPropertyValue('--srf-table-body-height')).toBe('180px')
    })

    rectSpy.mockReturnValue({
      bottom: -20,
      height: 260,
      left: 24,
      right: 824,
      top: -280,
      width: 800,
      x: 24,
      y: -280,
      toJSON: () => ({}),
    })
    fireEvent.scroll(window)
    await waitFor(() => expect(
      view.container.querySelector('.srf-smart-data-table--pagination-fixed'),
    ).toBeNull())

    view.unmount()
    rectSpy.mockRestore()
  })

  it('does not create a sticky bar when pagination is disabled', () => {
    const { container } = render(
      <SmartDataTable
        tableId="no-pagination"
        stickyPagination
        pagination={false}
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name' }]}
        dataSource={rows}
      />,
    )

    expect(container.querySelector('.srf-table-pagination-sticky')).toBeNull()
    expect(
      container.querySelector('.srf-smart-data-table--pagination-fixed'),
    ).toBeNull()
  })

  it('merges sticky styling with custom pagination options', async () => {
    const request = vi.fn().mockResolvedValue({ data: { results: rows, count: 30 } })
    const { container } = render(
      <SmartDataTable
        tableId="custom-pagination"
        stickyPagination
        pagination={{
          className: 'custom-pagination',
          defaultPageSize: 20,
          pageSizeOptions: [20, 200],
          showSizeChanger: true,
        }}
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name' }]}
        request={request}
      />,
    )

    await waitFor(() => expect(request).toHaveBeenCalledWith({ page: 1, page_size: 20 }))
    const pagination = container.querySelector('.srf-table-pagination-sticky')
    expect(pagination).toBeTruthy()
    expect(pagination.classList.contains('custom-pagination')).toBe(true)
    await userEvent.click(
      container.querySelector('.ant-pagination-options-size-changer .ant-select-selector'),
    )
    expect(screen.queryByText(/500\s*(\/\s*page|条\s*\/\s*页)/i)).toBeNull()
    await userEvent.click(await screen.findByText(/200\s*(\/\s*page|条\s*\/\s*页)/i))
    await waitFor(() => expect(request).toHaveBeenLastCalledWith({ page: 1, page_size: 200 }))
  })

  it('reloads the first page when stable external parameters change', async () => {
    const request = vi.fn().mockResolvedValue({ data: { results: rows, count: 1 } })
    const { rerender } = render(
      <SmartDataTable
        tableId="external-params"
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name' }]}
        params={{ processing_run_id: '18', processing_result: 'success' }}
        request={request}
      />,
    )

    await waitFor(() => expect(request).toHaveBeenCalledWith({
      page: 1,
      page_size: 10,
      processing_run_id: '18',
      processing_result: 'success',
    }))

    rerender(
      <SmartDataTable
        tableId="external-params"
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name' }]}
        params={{ processing_run_id: '19', processing_result: 'failed' }}
        request={request}
      />,
    )

    await waitFor(() => expect(request).toHaveBeenLastCalledWith({
      page: 1,
      page_size: 10,
      processing_run_id: '19',
      processing_result: 'failed',
    }))

    rerender(
      <SmartDataTable
        tableId="external-params"
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name' }]}
        params={{ processing_run_id: undefined, processing_result: undefined }}
        request={request}
      />,
    )

    await waitFor(() => expect(request).toHaveBeenLastCalledWith({
      page: 1,
      page_size: 10,
      processing_run_id: undefined,
      processing_result: undefined,
    }))
  })

  it('resetTable clears filters and persisted column configuration', async () => {
    const actionRef = createRef()
    const storageKey = 'srf:table:v1:anonymous:reset'
    localStorage.setItem(storageKey, JSON.stringify({
      widths: { name: 240 },
      columnsState: { name: { show: false } },
    }))
    render(
      <SmartDataTable
        tableId="reset"
        actionRef={actionRef}
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name', filter: { type: 'text' } }]}
        dataSource={rows}
      />,
    )

    await act(async () => actionRef.current.resetTable())

    expect(actionRef.current.getFilters()).toEqual({})
    expect(localStorage.getItem(storageKey)).toBeNull()
  })

  it('shows batch actions only after selecting a row', async () => {
    render(
      <SmartDataTable
        tableId="batch"
        rowKey="id"
        columns={[{ title: '姓名', dataIndex: 'name' }]}
        dataSource={rows}
        rowSelection={{}}
        batchActions={() => <button type="button">批量处理</button>}
      />,
    )

    expect(screen.queryByRole('button', { name: '批量处理' })).toBeNull()
    const checkboxes = screen.getAllByRole('checkbox')
    await userEvent.click(checkboxes[1])
    expect(screen.getByRole('button', { name: '批量处理' })).toBeTruthy()
  })

  it('isolates interactive controls from row click actions', async () => {
    const onRowClick = vi.fn()
    render(
      <SmartDataTable
        tableId="row-click"
        rowKey="id"
        columns={[
          { title: '姓名', dataIndex: 'name' },
          { title: '操作', render: () => <button type="button">编辑</button> },
        ]}
        dataSource={rows}
        onRowClick={onRowClick}
      />,
    )

    expect(screen.queryByRole('button', { name: '编辑' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }))
    await userEvent.click(await screen.findByRole('button', { name: '编辑' }))
    expect(onRowClick).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('张三'))
    expect(onRowClick).toHaveBeenCalledWith(rows[0], 0, expect.anything())
  })
})


describe('SmartDataTable bulk maintenance', () => {
  it('preserves cross-page selection, freezes the submitted rows, and clears on filter change', async () => {
    const user = userEvent.setup()
    const update = vi.fn().mockResolvedValue({})
    const request = vi.fn(({ page }) => Promise.resolve({ data: { results: [{ id: page, name: `记录${page}` }], count: 2 } }))
    const { container } = render(<SmartDataTable tableId="bulk-pages" rowKey="id"
      columns={[{ title: '名称', dataIndex: 'name', filter: { type: 'text', param: 'name' } }]}
      request={request} pagination={{ defaultPageSize: 1 }}
      bulkEdit={{ fields: [{ name: 'note', label: '备注' }], update }} />)
    await screen.findByText('记录1')
    await user.click(screen.getAllByRole('checkbox')[1])
    await user.click(container.querySelector('.ant-pagination-next button'))
    await screen.findByText('记录2')
    await user.click(screen.getAllByRole('checkbox')[1])
    expect(await screen.findByText('已选 2 项')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: '批量编辑' }))
    await user.type(screen.getByRole('textbox', { name: '备注' }), '共同备注')
    await user.click(screen.getByRole('button', { name: '预览修改' }))
    await user.click(screen.getByRole('button', { name: '确认保存 2 项' }))
    expect(await screen.findByText('修改完成：成功 2 项，未成功 0 项')).toBeTruthy()
    expect(update.mock.calls.map(([record]) => record.id)).toEqual([1, 2])
    expect(update.mock.calls.map(([, changes]) => changes)).toEqual([{ note: '共同备注' }, { note: '共同备注' }])
    await user.click(screen.getByRole('button', { name: /^关\s*闭$/ }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await user.click(screen.getAllByRole('checkbox')[1])
    expect(screen.getByText('已选 1 项')).toBeTruthy()
    await user.click(container.querySelector('.ant-table-filter-trigger'))
    await user.type(screen.getByPlaceholderText('请输入筛选内容'), '新条件')
    await user.click(screen.getByRole('button', { name: /确认/ }))
    await waitFor(() => expect(screen.queryByText('已选 1 项')).toBeNull())
  })

  it('keeps protected rows out of selection and supports name-based record editing', async () => {
    const open = vi.fn()
    render(<SmartDataTable tableId="bulk-protected" rowKey="id" columns={[{ title: '名称', dataIndex: 'name' }]}
      dataSource={[{ id: 1, name: '内置账号', protected: true }, { id: 2, name: '普通账号' }]}
      recordEditor={{ column: 'name', open, disabled: (record) => record.protected }}
      bulkEdit={{ fields: [], update: vi.fn(), disabled: (record) => record.protected }} />)
    expect(screen.getAllByRole('checkbox')[1].disabled).toBe(true)
    expect(screen.queryByRole('button', { name: '内置账号' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '普通账号' }))
    expect(open).toHaveBeenCalledWith({ id: 2, name: '普通账号' })
  })
})
