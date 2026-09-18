import { PageContainer } from '@ant-design/pro-components'
import ProcessingTaskCenter from '../components/ProcessingTaskCenter'

export default function ProcessingTasksPage() {
  return (
    <PageContainer
      title="任务中心"
      content="统一管理简历处理、定时计划和分配任务，查询执行记录与进度。"
    >
      <ProcessingTaskCenter />
    </PageContainer>
  )
}
