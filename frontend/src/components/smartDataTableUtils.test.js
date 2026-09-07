import { describe, expect, it } from 'vitest'
import {
  filterLocalData,
  localTextMatches,
  serializeTableFilters,
  tableStorageKey,
} from './smartDataTableUtils'

describe('smartDataTableUtils', () => {
  it('serializes multiple selectors with comma-separated server params', () => {
    const columns = [
      {
        dataIndex: 'department',
        filter: { type: 'select', param: 'department_in', multiple: true },
      },
      { dataIndex: 'name', filter: { type: 'text', param: 'name' } },
    ]

    expect(serializeTableFilters(columns, {
      department: ['研发', '产品'],
      name: ['张三'],
    })).toEqual({ department_in: '研发,产品', name: '张三' })
  })

  it('serializes two-sided and one-sided date ranges into separate params', () => {
    const columns = [{
      dataIndex: 'apply_date',
      filter: {
        type: 'dateRange',
        params: ['current_apply_date_from', 'current_apply_date_to'],
      },
    }]

    expect(serializeTableFilters(columns, {
      apply_date: ['2026-07-01', '2026-07-31'],
    })).toEqual({
      current_apply_date_from: '2026-07-01',
      current_apply_date_to: '2026-07-31',
    })
    expect(serializeTableFilters(columns, {
      apply_date: ['', '2026-07-31'],
    })).toEqual({ current_apply_date_to: '2026-07-31' })
  })

  it('matches local Chinese names by full pinyin and initials', () => {
    expect(localTextMatches('张三', 'zhangsan', true)).toBe(true)
    expect(localTextMatches('张三', 'zs', true)).toBe(true)
    expect(localTextMatches('李四', 'zs', true)).toBe(false)
  })

  it('filters local detail rows using pinyin metadata', () => {
    const rows = [{ name: '王五' }, { name: '赵六' }]
    const columns = [{
      dataIndex: 'name',
      filter: { type: 'text', pinyin: true },
    }]

    expect(filterLocalData(rows, columns, { name: ['ww'] })).toEqual([{ name: '王五' }])
  })

  it('filters local rows by an inclusive date range and excludes empty dates', () => {
    const rows = [
      { apply_date: '2026-07-01' },
      { apply_date: '2026-07-15' },
      { apply_date: '2026-07-31' },
      { apply_date: null },
    ]
    const columns = [{
      dataIndex: 'apply_date',
      filter: { type: 'dateRange' },
    }]

    expect(filterLocalData(rows, columns, {
      apply_date: ['2026-07-01', '2026-07-15'],
    })).toEqual(rows.slice(0, 2))
  })

  it('isolates persisted column settings by user and table', () => {
    expect(tableStorageKey(1, 'candidates')).toBe('srf:table:v1:1:candidates')
    expect(tableStorageKey(2, 'candidates')).not.toBe(tableStorageKey(1, 'candidates'))
    expect(tableStorageKey(1, 'jobs')).not.toBe(tableStorageKey(1, 'candidates'))
  })
})
