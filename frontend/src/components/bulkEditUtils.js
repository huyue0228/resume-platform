export function buildBulkChanges(fields, drafts) {
  const changes = {}
  const summary = []
  for (const field of fields) {
    const draft = drafts[field.name]
    if (!draft || draft.operation === 'keep') continue
    let value = draft.value
    if (draft.operation === 'clear') {
      if (!Object.hasOwn(field, 'clearValue')) throw new Error(`${field.label}不能清空`)
      value = field.clearValue
    } else {
      value = field.normalize ? field.normalize(value) : value
      if (value === undefined || value === null || (typeof value === 'string' && !value.trim()) || (Array.isArray(value) && !value.length)) {
        throw new Error(`请填写${field.label}；需要清空时请选择“清空”`)
      }
      if (field.type === 'number' && (!Number.isInteger(value) || (field.min != null && value < field.min))) {
        throw new Error(`${field.label}请输入有效整数`)
      }
    }
    changes[field.name] = value
    const display = (v) => field.options?.find((option) => option.value === v)?.label ?? (typeof v === 'boolean' ? (v ? '是' : '否') : String(v))
    summary.push({ label: field.label, value: draft.operation === 'clear' ? '清空' : Array.isArray(value) ? value.map(display).join('、') : display(value) })
  }
  if (!Object.keys(changes).length) throw new Error('请至少选择一个要修改的字段')
  return { changes, summary }
}

export function bulkErrorMessage(error) {
  const detail = error?.response?.data?.detail ?? error?.response?.data?.message
  if (detail && typeof detail === 'object') return Object.entries(detail).map(([key, value]) => `${key}：${Array.isArray(value) ? value.join('、') : value}`).join('；')
  if (detail) return String(detail)
  if (!error?.response) return '请求结果未确认，请刷新记录核对后再修改'
  return '保存失败，请刷新后重试'
}

export async function runBulkEdit(records, changes, update, onProgress) {
  const results = []
  let sessionExpired = false
  for (const record of records) {
    if (sessionExpired) {
      results.push({ record, success: false, error: '登录已失效，本条未执行' })
    } else {
      try {
        await update(record, changes)
        results.push({ record, success: true })
      } catch (error) {
        sessionExpired = error?.response?.status === 401
        results.push({ record, success: false, error: bulkErrorMessage(error) })
      }
    }
    onProgress?.([...results])
  }
  return results
}
