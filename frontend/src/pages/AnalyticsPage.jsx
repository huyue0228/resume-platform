import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { PageContainer } from '@ant-design/pro-components'
import {
  Alert,
  Button,
  Card,
  Col,
  DatePicker,
  Empty,
  Row,
  Select,
  Skeleton,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import {
  CalendarOutlined,
  CheckCircleOutlined,
  ClockCircleOutlined,
  CommentOutlined,
  DeploymentUnitOutlined,
  DownloadOutlined,
  ReloadOutlined,
  SearchOutlined,
  SendOutlined,
  TeamOutlined,
} from '@ant-design/icons'
import { exportResumeResultReport, fetchRecruitmentOverview } from '../api/services'
import { useRole } from '../contexts/roleState'
import { downloadBlobFromResponse } from '../utils/download'
import {
  DoughnutChartCard,
  HorizontalBarChartCard,
} from './analytics/DashboardCharts'
import { buildAnalyticsDrilldownLocation } from './analytics/drilldown'
import './AnalyticsPage.css'

const numberFormatter = new Intl.NumberFormat('zh-CN')

function formatCount(value) {
  return numberFormatter.format(Number(value || 0))
}

function formatRate(value) {
  return `${Number(value || 0).toFixed(1)}%`
}

function formatDuration(value) {
  if (value == null) return '暂无数据'
  const hours = Number(value)
  if (hours < 1) return `${Math.max(1, Math.round(hours * 60))} 分钟`
  if (hours >= 48) return `${(hours / 24).toFixed(1)} 天`
  return `${hours.toFixed(hours < 10 ? 1 : 0)} 小时`
}

function SectionHeading({ title, description }) {
  return (
    <div className="analytics-section-heading">
      <Typography.Title level={4}>{title}</Typography.Title>
      {description ? <Typography.Text type="secondary">{description}</Typography.Text> : null}
    </div>
  )
}

function FilterField({ label, className = '', children }) {
  return (
    <label className={`analytics-filter-field ${className}`}>
      <Typography.Text type="secondary">{label}</Typography.Text>
      {children}
    </label>
  )
}

function activateOnKeyboard(event, action) {
  if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    action?.()
  }
}

function MetricCard({
  title,
  value,
  icon,
  tone,
  rate,
  detail,
  detailActions = [],
  onDrilldown,
}) {
  const metricContent = (
    <>
      <Typography.Text className="analytics-metric-label">{title}</Typography.Text>
      <strong className="analytics-metric-value">{formatCount(value)}</strong>
    </>
  )
  return (
    <Card
      className={`analytics-metric-card analytics-metric-card--${tone}${onDrilldown ? ' analytics-metric-card--clickable' : ''}`}
    >
      <span className="analytics-metric-icon" aria-hidden="true">{icon}</span>
      {onDrilldown ? (
        <button
          type="button"
          className="analytics-metric-main"
          aria-label={`${title}，查看对应简历`}
          onClick={onDrilldown}
        >
          {metricContent}
        </button>
      ) : <div className="analytics-metric-main">{metricContent}</div>}
      <div className="analytics-metric-footer">
        {rate == null ? null : <Tag bordered={false}>{formatRate(rate)}</Tag>}
        {detailActions.length ? detailActions.map((action) => (
          <Button
            type="link"
            size="small"
            key={action.label}
            onClick={(event) => {
              event.stopPropagation()
              action.onClick?.()
            }}
          >
            {action.label}
          </Button>
        )) : <span>{detail}</span>}
      </div>
    </Card>
  )
}

function EfficiencyStrip({ values = {}, onItemClick }) {
  const items = [
    ['导入至分配', values.to_allocation, DeploymentUnitOutlined, 'allocated'],
    ['导入至下发', values.to_dispatch, SendOutlined, 'dispatched'],
    ['导入至反馈', values.to_feedback, CommentOutlined, 'feedback'],
  ]
  return (
    <Card className="analytics-efficiency-strip">
      {items.map(([label, value, Icon, dimension]) => (
        <div
          className={`analytics-efficiency-item${onItemClick ? ' analytics-efficiency-item--clickable' : ''}`}
          key={label}
          role={onItemClick ? 'link' : undefined}
          tabIndex={onItemClick ? 0 : undefined}
          aria-label={onItemClick ? `${label}，查看参与统计的简历` : undefined}
          onClick={onItemClick ? () => onItemClick(dimension, label) : undefined}
          onKeyDown={onItemClick
            ? (event) => activateOnKeyboard(event, () => onItemClick(dimension, label))
            : undefined}
        >
          <span className="analytics-efficiency-icon"><Icon /></span>
          <div>
            <Typography.Text type="secondary">{label}</Typography.Text>
            <strong>{formatDuration(value)}</strong>
          </div>
          <ClockCircleOutlined className="analytics-efficiency-clock" />
        </div>
      ))}
    </Card>
  )
}

