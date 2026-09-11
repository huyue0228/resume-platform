import { useState } from 'react'
import { runPipeline } from '../api/services'

// 仅负责提交后台任务。进度由 BasicLayout 的共享任务中心轮询，不再用 Modal 冻结当前页面。
export function useProcessRunner() {
  const [submitting, setSubmitting] = useState(false)

  const run = async (steps, _title, options = {}) => {
    const normalizedSteps = steps || []
    const isResumeProcess =
      normalizedSteps.length === 2 &&
      normalizedSteps[0]?.step === 'step1' &&
      normalizedSteps[1]?.step === 'step2'
    const step = isResumeProcess ? 'resume_process' : normalizedSteps[0]?.step
    if (!step) return { success: false, error: '未选择处理步骤' }
    setSubmitting(true)
    try {
      const scope = normalizedSteps[0]?.scope || options.scope
      const { data } = await runPipeline({
        step,
        ...(options.name ? { name: options.name } : {}),
        ...(scope ? { scope } : {}),
      })
      window.dispatchEvent(new Event('srf:processing-run-created'))
      return { success: true, run: data }
    } catch (error) {
      if (['ECONNABORTED', 'ETIMEDOUT'].includes(error?.code)
          || [502, 504].includes(error?.response?.status)) {
        // 请求超时不能证明服务端未创建任务，先刷新任务中心供用户确认。
        window.dispatchEvent(new Event('srf:processing-run-created'))
        return {
          success: false,
          error: '提交请求超时，任务可能已创建。请先查看任务中心确认，避免重复提交。',
        }
      }
      const detail = error?.response?.data?.detail || error?.response?.data?.message
        || error?.message || '提交处理任务失败，请重试'
      return { success: false, error: String(detail) }
    } finally {
      setSubmitting(false)
    }
  }

  return { run, submitting }
}
