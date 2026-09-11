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

  const { system_status: _ignoredSystemStatus, system_statuses: _ignoredStatuses, ...candidateFilters } = lastQuery
  return {
    system_statuses: processStatusSelection,
    candidate_filters: candidateFilters,
  }
}

const FILTER_LABELS = {
  name: '姓名', search: '搜索', highest_major_in: '最高学历专业', current_apply_id: '应聘 ID',
  current_entity_in: '招聘主体', current_position_name_in: '投递岗位', job_department_name_in: '岗位部门',
  current_primary_department_id: '一级部门', current_department_id: '接收部门', current_job_category_in: '岗位类别',
  school_tag_in: '院校标签', allocation_source: '分配来源', feedback_reason_code: '反馈原因',
  current_apply_date_from: '投递开始日期', current_apply_date_to: '投递结束日期',
  processing_run_id: '来源处理任务', processing_result: '处理结果',
}

export function describeProcessingFilters(query = {}) {
  const filters = Object.entries(query).filter(([key, value]) => !['system_status', 'system_statuses', 'ordering', 'page', 'page_size'].includes(key) && value != null && String(value) !== '')
  const result = filters.filter(([key]) => !key.startsWith('analytics_')).map(([key, value]) => `${FILTER_LABELS[key] || '附加条件'}：${Array.isArray(value) ? value.join('、') : value}`)
  if (filters.some(([key]) => key.startsWith('analytics_'))) result.push('保留数据看板下钻范围')
  return result
}
