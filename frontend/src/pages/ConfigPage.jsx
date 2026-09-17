import { PageContainer } from '@ant-design/pro-components'
import { Tabs } from 'antd'
import SchoolAdmissionRulesTab from './config/SchoolAdmissionRulesTab'
import SchoolTagsTab from './config/SchoolTagsTab'

export default function ConfigPage() {
  const items = [
    { key: 'school-tags', label: '院校标签字典', children: <SchoolTagsTab /> },
    {
      key: 'school-rules',
      label: '院校准入规则',
      children: <SchoolAdmissionRulesTab />,
    },

  ]
  return (
    <PageContainer title="配置项" className="config-page">
      <Tabs
        className="config-page-tabs"
        defaultActiveKey="school-tags"
        tabPosition="top"
        items={items}
      />
    </PageContainer>
  )
}
