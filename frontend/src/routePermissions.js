export const DEFAULT_AUTHENTICATED_PATH = '/analytics'

export const ROUTE_PERMISSIONS = {
  '/resumes': ['resume.view', 'attempt.view_department'],
  '/position-pools': ['resume.view'],
  '/jobs': ['job.view', 'settings.manage_config'],
  '/schools': ['school.view'],
  '/departments': ['department.view'],
  '/analytics': ['analytics.view'],
  '/processing-tasks': ['pipeline.view'],
  '/config': ['settings.manage_config'],
  '/ai-connection': ['settings.manage_ai_connection'],
  '/users': ['settings.manage_permissions'],
}

export function canAccessRoute(path, hasPermission) {
  const permissions = ROUTE_PERMISSIONS[path]
  return !permissions || permissions.some((code) => hasPermission(code))
}

const AUTHENTICATED_HOME_CANDIDATES = [
  '/analytics',
  '/processing-tasks',
  '/resumes',
  '/position-pools',
  '/jobs',
  '/schools',
  '/departments',
  '/config',
  '/ai-connection',
  '/users',
]

export function getDefaultAuthenticatedPath(hasPermission) {
  return AUTHENTICATED_HOME_CANDIDATES.find((path) => canAccessRoute(path, hasPermission)) || '/resumes'
}
