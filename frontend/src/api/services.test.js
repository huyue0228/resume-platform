import { afterEach, describe, expect, it, vi } from 'vitest'
import client from './client'
import {
  bulkTransferCandidates,
  fetchFeedbackReasons,
  fetchManualAssignmentOptions,
  fetchTransferOptions,
  reportUsagePageView,
  transferAllocation,
} from './services'

vi.mock('./client', () => ({
  default: {
    get: vi.fn(),
    post: vi.fn(),
  },
}))

describe('reportUsagePageView', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('posts the token-authenticated event through the silent fetch path', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem('srf_token', 'dev-token')
    const body = {
      event_id: 'fd072741-3d0a-4606-8d11-e79c496e5acc',
      session_id: '01fcffbc-472d-467a-a755-17d166744db0',
      page_key: '/analytics',
    }

    await reportUsagePageView(body)

    expect(fetchMock).toHaveBeenCalledWith('/api/analytics/usage/page-view/', {
      method: 'POST',
      credentials: 'same-origin',
      headers: {
        'Content-Type': 'application/json',
        Authorization: 'Token dev-token',
      },
      body: JSON.stringify(body),
    })
  })
})

describe('department inbox workflow services', () => {
  it('uses the department transfer and feedback option contracts', () => {
    transferAllocation(12, { target_department_id: 8, note: '转专业组' })
    fetchTransferOptions(12)
    fetchManualAssignmentOptions()
    fetchFeedbackReasons()
    bulkTransferCandidates({ candidate_ids: [1, 2], target_department_id: 8 })

    expect(client.post).toHaveBeenCalledWith('/workflow-attempts/12/transfer/', {
      target_department_id: 8,
      note: '转专业组',
    })
    expect(client.get).toHaveBeenCalledWith('/workflow-attempts/12/transfer-options/')
    expect(client.get).toHaveBeenCalledWith('/resumes/manual-assignment-options/')
    expect(client.get).toHaveBeenCalledWith('/workflow-attempts/feedback-reasons/')
    expect(client.post).toHaveBeenCalledWith('/candidates/bulk-transfer/', {
      candidate_ids: [1, 2],
      target_department_id: 8,
    })
  })
})
