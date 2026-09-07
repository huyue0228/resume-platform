import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Empty, Skeleton, Space, Typography, message } from 'antd'
import {
  DownloadOutlined,
  ExportOutlined,
  FileSearchOutlined,
  LeftOutlined,
  RightOutlined,
} from '@ant-design/icons'
import * as pdfjsLib from 'pdfjs-dist/build/pdf.mjs'
import pdfWorkerUrl from 'pdfjs-dist/build/pdf.worker.mjs?url'
import { previewAllocationResume, previewResume } from '../api/services'

pdfjsLib.GlobalWorkerOptions.workerSrc = pdfWorkerUrl

function downloadBlob(blob, filename) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename || 'resume'
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

function decodeFilename(value) {
  if (!value) return ''
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

async function previewErrorMessage(error) {
  const data = error?.response?.data
  if (data instanceof Blob) {
    try {
      const text = await data.text()
      const body = text ? JSON.parse(text) : {}
      return body.detail || body.message || error?.message || '暂无可预览的简历文件'
    } catch {
      return error?.message || '暂无可预览的简历文件'
    }
  }
  return data?.detail || data?.message || error?.message || '暂无可预览的简历文件'
}

export default function ResumePreview({ resume, attemptId, height = 520 }) {
  const canvasRef = useRef(null)
  const [state, setState] = useState({
    loading: false,
    rendering: false,
    url: '',
    blob: null,
    filename: '',
    contentType: '',
    pdfDoc: null,
    pageNumber: 1,
    pageCount: 0,
    error: '',
    renderError: '',
  })

  useEffect(() => {
    let alive = true
    let revokedUrl = ''
    let pdfDoc = null
    let loadingTask = null
    let loadingTaskDestroyed = false

    const destroyLoadingTask = () => {
      if (!loadingTask || loadingTaskDestroyed) return
      loadingTaskDestroyed = true
      try {
        const result = loadingTask.destroy()
        result?.catch?.(() => {})
      } catch {
        // 组件卸载时只做资源回收，销毁失败不能再次触发页面错误边界。
      }
    }

    if (!resume?.resume_file) {
      setState({
        loading: false,
        rendering: false,
        url: '',
        blob: null,
        filename: '',
        contentType: '',
        pdfDoc: null,
        pageNumber: 1,
        pageCount: 0,
        error: '',
        renderError: '',
      })
      return undefined
    }
    setState({
      loading: true,
      rendering: false,
      url: '',
      blob: null,
      filename: '',
      contentType: '',
      pdfDoc: null,
      pageNumber: 1,
      pageCount: 0,
      error: '',
      renderError: '',
    })
    const request = attemptId ? previewAllocationResume(attemptId) : previewResume(resume.id)
    request
      .then(async (response) => {
        const headerFilename = response.headers?.['x-resume-filename']
        const contentType = response.headers?.['content-type'] || response.data?.type || ''
        const blob = new Blob([response.data], {
          type: contentType,
        })
        const url = URL.createObjectURL(blob)
        revokedUrl = url
        const filename = decodeFilename(headerFilename)
        const isPdf = contentType.includes('pdf') || /\.pdf$/i.test(filename || resume?.resume_file || '')
        if (!alive) {
          URL.revokeObjectURL(url)
          return
        }
        if (isPdf) {
          const bytes = new Uint8Array(await blob.arrayBuffer())
          loadingTask = pdfjsLib.getDocument({ data: bytes })
          pdfDoc = await loadingTask.promise
        }
        if (!alive) {
          URL.revokeObjectURL(url)
          destroyLoadingTask()
          return
        }
        setState({
          loading: false,
          rendering: false,
          url,
          blob,
          filename,
          contentType,
          pdfDoc,
          pageNumber: 1,
          pageCount: pdfDoc?.numPages || 0,
          error: '',
          renderError: '',
        })
      })
      .catch(async (error) => {
        if (!alive) return
        const message = await previewErrorMessage(error)
        if (!alive) return
        setState({
          loading: false,
          rendering: false,
          url: '',
          blob: null,
          filename: '',
          contentType: '',
          pdfDoc: null,
          pageNumber: 1,
          pageCount: 0,
          error: message,
          renderError: '',
        })
      })

    return () => {
      alive = false
      if (revokedUrl) URL.revokeObjectURL(revokedUrl)
      destroyLoadingTask()
    }
  }, [attemptId, resume?.id, resume?.resume_file])

  useEffect(() => {
    if (!state.pdfDoc || !canvasRef.current) {
      return undefined
    }
    let cancelled = false
    let renderTask = null
    const renderPage = async () => {
      setState((prev) => ({ ...prev, rendering: true, renderError: '' }))
      try {
        const page = await state.pdfDoc.getPage(state.pageNumber)
        const canvas = canvasRef.current
        if (!canvas || cancelled) return
        const baseViewport = page.getViewport({ scale: 1 })
        const availableWidth = canvas.parentElement?.clientWidth || 760
        const scale = Math.max(0.6, Math.min(availableWidth / baseViewport.width, 1.8))
        const viewport = page.getViewport({ scale })
        const pixelRatio = window.devicePixelRatio || 1
        const context = canvas.getContext('2d')
        canvas.width = Math.floor(viewport.width * pixelRatio)
        canvas.height = Math.floor(viewport.height * pixelRatio)
        canvas.style.width = `${Math.floor(viewport.width)}px`
        canvas.style.height = `${Math.floor(viewport.height)}px`
        renderTask = page.render({
          canvasContext: context,
          viewport,
          transform: pixelRatio === 1 ? null : [pixelRatio, 0, 0, pixelRatio, 0, 0],
        })
        await renderTask.promise
        if (!cancelled) {
          setState((prev) => ({ ...prev, rendering: false, renderError: '' }))
        }
      } catch (error) {
        if (cancelled || error?.name === 'RenderingCancelledException') return
        setState((prev) => ({
          ...prev,
          rendering: false,
          renderError: 'PDF 渲染失败，请下载原文件查看',
        }))
      }
    }
    renderPage()
    return () => {
      cancelled = true
      renderTask?.cancel()
    }
  }, [state.pdfDoc, state.pageNumber])

  if (!resume && !attemptId) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="未选择简历" />
  }

  if (!resume?.resume_file) {
    return (
      <Empty
        image={Empty.PRESENTED_IMAGE_SIMPLE}
        description="该投递未关联简历文件，请先上传包含对应应聘ID的简历包"
      />
    )
  }

  if (state.loading) {
    return <Skeleton active paragraph={{ rows: 8 }} />
  }

  if (state.error) {
    return <Alert type="warning" showIcon message={state.error} />
  }

  const filename = state.filename || resume?.resume_file || resume?.apply_id || 'resume'
  const isPdf = state.contentType.includes('pdf') || /\.pdf$/i.test(filename)

  return (
    <Space direction="vertical" size="small" style={{ width: '100%' }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <Typography.Text type="secondary">
          <FileSearchOutlined /> {resume?.apply_id || resume?.position_name || '简历预览'}
        </Typography.Text>
        <Space size="small">
          <Button
            size="small"
            icon={<ExportOutlined />}
            disabled={!state.url}
            onClick={() => window.open(state.url, '_blank', 'noopener,noreferrer')}
          >
            新窗口打开
          </Button>
          <Button
            size="small"
            icon={<DownloadOutlined />}
            disabled={!state.blob}
            onClick={() => {
              if (!state.blob) return
              downloadBlob(state.blob, filename)
              message.success('已开始下载')
            }}
          >
            下载原文件
          </Button>
        </Space>
      </Space>
      {!isPdf && (
        <Alert type="info" showIcon message="该文件不是 PDF，请下载原文件查看" />
      )}
      {isPdf && state.renderError && (
        <Alert type="warning" showIcon message={state.renderError} />
      )}
      {isPdf && (
        <>
          <div
            style={{
              minHeight: height,
              border: '1px solid #f0f0f0',
              borderRadius: 6,
              background: '#f7f8fa',
              overflow: 'auto',
              textAlign: 'center',
              padding: 12,
            }}
          >
            {state.rendering && <Skeleton active paragraph={{ rows: 6 }} />}
            <canvas
              ref={canvasRef}
              style={{
                display: state.rendering ? 'none' : 'inline-block',
                maxWidth: '100%',
                background: '#fff',
                boxShadow: '0 1px 4px rgba(0,0,0,0.12)',
              }}
            />
          </div>
          {state.pageCount > 1 && (
            <Space style={{ justifyContent: 'center', width: '100%' }}>
              <Button
                size="small"
                icon={<LeftOutlined />}
                disabled={state.pageNumber <= 1}
                onClick={() =>
                  setState((prev) => ({ ...prev, pageNumber: prev.pageNumber - 1 }))
                }
              />
              <Typography.Text type="secondary">
                {state.pageNumber} / {state.pageCount}
              </Typography.Text>
              <Button
                size="small"
                icon={<RightOutlined />}
                disabled={state.pageNumber >= state.pageCount}
                onClick={() =>
                  setState((prev) => ({ ...prev, pageNumber: prev.pageNumber + 1 }))
                }
              />
            </Space>
          )}
        </>
      )}
    </Space>
  )
}
