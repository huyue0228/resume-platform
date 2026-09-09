import { useEffect, useState } from 'react'
import { Alert, Button, Modal, Space, Table, Typography } from 'antd'
import client from '../api/client'

export default function TaskMaterials({ run }) {
  const [open, setOpen] = useState(false)
  const [page, setPage] = useState(1)
  const [data, setData] = useState({ results: [], count: 0 })
  const [error, setError] = useState('')
  const [text, setText] = useState(null)
  useEffect(() => {
    if (!open) return undefined
    let disposed = false
    let timer
    const load = async () => {
      try {
        const response = await client.get(`/pipeline/runs/${run.id}/materials/`, { params: { page, page_size: 20 } })
        if (!disposed) { setData(response.data); setError('') }
      } catch { if (!disposed) setError('材料进度加载失败，请稍后重新打开') }
      if (!disposed && ['pending', 'running', 'cancelling', 'waiting_conflict'].includes(run.status)) {
        timer = setTimeout(load, 3000)
      }
    }
    load()
    return () => { disposed = true; clearTimeout(timer) }
  }, [open, page, run.id, run.status])

  const preview = async (item) => {
    try {
      const response = await client.get(`/pipeline/runs/${run.id}/material-text/`, { params: { item_id: item.id } })
      setText(response.data)
    } catch { setError('全文加载失败') }
  }
  let line = 0
  return <>
    <Button size="small" onClick={() => setOpen(true)}>查看逐份材料进度</Button>
    <Modal title={`任务 #${run.id} · 材料进度`} open={open} onCancel={() => setOpen(false)} footer={null} width={1000}>
      <Typography.Paragraph type="secondary">准入与岗位检查通过后，依次提取文字 → 校验文本 → Kernel 分析 → 保存结果。单份异常不影响其他材料。</Typography.Paragraph>
      {error && <Alert type="error" message={error} />}
      <Table size="small" rowKey="id" dataSource={data.results} pagination={{ current: page, pageSize: 20, total: data.count, onChange: setPage, showSizeChanger: false }}
        columns={[
          { title: '候选人 / 简历', key: 'candidate', render: (_, item) => <Space direction="vertical"><span>{item.candidate_name}</span><span>{item.apply_id || '待确定当前志愿'}</span></Space> },
          { title: '当前节点', dataIndex: 'node_label' },
          { title: '后续节点', key: 'next', render: (_, item) => item.next_nodes.join(' → ') || '无后续节点' },
          { title: '说明', dataIndex: 'message' },
          { title: '全文', key: 'text', render: (_, item) => <Button size="small" disabled={!item.has_text} onClick={() => preview(item)}>查看提取文本</Button> },
        ]} />
    </Modal>
    <Modal title="提取全文 · 页码与全局行号" open={Boolean(text)} onCancel={() => setText(null)} footer={null} width={1000}>
      {text?.warnings?.length > 0 && <Alert type="warning" message={text.warnings.join('；')} />}
      {text?.pages.map((content, pageIndex) => <section key={pageIndex}>
        <Typography.Title level={5}>第 {pageIndex + 1} 页{content.trim() ? '' : '（空白页）'}</Typography.Title>
        <pre style={{ overflowX: 'auto', whiteSpace: 'pre', fontSize: 13 }}>{content.split('\n').map((value) => `${++line}  ${value}`).join('\n')}</pre>
      </section>)}
    </Modal>
  </>
}
