import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import AISettingsTab from './AISettingsTab'
import { fetchAIConnectionSettings, updateAIConnectionSetting } from '../../api/services'

vi.mock('../../api/services', () => ({ fetchAIConnectionSettings: vi.fn(), updateAIConnectionSetting: vi.fn() }))

it('saves changed runtime parameters together and preserves failed edits', async () => {
  const concurrent = { key: 'concurrent', label: '并发数', section: 'runtime', value_type: 'integer', value: 3 }
  const tracing = { key: 'tracing', label: '记录过程', section: 'runtime', value_type: 'boolean', value: false }
  fetchAIConnectionSettings.mockResolvedValue({ data: { settings: [concurrent, tracing] } })
  updateAIConnectionSetting.mockImplementation((key, value) => key === 'concurrent' ? Promise.resolve({ data: { ...concurrent, value } }) : Promise.reject(new Error('save failed')))
  render(<AISettingsTab />)
  fireEvent.change(await screen.findByRole('spinbutton', { name: '并发数' }), { target: { value: '6' } })
  await userEvent.click(screen.getByRole('switch', { name: '记录过程' }))
  await userEvent.click(screen.getByRole('button', { name: '保存 2 项修改' }))
  await waitFor(() => expect(updateAIConnectionSetting).toHaveBeenCalledWith('concurrent', 6))
  expect(await screen.findByText('未保存：记录过程')).toBeTruthy()
  expect(await screen.findByRole('button', { name: '保存 1 项修改' })).toBeTruthy()
  expect(screen.getByRole('switch', { name: '记录过程' }).getAttribute('aria-checked')).toBe('true')
})
