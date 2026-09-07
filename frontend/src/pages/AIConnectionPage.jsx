import { PageContainer } from '@ant-design/pro-components'
import { Tabs } from 'antd'
import AIConnectionTab from './config/AIConnectionTab'
import AISettingsTab from './config/AISettingsTab'
import AISpecialSettingsTab from './config/AISpecialSettingsTab'

export default function AIConnectionPage() {
  return (
    <PageContainer title="AI 模型连接">
      <Tabs
        defaultActiveKey="connection"
        items={[
          { key: 'connection', label: '模型连接', children: <AIConnectionTab /> },
          { key: 'runtime', label: 'AI 运行参数', children: <AISettingsTab /> },
          { key: 'special', label: 'AI 专项', children: <AISpecialSettingsTab /> },
        ]}
      />
    </PageContainer>
  )
}
