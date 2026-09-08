import { afterEach } from 'vitest'
import { act, cleanup } from '@testing-library/react'
import { message, notification } from 'antd'

afterEach(async () => {
  await act(async () => {
    cleanup()
    // 静态通知挂载在独立 React root，不属于 Testing Library 的渲染树。
    // 在 jsdom 销毁前关闭通知并完成更新，避免自动关闭定时器跨测试泄漏。
    message.destroy()
    notification.destroy()
  })
  localStorage.clear()
})

class ResizeObserverMock {
  observe() {}
  unobserve() {}
  disconnect() {}
}

globalThis.ResizeObserver = ResizeObserverMock
globalThis.matchMedia = globalThis.matchMedia || (() => ({
  matches: false,
  addEventListener() {},
  removeEventListener() {},
  addListener() {},
  removeListener() {},
}))

if (!globalThis.CSS) globalThis.CSS = {}
globalThis.CSS.supports = globalThis.CSS.supports || (() => false)
