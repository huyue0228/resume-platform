import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AIConnectionTab from './AIConnectionTab'
import {
  fetchAIConnection,
  testAIConnection,
  updateAIConnection,
} from '../../api/services'

vi.mock('../../api/services', () => ({
  fetchAIConnection: vi.fn(),
  fetchAIModels: vi.fn(),
  testAIConnection: vi.fn(),
  updateAIConnection: vi.fn(),
}))

const connection = {
  api_style: 'chat_json',
  model_name: 'internal-model',
  base_url: 'https://model.internal/v1',
  api_key_configured: false,
  test_passed: true,
  tested_at: '2026-08-27T10:00:00+08:00',
  structured_output_mode: 'legacy_compat',
}

describe('AIConnectionTab', () => {
  beforeEach(() => {
    fetchAIConnection.mockReset()
    testAIConnection.mockReset()
    updateAIConnection.mockReset()
    fetchAIConnection.mockResolvedValue({ data: connection })
    updateAIConnection.mockResolvedValue({ data: connection })
    testAIConnection.mockResolvedValue({
      data: {
        ok: true,
        detail: '模型连接测试成功',
        tested_at: '2026-08-27T10:05:00+08:00',
        api_style: 'chat_json',
        model_name: 'internal-model',
        base_url: 'https://model.internal/v1',
        structured_output_mode: 'strict_schema',
      },
    })
  })

  it('shows legacy capability and refreshes it after a real schema test', async () => {
    const user = userEvent.setup()
    render(<AIConnectionTab />)

    expect(await screen.findByText('结构化能力待重测')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: '保存并测试连接' }))

    await waitFor(() => expect(testAIConnection).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('严格结构化')).toBeTruthy()
    expect(screen.getByText(/结构化模式：严格结构化/)).toBeTruthy()
  })

  it('shows the probed JSON compatibility mode', async () => {
    fetchAIConnection.mockResolvedValue({
      data: { ...connection, structured_output_mode: 'json_compat' },
    })

    render(<AIConnectionTab />)

    expect(await screen.findByText('JSON 兼容')).toBeTruthy()
  })
})
