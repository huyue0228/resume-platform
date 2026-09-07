import { useState } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation, useNavigate } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import useUsagePageView, {
  USAGE_SESSION_TIMEOUT_MS,
  resolveUsagePageKey,
} from './useUsagePageView'
import { reportUsagePageView } from '../api/services'

vi.mock('../api/services', () => ({
  reportUsagePageView: vi.fn(),
}))

function UsageHarness() {
  const location = useLocation()
  const navigate = useNavigate()
  const [, setRenderCount] = useState(0)
  useUsagePageView(location.pathname)
  return (
    <>
      <button type="button" onClick={() => navigate('/resumes?from=dashboard#detail')}>
        前往简历库
      </button>
      <button type="button" onClick={() => setRenderCount((count) => count + 1)}>
        重渲染
      </button>
    </>
  )
}

function renderUsageHarness(initialEntry = '/analytics') {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <UsageHarness />
    </MemoryRouter>,
  )
}

describe('useUsagePageView', () => {
  beforeEach(() => {
    sessionStorage.clear()
    reportUsagePageView.mockReset()
    reportUsagePageView.mockResolvedValue({ ok: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('reports the first page and route changes once while ignoring query and rerenders', async () => {
    const user = userEvent.setup()
    renderUsageHarness('/analytics?range=30d#top')

    await waitFor(() => expect(reportUsagePageView).toHaveBeenCalledTimes(1))
    const first = reportUsagePageView.mock.calls[0][0]
    expect(first.page_key).toBe('/analytics')
    expect(first.event_id).toMatch(/^[0-9a-f-]{36}$/)
    expect(first.session_id).toMatch(/^[0-9a-f-]{36}$/)

    await user.click(screen.getByRole('button', { name: '重渲染' }))
    expect(reportUsagePageView).toHaveBeenCalledTimes(1)

    await user.click(screen.getByRole('button', { name: '前往简历库' }))
    await waitFor(() => expect(reportUsagePageView).toHaveBeenCalledTimes(2))
    const second = reportUsagePageView.mock.calls[1][0]
    expect(second.page_key).toBe('/resumes')
    expect(second.session_id).toBe(first.session_id)
    expect(second.event_id).not.toBe(first.event_id)
  })

  it('reports a refresh as a new event in the existing active session', async () => {
    const firstRender = renderUsageHarness('/jobs')
    await waitFor(() => expect(reportUsagePageView).toHaveBeenCalledTimes(1))
    const first = reportUsagePageView.mock.calls[0][0]
    firstRender.unmount()

    renderUsageHarness('/jobs')
    await waitFor(() => expect(reportUsagePageView).toHaveBeenCalledTimes(2))
    const refreshed = reportUsagePageView.mock.calls[1][0]
    expect(refreshed.session_id).toBe(first.session_id)
    expect(refreshed.event_id).not.toBe(first.event_id)
  })

  it('reports focus and rotates the session after 30 minutes without activity', async () => {
    let now = Date.parse('2026-08-10T00:00:00Z')
    vi.spyOn(Date, 'now').mockImplementation(() => now)
    renderUsageHarness('/departments')
    await waitFor(() => expect(reportUsagePageView).toHaveBeenCalledTimes(1))
    const firstSessionId = reportUsagePageView.mock.calls[0][0].session_id

    now += USAGE_SESSION_TIMEOUT_MS
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(reportUsagePageView).toHaveBeenCalledTimes(2))
    const focused = reportUsagePageView.mock.calls[1][0]
    expect(focused.page_key).toBe('/departments')
    expect(focused.session_id).not.toBe(firstSessionId)
  })

  it('ignores untracked paths and swallows synchronous or asynchronous failures', async () => {
    expect(resolveUsagePageKey('/analytics/')).toBe('/analytics')
    expect(resolveUsagePageKey('/unknown')).toBeNull()

    reportUsagePageView.mockImplementationOnce(() => {
      throw new Error('offline')
    }).mockRejectedValueOnce(new Error('offline again'))
    renderUsageHarness('/users')
    await waitFor(() => expect(reportUsagePageView).toHaveBeenCalledTimes(1))
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(reportUsagePageView).toHaveBeenCalledTimes(2))
  })
})
