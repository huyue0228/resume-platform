import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, InputNumber, Space, Switch, Tag, message } from 'antd'
import {
  fetchAIConnectionSettings,
  updateAIConnectionSetting,
} from '../../api/services'
import SmartDataTable from '../../components/SmartDataTable'

function SettingEditor({ record, value, onChange }) {
  if (record.value_type === 'boolean') {
    return <Switch checked={Boolean(value)} onChange={onChange} />
  }
  return (
    <InputNumber
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

  const save = async (record) => {
    setSavingKey(record.key)
    try {
      const { data } = await updateAIConnectionSetting(record.key, drafts[record.key])
      setSettings((items) => items.map((item) => (item.key === record.key ? data : item)))
      setDrafts((values) => ({ ...values, [record.key]: data.value }))
      message.success('AI 配置已保存')
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
          value={drafts[record.key]}
          onChange={(value) => setDrafts((values) => ({ ...values, [record.key]: value }))}
        />
      ),
    },
    {
      title: '操作',
      key: 'action',
      width: 100,
      render: (_, record) => (
        <Button
          type="primary"
          size="small"
          loading={savingKey === record.key}
          onClick={() => save(record)}
        >
          保存
        </Button>
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
        toolBarRender={() => [<Button key="reload" onClick={load}>刷新</Button>]}
      />
    </Space>
  )
}
