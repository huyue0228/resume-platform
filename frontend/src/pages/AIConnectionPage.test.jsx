import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import AIConnectionPage from './AIConnectionPage'

vi.mock('./config/AIConnectionTab', () => ({ default: () => <div>模型连接内容</div> }))
vi.mock('./config/AISettingsTab', () => ({
  default: () => <div>运行参数内容</div>,
}))
vi.mock('./config/AISpecialSettingsTab', () => ({
  default: () => <div>AI 专项内容</div>,
}))

describe('AIConnectionPage', () => {
  it('hosts model connection, runtime settings and AI special settings', async () => {
    render(<AIConnectionPage />)

    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual([
      '模型连接',
      'AI 运行参数',
      'AI 专项',
    ])
    expect(screen.getByText('模型连接内容')).toBeTruthy()

    await userEvent.click(screen.getByRole('tab', { name: 'AI 运行参数' }))
    expect(screen.getByText('运行参数内容')).toBeTruthy()

    await userEvent.click(screen.getByRole('tab', { name: 'AI 专项' }))
    expect(screen.getByText('AI 专项内容')).toBeTruthy()
  })
})
