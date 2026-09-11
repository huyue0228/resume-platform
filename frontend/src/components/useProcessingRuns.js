import { fetchPipelineRuns } from '../api/services'
import useTaskRecords from './useTaskRecords'

const ACTIVE = new Set(['pending', 'running', 'waiting_conflict', 'cancelling'])

export default function useProcessingRuns(params = {}, enabled = true) {
  const records = useTaskRecords(fetchPipelineRuns, { page_size: 20, ...params }, { enabled, eventName: 'srf:processing-run-created', activeStatus: ACTIVE })
  return { ...records, runs: records.results }
}
