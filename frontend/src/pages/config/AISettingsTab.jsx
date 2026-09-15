import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, InputNumber, Space, Switch, Tag, message } from 'antd'
import {
  fetchAIConnectionSettings,
  updateAIConnectionSetting,
} from '../../api/services'
import SmartDataTable from '../../components/SmartDataTable'

function SettingEditor({ record, value, onChange, disabled }) {
  if (record.value_type === 'boolean') {
    return <Switch aria-label={record.label} disabled={disabled} checked={Boolean(value)} onChange={onChange} />
  }
  return (
    <InputNumber
      aria-label={record.label}
      disabled={disabled}
      min={record.min ?? 0}
      max={record.max}
      step={record.value_type === 'number' ? 0.01 : 1}
      precision={record.value_type === 'number' ? 2 : 0}
      value={Number(value)}
      onChange={onChange}
    />
  )
}

export default function AISettingsTab() {
  const [settings, setSettings] = useState([])
  const [drafts, setDrafts] = useState({})
  const [loading, setLoading] = useState(false)
  const [savingKey, setSavingKey] = useState('')
  const [saveErrors, setSaveErrors] = useState([])
  const changed = settings.filter((record) => drafts[record.key] !== record.value)
  const load = useCallback(async () => {
    setLoading(true)
    try {
      const { data } = await fetchAIConnectionSettings()
      const nextSettings = (data?.settings || []).filter((item) => item.section === 'runtime')
      setSettings(nextSettings)
      setDrafts(Object.fromEntries(nextSettings.map((item) => [item.key, item.value])))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const saveChanges = async () => {
    const failures = []
    let savedCount = 0
    setSaveErrors([])
    try {
      for (const record of changed) {
        setSavingKey(record.key)
        try {
          const { data } = await updateAIConnectionSetting(record.key, drafts[record.key])
          setSettings((items) => items.map((item) => (item.key === record.key ? data : item)))
          setDrafts((values) => ({ ...values, [record.key]: data.value }))
          savedCount += 1
        } catch { failures.push(record.label) }
      }
      if (savedCount) message.success(`已保存 ${savedCount} 项运行参数`)
      setSaveErrors(failures)
    } finally {
      setSavingKey('')
    }
  }

  const columns = [
    {
      title: '配置项',
      dataIndex: 'label',
      width: 190,
      fixed: 'left',
      filter: { type: 'text', placeholder: '筛选配置项' },
    },
    {
      title: '键',
      dataIndex: 'key',
      width: 220,
      render: (value) => <Tag color="purple">{value}</Tag>,
    },
    {
      title: '说明',
      dataIndex: 'description',
      ellipsis: true,
    },
    {
      title: '值',
      dataIndex: 'value',
      width: 180,
      render: (_, record) => (
        <SettingEditor
          record={record}
          disabled={Boolean(savingKey)}
          value={drafts[record.key]}
          onChange={(value) => setDrafts((values) => ({ ...values, [record.key]: value }))}
        />
      ),
    },
  ]

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message="AI 运行参数"
        description="这些参数只影响新提交的 AI 处理任务，不改变已经运行或完成的任务。"
      />
      <SmartDataTable
        tableId="ai-settings-runtime"
        rowKey="key"
        loading={loading}
        columns={columns}
        dataSource={settings}
        pagination={false}
        toolBarRender={() => [
          <Button key="reload" disabled={Boolean(savingKey)} onClick={load}>刷新</Button>,
          changed.length > 0 && <Button key="save" type="primary" aria-label={`保存 ${changed.length} 项修改`} aria-busy={Boolean(savingKey)} loading={Boolean(savingKey)} onClick={saveChanges}>保存 {changed.length} 项修改</Button>,
        ].filter(Boolean)}
      />
      {saveErrors.length > 0 && <Alert type="error" showIcon message={`未保存：${saveErrors.join('、')}`} description="已保留未成功项的修改内容，请修正后保存。" />}
    </Space>
  )
}
