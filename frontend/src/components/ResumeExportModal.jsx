import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Button, Checkbox, Modal, Space, Spin, Typography } from 'antd'
import { fetchCandidateExportFields } from '../api/services'

const STORAGE_PREFIX = 'srf.resume-export-fields'

function storageKey(userKey) {
  return `${STORAGE_PREFIX}:${String(userKey || 'anonymous')}`
}

function orderedKeys(groups, keys) {
  const selected = new Set(keys)
  return groups.flatMap((group) => group.fields || [])
    .map((field) => field.key)
    .filter((key) => selected.has(key))
}

function defaultKeys(groups) {
  return groups.flatMap((group) => group.fields || [])
    .filter((field) => field.default_selected)
    .map((field) => field.key)
}

function resolveRememberedFields(groups, userKey) {
  const defaults = defaultKeys(groups)
  try {
    const remembered = JSON.parse(localStorage.getItem(storageKey(userKey)) || 'null')
    const valid = orderedKeys(groups, Array.isArray(remembered?.fields) ? remembered.fields : [])
    return valid.length ? valid : defaults
  } catch {
    return defaults
  }
}

export default function ResumeExportModal({
  open,
  userKey,
  exporting = false,
  onCancel,
  onExport,
}) {
  const [catalog, setCatalog] = useState({ version: null, groups: [] })
  const [selected, setSelected] = useState([])
  const [includeResumeFiles, setIncludeResumeFiles] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const loadCatalog = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const { data } = await fetchCandidateExportFields()
      const groups = Array.isArray(data?.groups) ? data.groups : []
      setCatalog({ version: data?.version ?? null, groups })
      setSelected(resolveRememberedFields(groups, userKey))
    } catch (requestError) {
      setCatalog({ version: null, groups: [] })
      setSelected([])
      setError(requestError?.response?.data?.detail || '导出字段加载失败')
    } finally {
      setLoading(false)
    }
  }, [userKey])

  useEffect(() => {
    if (open) {
      setIncludeResumeFiles(false)
      loadCatalog()
    }
  }, [loadCatalog, open])

  const allKeys = useMemo(
    () => catalog.groups.flatMap((group) => group.fields || []).map((field) => field.key),
    [catalog.groups],
  )
  const defaults = useMemo(() => defaultKeys(catalog.groups), [catalog.groups])

  const toggleField = (key, checked) => {
    const next = new Set(selected)
    if (checked) next.add(key)
    else next.delete(key)
    setSelected(orderedKeys(catalog.groups, next))
  }

  const confirm = () => {
    const fields = orderedKeys(catalog.groups, selected)
    if (!fields.length) return
    try {
      localStorage.setItem(storageKey(userKey), JSON.stringify({
        version: catalog.version,
        fields,
      }))
    } catch {
      // 浏览器禁用本地存储时仍允许本次导出。
    }
    onExport(fields, includeResumeFiles)
  }

  return (
    <Modal
      title="选择简历导出属性"
      open={open}
      width={760}
      okText={includeResumeFiles ? '导出 ZIP' : '导出 Excel'}
      cancelText="取消"
      confirmLoading={exporting}
      okButtonProps={{ disabled: loading || Boolean(error) || selected.length === 0 }}
      onOk={confirm}
      onCancel={() => {
        if (!exporting) onCancel()
      }}
    >
      <Spin spinning={loading}>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Typography.Text type="secondary">
            {includeResumeFiles
              ? 'ZIP 将包含简历原件和一张按候选人汇总的 Excel 清单。字段按固定目录顺序写入。'
              : '将下载一张按候选人汇总的 Excel 清单，不包含简历原件。字段按固定目录顺序写入。'}
          </Typography.Text>
          {error ? (
            <Alert
              type="error"
              showIcon
              message={error}
              action={<Button size="small" onClick={loadCatalog}>重试</Button>}
            />
          ) : null}
          {!error && !loading ? (
            <>
              <Space wrap>
                <Button size="small" onClick={() => setSelected(allKeys)}>全选</Button>
                <Button size="small" onClick={() => setSelected([])}>清空</Button>
                <Button size="small" onClick={() => setSelected(defaults)}>恢复默认</Button>
                <Typography.Text type="secondary">已选择 {selected.length} 项</Typography.Text>
              </Space>
              <Checkbox
                checked={includeResumeFiles}
                onChange={(event) => setIncludeResumeFiles(event.target.checked)}
              >
                同时下载简历原件
              </Checkbox>
              {catalog.groups.map((group) => (
                <section key={group.key} aria-label={group.label}>
                  <Typography.Title level={5} style={{ margin: '4px 0 8px' }}>
                    {group.label}
                  </Typography.Title>
                  <Space wrap size={[16, 8]}>
                    {(group.fields || []).map((field) => (
                      <Checkbox
                        key={field.key}
                        checked={selected.includes(field.key)}
                        onChange={(event) => toggleField(field.key, event.target.checked)}
                      >
                        {field.label}
                      </Checkbox>
                    ))}
                  </Space>
                </section>
              ))}
            </>
          ) : null}
        </Space>
      </Spin>
    </Modal>
  )
}
