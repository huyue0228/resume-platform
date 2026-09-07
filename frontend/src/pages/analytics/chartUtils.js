const numberFormatter = new Intl.NumberFormat('zh-CN')

export function formatChartCount(value) {
  return numberFormatter.format(Number(value || 0))
}

export function prepareDistributionRows(rows = [], maximumItems = 5) {
  const ordered = rows
    .map((item) => ({
      ...item,
      count: Number(item.count || 0),
      drilldownItems: item.drilldownItems?.length ? item.drilldownItems : [item],
    }))
    .filter((item) => item.count > 0)
    .sort((left, right) => right.count - left.count)

  if (ordered.length <= maximumItems) return ordered

  return [
    ...ordered.slice(0, maximumItems),
    {
      key: 'other',
      label: '其他',
      count: ordered.slice(maximumItems).reduce((sum, item) => sum + item.count, 0),
      drilldownItems: ordered.slice(maximumItems).flatMap((item) => item.drilldownItems),
    },
  ]
}
