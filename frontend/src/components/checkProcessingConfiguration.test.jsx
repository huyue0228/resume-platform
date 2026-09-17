import { afterEach, expect, it, vi } from 'vitest'
import { Modal } from 'antd'
import { checkProcessingConfiguration } from '../api/pools'
import { confirmProcessingConfiguration } from './checkProcessingConfiguration'

vi.mock('../api/pools', () => ({ checkProcessingConfiguration: vi.fn() }))
afterEach(() => vi.restoreAllMocks())

it('checks the frozen selection and proceeds when it is ready', async () => {
  checkProcessingConfiguration.mockResolvedValue({ data: { issues: [] } })
  const scope = { candidate_ids: [17, 28] }
  expect(await confirmProcessingConfiguration(scope)).toBe(true)
  expect(checkProcessingConfiguration).toHaveBeenCalledWith(scope)
})

it('prevents submission when every selected candidate is blocked and allows cancellation', async () => {
  checkProcessingConfiguration.mockResolvedValue({ data: { total: 2, blocked_count: 2, ready_count: 0, allocation_wait_count: 0, issues: [{ code: 'source_job_conflict' }] } })
  const confirm = vi.spyOn(Modal, 'confirm').mockImplementation((options) => {
    expect(options.okButtonProps.disabled).toBe(true)
    options.onCancel()
  })
  expect(await confirmProcessingConfiguration({ candidate_ids: [17, 28] })).toBe(false)
  expect(confirm).toHaveBeenCalledOnce()
})

it('allows screening while allocation rules still need configuration', async () => {
  checkProcessingConfiguration.mockResolvedValue({ data: { total: 2, blocked_count: 0, ready_count: 2, allocation_wait_count: 2, issues: [{ code: 'allocation_rules_missing' }] } })
  vi.spyOn(Modal, 'confirm').mockImplementation((options) => {
    expect(options.okButtonProps.disabled).toBe(false)
    options.onOk()
  })
  expect(await confirmProcessingConfiguration({ candidate_ids: [17, 28] })).toBe(true)
})
