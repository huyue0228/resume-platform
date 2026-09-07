import React from 'react'
import { render, waitFor } from '@testing-library/react'
import { ProLayout } from '@ant-design/pro-components'
import { describe, expect, it } from 'vitest'
import { appLayoutSettings, appLayoutToken, appSiderMenuProps } from '../theme'
import { allRoute } from './menuRoutes'
import '../index.css'

describe('BasicLayout menu hierarchy', () => {
  it('puts the data dashboard and processing tasks before the collapsible menu groups', () => {
    expect(allRoute.routes.map((route) => route.name)).toEqual([
      '数据看板',
      '处理任务',
      '数据管理',
      '系统设置',
    ])

    const dataManagement = allRoute.routes.find((route) => route.path === '/data')
    const analytics = allRoute.routes.find((route) => route.path === '/analytics')

    expect(dataManagement.routes.some((route) => route.path === '/analytics')).toBe(false)
    expect(analytics.routes).toBeUndefined()
    const systemSettings = allRoute.routes.find((route) => route.path === '/system')
    expect(systemSettings.routes.map((route) => route.path)).toEqual([
      '/config',
      '/ai-connection',
      '/users',
    ])
  })

  it('uses the reference dashboard dark sider and light application background', () => {
    expect(appLayoutSettings).toMatchObject({
      layout: 'side',
      siderWidth: 220,
      siderMenuType: 'sub',
      fixedHeader: true,
      fixSiderbar: true,
    })
    expect(appLayoutToken.bgLayout).toBe('#f5f7fb')
    expect(appLayoutToken.sider.colorMenuBackground).toBe('#111827')
    expect(appLayoutToken.sider.colorBgMenuItemSelected).toBe('#4f46e5')
    expect(appLayoutToken.header.colorBgHeader).toBe('#ffffff')
    expect(appLayoutSettings.navTheme).toBeUndefined()
    expect(appSiderMenuProps.theme).toBe('dark')
  })

  it('keeps the selected parent icon visible when the sider is collapsed', async () => {
    render(
      <ProLayout
        {...appLayoutSettings}
        className="srf-app-layout"
        collapsed
        route={allRoute}
        location={{ pathname: '/jobs' }}
        token={appLayoutToken}
        menuProps={appSiderMenuProps}
      />,
    )

    await waitFor(() => {
      expect(document.querySelector('.ant-menu')).toBeTruthy()
    })

    const menu = document.querySelector('.ant-menu')
    const selectedParentTitle = document.querySelector(
      '.ant-menu-submenu-selected > .ant-menu-submenu-title',
    )
    const selectedParentIcon = selectedParentTitle?.querySelector('.anticon')
    expect(menu?.classList.contains('ant-menu-dark')).toBe(true)
    expect(selectedParentTitle).toBeTruthy()
    expect(selectedParentIcon).toBeTruthy()
    expect(window.getComputedStyle(selectedParentTitle).color).toBe('rgb(255, 255, 255)')
    expect(window.getComputedStyle(selectedParentIcon).color).toBe('rgb(255, 255, 255)')
    expect(window.getComputedStyle(selectedParentTitle).backgroundColor).toBe(
      'rgb(79, 70, 229)',
    )
  })
})
