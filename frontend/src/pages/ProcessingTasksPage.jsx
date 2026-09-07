import { PageContainer } from '@ant-design/pro-components'
import ProcessingTaskCenter from '../components/ProcessingTaskCenter'

export default function ProcessingTasksPage() {
  return (
    <PageContainer
      title="处理任务"
      content="查看处理任务进度、业务结果和异常，并可继续筛选对应候选人。"
    >
      <ProcessingTaskCenter />
    </PageContainer>
  )
}
