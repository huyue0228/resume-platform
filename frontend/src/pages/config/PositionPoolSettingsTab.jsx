import { useEffect, useState } from 'react'
import { Alert, Button, Collapse, Form, Input, InputNumber, Select, Space, Switch, Tabs, message } from 'antd'
import { fetchPoolPolicy, savePoolPolicy } from '../../api/pools'
import TableRowActions from '../../components/TableRowActions'

const required = [{ required: true, message: '请填写此项' }]
const categories = [{ value: 'direction', label: '专业方向' }, { value: 'skill', label: '技术技能' }, { value: 'experience', label: '实践经历' }]
const id = () => `p_${Array.from(crypto.getRandomValues(new Uint8Array(16)), (v) => v.toString(16).padStart(2, '0')).join('')}`

export default function PositionPoolSettingsTab() {
  const [form] = Form.useForm()
  const [version, setVersion] = useState(null)
  const [saved, setSaved] = useState(new Set())
  const [jobs, setJobs] = useState([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(false)
  const values = Form.useWatch([], form) || {}
  const options = (kind) => (values[kind] || []).filter((v) => v?.active !== false).map((v) => ({ value: v.code, label: v.name || '未命名' }))
  const install = (data) => {
    setVersion(data.version)
    setSaved(new Set(['tags', 'pools', 'standards'].flatMap((k) => (data.policy[k] || []).map((v) => v.code))))
    form.setFieldsValue(data.policy)
  }
  useEffect(() => {
    let active = true
    setBusy(true)
    fetchPoolPolicy().then((policy) => {
      if (active) { install(policy.data); setJobs(policy.data.job_options || []); setError(false) }
    }).catch(() => { if (active) setError(true) }).finally(() => { if (active) setBusy(false) })
    return () => { active = false }
  }, [form]) // eslint-disable-line react-hooks/exhaustive-deps
  const field = (index, name, label, control = <Input />, mandatory = false) => <Form.Item key={name} name={[index, name]} label={label} rules={mandatory ? required : []} valuePropName={name === "active" ? "checked" : "value"}>{control}</Form.Item>
  const list = (kind, label, children) => <Form.List name={kind}>{(fields, { add, remove }) => <Space direction="vertical" style={{ width: '100%' }}>
    {fields.map(({ key, name }) => <Collapse key={key} style={{ width: '100%' }} items={[{
      key: String(key),
      label: <Space><strong>{values[kind]?.[name]?.name || (kind === 'rules' ? jobs.find((job) => job.id === values[kind]?.[name]?.job_id)?.position_name : '') || `${label} ${name + 1}`}</strong><span style={{ color: 'var(--srf-muted)' }}>{values[kind]?.[name]?.active === false ? '已停用' : '已启用'}</span></Space>,
      forceRender: true,
      extra: <div onClick={(event) => event.stopPropagation()}><TableRowActions><Space><span>启用 <Switch aria-label={`启用${label} ${name + 1}`} checked={values[kind]?.[name]?.active !== false} onChange={(active) => form.setFieldValue([kind, name, 'active'], active)} /></span>{!saved.has(values[kind]?.[name]?.code) && <Button danger onClick={() => remove(name)}>移除</Button>}</Space></TableRowActions></div>,
      children: <><Form.Item name={[name, 'code']} hidden><Input /></Form.Item><Form.Item name={[name, 'active']} hidden valuePropName="checked"><Switch /></Form.Item>{children(name)}</>,
    }]} />)}
    <Button onClick={() => add({ code: id(), active: true, application_names: [], tag_codes: [], required_majors: [], required_tags: [], preferred_tags: [], priority: 0 })}>新增{label}</Button>
  </Space>}</Form.List>
  const save = async (policy) => {
    setBusy(true)
    try {
      policy.standards = (policy.standards || []).map((s) => ({ ...s, entity: policy.pools.find((p) => p.code === s.pool_code)?.entity || '' }))
      policy.rules = (policy.rules || []).map(({ code: _code, ...rule }) => rule)
      const { data } = await savePoolPolicy({ version, policy })
      install(data); message.success('职位池配置已保存；评估标准和标签定义变更后，需要重新评估受影响候选人')
    } catch { /* API client displays the validation error; keep edits for correction. */ } finally { setBusy(false) }
  }
  return <Form form={form} layout="vertical" onFinish={save} initialValues={{ tags: [], pools: [], standards: [], rules: [] }}>
    <Alert type={error ? 'error' : 'info'} showIcon message={error ? '配置加载失败，请刷新页面' : '配置顺序：能力标签 → 内部职位池 → 投递评估标准 → 部门分配规则'} description="投递标准用于判断是否入池；部门规则在入池后按标签、优先级和需求供给分配。已保存的标识保留历史，可停用。" style={{ marginBottom: 16 }} />
    <Tabs items={[
      { key: 'tags', label: '能力标签', forceRender: true, children: list('tags', '能力标签', (n) => <>{field(n, 'name', '标签名称', <Input />, true)}{field(n, 'category', '类别', <Select options={categories} />, true)}{field(n, 'description', '证据判定说明', <Input.TextArea placeholder="例如：简历明确描述使用 SolidWorks 完成三维建模；仅提到软件名称不足以确认" />, true)}</>) },
      { key: 'pools', label: '内部职位池', forceRender: true, children: list('pools', '内部职位池', (n) => <>{field(n, 'name', '内部职位名称', <Input />, true)}{field(n, 'entity', '招聘主体', <Input />, true)}</>) },
      { key: 'standards', label: '投递评估标准', forceRender: true, children: list('standards', '投递评估标准', (n) => <>{field(n, 'name', '评估标准名称', <Input />, true)}{field(n, 'pool_code', '通过后进入的内部职位池', <Select options={options('pools')} />, true)}{field(n, 'application_names', '导入投递名称映射', <Select mode="tags" placeholder="填写导入记录中的投递名称，回车添加；可关联多个名称" />, true)}{field(n, 'responsibilities', '当前投递的统一职责和能力要求', <Input.TextArea rows={5} />, true)}{field(n, 'education', '学历要求')}{field(n, 'required_majors', '专业要求', <Select mode="tags" />)}{field(n, 'tag_codes', '评估时提取的能力标签', <Select mode="multiple" options={options('tags')} />)}</>) },
      { key: 'rules', label: '部门分配规则', forceRender: true, children: list('rules', '分配规则', (n) => <>{field(n, 'pool_code', '候选人来源职位池', <Select options={options('pools')} />, true)}{field(n, 'job_id', '部门岗位需求', <Select showSearch optionFilterProp="label" options={jobs.map((j) => ({ value: j.id, label: [j.entity, j.position_name, j.department_name, j.public_name].filter(Boolean).join(' / ') }))} />, true)}{field(n, 'required_tags', '必需标签（全部满足）', <Select mode="multiple" options={options('tags')} />)}{field(n, 'preferred_tags', '优先标签（命中越多越优先）', <Select mode="multiple" options={options('tags')} />)}{field(n, 'priority', '同分优先级（数字小的优先）', <InputNumber min={0} max={999} precision={0} />)}</>) },
    ]} />
    <Button type="primary" htmlType="submit" loading={busy} disabled={version === null || error} style={{ marginTop: 20 }}>保存职位池配置</Button>
  </Form>
}
