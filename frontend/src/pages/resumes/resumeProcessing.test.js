import { describe, expect, it } from 'vitest'
import { buildResumeProcessingScope } from './resumeProcessing'

describe('buildResumeProcessingScope', () => {
  it('uses only the frozen candidate ids for current selection reprocessing', () => {
    expect(buildResumeProcessingScope({
      processCurrentSelected: true,
      processCandidateSnapshot: [3, 5],
      processStatusSelection: ['raw'],
      lastQuery: { name: '张三', system_status: 'raw' },
    })).toEqual({
      candidate_ids: [3, 5],
      force_reprocess: true,
    })
  })

  it('removes the table status while preserving the other candidate filters', () => {
    expect(buildResumeProcessingScope({
      processCurrentSelected: false,
      processCandidateSnapshot: [3, 5],
      processStatusSelection: ['screening_passed'],
      lastQuery: {
        name: '张三',
        system_status: 'raw',
        processing_run_id: '18',
        processing_result: 'success',
      },
    })).toEqual({
      system_statuses: ['screening_passed'],
      candidate_filters: {
        name: '张三',
        processing_run_id: '18',
        processing_result: 'success',
      },
    })
  })
})
