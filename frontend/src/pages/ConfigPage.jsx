import { PageContainer } from '@ant-design/pro-components'
import { Tabs } from 'antd'
import PositionPoolSettingsTab from './config/PositionPoolSettingsTab'
import MajorDictionaryTab from './config/MajorDictionaryTab'
import SchoolAdmissionRulesTab from './config/SchoolAdmissionRulesTab'
import SchoolTagsTab from './config/SchoolTagsTab'
import AllocationSettingsTab from './config/AllocationSettingsTab'

export default function ConfigPage() {
  const items = [
    { key: 'school-tags', label: '院校标签字典', children: <SchoolTagsTab /> },
    {
      key: 'school-rules',
      label: '院校准入规则',
      children: <SchoolAdmissionRulesTab />,
    },
    {
      key: 'major-dictionary',
      label: '专业大类词表',
      children: <MajorDictionaryTab />,
    },
    {
      key: 'allocation-settings',
      label: '分配参数',
      children: <AllocationSettingsTab />,
    },
    { key: 'position-pools', label: '职位池与标签', children: <PositionPoolSettingsTab /> },
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
