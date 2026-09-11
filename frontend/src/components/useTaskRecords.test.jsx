import { act, renderHook, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import useTaskRecords from './useTaskRecords'

it('aborts an old query and rejects its stale response after filters change', async () => {
  let first, second
  const fetcher = vi.fn()
    .mockImplementationOnce(() => new Promise((resolve) => { first = resolve }))
    .mockImplementationOnce(() => new Promise((resolve) => { second = resolve }))
  const { result, rerender } = renderHook(({ search }) => useTaskRecords(fetcher, { search }), { initialProps: { search: 'old' } })
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))
  const oldSignal = fetcher.mock.calls[0][1].signal
  rerender({ search: 'new' })
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2))
  expect(oldSignal.aborted).toBe(true)
  await act(async () => { second({ data: { results: [{ id: 2 }], count: 50 } }) })
  await act(async () => { first({ data: { results: [{ id: 1 }], count: 1 } }) })
  expect(result.current.results).toEqual([{ id: 2 }])
  expect(result.current.count).toBe(50)
})
