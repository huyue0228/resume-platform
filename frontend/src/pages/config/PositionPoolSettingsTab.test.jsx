import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import PositionPoolSettingsTab from './PositionPoolSettingsTab'
import { fetchPoolPolicy, savePoolPolicy } from '../../api/pools'

vi.mock('../../api/pools', () => ({ fetchPoolPolicy: vi.fn(), savePoolPolicy: vi.fn() }))

it('preserves stable identities and saves reviewed application mapping with its configuration version', async () => {
  const policy = {
    tags: [{ code: 'cad', name: '三维设计', category: 'skill', description: '原文说明完成三维建模', active: false }],
    pools: [{ code: 'mechanical', name: '机械工程师', entity: 'YLS', active: true }],
    standards: [{ code: 'mech_standard', name: '机械投递标准', entity: 'YLS', pool_code: 'mechanical', application_names: ['机构设计校招'], responsibilities: '机械设计和验证', required_majors: [], tag_codes: ['cad'], active: true }],
    rules: [{ job_id: 18, pool_code: 'mechanical', required_tags: ['cad'], preferred_tags: [], priority: 0, active: true }],
  }
  fetchPoolPolicy.mockResolvedValue({ data: { version: 9, policy, job_options: [{ id: 18, position_name: '机构设计', department_name: '研发部' }] } })
  savePoolPolicy.mockResolvedValue({ data: { version: 10, policy } })
  render(<PositionPoolSettingsTab />)
  await screen.findByDisplayValue('三维设计')
  const save = await screen.findByRole('button', { name: /保存职位池配置/ })
  await waitFor(() => expect(save.classList.contains('ant-btn-loading')).toBe(false))
  await userEvent.click(save)
  await waitFor(() => expect(savePoolPolicy).toHaveBeenCalled())
  const body = savePoolPolicy.mock.calls[0][0]
  expect(body.version).toBe(9)
  expect(body.policy.tags[0]).toMatchObject({ code: 'cad', active: false })
  expect(body.policy.pools[0].active).toBe(true)
  expect(body.policy.standards[0]).toMatchObject({ code: 'mech_standard', entity: 'YLS', pool_code: 'mechanical', application_names: ['机构设计校招'] })
  expect(body.policy.rules[0]).toMatchObject({ job_id: 18, required_tags: ['cad'] })
})
