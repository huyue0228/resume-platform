import { Tag } from 'antd'
import { schoolTagColor } from './schoolTagColors'

export default function SchoolTagBadge({ value }) {
  const label = String(value ?? '').trim()
  if (!label) return null
  return <Tag color={schoolTagColor(label)}>{label}</Tag>
}
