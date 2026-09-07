import React from 'react'
import { render, waitFor } from '@testing-library/react'
import { ProLayout } from '@ant-design/pro-components'
import { ConfigProvider } from 'antd'
import { describe, expect, it } from 'vitest'
import { appLayoutSettings, appLayoutToken, appSiderMenuProps, appTheme } from '../theme'
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

  it('uses a quiet light workspace with consistent green navigation emphasis', () => {
    expect(appLayoutSettings).toMatchObject({
      layout: 'side',
      siderWidth: 232,
      siderMenuType: 'sub',
      fixedHeader: true,
      fixSiderbar: true,
    })
    expect(appLayoutToken.bgLayout).toBe('#f7f8fa')
    expect(appLayoutToken.sider.colorMenuBackground).toBe('#f0f3f1')
    expect(appLayoutToken.sider.colorBgMenuItemSelected).toBe('#e0ece5')
    expect(appLayoutToken.header.colorBgHeader).toBe('#ffffff')
    expect(appLayoutSettings.navTheme).toBeUndefined()
    expect(appSiderMenuProps.theme).toBe('light')
  })

  it('keeps the selected parent icon visible when the sider is collapsed', async () => {
    render(
      <ConfigProvider theme={appTheme}><ProLayout
        {...appLayoutSettings}
        className="srf-app-layout"
        collapsed
        route={allRoute}
        location={{ pathname: '/jobs' }}
        token={appLayoutToken}
        menuProps={appSiderMenuProps}
      /></ConfigProvider>,
    )

    await waitFor(() => {
      expect(document.querySelector('.ant-menu')).toBeTruthy()
    })

    const menu = document.querySelector('.ant-menu')
    const selectedParentTitle = document.querySelector(
      '.ant-menu-submenu-selected > .ant-menu-submenu-title',
    )
    const selectedParentIcon = selectedParentTitle?.querySelector('.anticon')
    expect(menu?.classList.contains('ant-menu-light')).toBe(true)
    expect(selectedParentTitle).toBeTruthy()
    expect(selectedParentIcon).toBeTruthy()
    const foreground = window.getComputedStyle(selectedParentTitle).color
    const background = window.getComputedStyle(selectedParentTitle).backgroundColor
    expect(window.getComputedStyle(selectedParentIcon).color).toBe(foreground)
    const luminance = (color) => color.match(/[\d.]+/g).slice(0, 3)
      .map((value) => Number(value) / 255)
      .map((value) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
      .reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0)
    expect((luminance(background) + 0.05) / (luminance(foreground) + 0.05)).toBeGreaterThan(4.5)
  })
})
