import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Drawer, Form, Input, InputNumber, Modal, Select, Space, Tabs, Tag, message } from 'antd'
import { fetchPoolPolicy, savePoolPolicy, initializeJobPolicy, reprocessConfiguration } from '../../api/pools'
import SmartDataTable from '../../components/SmartDataTable'
import { useRole } from '../../contexts/roleState'

const labels = { standards: '投递筛选标准', rules: '部门分配规则', tags: '能力标签', pools: '内部职位池' }
const required = [{ required: true, message: '请填写此项' }]
const empty = { tags: [], pools: [], standards: [], rules: [] }
export default function PositionPoolSettingsTab() {
  const { hasPermission } = useRole()
  const [data, setData] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [kind, setKind] = useState('standards')
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState([])
  const [edit, setEdit] = useState(null)
  const [form] = Form.useForm()
  const policy = data?.policy || empty
  const jobs = data?.job_options || []
  const options = (type) => (policy[type] || []).filter((v) => v.active !== false).map((v) => ({ value: v.code, label: v.name }))
  const load = useCallback(async () => {
    setBusy(true)
    try { const { data: value } = await fetchPoolPolicy(); setData(value); setError('') } catch { setError('配置加载失败，请刷新重试') } finally { setBusy(false) }
  }, [])
  useEffect(() => { load() }, [load])
  const jobName = (id) => { const j = jobs.find((j) => j.id === id); return j ? [j.entity, j.position_name, j.department_name, j.public_name].filter(Boolean).join(' / ') : `岗位 #${id}` }
  const rowKey = (row) => kind === 'rules' ? row.job_id : row.code
  const open = (record, bulk = false) => {
    const initial = record || { code: `p_${crypto.randomUUID().replaceAll('-', '')}`, active: true, application_names: [], tag_codes: [], required_majors: [], required_tags: [], preferred_tags: [], priority: 0 }
    setEdit({ record: initial, bulk }); form.resetFields(); form.setFieldsValue(bulk ? {} : initial)
  }
  const publish = async (next) => {
    const body = { version: data.version, policy: next }
    setBusy(true)
    try {
      const { data: impact } = await savePoolPolicy({ ...body, preview: true })
      Modal.confirm({ title: '保存配置', content: `本次变更影响 ${impact.changed_standard_codes.length} 个筛选标准，${impact.reassessment_count} 名待分配候选人需要重新评估。部门接收和优先级变更由分配任务处理。`, okText: '保存', cancelText: '取消', onOk: async () => {
        const { data: saved } = await savePoolPolicy(body); setData(saved); setEdit(null); setSelected([]); message.success('配置已保存')
      } })
    } finally { setBusy(false) }
  }
  const save = async () => {
    const values = await form.validateFields()
    let records
    if (edit.bulk) {
      const patch = Object.fromEntries(Object.entries(values).filter(([, value]) => value !== undefined))
      if (!Object.keys(patch).length) { message.info('请选择至少一个要修改的字段'); return }
      records = policy[kind].map((row) => selected.includes(rowKey(row)) ? { ...row, ...patch } : row)
    } else {
      const record = { ...edit.record, ...values }
      if (kind === 'standards' && !record.generated) record.entity = policy.pools.find((p) => p.code === record.pool_code)?.entity || ''
      const exists = policy[kind].some((row) => rowKey(row) === rowKey(record))
      records = exists ? policy[kind].map((row) => rowKey(row) === rowKey(record) ? record : row) : [...policy[kind], record]
    }
    await publish({ ...policy, [kind]: records })
  }
  const field = (name, label, control, mandatory = false) => <Form.Item name={name} label={label} rules={mandatory && !edit?.bulk ? required : []}>{control}</Form.Item>
  const columns = [
    { key: 'name', title: kind === 'rules' ? '部门需求' : '名称', render: (_, row) => kind === 'rules' ? jobName(row.job_id) : row.name },
    { key: 'relation', title: '关联', render: (_, row) => kind === 'standards' ? (row.application_names || []).join('、') : kind === 'rules' ? policy.pools.find((p) => p.code === row.pool_code)?.name : kind === 'pools' ? row.entity : row.category },
    { key: 'active', title: '状态', render: (_, row) => <Tag color={row.active === false ? 'default' : 'blue'}>{row.active === false ? '停用' : '启用'}</Tag> },
    ...(kind === 'standards' ? [{ key: 'coverage', title: '配置检查', render: (_, row) => { const c = data.coverage?.find((v) => v.code === row.code); return <><Tag color={c?.status ? c.screening_ready ? 'orange' : 'red' : 'green'}>{c?.status ? c.screening_ready ? '可筛选，分配待配置' : '筛选受阻' : '已就绪'}</Tag><div>{c?.message}</div><small>可达需求：{c?.demand_ids?.map(jobName).join('；') || '尚未配置'}</small></> } }] : []),
    { key: 'actions', valueType: 'option', title: '操作', render: (_, row) => <Button type="link" onClick={() => open(row)}>配置</Button> },
  ]
  return <Space direction="vertical" style={{ width: '100%' }}>
    <Alert showIcon type="info" message="岗位资料维护职责、学历和专业要求；这里维护筛选来源、能力标签和部门关联。" description="新增、修改和导入岗位会同步生成标准与规则草稿。部门规则启用前需要明确能力标签；同一投递存在不同要求时，请选择标准来源。" />
    {error && <Alert type="error" message={error} />}
    <Space wrap>
      <Button loading={busy} onClick={load}>刷新配置</Button>
      <Button aria-label="从当前岗位生成关联" loading={busy} onClick={async () => { setBusy(true); try { const { data: value } = await initializeJobPolicy(); setData(value); message.success('已按当前岗位生成并检查关联') } finally { setBusy(false) } }}>从当前岗位生成关联</Button>
      {hasPermission('pipeline.run') && <Button onClick={() => Modal.confirm({ title: '重新处理配置受阻的候选人', content: '将重新提交因配置受阻或标准变化需要重新评估的候选人，可在任务中心查看进度。', okText: '提交处理', cancelText: '取消', onOk: async () => { const { data: run } = await reprocessConfiguration(); message.success(`已提交任务 #${run.run_id}，共 ${run.candidate_count} 名候选人`); window.dispatchEvent(new Event('srf:processing-run-created')) } })}>修复后批量重新处理</Button>}
    </Space>
    <Tabs activeKey={kind} onChange={(value) => { setKind(value); setSelected([]); setSearch('') }} items={Object.entries(labels).map(([key, label]) => ({ key, label }))} />
    <Space><Input.Search aria-label="搜索配置" placeholder="名称、主体、岗位" value={search} onChange={(e) => setSearch(e.target.value)} allowClear />
      <Button disabled={!data} onClick={() => open()}>新增{labels[kind]}</Button>
      <Button disabled={!selected.length} onClick={() => open(null, true)}>批量配置（{selected.length}）</Button>
    </Space>
    <SmartDataTable key={kind} tableId={`job-policy-${kind}`} search={false} options={false} loading={busy} rowKey={rowKey} rowSelection={{ selectedRowKeys: selected, onChange: setSelected }} columns={columns} dataSource={policy[kind].filter((r) => `${r.name || ''} ${r.entity || ''} ${(r.application_names || []).join(' ')} ${kind === 'rules' ? jobName(r.job_id) : ''}`.toLowerCase().includes(search.toLowerCase()))} scroll={{ x: 900 }} />
    <Drawer title={`${edit?.bulk ? `批量配置 ${selected.length} 项` : '配置'} · ${labels[kind]}`} open={!!edit} onClose={() => setEdit(null)} width={640} extra={<Button type="primary" loading={busy} onClick={save}>预览并保存</Button>}>
      {edit && <Form form={form} layout="vertical">
        {edit.bulk && <Alert message="仅修改填写的字段；标签字段替换原有选择。清空标签请选择空列表。" style={{ marginBottom: 16 }} />}
        {!edit.bulk && kind !== 'rules' && field('name', '名称', <Input disabled={edit.record.generated} />, true)}
        {field('active', '启用状态', <Select allowClear options={[{ value: true, label: '启用' }, { value: false, label: '停用' }]} />)}
        {kind === 'tags' && <>{field('category', '类别', <Select options={[{ value: 'direction', label: '专业方向' }, { value: 'skill', label: '技术技能' }, { value: 'experience', label: '实践经历' }]} />, true)}{field('description', '证据判定说明', <Input.TextArea rows={4} />, true)}</>}
        {kind === 'pools' && field('entity', '招聘主体', <Input disabled={edit.record.generated} />, true)}
        {kind === 'standards' && <>
          {!edit.bulk && edit.record.generated && <>
            {field('source_job_id', '标准来源岗位', <Select allowClear placeholder="要求一致时自动选择；有冲突时请选择" options={(edit.record.source_job_ids || []).map((id) => ({ value: id, label: jobName(id) }))} />)}
            <Alert message="职责、学历和专业要求来自所选岗位。修改要求请在岗位资料中编辑。" description={edit.record.responsibilities || '选择标准来源后生成要求'} />
          </>}
          {!edit.bulk && !edit.record.generated && <>{field('pool_code', '内部职位池', <Select options={options('pools')} />, true)}{field('application_names', '导入投递名称', <Select mode="tags" />, true)}{field('responsibilities', '统一职责和能力要求', <Input.TextArea rows={5} />, true)}{field('education', '学历要求', <Input />)}{field('required_majors', '专业要求', <Select mode="tags" />)}</>}
          {field('tag_codes', '筛选时提取的能力标签', <Select mode="multiple" allowClear options={options('tags')} />)}
        </>}
        {kind === 'rules' && <>
          {!edit.bulk && field('job_id', '部门岗位需求', <Select disabled={!!edit.record.job_id} showSearch optionFilterProp="label" options={jobs.filter((j) => j.is_active).map((j) => ({ value: j.id, label: jobName(j.id) }))} />, true)}
          {field('pool_code', '候选人来源职位池', <Select allowClear options={options('pools')} />, true)}
          {field('required_tags', '必须全部满足的标签', <Select mode="multiple" allowClear options={options('tags')} />)}
          {field('preferred_tags', '优先标签', <Select mode="multiple" allowClear options={options('tags')} />)}
          {field('priority', '同等匹配下的优先级（数字小的优先）', <InputNumber min={0} max={999} precision={0} />)}
        </>}
      </Form>}
    </Drawer>
  </Space>
}
