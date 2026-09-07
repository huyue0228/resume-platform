import {
  ArcElement,
  BarElement,
  CategoryScale,
  Chart as ChartJS,
  Legend,
  LinearScale,
  Tooltip,
} from 'chart.js'
import { Bar, Doughnut } from 'react-chartjs-2'
import { CheckCircleFilled } from '@ant-design/icons'
import { Card, Empty, Typography } from 'antd'
import { formatChartCount, prepareDistributionRows } from './chartUtils'

ChartJS.register(ArcElement, BarElement, CategoryScale, LinearScale, Tooltip, Legend)

const CHART_COLORS = ['#4f46e5', '#16a34a', '#d97706', '#08979c', '#dc2626', '#64748b']
const PIPELINE_COLORS = ['#c7d2fe', '#a5b4fc', '#818cf8', '#6366f1', '#4f46e5']

function percentage(value, total) {
  return total ? value * 100 / total : 0
}

function chartSummary(title, rows, total) {
  const detail = rows
    .map((item) => `${item.label} ${formatChartCount(item.count)}，占 ${percentage(item.count, total).toFixed(1)}%`)
    .join('；')
  return `${title}：${detail}`
}

function EmptyChart({ description = '当前范围暂无数据' }) {
  return (
    <div className="analytics-chart-empty">
      <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={description} />
    </div>
  )
}

export function DoughnutChartCard({
  title,
  rows = [],
  emptyText,
  successWhenEmpty = false,
  onRowClick,
}) {
  const chartRows = prepareDistributionRows(rows)
  const total = chartRows.reduce((sum, item) => sum + item.count, 0)
  const colors = chartRows.map((_, index) => CHART_COLORS[index % CHART_COLORS.length])
  const data = {
    labels: chartRows.map((item) => item.label),
    datasets: [{
      data: chartRows.map((item) => item.count),
      backgroundColor: colors,
      borderWidth: 0,
      hoverOffset: 5,
    }],
  }
  const options = {
    responsive: true,
    maintainAspectRatio: false,
    cutout: '68%',
    animation: false,
    onClick: onRowClick
      ? (_event, elements = []) => {
          const index = elements[0]?.index
          if (index !== undefined && chartRows[index]) onRowClick(chartRows[index])
        }
      : undefined,
    plugins: {
      legend: { display: false },
      tooltip: {
        callbacks: {
          label: (context) => {
            const value = Number(context.raw || 0)
            return ` ${context.label}: ${formatChartCount(value)} (${percentage(value, total).toFixed(1)}%)`
          },
        },
      },
    },
  }

  return (
    <Card
      title={title}
      extra={onRowClick ? <Typography.Text type="secondary">点击数据项查看简历</Typography.Text> : null}
      className={`analytics-panel analytics-chart-card${onRowClick ? ' analytics-chart-card--clickable' : ''}`}
    >
      {chartRows.length ? (
        <div className="analytics-doughnut-layout">
          <div
            className="analytics-doughnut-canvas"
            role="img"
            aria-label={chartSummary(title, chartRows, total)}
          >
            <Doughnut data={data} options={options} aria-label={title} />
            <div className="analytics-doughnut-total" aria-hidden="true">
              <strong>{formatChartCount(total)}</strong>
              <span>合计</span>
            </div>
          </div>
          <div className="analytics-chart-legend">
            {chartRows.map((item, index) => (
              <button
                type="button"
                className="analytics-chart-legend-item"
                disabled={!onRowClick}
                key={`${String(item.key)}-${index}`}
                onClick={() => onRowClick?.(item)}
              >
                <span className="analytics-chart-dot" style={{ backgroundColor: colors[index] }} />
                <Typography.Text ellipsis={{ tooltip: item.label }}>{item.label}</Typography.Text>
                <strong>{formatChartCount(item.count)}</strong>
                <small>{percentage(item.count, total).toFixed(1)}%</small>
              </button>
            ))}
          </div>
        </div>
      ) : successWhenEmpty ? (
        <div className="analytics-success-empty">
          <CheckCircleFilled />
          <strong>暂无 AI 错误记录</strong>
          <Typography.Text type="secondary">当前筛选范围内未记录模型处理错误</Typography.Text>
        </div>
      ) : (
        <EmptyChart description={emptyText} />
      )}
    </Card>
  )
}

export function HorizontalBarChartCard({
  title,
  rows = [],
  emptyText,
  palette = 'default',
  onRowClick,
}) {
  const chartRows = rows
    .map((item) => ({ ...item, count: Number(item.count || 0) }))
    .filter((item) => item.count > 0)
  const colors = palette === 'pipeline'
    ? chartRows.map((_, index) => PIPELINE_COLORS[Math.min(index, PIPELINE_COLORS.length - 1)])
    : chartRows.map((_, index) => CHART_COLORS[index % CHART_COLORS.length])
  const data = {
    labels: chartRows.map((item) => item.label),
    datasets: [{
      label: '候选人数',
      data: chartRows.map((item) => item.count),
      backgroundColor: colors,
      borderRadius: 6,
      borderSkipped: false,
      barThickness: 22,
    }],
  }
  const options = {
    indexAxis: 'y',
    responsive: true,
    maintainAspectRatio: false,
    animation: false,
    onClick: onRowClick
      ? (_event, elements = []) => {
          const index = elements[0]?.index
          if (index !== undefined && chartRows[index]) onRowClick(chartRows[index])
        }
      : undefined,
    scales: {
      x: {
        beginAtZero: true,
        ticks: { precision: 0, color: '#6b7280' },
        grid: { color: '#eef2f7' },
        border: { display: false },
      },
      y: {
        ticks: { color: '#374151', font: { weight: 500 } },
        grid: { display: false },
        border: { display: false },
      },
    },
    plugins: {
      legend: { display: false },
      tooltip: {
        callbacks: {
          label: (context) => ` 候选人数：${formatChartCount(context.raw)}`,
        },
      },
    },
  }
  const height = Math.max(220, Math.min(360, chartRows.length * 36 + 56))
  const summary = `${title}：${chartRows.map((item) => `${item.label} ${formatChartCount(item.count)}`).join('；')}`

  return (
    <Card
      title={title}
      extra={onRowClick ? <Typography.Text type="secondary">点击柱形查看简历</Typography.Text> : null}
      className={`analytics-panel analytics-chart-card analytics-bar-chart-card${onRowClick ? ' analytics-chart-card--clickable' : ''}`}
    >
      {chartRows.length ? (
        <div className="analytics-bar-canvas" style={{ height }} role="img" aria-label={summary}>
          <Bar data={data} options={options} aria-label={title} />
        </div>
      ) : (
        <EmptyChart description={emptyText} />
      )}
    </Card>
  )
}
