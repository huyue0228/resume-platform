import { useState } from 'react'
import { Modal, Upload, Radio, Button, Space, Alert, message } from 'antd'
import { DownloadOutlined, InboxOutlined, ImportOutlined } from '@ant-design/icons'
import { downloadImportTemplate, importData } from '../api/services'
import { downloadBlobFromResponse } from '../utils/download'

const { Dragger } = Upload

// 复用的导入按钮：点开弹窗，按 fields 配置上传一类或多类源文件，调用 /api/import/。
// props:
//   fields: [{ key, label, accept }]  —— 后端 multipart 字段
//   buttonText / title
//   onDone(data) —— 导入成功回调（用于刷新列表）
//   templateType / templateFilename —— 标准模板类型与下载兜底文件名
export default function ImportButton({
  fields,
  buttonText = '导入',
  title,
  onDone,
  templateType,
  templateFilename = '标准导入模板.xlsx',
}) {
  const [open, setOpen] = useState(false)
  const [files, setFiles] = useState({})
  const [mode, setMode] = useState('incremental')
  const [loading, setLoading] = useState(false)
  const [templateLoading, setTemplateLoading] = useState(false)
  const [result, setResult] = useState('')

  const reset = () => {
    setFiles({})
    setMode('incremental')
    setResult('')
  }

  const onChange = (key, file) => setFiles((prev) => ({ ...prev, [key]: file }))

  const handleImport = async () => {
    const picked = fields.filter((f) => files[f.key])
    if (picked.length === 0) {
      message.warning('请至少选择一个文件再导入')
      return
    }
    const formData = new FormData()
    picked.forEach((f) => formData.append(f.key, files[f.key]))
    formData.append('mode', mode)

    setLoading(true)
    setResult('')
    try {
      const { data } = await importData(formData)
      if (data?.warnings?.length) {
        message.warning(data?.detail || '导入完成，部分数据已跳过')
      } else {
        message.success(data?.detail || '导入完成')
      }
      setOpen(false) // 关闭导入弹窗，便于后续自动处理进度条展示
      reset()
      await onDone?.(data)
    } catch {
      // 错误已由 axios 拦截器统一提示
    } finally {
      setLoading(false)
    }
  }

  const handleTemplateDownload = async () => {
    setTemplateLoading(true)
    try {
      const response = await downloadImportTemplate(templateType)
      downloadBlobFromResponse(response, templateFilename)
      message.success('标准模板已下载')
    } catch {
      // 错误已由 axios 拦截器统一提示
    } finally {
      setTemplateLoading(false)
    }
  }

  return (
    <>
      <Button
        type="primary"
        icon={<ImportOutlined />}
        onClick={() => setOpen(true)}
      >
        {buttonText}
      </Button>
      <Modal
        title={title || buttonText}
        open={open}
        width={560}
        okText="开始导入"
        cancelText="取消"
        confirmLoading={loading}
        onOk={handleImport}
        onCancel={() => {
          setOpen(false)
          reset()
        }}
        destroyOnHidden
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          {templateType && (
            <Alert
              type="info"
              showIcon
              message="请使用系统标准模板填写，导入时会严格校验表头。"
              action={(
                <Button
                  icon={<DownloadOutlined />}
                  loading={templateLoading}
                  onClick={handleTemplateDownload}
                >
                  下载标准模板
                </Button>
              )}
            />
          )}
          {fields.map((field) => (
            <div key={field.key}>
              <div style={{ marginBottom: 8, fontWeight: 500 }}>{field.label}</div>
              <Dragger
                multiple={false}
                maxCount={1}
                accept={field.accept}
                fileList={files[field.key] ? [files[field.key]] : []}
                beforeUpload={(f) => {
                  onChange(field.key, f)
                  return false // 不自动上传，提交时统一发送
                }}
                onRemove={() => onChange(field.key, null)}
              >
                <p className="ant-upload-drag-icon">
                  <InboxOutlined />
                </p>
                <p className="ant-upload-text">点击或拖拽文件到此区域</p>
                <p className="ant-upload-hint">支持 {field.accept}，单文件</p>
              </Dragger>
            </div>
          ))}

          <div>
            导入模式：
            <Radio.Group
              value={mode}
              onChange={(e) => setMode(e.target.value)}
              style={{ marginLeft: 8 }}
            >
              <Radio value="incremental">增量更新</Radio>
              <Radio value="replace">清空重导</Radio>
            </Radio.Group>
          </div>

          {result && <Alert type="success" showIcon message={result} />}
        </Space>
      </Modal>
    </>
  )
}
