import { Component, lazy, Suspense } from 'react'
import { Button, Result, Skeleton } from 'antd'
import { Routes, Route, Navigate } from 'react-router-dom'
import { RoleProvider } from './contexts/RoleContext'
import { useRole } from './contexts/roleState'
import { canAccessRoute, getDefaultAuthenticatedPath } from './routePermissions'
import LoginPage from './pages/LoginPage'

const BasicLayout = lazy(() => import('./layouts/BasicLayout'))
const ResumesPage = lazy(() => import('./pages/ResumesPage'))
const JobsPage = lazy(() => import('./pages/JobsPage'))
const SchoolsPage = lazy(() => import('./pages/SchoolsPage'))
const DepartmentsPage = lazy(() => import('./pages/DepartmentsPage'))
const ConfigPage = lazy(() => import('./pages/ConfigPage'))
const AIConnectionPage = lazy(() => import('./pages/AIConnectionPage'))
const UsersPage = lazy(() => import('./pages/UsersPage'))
const AnalyticsPage = lazy(() => import('./pages/AnalyticsPage'))
const ProcessingTasksPage = lazy(() => import('./pages/ProcessingTasksPage'))

function PageLoading() {
  return <div role="status" aria-label="页面加载中" style={{ padding: 28 }}><Skeleton active paragraph={{ rows: 6 }} /></div>
}

class PageErrorBoundary extends Component {
  state = { failed: false }
  static getDerivedStateFromError() { return { failed: true } }
  render() {
    return this.state.failed
      ? <Result status="warning" title="页面暂时无法打开" subTitle="请刷新页面重试，已提交的处理任务不会受影响。" extra={<Button type="primary" onClick={() => window.location.reload()}>刷新页面</Button>} />
      : this.props.children
  }
}

function AppRoutes() {
  const { loading, isAuthenticated, hasPermission } = useRole()

  if (loading) return <PageLoading />

  if (!isAuthenticated) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    )
  }

  const defaultPath = getDefaultAuthenticatedPath(hasPermission)

  const guarded = (path, element) => {
    return canAccessRoute(path, hasPermission) ? (
      <Suspense fallback={<PageLoading />}>{element}</Suspense>
    ) : (
      <Navigate to={defaultPath} replace />
    )
  }

  return (
    <Routes>
      <Route path="/login" element={<Navigate to={defaultPath} replace />} />
      <Route element={<BasicLayout />}>
        <Route index element={<Navigate to={defaultPath} replace />} />
        <Route
          path="/resumes"
          element={guarded('/resumes', <ResumesPage />)}
        />
        <Route path="/jobs" element={guarded('/jobs', <JobsPage />)} />
        <Route path="/schools" element={guarded('/schools', <SchoolsPage />)} />
        <Route
          path="/departments"
          element={guarded('/departments', <DepartmentsPage />)}
        />
        <Route
          path="/analytics"
          element={guarded('/analytics', <AnalyticsPage />)}
        />
        <Route
          path="/processing-tasks"
          element={guarded('/processing-tasks', <ProcessingTasksPage />)}
        />
        <Route
          path="/config"
          element={guarded('/config', <ConfigPage />)}
        />
        <Route
          path="/ai-connection"
          element={guarded('/ai-connection', <AIConnectionPage />)}
        />
        <Route
          path="/users"
          element={guarded('/users', <UsersPage />)}
        />
        <Route path="*" element={<Navigate to={defaultPath} replace />} />
      </Route>
    </Routes>
  )
}

export default function App() {
  return (
    <RoleProvider>
      <PageErrorBoundary><Suspense fallback={<PageLoading />}><AppRoutes /></Suspense></PageErrorBoundary>
    </RoleProvider>
  )
}
