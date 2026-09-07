import { useCallback, useEffect, useRef } from 'react'
import { reportUsagePageView } from '../api/services'

export const USAGE_SESSION_TIMEOUT_MS = 30 * 60 * 1000
export const USAGE_SESSION_ID_KEY = 'srf.usage-session-id'
export const USAGE_LAST_ACTIVITY_KEY = 'srf.usage-last-activity-at'

const TRACKED_PAGE_PATHS = new Set([
  '/analytics',
  '/processing-tasks',
  '/resumes',
  '/jobs',
  '/schools',
  '/departments',
  '/config',
  '/ai-connection',
  '/users',
])

export function resolveUsagePageKey(pathname) {
  if (typeof pathname !== 'string') return null
  const normalized = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname
  return TRACKED_PAGE_PATHS.has(normalized) ? normalized : null
}

export function createUsageEventId() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()
  const bytes = new Uint8Array(16)
  if (globalThis.crypto?.getRandomValues) {
    globalThis.crypto.getRandomValues(bytes)
  } else {
    for (let index = 0; index < bytes.length; index += 1) {
      bytes[index] = Math.floor(Math.random() * 256)
    }
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  return [...bytes].map((value, index) => {
    const separator = [4, 6, 8, 10].includes(index) ? '-' : ''
    return `${separator}${value.toString(16).padStart(2, '0')}`
  }).join('')
}

export function resolveUsageSession(storage, now = Date.now(), createId = createUsageEventId) {
  let sessionId = null
  let lastActivity = Number.NaN
  try {
    sessionId = storage.getItem(USAGE_SESSION_ID_KEY)
    lastActivity = Number(storage.getItem(USAGE_LAST_ACTIVITY_KEY))
  } catch {
    return createId()
  }

  if (
    !sessionId
    || !Number.isFinite(lastActivity)
    || now - lastActivity >= USAGE_SESSION_TIMEOUT_MS
    || now < lastActivity
  ) {
    sessionId = createId()
  }

  try {
    storage.setItem(USAGE_SESSION_ID_KEY, sessionId)
    storage.setItem(USAGE_LAST_ACTIVITY_KEY, String(now))
  } catch {
    // 会话存储不可用时仍可用本次临时会话完成上报。
  }
  return sessionId
}

export default function useUsagePageView(pathname) {
  const pageKey = resolveUsagePageKey(pathname)
  const currentPageKeyRef = useRef(pageKey)
  const lastReportedRouteRef = useRef(null)
  currentPageKeyRef.current = pageKey

  const report = useCallback((nextPageKey) => {
    if (!nextPageKey) return
    try {
      const sessionId = resolveUsageSession(window.sessionStorage)
      const request = reportUsagePageView({
        event_id: createUsageEventId(),
        session_id: sessionId,
        page_key: nextPageKey,
      })
      Promise.resolve(request).catch(() => {})
    } catch {
      // 使用频率统计失败必须完全静默，不影响当前业务页面。
    }
  }, [])

  useEffect(() => {
    if (!pageKey || lastReportedRouteRef.current === pageKey) return
    lastReportedRouteRef.current = pageKey
    report(pageKey)
  }, [pageKey, report])

  useEffect(() => {
    const handleFocus = () => report(currentPageKeyRef.current)
    window.addEventListener('focus', handleFocus)
    return () => window.removeEventListener('focus', handleFocus)
  }, [report])
}
