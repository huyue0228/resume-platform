import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fetchPipelineRuns, runPipeline } from '../api/services'
import { useProcessRunner } from './useProcessRunner'
import useProcessingRuns from './useProcessingRuns'

vi.mock('../api/services', () => ({
  fetchPipelineRuns: vi.fn(),
  runPipeline: vi.fn(),
}))

describe('useProcessRunner', () => {
  beforeEach(() => {
    vi.resetAllMocks()
  })

  it('submits the selected scope and immediately refreshes the shared task center', async () => {
    const task = { id: 18, step: 'step2', status: 'pending' }
    fetchPipelineRuns
      .mockResolvedValueOnce({ data: { results: [] } })
      .mockResolvedValue({ data: { results: [task] } })
    runPipeline.mockResolvedValue({ data: { processing_runs: [task] } })
    const { result } = renderHook(() => ({
      runner: useProcessRunner(),
      tasks: useProcessingRuns(),
    }))
    await waitFor(() => expect(result.current.tasks.loading).toBe(false))

    let response
    await act(async () => {
      response = await result.current.runner.run([{ step: 'step2' }], '', {
        scope: { candidate_ids: [1, 2], force_reprocess: true },
      })
    })

    expect(runPipeline).toHaveBeenCalledExactlyOnceWith({
      step: 'step2', scope: { candidate_ids: [1, 2], force_reprocess: true },
    })
    expect(response.success).toBe(true)
    await waitFor(() => expect(result.current.tasks.runs).toEqual([task]))
  })

  it('returns the API rejection reason without announcing a new task', async () => {
    runPipeline.mockRejectedValue({
      response: { status: 409, data: { detail: 'Agent Kernel 或模型连接尚未就绪' } },
    })
    const listener = vi.fn()
    window.addEventListener('srf:processing-run-created', listener)
    try {
      const { result } = renderHook(() => useProcessRunner())
      let response
      await act(async () => { response = await result.current.run([{ step: 'step2' }]) })

      expect(response).toEqual({ success: false, error: 'Agent Kernel 或模型连接尚未就绪' })
      expect(result.current.submitting).toBe(false)
      expect(listener).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('srf:processing-run-created', listener)
    }
  })
})
