import { lazy, Suspense } from 'react'
import { Skeleton } from 'antd'
import { Navigate, useSearchParams } from 'react-router-dom'
import { useRole } from '../contexts/roleState'

const ResumesPage = lazy(() => import('./ResumesPage'))

export default function ResumeWorkspace() {
  const [params] = useSearchParams()
  const { hasPermission } = useRole()
  if (params.has('tab')) {
    const next = new URLSearchParams(params)
    if (params.get('tab') === 'pool' && hasPermission('resume.view') && !next.has('pool_status')) {
      next.set('pool_status', 'admitted')
    }
    next.delete('tab')
    return <Navigate to={{ pathname: '/resumes', search: next.toString() }} replace />
  }
  return <Suspense fallback={<Skeleton active paragraph={{ rows: 6 }} />}>
    <ResumesPage />
  </Suspense>
}
