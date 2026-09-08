import { Alert, Checkbox, Modal, Space, Typography } from 'antd'

export default function ResumeProcessModal({
  open,
  processing,
  error,
  processCurrentSelected,
  processCandidateCount,
  processStatusSelection,
  statusOptions,
  onCurrentSelectedChange,
  onStatusChange,
  onConfirm,
  onCancel,
}) {
  return (
    <Modal
      title="处理简历"
      open={open}
      okText="开始处理"
      cancelText="取消"
      confirmLoading={processing}
      okButtonProps={{
        disabled: !processCurrentSelected && !processStatusSelection.length,
      }}
      onOk={onConfirm}
      onCancel={onCancel}
    >
      <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {error && <Alert type="error" showIcon message="处理任务提交失败" description={error} />}
        <Typography.Text>
          选择当前勾选的候选人，或按简历状态重新处理。两种范围互斥，系统会保留历史分配与反馈记录。
        </Typography.Text>
        <Typography.Text type="secondary">
          系统将使用 Agent Kernel 处理；确定性业务约束由后端统一校验。
        </Typography.Text>
        <Checkbox
          checked={processCurrentSelected}
          disabled={!processCandidateCount}
          onChange={onCurrentSelectedChange}
        >
          当前选中（{processCandidateCount}）
        </Checkbox>
        <Checkbox.Group
          value={processStatusSelection}
          onChange={onStatusChange}
          style={{ width: '100%' }}
        >
          <Space direction="vertical">
            {Object.entries(statusOptions).map(([value, item]) => (
              <Checkbox key={value} value={value}>
                {item.text}
              </Checkbox>
            ))}
          </Space>
        </Checkbox.Group>
        {!processCurrentSelected && !processStatusSelection.length && (
          <Typography.Text type="secondary">
            请先勾选当前选中或需要处理的简历状态，再开始处理。
          </Typography.Text>
        )}
      </Space>
    </Modal>
  )
}
