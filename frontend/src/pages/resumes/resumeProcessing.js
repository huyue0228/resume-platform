export function buildResumeProcessingScope({
  processCurrentSelected,
  processCandidateSnapshot,
  processStatusSelection,
  lastQuery,
}) {
  if (processCurrentSelected) {
    return {
      candidate_ids: processCandidateSnapshot,
      force_reprocess: true,
    }
  }

  const { system_status: _ignoredSystemStatus, ...candidateFilters } = lastQuery
  return {
    system_statuses: processStatusSelection,
    candidate_filters: candidateFilters,
  }
}
