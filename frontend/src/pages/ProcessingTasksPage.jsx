import { PageContainer } from '@ant-design/pro-components'
import ProcessingTaskCenter from '../components/ProcessingTaskCenter'

export default function ProcessingTasksPage() {
  return (
    <PageContainer
      title="处理任务"
      content="统一创建立即或定时处理任务，按状态、来源与时间查询执行记录。"
    >
      <ProcessingTaskCenter />
    </PageContainer>
  )
}
