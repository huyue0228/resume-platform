import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { ConfigProvider } from 'antd'
import { describe, expect, it } from 'vitest'
import QuickNavigation from './QuickNavigation'

const routes = [
  { name: '数据看板', path: '/analytics' },
  { name: '数据管理', path: '/data', routes: [{ name: '简历库', path: '/resumes' }] },
]

describe('QuickNavigation', () => {
  it('searches only the permission-filtered destinations supplied by the layout', async () => {
    const user = userEvent.setup()
    render(<ConfigProvider theme={{ token: { motion: false } }}><MemoryRouter><QuickNavigation routes={routes} /></MemoryRouter></ConfigProvider>)
    await user.click(screen.getByRole('button', { name: /快速导航/ }))
    expect(screen.getByRole('link', { name: /数据看板/ }).getAttribute('href')).toBe('/analytics')
    expect(screen.queryByRole('link', { name: /用户权限/ })).toBeNull()
    expect(screen.queryByRole('link', { name: '数据管理' })).toBeNull()
    await user.type(screen.getByRole('textbox', { name: '搜索可访问页面' }), '简历')
    expect(screen.getByRole('link', { name: /简历库/ }).getAttribute('href')).toBe('/resumes')
    expect(screen.queryByRole('link', { name: /数据看板/ })).toBeNull()
    await user.clear(screen.getByRole('textbox', { name: '搜索可访问页面' }))
    await user.type(screen.getByRole('textbox', { name: '搜索可访问页面' }), '不存在的页面')
    expect(screen.getByText('未找到可访问的页面')).toBeTruthy()
  })

  it('opens by keyboard and preserves ordinary link navigation', async () => {
    const user = userEvent.setup()
    render(<ConfigProvider theme={{ token: { motion: false } }}><MemoryRouter><QuickNavigation routes={routes} /></MemoryRouter></ConfigProvider>)
    await user.keyboard('{Control>}k{/Control}')
    expect(await screen.findByRole('dialog', { name: '快速导航' })).toBeTruthy()
    await user.click(screen.getByRole('link', { name: /简历库/ }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '快速导航' })).toBeNull())
  })
})
