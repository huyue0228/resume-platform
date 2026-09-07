import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchPipelineRuns } from '../api/services'

const ACTIVE = new Set(['pending', 'running', 'waiting_conflict', 'cancelling'])

// 串行轮询，避免旧响应覆盖新状态；隐藏页面暂停请求，卸载时取消在途请求。
export default function useProcessingRuns() {
  const [runs, setRuns] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const refreshRef = useRef(() => Promise.resolve())
  useEffect(() => {
    let disposed = false
    let timer
    let pending
    let controller
    let active = false
    const refresh = () => {
      if (disposed) return Promise.resolve()
      if (pending) return pending
      window.clearTimeout(timer)
      controller = new AbortController()
      setLoading(true)
      pending = fetchPipelineRuns({ page_size: 20 }, { signal: controller.signal })
        .then(({ data }) => {
          if (disposed) return
          const records = data?.results || []
          active = records.some((run) => ACTIVE.has(run.status))
          setRuns(records)
          setError(false)
        })
        .catch(() => { if (!disposed) setError(true) })
        .finally(() => {
          pending = null
          if (disposed) return
          setLoading(false)
          timer = window.setTimeout(() => {
            if (document.visibilityState === 'visible') refresh()
          }, active ? 2000 : 15000)
        })
      return pending
    }
    refreshRef.current = refresh
    const visible = () => { if (document.visibilityState === 'visible') refresh() }
    refresh()
    document.addEventListener('visibilitychange', visible)
    window.addEventListener('srf:processing-run-created', refresh)
    return () => {
      disposed = true
      window.clearTimeout(timer)
      controller?.abort()
      document.removeEventListener('visibilitychange', visible)
      window.removeEventListener('srf:processing-run-created', refresh)
    }
  }, [])
  const refresh = useCallback(() => refreshRef.current(), [])
  return { runs, loading, error, refresh }
}
