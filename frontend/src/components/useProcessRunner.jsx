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
    if (!step) return { success: false }
    setSubmitting(true)
    try {
      const scope = normalizedSteps[0]?.scope || options.scope
      const { data } = await runPipeline({
        step,
        ...(scope ? { scope } : {}),
      })
      return { success: true, run: data }
    } catch {
      return { success: false }
    } finally {
      setSubmitting(false)
    }
  }

  return { run, submitting }
}
