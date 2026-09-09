import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import TaskMaterials from './TaskMaterials'

const get = vi.hoisted(() => vi.fn())
vi.mock('../api/client', () => ({ default: { get } }))

it('keeps blank pages, trailing newlines and global evidence line numbers in full text', async () => {
  get.mockImplementation(async (path) => ({ data: path.endsWith('/materials/')
    ? { count: 1, results: [{ id: 12, candidate_name: '验收候选人', apply_id: 'text-test', node_label: '材料需处理', next_nodes: [], message: '部分页面可能不完整', has_text: true }] }
    : { pages: ['第一行\n', '', '  第四行\n第五行'], warnings: ['第 2 页需要核对'] } }))
  render(<TaskMaterials run={{ id: 8, status: 'needs_attention' }} />)
  await userEvent.click(screen.getByRole('button', { name: '查看逐份材料进度' }))
  expect(await screen.findByText('材料需处理')).toBeTruthy()
  expect(screen.getByText('部分页面可能不完整')).toBeTruthy()
  await userEvent.click(screen.getByRole('button', { name: '查看提取文本' }))
  expect(await screen.findByText('第 2 页需要核对')).toBeTruthy()
  const pages = Array.from(document.querySelectorAll('pre')).map((page) => page.textContent)
  expect(pages).toEqual(['1  第一行\n2  ', '3  ', '4    第四行\n5  第五行'])
  expect(get).toHaveBeenCalledWith('/pipeline/runs/8/material-text/', { params: { item_id: 12 } })
})