function HandlingDurationCard({ title, metric = {} }) {
  return (
    <Card size="small" className="analytics-handling-metric">
      <Typography.Text type="secondary">{title}</Typography.Text>
      <strong>{formatDuration(metric.avg)}</strong>
      <Typography.Text type="secondary">
        中位数 {formatDuration(metric.median)} · P90 {formatDuration(metric.p90)}
      </Typography.Text>
      <Typography.Text type="secondary">已完成样本 {formatCount(metric.sample_count)}</Typography.Text>
    </Card>
  )
}

function HandlingSpeedPanel({ value = {} }) {
  const overall = value.overall || {}
  const departmentRows = value.departments || []
  const columns = [
    {
      title: '当前接收部门',
      dataIndex: 'department_name',
      key: 'department_name',
      render: (name, row) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{name || '-'}</Typography.Text>
          <Typography.Text type="secondary">{row.primary_department_name || '-'}</Typography.Text>
        </Space>
      ),
    },
    {
      title: '平均处理时长',
      dataIndex: ['processing_hours', 'avg'],
      key: 'avg',
      render: formatDuration,
    },
    {
      title: '中位数',
      dataIndex: ['processing_hours', 'median'],
      key: 'median',
      render: formatDuration,
    },
    {
      title: 'P90',
      dataIndex: ['processing_hours', 'p90'],
      key: 'p90',
      render: formatDuration,
    },
    {
      title: '已完成样本',
      dataIndex: ['processing_hours', 'sample_count'],
      key: 'sample_count',
      render: formatCount,
    },
    {
      title: '待处理',
      dataIndex: 'pending_count',
      key: 'pending_count',
      render: formatCount,
    },
    {
      title: '最长待处理',
      dataIndex: 'max_pending_age_hours',
      key: 'max_pending_age_hours',
      render: formatDuration,
    },
  ]

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <div className="analytics-handling-grid">
        <HandlingDurationCard title="HR 下发时长" metric={overall.hr_dispatch_hours} />
        <HandlingDurationCard title="部门处理时长" metric={overall.department_processing_hours} />
        <HandlingDurationCard title="总反馈时长" metric={overall.total_feedback_hours} />
        <Card size="small" className="analytics-handling-metric">
          <Typography.Text type="secondary">当前待处理</Typography.Text>
          <strong>{formatCount(overall.pending_count)}</strong>
          <Typography.Text type="secondary">
            最长已等待 {formatDuration(overall.max_pending_age_hours)}
          </Typography.Text>
        </Card>
      </div>
      <Card size="small" title="部门处理时效" className="analytics-panel">
        <Table
          rowKey="department_id"
          columns={columns}
          dataSource={departmentRows}
          pagination={false}
          size="small"
          scroll={{ x: 920 }}
          locale={{ emptyText: '当前范围暂无部门处理记录' }}
        />
      </Card>
    </Space>
  )
}

