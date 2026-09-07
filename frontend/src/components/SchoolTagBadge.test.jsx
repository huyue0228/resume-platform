import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import SchoolTagBadge from './SchoolTagBadge'
import { schoolTagColor } from './schoolTagColors'

describe('SchoolTagBadge', () => {
  it('keeps the same label color stable and separates different labels', () => {
    const labels = ['平台A', '平台B', '平台C', '非目标院校']
    const colors = labels.map(schoolTagColor)

    expect(schoolTagColor('平台A')).toBe(schoolTagColor('平台A'))
    expect(new Set(colors).size).toBe(labels.length)
  })

  it('renders a colored tag and ignores empty values', () => {
    const { rerender } = render(<SchoolTagBadge value="平台A" />)
    const tag = screen.getByText('平台A')

    expect(tag.classList.contains('ant-tag')).toBe(true)
    expect(tag.style.backgroundColor).toBeTruthy()

    rerender(<SchoolTagBadge value="" />)
    expect(screen.queryByText('平台A')).toBeNull()
  })
})
