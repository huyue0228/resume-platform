import { describe, expect, it, vi } from 'vitest'
import { canAccessRoute, DEFAULT_AUTHENTICATED_PATH, getDefaultAuthenticatedPath } from './routePermissions'

describe('route permissions', () => {
  it('uses the data dashboard as the authenticated landing page', () => {
    expect(DEFAULT_AUTHENTICATED_PATH).toBe('/analytics')
    expect(getDefaultAuthenticatedPath((code) => code === 'analytics.view')).toBe('/analytics')
  })

  it('falls back to the first accessible navigation item', () => {
    expect(getDefaultAuthenticatedPath((code) => code === 'pipeline.view')).toBe('/processing-tasks')
    expect(getDefaultAuthenticatedPath((code) => code === 'attempt.view_department')).toBe('/resumes')
  })

  it('allows a route when any configured permission is available', () => {
    const hasPermission = vi.fn((code) => code === 'attempt.view_department')

    expect(canAccessRoute('/resumes', hasPermission)).toBe(true)
    expect(canAccessRoute('/jobs', hasPermission)).toBe(false)
  })

  it('does not restrict menu groups without a permission entry', () => {
    expect(canAccessRoute('/data', vi.fn(() => false))).toBe(true)
  })

  it('requires the dedicated analytics permission for the dashboard', () => {
    expect(canAccessRoute('/analytics', (code) => code === 'analytics.view')).toBe(true)
    expect(canAccessRoute('/analytics', () => false)).toBe(false)
  })

  it('requires pipeline permission for the processing task page', () => {
    expect(canAccessRoute('/processing-tasks', (code) => code === 'pipeline.view')).toBe(true)
    expect(canAccessRoute('/processing-tasks', () => false)).toBe(false)
  })

})
