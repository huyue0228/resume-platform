import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import ConfigPage from './ConfigPage'

vi.mock('./config/SchoolTagsTab', () => ({ default: () => <div>院校标签内容</div> }))
vi.mock('./config/SchoolAdmissionRulesTab', () => ({ default: () => <div>院校准入内容</div> }))
vi.mock('./config/MajorDictionaryTab', () => ({ default: () => <div>专业词表内容</div> }))
vi.mock('./config/AllocationSettingsTab', () => ({ default: () => <div>分配参数内容</div> }))

describe('ConfigPage', () => {
  it('removes system parameters and opens the first business configuration tab', () => {
    render(<ConfigPage />)

    const tabs = screen.getAllByRole('tab')
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      '院校标签字典',
      '院校准入规则',
      '专业大类词表',
      '分配参数',
    ])
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    expect(screen.getByText('院校标签内容')).toBeTruthy()
  })
})
