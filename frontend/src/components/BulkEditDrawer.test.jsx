import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import BulkEditDrawer from './BulkEditDrawer'

describe('BulkEditDrawer', () => {
  it('previews an explicit patch and keeps only failed records for correction', async () => {
    const update = vi.fn().mockResolvedValueOnce({}).mockRejectedValueOnce({ response: { status: 400, data: { detail: '角色与部门不匹配' } } })
    const complete = vi.fn()
    const user = userEvent.setup()
    render(<BulkEditDrawer records={[{ id: 1, name: '张三' }, { id: 2, name: '李四' }]} config={{ fields: [{ name: 'department', label: '所属部门' }, { name: 'note', label: '备注' }], update }} onClose={vi.fn()} onComplete={complete} />)
    await user.click(screen.getByRole('combobox', { name: '需要修改的字段' }))
    await user.click(await screen.findByText('所属部门'))
    await user.keyboard('{Escape}')
    await user.type(screen.getByRole('textbox', { name: '所属部门' }), '研发部')
    await user.click(screen.getByRole('button', { name: '预览修改' }))
    expect(update).not.toHaveBeenCalled()
    expect(screen.getByText('将以下修改应用到 2 项')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: '确认保存 2 项' }))
    expect(await screen.findByText('修改完成：成功 1 项，未成功 1 项')).toBeTruthy()
    expect(update).toHaveBeenNthCalledWith(1, { id: 1, name: '张三' }, { department: '研发部' })
    expect(await screen.findByText('角色与部门不匹配')).toBeTruthy()
    await waitFor(() => expect(complete).toHaveBeenCalledTimes(1))
    await user.click(screen.getByRole('button', { name: '修改未成功项' }))
    expect(screen.getByText('本次修改 1 项（含跨页勾选）')).toBeTruthy()
    expect(screen.getByRole('textbox', { name: '所属部门' }).value).toBe('研发部')
    const drawer = screen.getByRole('dialog')
    expect(within(drawer).queryByText('张三 · #1')).toBeNull()
  })
})
