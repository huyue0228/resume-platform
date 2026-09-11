import { useCallback, useEffect, useRef, useState } from 'react'

export default function useTaskRecords(fetcher, params, { enabled = true, eventName, activeStatus } = {}) {
  const queryKey = JSON.stringify(params)
  const [data, setData] = useState({ results: [], count: 0, summary: null })
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const refreshRef = useRef(() => Promise.resolve())
  useEffect(() => {
    let disposed = false
    let timer, pending, controller
    let active = false
    setData({ results: [], count: 0, summary: null })
    setError(false)
    if (!enabled) { setLoading(false); refreshRef.current = () => Promise.resolve(); return }
    const refresh = () => {
      if (disposed) return Promise.resolve()
      if (pending) return pending
      window.clearTimeout(timer)
      controller = new AbortController()
      setLoading(true)
      pending = fetcher(JSON.parse(queryKey), { signal: controller.signal })
        .then(({ data: value }) => {
          if (disposed) return
          const records = value?.results || []
          active = Boolean(value?.summary?.active) || records.some((row) => activeStatus?.has(row.status))
          setData({ results: records, count: value?.count ?? records.length, summary: value?.summary || null })
          setError(false)
        })
        .catch(() => { if (!disposed) setError(true) })
        .finally(() => {
          pending = null
          if (disposed) return
          setLoading(false)
          timer = window.setTimeout(() => { if (document.visibilityState === 'visible') refresh() }, active ? 2000 : 15000)
        })
      return pending
    }
    refreshRef.current = refresh
    const visible = () => { if (document.visibilityState === 'visible') refresh() }
    refresh()
    document.addEventListener('visibilitychange', visible)
    if (eventName) window.addEventListener(eventName, refresh)
    return () => {
      disposed = true
      window.clearTimeout(timer)
      controller?.abort()
      document.removeEventListener('visibilitychange', visible)
      if (eventName) window.removeEventListener(eventName, refresh)
    }
  }, [fetcher, queryKey, enabled, eventName, activeStatus])
  const refresh = useCallback(() => refreshRef.current(), [])
  return { ...data, loading, error, refresh }
}
