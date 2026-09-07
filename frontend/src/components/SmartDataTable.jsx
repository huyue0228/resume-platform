import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
} from 'react'
import { FilterOutlined, SearchOutlined } from '@ant-design/icons'
import { ProTable } from '@ant-design/pro-components'
import { Alert, Button, Input, Select, Space } from 'antd'
import { useRole } from '../contexts/roleState'
import ResizableHeaderCell from './ResizableHeaderCell'
import {
  filterLocalData,
  serializeTableFilters,
  tableColumnKey,
  tableStorageKey,
} from './smartDataTableUtils'

const DEFAULT_COLUMN_WIDTH = 120
const MIN_VIEWPORT_BODY_HEIGHT = 180
const VIEWPORT_BOTTOM_GAP = 8
const RESIZE_SETTLE_DELAY = 80
const EMPTY_COLUMN_STATE = {}
const EMPTY_STICKY_PAGINATION_METRICS = {
  fixed: false,
  height: 0,
  left: 0,
  width: 0,
}
const STICKY_PAGINATION_CLASS = 'srf-table-pagination-sticky'
const DEFAULT_SERVER_PAGE_SIZE_OPTIONS = [10, 20, 50, 100, 500]
const INTERACTIVE_SELECTOR = [
  'a',
  'button',
  'input',
  'select',
  'textarea',
  '[role="button"]',
  '.ant-checkbox-wrapper',
  '.ant-table-filter-trigger',
  '.srf-column-resize-handle',
].join(',')

function normalizeOption(option) {
  if (option && typeof option === 'object') {
    const label = option.label ?? option.text ?? option.value
    return {
      label,
      value: option.value ?? label,
      searchText: option.searchText ?? option.search_text ?? label,
    }
  }
  return { label: option, value: option, searchText: option }
}

function optionValues(options, filterOptions) {
  const source = typeof options === 'string' ? filterOptions?.[options] : options
  return (source || []).map(normalizeOption)
}

function TextFilterDropdown({ selectedKeys, onApply, placeholder }) {
  const [draft, setDraft] = useState(selectedKeys[0] || '')

  useEffect(() => setDraft(selectedKeys[0] || ''), [selectedKeys])

  return (
    <div className="srf-table-filter" onKeyDown={(event) => event.stopPropagation()}>
      <Input
        autoFocus
        allowClear
        placeholder={placeholder || '请输入筛选内容'}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onPressEnter={() => onApply(draft ? [draft] : [])}
      />
      <Space>
        <Button
          icon={<SearchOutlined />}
          size="small"
          type="primary"
          onClick={() => onApply(draft ? [draft] : [])}
        >
          确认
        </Button>
        <Button size="small" onClick={() => { setDraft(''); onApply([]) }}>
          重置
        </Button>
      </Space>
    </div>
  )
}

function SelectFilterDropdown({ multiple, options, selectedKeys, onApply }) {
  const [draft, setDraft] = useState(selectedKeys)

  useEffect(() => setDraft(selectedKeys), [selectedKeys])

  return (
    <div className="srf-table-filter" onKeyDown={(event) => event.stopPropagation()}>
      <Select
        allowClear
        mode={multiple ? 'multiple' : undefined}
        options={options}
        placeholder="请选择"
        showSearch
        value={multiple ? draft : draft[0]}
        filterOption={(input, option) =>
          String(option?.searchText ?? option?.label ?? '')
            .toLocaleLowerCase()
            .includes(String(input).trim().toLocaleLowerCase())
        }
        onChange={(value) =>
          setDraft(multiple ? value || [] : value === undefined ? [] : [value])
        }
      />
      <Space>
        <Button
          icon={<FilterOutlined />}
          size="small"
          type="primary"
          onClick={() => onApply(draft)}
        >
          确认
        </Button>
        <Button size="small" onClick={() => { setDraft([]); onApply([]) }}>
          重置
        </Button>
      </Space>
    </div>
  )
}

