import { useCallback, useEffect, useState } from 'react'
import { Alert, AutoComplete, Button, Form, Input, Popconfirm, Select, Space, Tag, Typography, message } from 'antd'
import { fetchAIConnection, fetchAIModels, testAIConnection, updateAIConnection } from '../../api/services'

const structuredModeMeta = {
  strict_schema: { color: 'success', label: '严格结构化' },
  json_compat: { color: 'processing', label: 'JSON 兼容' },
  legacy_compat: { color: 'warning', label: '结构化能力待重测' },
}

function getStructuredModeMeta(mode) {
  return structuredModeMeta[mode] || structuredModeMeta.legacy_compat
}

export default function AIConnectionTab() {
  const [form] = Form.useForm()
  const [connection, setConnection] = useState(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [fetchingModels, setFetchingModels] = useState(false)
  const [modelOptions, setModelOptions] = useState([])
  const [testResult, setTestResult] = useState(null)
  const modeMeta = getStructuredModeMeta(connection?.structured_output_mode)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const { data } = await fetchAIConnection()
      setConnection(data)
      form.setFieldsValue({
        api_style: data.api_style,
        model_name: data.model_name,
        base_url: data.base_url,
        api_key: '',
      })
    } finally {
      setLoading(false)
    }
  }, [form])

  useEffect(() => {
    load()
  }, [load])

  const save = async (clearApiKey = false) => {
    const values = await form.validateFields()
    setSaving(true)
    try {
      const { data } = await updateAIConnection({ ...values, clear_api_key: clearApiKey })
      setConnection(data)
      form.setFieldValue('api_key', '')
      setTestResult(null)
      message.success(clearApiKey ? '已清除已保存的访问令牌' : '模型连接配置已保存')
    } finally {
      setSaving(false)
    }
  }

  const loadModels = async () => {
    await form.validateFields(['base_url'])
    setFetchingModels(true)
    try {
      const { data } = await fetchAIModels({
        base_url: form.getFieldValue('base_url'),
        api_key: form.getFieldValue('api_key') || '',
      })
      if (data.code) {
        setModelOptions([])
        message.error(data.detail || '获取模型列表失败')
        return
      }
      const models = data.models || []
      setModelOptions(models.map((name) => ({ label: name, value: name })))
      if (!form.getFieldValue('model_name') && models.length === 1) {
        form.setFieldValue('model_name', models[0])
      }
      message.success(`已获取 ${models.length} 个模型`)
    } finally {
      setFetchingModels(false)
    }
  }

  const test = async () => {
    const values = await form.validateFields()
    setTesting(true)
    try {
      const { data: savedConnection } = await updateAIConnection({ ...values, clear_api_key: false })
      setConnection(savedConnection)
      form.setFieldValue('api_key', '')
      const { data } = await testAIConnection()
      setTestResult(data)
      if (data.ok) {
        setConnection((previous) => ({
          ...previous,
          test_passed: true,
          tested_at: data.tested_at,
          structured_output_mode: data.structured_output_mode,
        }))
        message.success('模型连接测试成功')
      }
      else {
        setConnection((previous) => ({ ...previous, test_passed: false, tested_at: '' }))
        message.error(data.detail || '模型连接测试失败')
      }
    } finally {
      setTesting(false)
    }
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 760 }}>
      <Alert
        type="info"
        showIcon
        message="填写内网模型 Base URL 后可获取支持的模型。访问令牌为可选项，保存后服务端密文存储且不会回显。"
      />
      <Form form={form} layout="vertical" disabled={loading}>
        <Form.Item
          name="base_url"
          label="Base URL"
          rules={[
            { required: true, message: '请输入 Base URL' },
            { type: 'url', message: '请输入完整的 http(s) 地址' },
          ]}
        >
          <Input placeholder="例如 http://model-gateway.internal/v1" onChange={() => setModelOptions([])} />
        </Form.Item>
        <Form.Item name="api_key" label="API Key / 访问令牌（可选）">
          <Input.Password
            autoComplete="new-password"
            placeholder={connection?.api_key_configured
              ? '已配置；地址不变可留空，修改地址需重新输入'
              : '无鉴权的内网服务可留空'}
            onChange={() => setModelOptions([])}
          />
        </Form.Item>
        <Form.Item name="api_style" label="API 风格" rules={[{ required: true }]}>
          <Select
            options={[
              { label: 'Responses API', value: 'responses' },
              { label: 'Chat Completions（JSON）', value: 'chat_json' },
            ]}
          />
        </Form.Item>
        <Form.Item
          name="model_name"
          label={(
            <Space>
              <span>模型名称</span>
              <Button type="link" size="small" loading={fetchingModels} onClick={loadModels}>
                获取模型
              </Button>
            </Space>
          )}
          rules={[{ required: true, message: '请选择或输入模型名称' }]}
          extra="模型服务应提供 OpenAI 兼容的 GET /models；也可以直接输入模型 ID。"
        >
          <AutoComplete
            options={modelOptions}
            filterOption
            placeholder="选择或输入模型 ID，例如 deepseek-v4"
          />
        </Form.Item>
      </Form>
      <Space wrap>
        <Tag color={connection?.api_key_configured ? 'success' : 'default'}>
          {connection?.api_key_configured ? '访问令牌已配置' : '未配置访问令牌（允许）'}
        </Tag>
        <Tag color={connection?.test_passed ? 'success' : 'warning'}>
          {connection?.test_passed ? '当前连接已测试通过' : '当前连接尚未测试通过'}
        </Tag>
        <Tag color={modeMeta.color}>{modeMeta.label}</Tag>
        <Button type="primary" loading={saving} onClick={() => save(false)}>
          保存连接配置
        </Button>
        <Button loading={testing} onClick={test}>保存并测试连接</Button>
        {connection?.api_key_configured && (
          <Popconfirm title="清除已保存的访问令牌？" onConfirm={() => save(true)}>
            <Button danger loading={saving}>清除访问令牌</Button>
          </Popconfirm>
        )}
      </Space>
      {testResult && (
        <Alert
          type={testResult.ok ? 'success' : 'error'}
          showIcon
          message={testResult.detail}
          description={testResult.ok
            ? `API 风格：${testResult.api_style}；结构化模式：${getStructuredModeMeta(testResult.structured_output_mode).label}；模型：${testResult.model_name}；地址：${testResult.base_url}`
            : `错误码：${testResult.code || 'unknown'}`}
        />
      )}
      {connection?.tested_at && (
        <Typography.Text type="secondary">最近测试时间：{connection.tested_at}</Typography.Text>
      )}
      <Typography.Text type="secondary">
        运行中的 AI 筛选失败会写入对应候选人的 AI 决策错误信息，并同步输出到后端/worker 容器日志；不会记录或打印 API Key。
      </Typography.Text>
    </Space>
  )
}
