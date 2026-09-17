import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { Modal } from 'antd'
import PositionPoolSettingsTab from './PositionPoolSettingsTab'
import { fetchPoolPolicy, savePoolPolicy, initializeJobPolicy } from '../../api/pools'

vi.mock('../../contexts/roleState', () => ({ useRole: () => ({ hasPermission: () => true }) }))
vi.mock('../../api/pools', () => ({ fetchPoolPolicy: vi.fn(), savePoolPolicy: vi.fn(), initializeJobPolicy: vi.fn(), reprocessConfiguration: vi.fn() }))
const policy = {
  tags: [{ code: 'cad', name: '三维设计', category: 'skill', description: '原文说明完成三维建模', active: true }],
  pools: [{ code: 'mechanical', name: '机械工程师', entity: 'YLS', active: true }],
  standards: [{ code: 'mech_standard', name: '机械投递标准', entity: 'YLS', pool_code: 'mechanical', application_names: ['机构设计校招'], responsibilities: '机械设计和验证', required_majors: [], tag_codes: ['cad'], active: true }],
  rules: [{ job_id: 18, pool_code: 'mechanical', required_tags: ['cad'], preferred_tags: [], priority: 0, active: true }],
}
const data = { version: 9, policy, coverage: [{ code: 'mech_standard', status: '', screening_ready: true, demand_ids: [18] }], job_options: [{ id: 18, is_active: true, position_name: '机构设计', department_name: '研发部' }] }
beforeEach(() => { vi.clearAllMocks(); fetchPoolPolicy.mockResolvedValue({ data }); initializeJobPolicy.mockResolvedValue({ data }); savePoolPolicy.mockImplementation(async (body) => ({ data: body.preview ? { changed_standard_codes: ['mech_standard'], reassessment_count: 2 } : { ...data, policy: body.policy, version: 10 } })) })
it('previews the affected qualifications before publishing versioned configuration', async () => {
  const confirm = vi.spyOn(Modal, 'confirm').mockImplementation(() => ({}))
  render(<PositionPoolSettingsTab />)
  await screen.findByText('机械投递标准')
  await userEvent.click(screen.getByRole('button', { name: '更多操作' }))
  await userEvent.click(screen.getByRole('button', { name: '配置' }))
  await userEvent.click(screen.getByRole('button', { name: '预览并保存' }))
  await waitFor(() => expect(confirm).toHaveBeenCalled())
  expect(savePoolPolicy.mock.calls[0][0]).toMatchObject({ preview: true, version: 9 })
  expect(savePoolPolicy).toHaveBeenCalledTimes(1)
  await confirm.mock.calls[0][0].onOk()
  expect(savePoolPolicy.mock.calls[1][0].policy.standards[0]).toMatchObject({ code: 'mech_standard', application_names: ['机构设计校招'] })
  confirm.mockRestore()
})
it('initializes current jobs and shows screening blockers separately from allocation waiting', async () => {
  fetchPoolPolicy.mockResolvedValue({ data: { ...data, coverage: [{ code: 'mech_standard', status: 'allocation_rules_missing', screening_ready: true, message: '尚未启用部门分配规则', demand_ids: [] }] } })
  render(<PositionPoolSettingsTab />)
  await screen.findByText('可筛选，分配待配置')
  await waitFor(() => expect(screen.getByRole('button', { name: '从当前岗位生成关联' }).classList.contains('ant-btn-loading')).toBe(false))
  await userEvent.click(screen.getByRole('button', { name: '从当前岗位生成关联' }))
  await waitFor(() => expect(initializeJobPolicy).toHaveBeenCalledOnce())
})
