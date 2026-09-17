import { Alert, Modal, Table } from 'antd'
import { checkProcessingConfiguration } from '../api/pools'

export async function confirmProcessingConfiguration(scope) {
  const { data } = await checkProcessingConfiguration(scope)
  if (!data.issues.length) return true
  return new Promise((resolve) => {
    Modal.confirm({
      title: '处理前配置检查', width: 860,
      content: <>
        <Alert type={data.blocked_count ? 'warning' : 'info'} showIcon message={`共 ${data.total} 人：${data.ready_count} 人可筛选，${data.blocked_count} 人配置受阻；可筛选者中 ${data.allocation_wait_count} 人需等待分配配置。`} />
        <Table size="small" rowKey={(r) => `${r.entity}/${r.application_name}/${r.code}`} pagination={{ pageSize: 5 }} dataSource={data.issues} columns={[{ title: '主体', dataIndex: 'entity' }, { title: '投递', dataIndex: 'application_name' }, { title: '人数', dataIndex: 'count' }, { title: '待处理问题', dataIndex: 'message' }]} />
        <a href="/jobs?tab=configuration" target="_blank" rel="noreferrer">打开岗位筛选与分配配置</a>
        <p>继续提交后，配置受阻的候选人会保留具体原因，可在修复后批量重新处理。</p>
      </>,
      okText: '继续提交', cancelText: '先修复配置',
      okButtonProps: { disabled: data.blocked_count === data.total },
      onOk: () => resolve(true), onCancel: () => resolve(false),
    })
  })
}
