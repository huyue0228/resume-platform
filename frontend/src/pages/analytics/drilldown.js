const FILTER_KEYS = [
  'date_from',
  'date_to',
  'entity',
  'job_id',
  'primary_department_id',
  'department_id',
  'school_tag_id',
  'education',
  'source',
]

function optionLabel(items, value) {
  return (items || []).find((item) => String(item.value) === String(value))?.label
}

export function drilldownItems(row) {
  if (!row) return []
  return row.drilldownItems?.length ? row.drilldownItems : [row]
}

export function buildAnalyticsContext(filters = {}, options = {}) {
  const parts = []
  if (filters.date_from && filters.date_to) {
    parts.push(`导入时间 ${filters.date_from} 至 ${filters.date_to}`)
  }
  if (filters.entity) parts.push(`主体 ${filters.entity}`)
  if (filters.job_id) {
    parts.push(`岗位 ${optionLabel(options.jobs, filters.job_id) || filters.job_id}`)
  }
  if (filters.primary_department_id) {
    parts.push(
      `一级部门 ${optionLabel(options.primary_departments, filters.primary_department_id) || filters.primary_department_id}`,
    )
  }
  if (filters.department_id) {
    parts.push(`二级部门 ${optionLabel(options.departments, filters.department_id) || filters.department_id}`)
  }
  if (filters.school_tag_id) {
    parts.push(`院校标签 ${optionLabel(options.school_tags, filters.school_tag_id) || filters.school_tag_id}`)
  }
  if (filters.education) {
    parts.push(`最高学历 ${optionLabel(options.educations, filters.education) || filters.education}`)
  }
  if (filters.source) {
    parts.push(`分配来源 ${optionLabel(options.sources, filters.source) || filters.source}`)
  }
  return parts.join('；')
}

export function buildAnalyticsDrilldownLocation({
  filters = {},
  options = {},
  dimension,
  row,
  title,
}) {
  const params = new URLSearchParams()
  FILTER_KEYS.forEach((key) => {
    const value = filters[key]
    if (value !== undefined && value !== null && value !== '') {
      params.set(`analytics_${key}`, String(value))
    }
  })
  params.set('analytics_dimension', dimension)
  const items = drilldownItems(row)
  if (items.length) {
    params.set('analytics_values', JSON.stringify(items.map((item) => item.key)))
    params.set('analytics_value_labels', JSON.stringify(items.map((item) => item.label || '')))
  }
  const itemLabel = items.length === 1 ? items[0].label : row?.label
  params.set('analytics_title', itemLabel ? `${title} · ${itemLabel}` : title)
  const context = buildAnalyticsContext(filters, options)
  if (context) params.set('analytics_context', context)
  return { pathname: '/resumes', search: `?${params.toString()}` }
}
