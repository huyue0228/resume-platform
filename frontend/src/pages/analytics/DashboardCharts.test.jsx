import { describe, expect, it, vi } from 'vitest'

vi.mock('react-chartjs-2', () => ({
  Doughnut: () => null,
  Bar: () => null,
}))

import { formatChartCount, prepareDistributionRows } from './chartUtils'

describe('analytics chart helpers', () => {
  it('sorts distribution rows and aggregates categories beyond the top five', () => {
    const rows = prepareDistributionRows([
      { key: 'a', label: 'A', count: 2 },
      { key: 'b', label: 'B', count: 9 },
      { key: 'c', label: 'C', count: 8 },
      { key: 'd', label: 'D', count: 7 },
      { key: 'e', label: 'E', count: 6 },
      { key: 'f', label: 'F', count: 5 },
      { key: 'g', label: 'G', count: 4 },
      { key: 'empty', label: '空值', count: 0 },
    ])

    expect(rows.map((item) => item.label)).toEqual(['B', 'C', 'D', 'E', 'F', '其他'])
    expect(rows.at(-1).count).toBe(6)
  })

  it('formats chart counts with the Chinese locale separator', () => {
    expect(formatChartCount(12345)).toBe('12,345')
  })
})