function DateRangeFilterDropdown({ selectedKeys, onApply, placeholders }) {
  const [draft, setDraft] = useState([
    selectedKeys[0] || '',
    selectedKeys[1] || '',
  ])

  useEffect(() => {
    setDraft([selectedKeys[0] || '', selectedKeys[1] || ''])
  }, [selectedKeys])

  const apply = () => onApply(draft.some(Boolean) ? draft : [])
  const [fromPlaceholder = '开始日期', toPlaceholder = '结束日期'] = placeholders || []

  return (
    <div className="srf-table-filter" onKeyDown={(event) => event.stopPropagation()}>
      <Input
        addonBefore="从"
        aria-label={fromPlaceholder}
        placeholder={fromPlaceholder}
        type="date"
        value={draft[0]}
        onChange={(event) => setDraft((current) => [event.target.value, current[1]])}
      />
      <Input
        addonBefore="至"
        aria-label={toPlaceholder}
        placeholder={toPlaceholder}
        type="date"
        value={draft[1]}
        onChange={(event) => setDraft((current) => [current[0], event.target.value])}
      />
      <Space>
        <Button
          icon={<FilterOutlined />}
          size="small"
          type="primary"
          onClick={apply}
        >
          确认
        </Button>
        <Button size="small" onClick={() => { setDraft(['', '']); onApply([]) }}>
          重置
        </Button>
      </Space>
    </div>
  )
}

function loadPersisted(key) {
  try {
    return JSON.parse(localStorage.getItem(key) || '{}')
  } catch {
    return {}
  }
}

function persist(key, widths, columnsState) {
  if (!Object.keys(widths).length && !Object.keys(columnsState).length) {
    localStorage.removeItem(key)
    return
  }
  localStorage.setItem(key, JSON.stringify({ widths, columnsState }))
}

function sameState(left, right) {
  return JSON.stringify(left || {}) === JSON.stringify(right || {})
}

function sameStickyPaginationMetrics(left, right) {
  return left.fixed === right.fixed
    && left.height === right.height
    && left.left === right.left
    && left.width === right.width
}

function appendClassName(current, next) {
  return [current, next].filter(Boolean).join(' ')
}