export default function AnalyticsPage() {
  const navigate = useNavigate()
  const { hasPermission } = useRole()
  const [loading, setLoading] = useState(true)
  const [reportExporting, setReportExporting] = useState(false)
  const [error, setError] = useState('')
  const [data, setData] = useState(null)
  const [filters, setFilters] = useState({})
  const [dateRange, setDateRange] = useState(null)

  const load = useCallback(async (nextFilters = {}) => {
    setLoading(true)
    setError('')
    try {
      const response = await fetchRecruitmentOverview(nextFilters)
      setData(response.data)
    } catch (requestError) {
      setError(requestError?.response?.data?.detail || '招聘分析数据加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const options = data?.filter_options || {}
  const canDrilldown = hasPermission('resume.view')
  const secondaryDepartmentOptions = useMemo(() => {
    const departments = options.departments || []
    if (!filters.primary_department_id) return departments
    return departments.filter(
      (department) => department.parent_id === filters.primary_department_id,
    )
  }, [filters.primary_department_id, options.departments])
  const activeRange = data?.filters
    ? `${data.filters.date_from} 至 ${data.filters.date_to}`
    : '最近 30 天'
  const openDrilldown = useCallback((dimension, row, title) => {
    if (!canDrilldown || !data?.filters) return
    navigate(buildAnalyticsDrilldownLocation({
      filters: data.filters,
      options: data.filter_options,
      dimension,
      row,
      title,
    }))
  }, [canDrilldown, data?.filter_options, data?.filters, navigate])
  const asOf = useMemo(() => {
    if (!data?.data_as_of) return '-'
    return new Date(data.data_as_of).toLocaleString('zh-CN', { hour12: false })
  }, [data?.data_as_of])

  const apply = () => load(filters)
  const reset = () => {
    setFilters({})
    setDateRange(null)
    load({})
  }
  const exportResultReport = async () => {
    const activeFilters = data?.filters
    if (!activeFilters?.date_from || !activeFilters?.date_to) return
    setReportExporting(true)
    try {
      const params = {
        imported_after: activeFilters.date_from,
        imported_before: activeFilters.date_to,
      }
      if (activeFilters.primary_department_id) {
        params.primary_department_id = activeFilters.primary_department_id
      }
      if (activeFilters.department_id) params.department_id = activeFilters.department_id
      const response = await exportResumeResultReport(params)
      downloadBlobFromResponse(response, '简历结果报表.xlsx')
      message.success('结果报表已导出')
    } catch {
      message.error('结果报表导出失败')
    } finally {
      setReportExporting(false)
    }
  }
  const initialLoading = loading && !data
  const summary = data?.summary || {}
  const conversion = data?.conversion || {}
  const pipelineRows = [
    { key: 'candidate', label: '候选人', count: summary.candidate_count },
    { key: 'allocated', label: '生成分配', count: summary.allocated_count },
    { key: 'dispatched', label: '完成下发', count: summary.dispatched_count },
    { key: 'feedback', label: '收到反馈', count: summary.feedback_count },
    { key: 'passed', label: '最终通过', count: summary.passed_count },
  ]

  return (
    <PageContainer
      className="analytics-page"
      title="数据看板"
      content="聚焦候选规模、转化效率与招聘结果，所有阶段指标按候选人去重。"
      extra={(
        <Space wrap>
          {hasPermission('resume.view') ? (
            <Button
              icon={<DownloadOutlined />}
              loading={reportExporting}
              disabled={!data?.filters?.date_from || !data?.filters?.date_to}
              onClick={exportResultReport}
            >
              导出结果报表
            </Button>
          ) : null}
          <div className="analytics-as-of">
            <CalendarOutlined />
            <span>数据截至</span>
            <strong>{asOf}</strong>
          </div>
        </Space>
      )}
    >
      {error ? (
        <Alert
          className="analytics-alert"
          type="error"
          showIcon
          message={error}
          action={<Button size="small" onClick={() => load(filters)}>重试</Button>}
        />
      ) : null}

      {initialLoading ? (
        <Card className="analytics-loading-card">
          <Skeleton active paragraph={{ rows: 8 }} />
        </Card>
      ) : data ? (
        <Spin spinning={loading}>
          <div className="analytics-content">
            <section className="analytics-overview-section">
              <div className="analytics-overview-toolbar">
                <div className="analytics-inline-filter">
                  <FilterField label="时间区间" className="analytics-filter-field--range">
                    <DatePicker.RangePicker
                      value={dateRange}
                      onChange={(range, dateStrings) => {
                        setDateRange(range)
                        setFilters((current) => ({
                          ...current,
                          date_from: dateStrings?.[0] || undefined,
                          date_to: dateStrings?.[1] || undefined,
                        }))
                      }}
                    />
                  </FilterField>
                  <FilterField label="一级部门">
                    <Select
                      allowClear
                      value={filters.primary_department_id}
                      showSearch
                      optionFilterProp="label"
                      placeholder="全部一级部门"
                      options={options.primary_departments || []}
                      onChange={(value) => setFilters((current) => {
                        const selectedSecondary = (options.departments || []).find(
                          (department) => department.value === current.department_id,
                        )
                        return {
                          ...current,
                          primary_department_id: value,
                          department_id: (
                            value
                            && selectedSecondary?.parent_id !== value
                              ? undefined
                              : current.department_id
                          ),
                        }
                      })}
                    />
                  </FilterField>
                  <FilterField label="二级部门">
                    <Select
                      allowClear
                      value={filters.department_id}
                      showSearch
                      optionFilterProp="label"
                      placeholder="全部二级部门"
                      options={secondaryDepartmentOptions}
                      onChange={(value) => setFilters((current) => ({ ...current, department_id: value }))}
                    />
                  </FilterField>
                  <div className="analytics-filter-actions">
                    <Button type="primary" icon={<SearchOutlined />} onClick={apply}>查询</Button>
                    <Button icon={<ReloadOutlined />} onClick={reset}>重置</Button>
                  </div>
                </div>
                <div className="analytics-filter-note">
                  <CalendarOutlined />
                  <Typography.Text>当前范围：{activeRange}</Typography.Text>
                </div>
              </div>

              <div className="analytics-metric-grid">
                <MetricCard
                  title="候选人数"
                  value={summary.candidate_count}
                  icon={<TeamOutlined />}
                  tone="primary"
                  detailActions={canDrilldown ? [
                    {
                      label: `投递 ${formatCount(summary.resume_count)}`,
                      onClick: () => openDrilldown('candidate', null, '投递对应候选人'),
                    },
                    {
                      label: `已分类 ${formatCount(summary.classified_count)}`,
                      onClick: () => openDrilldown('classified', null, '已分类'),
                    },
                  ] : []}
                  detail={`投递 ${formatCount(summary.resume_count)} · 已分类 ${formatCount(summary.classified_count)}`}
                  onDrilldown={canDrilldown ? () => openDrilldown('candidate', null, '候选人数') : undefined}
                />
                <MetricCard
                  title="已生成分配"
                  value={summary.allocated_count}
                  icon={<DeploymentUnitOutlined />}
                  tone="violet"
                  rate={conversion.allocated_rate}
                  detail="候选人分配转化率"
                  onDrilldown={canDrilldown ? () => openDrilldown('allocated', null, '已生成分配') : undefined}
                />
                <MetricCard
                  title="已下发"
                  value={summary.dispatched_count}
                  icon={<SendOutlined />}
                  tone="cyan"
                  rate={conversion.dispatched_rate}
                  detail="候选人下发转化率"
                  onDrilldown={canDrilldown ? () => openDrilldown('dispatched', null, '已下发') : undefined}
                />
                <MetricCard
                  title="已反馈"
                  value={summary.feedback_count}
                  icon={<CommentOutlined />}
                  tone="warning"
                  rate={conversion.feedback_rate}
                  detail="候选人反馈转化率"
                  onDrilldown={canDrilldown ? () => openDrilldown('feedback', null, '已反馈') : undefined}
                />
                <MetricCard
                  title="已通过"
                  value={summary.passed_count}
                  icon={<CheckCircleOutlined />}
                  tone="success"
                  rate={conversion.passed_rate}
                  detailActions={canDrilldown ? [{
                    label: `已归档 ${formatCount(summary.archived_count)}`,
                    onClick: () => openDrilldown('archived', null, '已归档'),
                  }] : []}
                  detail={`已归档 ${formatCount(summary.archived_count)}`}
                  onDrilldown={canDrilldown ? () => openDrilldown('passed', null, '已通过') : undefined}
                />
              </div>
            </section>

            <section>
              <SectionHeading title="招聘概览" description="分配来源、流程转化与 AI 建议构成" />
              <Row gutter={[16, 16]} className="analytics-equal-row">
                <Col xs={24} xl={7}>
                  <DoughnutChartCard
                    title="分配来源"
                    rows={data.source_distribution}
                    onRowClick={canDrilldown ? (row) => openDrilldown('source', row, '分配来源') : undefined}
                  />
                </Col>
                <Col xs={24} xl={10}>
                  <HorizontalBarChartCard
                    title="招聘流程"
                    rows={pipelineRows}
                    palette="pipeline"
                    onRowClick={canDrilldown
                      ? (row) => openDrilldown(row.key === 'candidate' ? 'candidate' : row.key, row, '招聘流程')
                      : undefined}
                  />
                </Col>
                <Col xs={24} xl={7}>
                  <DoughnutChartCard
                    title="AI 建议分布"
                    rows={data.ai_recommendation_distribution}
                    onRowClick={canDrilldown ? (row) => openDrilldown('ai_recommendation', row, 'AI 建议分布') : undefined}
                  />
                </Col>
              </Row>
            </section>

            <section>
              <SectionHeading title="处理效率" description="候选人从导入到关键阶段的平均耗时" />
              <EfficiencyStrip
                values={data.average_hours}
                onItemClick={canDrilldown
                  ? (dimension, label) => openDrilldown(dimension, null, label)
                  : undefined}
              />
            </section>

            <section>
              <SectionHeading
                title="人工处理时效"
                description="按自然时间统计下发、部门处理和最终反馈速度，系统自动路由不计入人工时长"
              />
              <HandlingSpeedPanel value={data.handling_speed} />
            </section>

            <section>
              <SectionHeading title="岗位与人才结构" description="识别需求集中度和候选人结构分布" />
              <Row gutter={[16, 16]} className="analytics-equal-row">
                <Col xs={24} xl={8}>
                  <HorizontalBarChartCard
                    title="岗位排行"
                    rows={data.job_ranking}
                    onRowClick={canDrilldown ? (row) => openDrilldown('job', row, '岗位排行') : undefined}
                  />
                </Col>
                <Col xs={24} xl={8}>
                  <HorizontalBarChartCard
                    title="一级部门排行"
                    rows={data.primary_department_ranking}
                    onRowClick={canDrilldown ? (row) => openDrilldown('primary_department', row, '一级部门排行') : undefined}
                  />
                </Col>
                <Col xs={24} xl={8}>
                  <HorizontalBarChartCard
                    title="二级部门排行"
                    rows={data.department_ranking}
                    onRowClick={canDrilldown ? (row) => openDrilldown('department', row, '二级部门排行') : undefined}
                  />
                </Col>
                <Col xs={24} lg={12}>
                  <DoughnutChartCard
                    title="院校标签"
                    rows={data.school_tag_ranking}
                    onRowClick={canDrilldown ? (row) => openDrilldown('school_tag', row, '院校标签') : undefined}
                  />
                </Col>
                <Col xs={24} lg={12}>
                  <DoughnutChartCard
                    title="最高学历"
                    rows={data.education_distribution}
                    onRowClick={canDrilldown ? (row) => openDrilldown('education', row, '最高学历') : undefined}
                  />
                </Col>
              </Row>
            </section>

            <section>
              <SectionHeading title="结果诊断" description="集中查看 AI 异常、归档和未通过原因" />
              <Row gutter={[16, 16]} className="analytics-equal-row">
                <Col xs={24} xl={8}>
                  <DoughnutChartCard
                    title="AI 错误码"
                    rows={data.ai_error_distribution}
                    successWhenEmpty
                    onRowClick={canDrilldown ? (row) => openDrilldown('ai_error', row, 'AI 错误码') : undefined}
                  />
                </Col>
                <Col xs={24} xl={8}>
                  <DoughnutChartCard
                    title="归档原因"
                    rows={data.archive_reason_distribution}
                    onRowClick={canDrilldown ? (row) => openDrilldown('archive_reason', row, '归档原因') : undefined}
                  />
                </Col>
                <Col xs={24} xl={8}>
                  <DoughnutChartCard
                    title="未通过原因"
                    rows={data.rejection_reason_distribution}
                    onRowClick={canDrilldown ? (row) => openDrilldown('rejection_reason', row, '未通过原因') : undefined}
                  />
                </Col>
              </Row>
            </section>
          </div>
        </Spin>
      ) : !error ? (
        <Card className="analytics-empty-card">
          <Empty description="暂无招聘分析数据" />
        </Card>
      ) : null}
    </PageContainer>
  )
}
