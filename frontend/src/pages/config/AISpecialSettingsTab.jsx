import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  InputNumber,
  Select,
  Space,
  Switch,
  Tag,
  Typography,
  message,
} from 'antd'
import {
  fetchAIConnectionSettings,
  updateAIConnectionSetting,
} from '../../api/services'
import {
  DEFAULT_VALUES,
  ENABLED_KEY,
  SECONDARY_KEY,
  TERTIARY_KEY,
  THRESHOLD_KEY,
  saveAISpecialSettings,
} from './aiSpecialSettings'

function departmentLabel(department) {
  return department.entity ? `${department.name}（${department.entity}）` : department.name
}

export default function AISpecialSettingsTab() {
  const [persisted, setPersisted] = useState(DEFAULT_VALUES)
  const [drafts, setDrafts] = useState(DEFAULT_VALUES)
  const [departments, setDepartments] = useState([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const { data } = await fetchAIConnectionSettings()
      const specialSettings = (data?.settings || [])
        .filter((item) => item.section === 'special_route')
      const values = {
        ...DEFAULT_VALUES,
        ...Object.fromEntries(specialSettings.map((item) => [item.key, item.value])),
      }
      values[ENABLED_KEY] = Boolean(values[ENABLED_KEY])
      values[THRESHOLD_KEY] = Number(values[THRESHOLD_KEY])
      values[SECONDARY_KEY] = Number(values[SECONDARY_KEY]) || 0
      values[TERTIARY_KEY] = Number(values[TERTIARY_KEY]) || 0
      setPersisted(values)
      setDrafts(values)
      setDepartments(Array.isArray(data?.departments) ? data.departments : [])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const secondaryDepartments = useMemo(
    () => departments.filter((department) => department.level === 2),
    [departments],
  )
  const selectedSecondary = secondaryDepartments.find(
    (department) => department.id === drafts[SECONDARY_KEY],
  )
  const tertiaryDepartments = useMemo(
    () => departments.filter((department) => (
      department.level === 3
      && selectedSecondary
      && department.parent === selectedSecondary.id
    )),
    [departments, selectedSecondary],
  )

  const updateSecondary = (departmentId) => {
    const secondary = secondaryDepartments.find((department) => department.id === departmentId)
    const currentTertiary = departments.find(
      (department) => department.id === drafts[TERTIARY_KEY],
    )
    setDrafts((values) => ({
      ...values,
      [SECONDARY_KEY]: departmentId || 0,
      [TERTIARY_KEY]: (
        secondary
        && currentTertiary?.parent === secondary.id
      ) ? values[TERTIARY_KEY] : 0,
    }))
  }

  const save = async () => {
    const secondary = departments.find((department) => department.id === drafts[SECONDARY_KEY])
    const tertiary = departments.find((department) => department.id === drafts[TERTIARY_KEY])
    if (drafts[ENABLED_KEY] && (!secondary || !tertiary)) {
      message.error('启用 AI 专项前，请选择二级部门和其下属三级部门')
      return
    }
    if (
      drafts[ENABLED_KEY]
      && tertiary.parent !== secondary.id
    ) {
      message.error('三级部门必须属于所选二级部门')
      return
    }

    setSaving(true)
    try {
      await saveAISpecialSettings({
        persisted,
        drafts,
        update: updateAIConnectionSetting,
      })
      await load()
      message.success('AI 专项配置已保存')
    } catch {
      // 多键保存可能已经先安全关闭专项；失败后重新读取服务端真实状态。
      await load().catch(() => {})
    } finally {
      setSaving(false)
    }
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message="AI 专项分配"
        description="AI 识别到专项人才且置信度严格大于阈值时，先进入所选二级部门，再自动路由至其下属三级部门。普通列表仍统一显示为 AI 自动分配。"
      />
      <Card
        size="small"
        title="专项路由"
        extra={drafts[ENABLED_KEY] ? <Tag color="green">已开启</Tag> : <Tag>已关闭</Tag>}
        loading={loading}
      >
        <Space direction="vertical" size="large" style={{ width: '100%' }}>
          <Space>
            <Typography.Text strong>启用 AI 专项</Typography.Text>
            <Switch
              aria-label="启用 AI 专项"
              checked={Boolean(drafts[ENABLED_KEY])}
              onChange={(checked) => setDrafts((values) => ({
                ...values,
                [ENABLED_KEY]: checked,
              }))}
            />
          </Space>
          <Space wrap size="large" align="start">
            <Space direction="vertical" size={4}>
              <Typography.Text>触发阈值</Typography.Text>
              <InputNumber
                aria-label="AI 专项触发阈值"
                min={0.9}
                max={1}
                step={0.01}
                precision={2}
                value={drafts[THRESHOLD_KEY]}
                onChange={(value) => setDrafts((values) => ({
                  ...values,
                  [THRESHOLD_KEY]: Number(value) || 0.9,
                }))}
              />
              <Typography.Text type="secondary">必须严格大于该值才触发</Typography.Text>
            </Space>
            <Space direction="vertical" size={4}>
              <Typography.Text>父级二级部门</Typography.Text>
              <Select
                aria-label="AI 专项父级二级部门"
                showSearch
                allowClear
                optionFilterProp="label"
                placeholder="请选择二级部门"
                value={drafts[SECONDARY_KEY] || undefined}
                options={secondaryDepartments.map((department) => ({
                  value: department.id,
                  label: departmentLabel(department),
                }))}
                onChange={updateSecondary}
                style={{ width: 300 }}
              />
            </Space>
            <Space direction="vertical" size={4}>
              <Typography.Text>目标三级部门</Typography.Text>
              <Select
                aria-label="AI 专项目标三级部门"
                showSearch
                allowClear
                optionFilterProp="label"
                disabled={!selectedSecondary}
                placeholder={selectedSecondary ? '请选择三级部门' : '请先选择二级部门'}
                value={drafts[TERTIARY_KEY] || undefined}
                options={tertiaryDepartments.map((department) => ({
                  value: department.id,
                  label: departmentLabel(department),
                }))}
                onChange={(departmentId) => setDrafts((values) => ({
                  ...values,
                  [TERTIARY_KEY]: departmentId || 0,
                }))}
                style={{ width: 300 }}
              />
            </Space>
          </Space>
          <Space>
            <Button type="primary" loading={saving} onClick={save}>
              保存 AI 专项配置
            </Button>
            <Button disabled={saving} onClick={load}>重新加载</Button>
          </Space>
        </Space>
      </Card>
    </Space>
  )
}