const SmartDataTable = forwardRef(function SmartDataTable(
  {
    tableId,
    columns: baseColumns,
    request: dataRequest,
    filterOptionsRequest,
    dataSource,
    actionRef,
    defaultColumnsState = EMPTY_COLUMN_STATE,
    batchActions,
    rowSelection,
    onRowClick,
    onRow,
    pagination,
    params: externalParams,
    options,
    scroll,
    stickyPagination = true,
    viewportScroll = true,
    ...tableProps
  },
  forwardedRef,
) {
  const roleContext = useRole()
  const userId = roleContext?.user?.id || roleContext?.user?.username || 'anonymous'
  const storageKey = tableStorageKey(userId, tableId)
  const defaultColumnsStateRef = useRef(defaultColumnsState)
  if (JSON.stringify(defaultColumnsStateRef.current) !== JSON.stringify(defaultColumnsState)) {
    defaultColumnsStateRef.current = defaultColumnsState
  }
  const stableDefaultColumnsState = defaultColumnsStateRef.current
  const persisted = useMemo(() => loadPersisted(storageKey), [storageKey])
  const proActionRef = useRef()
  const filtersRef = useRef({})
  const columnsStateRef = useRef(persisted.columnsState || stableDefaultColumnsState)
  const [filters, setFilters] = useState({})
  const [filterOptions, setFilterOptions] = useState({})
  const [widths, setWidths] = useState(persisted.widths || {})
  const [columnsState, setColumnsState] = useState(columnsStateRef.current)
  const [selectedRowKeys, setSelectedRowKeys] = useState([])
  const [selectedRows, setSelectedRows] = useState([])
  const tableRootRef = useRef()
  const stickyPaginationFrameRef = useRef()
  const stickyPaginationResizeTimerRef = useRef()
  const [stickyPaginationMetrics, setStickyPaginationMetrics] = useState(
    EMPTY_STICKY_PAGINATION_METRICS,
  )
  const [viewportBodyHeight, setViewportBodyHeight] = useState(0)
  const hasDataRequest = Boolean(dataRequest)

  const resolvedPagination = useMemo(() => {
    if (!hasDataRequest) return pagination === undefined ? false : pagination
    if (pagination === false) return false
    const defaults = {
      defaultPageSize: 10,
      showSizeChanger: true,
      pageSizeOptions: DEFAULT_SERVER_PAGE_SIZE_OPTIONS,
    }
    if (pagination === undefined || pagination === true) return defaults
    return { ...defaults, ...pagination }
  }, [hasDataRequest, pagination])
  const stickyPaginationEnabled = Boolean(
    stickyPagination && resolvedPagination && resolvedPagination !== false,
  )
  const viewportScrollEnabled = Boolean(viewportScroll && stickyPaginationEnabled)
  const mergedPagination = useMemo(() => {
    if (!stickyPaginationEnabled) return resolvedPagination
    const paginationProps = resolvedPagination === true ? {} : resolvedPagination
    return {
      ...paginationProps,
      className: appendClassName(paginationProps.className, STICKY_PAGINATION_CLASS),
    }
  }, [resolvedPagination, stickyPaginationEnabled])

  const updateStickyPagination = useCallback(() => {
    const root = tableRootRef.current
    const paginationElement = root?.querySelector(`.${STICKY_PAGINATION_CLASS}`)
    if (!stickyPaginationEnabled || !root || !paginationElement) {
      setStickyPaginationMetrics((previous) =>
        sameStickyPaginationMetrics(previous, EMPTY_STICKY_PAGINATION_METRICS)
          ? previous
          : EMPTY_STICKY_PAGINATION_METRICS)
      setViewportBodyHeight((previous) => (previous ? 0 : previous))
      return
    }

    const rootRect = root.getBoundingClientRect()
    const anchor = paginationElement.closest('.ant-pro-table') || root
    const anchorRect = anchor.getBoundingClientRect()
    const paginationRect = paginationElement.getBoundingClientRect()
    const viewportHeight = window.innerHeight || document.documentElement.clientHeight
    const viewportWidth = window.innerWidth || document.documentElement.clientWidth
    const isFullscreen = Boolean(
      document.fullscreenElement?.contains(paginationElement),
    )
    const isVisible = isFullscreen || (
      rootRect.width > 0
      && rootRect.height > 0
      && rootRect.bottom > 0
      && rootRect.top < viewportHeight
    )
    const left = Math.max(0, Math.round(anchorRect.left))
    const width = Math.max(0, Math.min(
      Math.round(anchorRect.width),
      viewportWidth - left,
    ))
    const next = isVisible && width > 0
      ? {
          fixed: true,
          height: Math.ceil(paginationRect.height),
          left,
          width,
        }
      : EMPTY_STICKY_PAGINATION_METRICS

    setStickyPaginationMetrics((previous) =>
      sameStickyPaginationMetrics(previous, next) ? previous : next)

    if (viewportScrollEnabled && isVisible) {
      const headerElement = root.querySelector('.ant-table-thead')
      const headerBottom = headerElement?.getBoundingClientRect().bottom || 0
      const availableHeight = Math.floor(
        viewportHeight
        - headerBottom
        - Math.ceil(paginationRect.height)
        - VIEWPORT_BOTTOM_GAP,
      )
      const nextBodyHeight = headerBottom > 0
        ? Math.max(MIN_VIEWPORT_BODY_HEIGHT, availableHeight)
        : 0
      setViewportBodyHeight((previous) => (
        previous === nextBodyHeight ? previous : nextBodyHeight
      ))
    } else {
      setViewportBodyHeight((previous) => (previous ? 0 : previous))
    }
  }, [stickyPaginationEnabled, viewportScrollEnabled])

  const scheduleStickyPaginationUpdate = useCallback(() => {
    if (stickyPaginationFrameRef.current !== undefined) return
    stickyPaginationFrameRef.current = window.requestAnimationFrame(() => {
      stickyPaginationFrameRef.current = undefined
      updateStickyPagination()
    })
  }, [updateStickyPagination])

  const scheduleStickyPaginationResizeUpdate = useCallback(() => {
    if (stickyPaginationResizeTimerRef.current !== undefined) {
      window.clearTimeout(stickyPaginationResizeTimerRef.current)
    }
    stickyPaginationResizeTimerRef.current = window.setTimeout(() => {
      stickyPaginationResizeTimerRef.current = undefined
      scheduleStickyPaginationUpdate()
    }, RESIZE_SETTLE_DELAY)
  }, [scheduleStickyPaginationUpdate])

  useEffect(() => {
    if (!stickyPaginationEnabled) {
      setStickyPaginationMetrics((previous) =>
        sameStickyPaginationMetrics(previous, EMPTY_STICKY_PAGINATION_METRICS)
          ? previous
          : EMPTY_STICKY_PAGINATION_METRICS)
      return undefined
    }

    const root = tableRootRef.current
    if (!root) return undefined
    // 侧边栏折叠会连续改变表格宽度；等待尺寸稳定后再统一测量，避免动画逐帧重排。
    const resizeObserver = new ResizeObserver(scheduleStickyPaginationResizeUpdate)
    const mutationObserver = new MutationObserver(scheduleStickyPaginationUpdate)
    resizeObserver.observe(root)
    mutationObserver.observe(root, { childList: true, subtree: true })
    window.addEventListener('resize', scheduleStickyPaginationResizeUpdate)
    window.addEventListener('scroll', scheduleStickyPaginationUpdate, true)
    document.addEventListener('fullscreenchange', scheduleStickyPaginationUpdate)
    scheduleStickyPaginationUpdate()

    return () => {
      resizeObserver.disconnect()
      mutationObserver.disconnect()
      window.removeEventListener('resize', scheduleStickyPaginationResizeUpdate)
      window.removeEventListener('scroll', scheduleStickyPaginationUpdate, true)
      document.removeEventListener('fullscreenchange', scheduleStickyPaginationUpdate)
      if (stickyPaginationFrameRef.current !== undefined) {
        window.cancelAnimationFrame(stickyPaginationFrameRef.current)
        stickyPaginationFrameRef.current = undefined
      }
      if (stickyPaginationResizeTimerRef.current !== undefined) {
        window.clearTimeout(stickyPaginationResizeTimerRef.current)
        stickyPaginationResizeTimerRef.current = undefined
      }
    }
  }, [
    scheduleStickyPaginationResizeUpdate,
    scheduleStickyPaginationUpdate,
    stickyPaginationEnabled,
  ])

  const loadOptions = useCallback(async () => {
    if (!filterOptionsRequest) return
    try {
      const response = await filterOptionsRequest()
      setFilterOptions(response?.data ?? response ?? {})
    } catch {
      setFilterOptions({})
    }
  }, [filterOptionsRequest])

  useEffect(() => {
    loadOptions()
  }, [loadOptions])

  useEffect(() => {
    const next = loadPersisted(storageKey)
    const nextColumnsState = next.columnsState || stableDefaultColumnsState
    setWidths(next.widths || {})
    setColumnsState(nextColumnsState)
    columnsStateRef.current = nextColumnsState
  }, [storageKey, stableDefaultColumnsState])

  const updateFilter = useCallback((key, values, confirm) => {
    const next = { ...filtersRef.current }
    if (values?.some(Boolean)) next[key] = values
    else delete next[key]
    filtersRef.current = next
    setFilters(next)
    confirm?.({ closeDropdown: true })
    if (dataRequest) queueMicrotask(() => proActionRef.current?.reload(true))
  }, [dataRequest])

  const columns = useMemo(
    () => baseColumns.map((column, index) => {
      const key = tableColumnKey(column, index)
      const width = widths[key] || column.width || DEFAULT_COLUMN_WIDTH
      const filter = column.filter
      const next = {
        ...column,
        key,
        width,
        onHeaderCell: (...args) => ({
          ...(column.onHeaderCell?.(...args) || {}),
          width,
          minWidth: column.minWidth || 72,
          onResize: (nextWidth) => {
            setWidths((previous) => {
              const nextWidths = { ...previous, [key]: nextWidth }
              persist(storageKey, nextWidths, columnsStateRef.current)
              return nextWidths
            })
          },
        }),
      }
      if (!filter) return next
      const selectedKeys = filters[key] || []
      const apply = (values, confirm) => updateFilter(key, values, confirm)
      let filterDropdown
      if (filter.type === 'select') {
        filterDropdown = ({ confirm }) => (
          <SelectFilterDropdown
            multiple={filter.multiple}
            options={optionValues(filter.options, filterOptions)}
            selectedKeys={selectedKeys}
            onApply={(values) => apply(values, confirm)}
          />
        )
      } else if (filter.type === 'dateRange') {
        filterDropdown = ({ confirm }) => (
          <DateRangeFilterDropdown
            placeholders={filter.placeholders}
            selectedKeys={selectedKeys}
            onApply={(values) => apply(values, confirm)}
          />
        )
      } else {
        filterDropdown = ({ confirm }) => (
          <TextFilterDropdown
            placeholder={filter.placeholder}
            selectedKeys={selectedKeys}
            onApply={(values) => apply(values, confirm)}
          />
        )
      }
      return {
        ...next,
        filteredValue: selectedKeys.some(Boolean) ? selectedKeys : null,
        filterMultiple: Boolean(filter.multiple),
        filterIcon: (filtered) => filter.type !== 'text'
          ? <FilterOutlined style={{ color: filtered ? '#1677ff' : undefined }} />
          : <SearchOutlined style={{ color: filtered ? '#1677ff' : undefined }} />,
        filterDropdown,
      }
    }),
    [baseColumns, widths, filters, filterOptions, storageKey, updateFilter],
  )

  const totalWidth = columns.reduce(
    (sum, column) => sum + Number(column.width || DEFAULT_COLUMN_WIDTH),
    0,
  )
  const localData = useMemo(
    () => filterLocalData(dataSource, columns, filters),
    [dataSource, columns, filters],
  )

  const clearSelection = useCallback(() => {
    setSelectedRowKeys([])
    setSelectedRows([])
    proActionRef.current?.clearSelected?.()
    rowSelection?.onChange?.([], [])
  }, [rowSelection])

  const resetTable = useCallback(() => {
    filtersRef.current = {}
    setFilters({})
    setWidths({})
    setColumnsState(stableDefaultColumnsState)
    columnsStateRef.current = stableDefaultColumnsState
    localStorage.removeItem(storageKey)
    clearSelection()
    queueMicrotask(() => proActionRef.current?.reload?.(true))
  }, [clearSelection, stableDefaultColumnsState, storageKey])

  const publicActions = useMemo(() => ({
    reload: (...args) => proActionRef.current?.reload?.(...args),
    reloadOptions: loadOptions,
    resetTable,
    clearSelected: clearSelection,
    getFilters: () => serializeTableFilters(baseColumns, filtersRef.current),
  }), [baseColumns, clearSelection, loadOptions, resetTable])

  useImperativeHandle(actionRef, () => publicActions, [publicActions])
  useImperativeHandle(forwardedRef, () => publicActions, [publicActions])

  const effectiveSelectedRowKeys = rowSelection?.selectedRowKeys ?? selectedRowKeys
  const mergedRowSelection = rowSelection ? {
    ...rowSelection,
    selectedRowKeys: effectiveSelectedRowKeys,
    onChange: (keys, rows, info) => {
      setSelectedRowKeys(keys)
      setSelectedRows(rows)
      rowSelection.onChange?.(keys, rows, info)
    },
  } : undefined

  const mergedOnRow = (record, index) => {
    const original = onRow?.(record, index) || {}
    return {
      ...original,
      onClick: (event) => {
        original.onClick?.(event)
        if (event.defaultPrevented || event.target.closest?.(INTERACTIVE_SELECTOR)) return
        onRowClick?.(record, index, event)
      },
    }
  }

  const batchContent = effectiveSelectedRowKeys.length && batchActions
    ? batchActions({
        selectedRowKeys: effectiveSelectedRowKeys,
        selectedRows,
        clearSelection,
        filters: serializeTableFilters(baseColumns, filtersRef.current),
      })
    : null

  const tableRootClassName = appendClassName(
    appendClassName(
      'srf-smart-data-table',
      viewportScrollEnabled ? 'srf-smart-data-table--viewport-scroll' : '',
    ),
    stickyPaginationMetrics.fixed ? 'srf-smart-data-table--pagination-fixed' : '',
  )
  const tableRootStyle = {
    width: '100%',
    '--srf-sticky-pagination-height': `${stickyPaginationMetrics.height}px`,
    '--srf-sticky-pagination-left': `${stickyPaginationMetrics.left}px`,
    '--srf-sticky-pagination-width': `${stickyPaginationMetrics.width}px`,
    '--srf-table-body-height': `${viewportBodyHeight}px`,
  }

  return (
    <Space
      ref={tableRootRef}
      className={tableRootClassName}
      direction="vertical"
      size={12}
      style={tableRootStyle}
    >
      {batchContent ? (
        <Alert
          type="info"
          showIcon
          message={
            <Space wrap>
              <span>已选 {effectiveSelectedRowKeys.length} 项</span>
              {batchContent}
            </Space>
          }
        />
      ) : null}
      <ProTable
        {...tableProps}
        actionRef={proActionRef}
        columns={columns}
        columnsState={{
          defaultValue: stableDefaultColumnsState,
          value: columnsState,
          onChange: (nextState) => {
            const reset = sameState(nextState, stableDefaultColumnsState)
            const nextWidths = reset ? {} : widths
            setColumnsState(nextState)
            columnsStateRef.current = nextState
            if (reset) setWidths({})
            if (reset) localStorage.removeItem(storageKey)
            else persist(storageKey, nextWidths, nextState)
          },
        }}
        components={{ header: { cell: ResizableHeaderCell } }}
        dataSource={dataRequest ? undefined : localData}
        onRow={onRow || onRowClick ? mergedOnRow : undefined}
        options={{
          density: true,
          fullScreen: true,
          reload: Boolean(dataRequest),
          setting: { draggable: true },
          ...options,
        }}
        pagination={mergedPagination}
        params={externalParams}
        request={dataRequest ? async (params) => {
          try {
            const { current, pageSize, ...requestParams } = params
            const response = await dataRequest({
              ...requestParams,
              page: current,
              page_size: pageSize,
              ...serializeTableFilters(baseColumns, filtersRef.current),
            })
            const payload = response?.data ?? response ?? {}
            return {
              data: payload.results || [],
              total: payload.count || 0,
              success: true,
            }
          } catch {
            return { data: [], total: 0, success: false }
          }
        } : undefined}
        rowSelection={mergedRowSelection}
        scroll={{
          ...scroll,
          x: Math.max(Number(scroll?.x || 0), totalWidth),
          y: scroll?.y ?? (viewportBodyHeight || undefined),
        }}
        search={false}
      />
    </Space>
  )
})

export default SmartDataTable
