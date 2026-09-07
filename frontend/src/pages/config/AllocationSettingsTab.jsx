import { useEffect, useState } from 'react'
import { Button, Card, InputNumber, message, Space, Typography } from 'antd'
import { fetchConfig, updateConfig } from '../../api/services'

export default function AllocationSettingsTab() {
  const [value, setValue] = useState(1)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    let active = true
    setLoading(true)
    fetchConfig('job_hc_coefficient')
      .then(({ data }) => {
        if (active) setValue(Number(data?.value) || 1)
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [])

  const save = async () => {
    setLoading(true)
    try {
      const { data } = await updateConfig('job_hc_coefficient', value)
      setValue(Number(data?.value) || 1)
      message.success('分配参数已保存；新建处理任务将使用新系数')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Card size="small" title="岗位容量">
      <Space direction="vertical" size="middle">
        <Typography.Text>
          每个处理任务按岗位 HC × 系数冻结独立容量；已创建任务不受后续修改影响。
        </Typography.Text>
        <Space>
          <Typography.Text>HC 系数</Typography.Text>
          <InputNumber
            aria-label="HC 系数"
            min={1}
            max={100}
            precision={0}
            value={value}
            onChange={(next) => setValue(next || 1)}
          />
          <Button type="primary" loading={loading} onClick={save}>保存</Button>
        </Space>
      </Space>
    </Card>
  )
}
