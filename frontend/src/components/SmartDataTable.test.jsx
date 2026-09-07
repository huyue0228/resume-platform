import { createRef } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import SmartDataTable from './SmartDataTable'

const rows = [{ id: 1, name: '张三' }]

describe('SmartDataTable', () => {
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

    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    expect(onRowClick).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('张三'))
    expect(onRowClick).toHaveBeenCalledWith(rows[0], 0, expect.anything())
  })
})
