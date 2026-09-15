import { useRef, useState } from 'react'
import { Alert, Button, Collapse, Descriptions, Drawer, Input, InputNumber, Progress, Select, Space, Table, Typography } from 'antd'
import { buildBulkChanges, runBulkEdit } from './bulkEditUtils'

function FieldInput({ field, value, onChange }) {
  const props = { 'aria-label': field.label, value, onChange, style: { width: '100%' } }
  if (field.type === 'number') return <InputNumber {...props} min={field.min} precision={0} />
  if (field.type === 'select' || field.type === 'boolean' || field.type === 'multiple') {
    return <Select {...props} showSearch optionFilterProp="label" mode={field.type === 'multiple' ? 'multiple' : undefined} options={field.options || [{ value: true, label: '是' }, { value: false, label: '否' }]} placeholder="请选择新值" />
  }
  const TextInput = field.type === 'textarea' ? Input.TextArea : Input
  return <TextInput {...props} rows={3} onChange={(event) => onChange(event.target.value)} placeholder="请输入新值" />
}

// Mount a new drawer per operation so its record scope and revisions stay frozen.
export default function BulkEditDrawer({ config, records, onClose, onComplete }) {
  const [targets, setTargets] = useState(records)
  const [drafts, setDrafts] = useState(() => {
    const initial = typeof config.fields === 'function' ? config.fields(records) : config.fields
    return Object.fromEntries(initial.filter((field) => field.always || initial.length === 1).map((field) => [field.name, { operation: 'set' }]))
  })
  const [preview, setPreview] = useState(null)
  const [results, setResults] = useState(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const savingRef = useRef(false)
  const fields = typeof config.fields === 'function' ? config.fields(targets) : config.fields
  const label = (record) => config.getLabel?.(record) || record.name || record.username || `#${record.id}`
  const failed = results?.filter((item) => !item.success) || []
  const succeeded = results?.filter((item) => item.success).length || 0
  const changeDraft = (name, values) => setDrafts((previous) => ({ ...previous, [name]: { operation: 'keep', ...previous[name], ...values } }))

  const review = () => {
    try {
      const next = buildBulkChanges(fields, drafts)
      const validationError = config.validate?.(next.changes, targets)
      if (validationError) throw new Error(validationError)
      setPreview(next)
      setError('')
    } catch (e) { setError(e.message) }
  }

  const save = async () => {
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    setResults([])
    try {
      const next = await runBulkEdit(targets, preview.changes, config.update, setResults)
      await onComplete(next)
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return <Drawer
    title={config.title || '批量编辑'}
    open
    width={600}
    onClose={saving ? undefined : onClose}
    closable={!saving}
    maskClosable={!saving}
    keyboard={!saving}
    footer={<Space style={{ width: '100%', justifyContent: 'flex-end' }}>
      <Button disabled={saving} onClick={onClose}>{results ? '关闭' : '取消'}</Button>
      {results ? failed.length > 0 && <Button disabled={saving} onClick={() => { setTargets(failed.map((item) => item.record)); setResults(null); setPreview(null) }}>修改未成功项</Button>
        : preview ? <><Button onClick={() => setPreview(null)}>返回修改</Button><Button type="primary" onClick={save}>确认保存 {targets.length} 项</Button></>
          : <Button type="primary" onClick={review}>预览修改</Button>}
    </Space>}
  >
    <Space direction="vertical" size={20} style={{ width: '100%' }}>
      <Alert type="info" showIcon message={`本次修改 ${targets.length} 项（含跨页勾选）`} description="仅修改明确选择的字段。表头勾选只选择当前页，不会自动包含筛选结果中的其他记录。" />
      {config.description && <Typography.Paragraph>{config.description}</Typography.Paragraph>}
      <Collapse items={[{ key: 'scope', label: `查看本次记录（${targets.length}）`, children: <div style={{ maxHeight: 180, overflow: 'auto' }}>{targets.map((record) => <div key={record.id}>{label(record)} · #{record.id}</div>)}</div> }]} />
      {error && <Alert type="error" showIcon message={error} />}
      {results ? <>
        <Progress percent={Math.round(results.length / targets.length * 100)} status={saving ? 'active' : failed.length ? 'exception' : 'success'} />
        <Typography.Text>{saving ? `已处理 ${results.length} / ${targets.length} 项` : `修改完成：成功 ${succeeded} 项，未成功 ${failed.length} 项`}</Typography.Text>
        {failed.length > 0 && <Alert type="warning" showIcon message="成功项已保存，未成功项保留原修改内容" description="请根据原因修正后再提交。遇到版本变化或结果未确认，请关闭侧边栏，刷新并重新勾选记录。" />}
        {failed.length > 0 && <Table size="small" rowKey={(item) => item.record.id} dataSource={failed} pagination={{ pageSize: 10 }} columns={[{ title: '记录', render: (_, item) => `${label(item.record)} · #${item.record.id}` }, { title: '未成功原因', dataIndex: 'error' }]} />}
      </> : preview ? <>
        <Typography.Title level={5}>将以下修改应用到 {targets.length} 项</Typography.Title>
        <Descriptions column={1} bordered size="small" items={preview.summary.map((item) => ({ key: item.label, label: item.label, children: <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{item.value}</span> }))} />
        <Typography.Text type="secondary">记录逐条保存并校验权限；未成功项会单独列出。</Typography.Text>
      </> : <>
      {fields.some((field) => !field.always) && <div>
        <Typography.Paragraph strong>选择需要修改的字段</Typography.Paragraph>
        <Select aria-label="需要修改的字段" mode="multiple" style={{ width: '100%' }} placeholder="选择字段，未选择的字段保持原值" options={fields.filter((field) => !field.always).map((field) => ({ value: field.name, label: field.label }))}
          value={fields.filter((field) => !field.always && drafts[field.name]).map((field) => field.name)}
          onChange={(names) => setDrafts((previous) => Object.fromEntries(fields.filter((field) => field.always || names.includes(field.name)).map((field) => [field.name, previous[field.name] || { operation: 'set' }])))} />
      </div>}
      {fields.filter((field) => drafts[field.name]).map((field) => <section key={field.name}>
        <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }}>
          <Typography.Text strong>{field.label}</Typography.Text>
          {Object.hasOwn(field, 'clearValue') && <Select aria-label={`${field.label}修改方式`} value={drafts[field.name]?.operation || 'set'} style={{ width: 150 }} onChange={(operation) => changeDraft(field.name, { operation })} options={[
            { value: 'set', label: '设置为' }, { value: 'clear', label: field.clearLabel || '清空' },
          ]} />}
        </Space>
        {drafts[field.name]?.operation === 'set' && <FieldInput field={field} value={drafts[field.name]?.value} onChange={(value) => changeDraft(field.name, { value })} />}
        {field.help && drafts[field.name]?.operation !== 'keep' && drafts[field.name]?.operation && <div><Typography.Text type="secondary">{field.help}</Typography.Text></div>}
      </section>)}
      </>}
    </Space>
  </Drawer>
}
