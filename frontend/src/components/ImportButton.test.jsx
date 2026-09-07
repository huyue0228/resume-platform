import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ImportButton from './ImportButton'

const importData = vi.hoisted(() => vi.fn())
const downloadImportTemplate = vi.hoisted(() => vi.fn())
const downloadBlobFromResponse = vi.hoisted(() => vi.fn())

vi.mock('../api/services', () => ({ downloadImportTemplate, importData }))
vi.mock('../utils/download', () => ({ downloadBlobFromResponse }))

describe('ImportButton', () => {
  beforeEach(() => {
    importData.mockReset()
    importData.mockResolvedValue({ data: { detail: '导入完成' } })
    downloadImportTemplate.mockReset()
    downloadImportTemplate.mockResolvedValue({ data: new ArrayBuffer(8), headers: {} })
    downloadBlobFromResponse.mockReset()
  })

  it('submits resume uploads without a client-selected processing mode', async () => {
    const user = userEvent.setup()
    render(
      <ImportButton
        buttonText="上传简历"
        fields={[{ key: 'resume_package', label: '简历包', accept: '.zip' }]}
      />,
    )

    await user.click(screen.getByRole('button', { name: /上传简历/ }))
    const input = document.querySelector('input[type="file"]')
    await user.upload(input, new File(['resume'], '简历包.zip', { type: 'application/zip' }))
    await user.click(screen.getByRole('button', { name: '开始导入' }))

    await waitFor(() => expect(importData).toHaveBeenCalledTimes(1))
    const formData = importData.mock.calls[0][0]
    expect(formData.get('mode')).toBe('incremental')
    expect(formData.has('processing_mode')).toBe(false)
    expect(formData.get('resume_package').name).toBe('简历包.zip')
  })

  it('downloads the configured standard template from the import dialog', async () => {
    const user = userEvent.setup()
    render(
      <ImportButton
        buttonText="导入岗位"
        fields={[{ key: 'jobs', label: '岗位表', accept: '.xlsx' }]}
        templateType="jobs"
        templateFilename="岗位标准模板.xlsx"
      />,
    )

    await user.click(screen.getByRole('button', { name: /导入岗位/ }))
    await user.click(screen.getByRole('button', { name: /下载标准模板/ }))

    await waitFor(() => expect(downloadImportTemplate).toHaveBeenCalledWith('jobs'))
    expect(downloadBlobFromResponse).toHaveBeenCalledWith(
      expect.objectContaining({ data: expect.any(ArrayBuffer) }),
      '岗位标准模板.xlsx',
    )
  })
})
