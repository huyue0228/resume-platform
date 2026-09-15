import { describe, expect, it, vi } from 'vitest'
import { buildBulkChanges, runBulkEdit } from './bulkEditUtils'
import { contactBulkEdit, jobBulkEdit, roleBulkEdit } from './tableEditConfigs'

describe('bulk editing boundaries', () => {
  const fields = [{ name: 'name', label: '姓名' }, { name: 'is_active', label: '启用' }, { name: 'school_tag', label: '院校标签', clearValue: null }, { name: 'headcount', label: 'HC', type: 'number', min: 0 }]
  it('sends only explicit fields and preserves false, zero, and explicit clearing', () => {
    expect(buildBulkChanges(fields, { name: { operation: 'keep', value: 'old' }, is_active: { operation: 'set', value: false }, school_tag: { operation: 'clear' }, headcount: { operation: 'set', value: 0 } }).changes).toEqual({ is_active: false, school_tag: null, headcount: 0 })
  })
  it('requires an intentional value or clear operation', () => {
    expect(() => buildBulkChanges(fields, {})).toThrow('请至少选择')
    expect(() => buildBulkChanges(fields, { name: { operation: 'set', value: '' } })).toThrow('请填写姓名')
    expect(() => buildBulkChanges(fields, { name: { operation: 'clear' } })).toThrow('不能清空')
  })
  it('records partial failures without replaying successful writes', async () => {
    const records = [{ id: 1 }, { id: 2 }, { id: 3 }]
    const update = vi.fn().mockResolvedValueOnce({}).mockRejectedValueOnce({ response: { status: 403, data: { detail: '无当前部门权限' } } }).mockResolvedValueOnce({})
    const progress = vi.fn()
    const results = await runBulkEdit(records, { is_active: false }, update, progress)
    expect(results.map((item) => item.success)).toEqual([true, false, true])
    expect(results[1].error).toBe('无当前部门权限')
    expect(progress).toHaveBeenCalledTimes(3)
    expect(update.mock.calls.map(([record]) => record.id)).toEqual([1, 2, 3])
  })
  it('stops writes after authentication expires', async () => {
    const update = vi.fn().mockRejectedValue({ response: { status: 401, data: { detail: '登录已失效' } } })
    const results = await runBulkEdit([{ id: 1 }, { id: 2 }], {}, update)
    expect(update).toHaveBeenCalledTimes(1)
    expect(results[1].error).toContain('本条未执行')
  })
  it('enforces the screener delegation constraint on mixed selections', () => {
    expect(contactBulkEdit([]).validate({ can_delegate: true }, [{ contact_level: 'secondary' }, { contact_level: 'tertiary' }])).toContain('不允许转派')
  })
  it('requires both reception status and a reason', () => {
    expect(jobBulkEdit([], []).actions[0].validate({ reception_state: 'paused' })).toContain('调整原因')
  })
  it('limits bulk role choices to the shared allowed permissions', () => {
    const config = roleBulkEdit([{ name: '管理', children: [{ code: 'resume.view', name: '查看' }, { code: 'settings.manage_config', name: '设置' }] }])
    const fields = config.fields([{ allowed_permission_codes: ['resume.view'] }, { allowed_permission_codes: ['resume.view', 'settings.manage_config'] }])
    expect(fields[0].options.map((item) => item.value)).toEqual(['resume.view'])
  })
})
