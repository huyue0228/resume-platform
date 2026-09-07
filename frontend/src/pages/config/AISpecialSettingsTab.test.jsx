import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AISpecialSettingsTab from './AISpecialSettingsTab'
import { saveAISpecialSettings } from './aiSpecialSettings'
import {
  fetchAIConnectionSettings,
  updateAIConnectionSetting,
} from '../../api/services'

vi.mock('../../api/services', () => ({
  fetchAIConnectionSettings: vi.fn(),
  updateAIConnectionSetting: vi.fn(),
}))

const configValues = {
  ai_special_route_enabled: false,
  ai_special_route_threshold: 0.9,
  ai_special_route_secondary_department_id: 11,
  ai_special_route_tertiary_department_id: 21,
}

const departments = [
  {
    id: 11,
    name: '研发部',
    level: 2,
    parent: 1,
    entity: '总部',
  },
  {
    id: 21,
    name: '算法组',
    level: 3,
    parent: 11,
    entity: '总部',
  },
  {
    id: 12,
    name: '产品部',
    level: 2,
    parent: 1,
    entity: '总部',
  },
  {
    id: 22,
    name: '产品组',
    level: 3,
    parent: 12,
    entity: '总部',
  },
]

describe('AISpecialSettingsTab', () => {
  beforeEach(() => {
    fetchAIConnectionSettings.mockReset()
    updateAIConnectionSetting.mockReset()
    fetchAIConnectionSettings.mockResolvedValue({
      data: {
        settings: Object.entries(configValues).map(([key, value]) => ({
          key,
          value,
          section: 'special_route',
        })),
        departments,
      },
    })
    updateAIConnectionSetting.mockResolvedValue({ data: {} })
  })

  it('loads the special settings and saves the enable switch', async () => {
    const user = userEvent.setup()
    render(<AISpecialSettingsTab />)

    const enabled = await screen.findByRole('switch', { name: '启用 AI 专项' })
    expect(enabled.getAttribute('aria-checked')).toBe('false')
    expect(screen.getByRole('spinbutton', { name: 'AI 专项触发阈值' }).value).toBe('0.90')

    await user.click(enabled)
    await user.click(screen.getByRole('button', { name: '保存 AI 专项配置' }))

    expect(fetchAIConnectionSettings).toHaveBeenCalledTimes(2)
    expect(screen.getByText('父级二级部门')).toBeTruthy()
    expect(screen.getByText('目标三级部门')).toBeTruthy()
    await waitFor(() => expect(updateAIConnectionSetting).toHaveBeenCalledWith(
      'ai_special_route_enabled',
      true,
    ))
  })

  it('only offers tertiary departments under the selected secondary department', async () => {
    const user = userEvent.setup()
    render(<AISpecialSettingsTab />)

    const secondarySelect = await screen.findByRole('combobox', { name: 'AI 专项父级二级部门' })
    await user.click(secondarySelect)
    await user.click(await screen.findByText('产品部（总部）'))

    const tertiarySelect = screen.getByRole('combobox', { name: 'AI 专项目标三级部门' })
    await user.click(tertiarySelect)
    expect(await screen.findByText('产品组（总部）')).toBeTruthy()
    expect(screen.queryByText('算法组（总部）')).toBeNull()
  })

  it('safely disables an enabled route before changing its targets', async () => {
    const update = vi.fn().mockResolvedValue({ data: {} })
    const persisted = {
      ai_special_route_enabled: true,
      ai_special_route_threshold: 0.9,
      ai_special_route_secondary_department_id: 11,
      ai_special_route_tertiary_department_id: 21,
    }
    const drafts = {
      ...persisted,
      ai_special_route_secondary_department_id: 12,
      ai_special_route_tertiary_department_id: 22,
    }

    await saveAISpecialSettings({ persisted, drafts, update })

    expect(update.mock.calls).toEqual([
      ['ai_special_route_enabled', false],
      ['ai_special_route_secondary_department_id', 12],
      ['ai_special_route_tertiary_department_id', 22],
      ['ai_special_route_enabled', true],
    ])
  })
})
