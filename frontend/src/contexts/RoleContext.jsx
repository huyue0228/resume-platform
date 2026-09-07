import { useEffect, useMemo, useState } from 'react'
import {
  completeW3OAuth2Login as completeW3OAuth2LoginApi,
  fetchMe,
  logout as logoutApi,
  validateDevToken,
} from '../api/services'
import { RoleContext } from './roleState'

export function RoleProvider({ children }) {
  const [token, setToken] = useState(() => localStorage.getItem('srf_token') || '')
  const [user, setUser] = useState(null)
  const [loading, setLoading] = useState(Boolean(token))

  const refreshMe = async () => {
    if (!localStorage.getItem('srf_token')) {
      setUser(null)
      setLoading(false)
      return null
    }
    setLoading(true)
    try {
      const { data } = await fetchMe()
      setUser(data)
      return data
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refreshMe().catch(() => {
      localStorage.removeItem('srf_token')
      setToken('')
      setUser(null)
      setLoading(false)
    })
  }, [token])

  const applyLogin = (data) => {
    localStorage.setItem('srf_token', data.token)
    setToken(data.token)
    setUser(data.user)
    return data.user
  }

  const completeW3OAuth2Login = async () => {
    const { data } = await completeW3OAuth2LoginApi()
    return applyLogin(data)
  }

  const loginWithDevToken = async (rawToken) => {
    const devToken = String(rawToken || '').trim()
    const { data: userData } = await validateDevToken(devToken)
    return applyLogin({ token: devToken, user: userData })
  }

  const logout = async () => {
    try {
      await logoutApi()
    } catch {
      // token may already be invalid
    }
    localStorage.removeItem('srf_token')
    setToken('')
    setUser(null)
  }

  const permissions = useMemo(() => new Set(user?.permissions || []), [user])
  const hasPermission = (code) => permissions.has(code)
  const role = user?.role || 'hr'
  const dataScope = user?.data_scope || { type: 'none' }
  const contactDepartmentLevel = Number(
    dataScope.department_level
      ?? dataScope.level
      ?? user?.contact?.department_level
      ?? 0,
  )
  const isContact = Boolean(user?.contact) && hasPermission('attempt.view_department')
  const isSecondaryContact = isContact && contactDepartmentLevel === 2
  const isTertiaryContact = isContact && contactDepartmentLevel === 3

  return (
    <RoleContext.Provider
      value={{
        token,
        user,
        loading,
        role,
        roles: user?.roles || [],
        permissions: user?.permissions || [],
        contact: user?.contact || null,
        dataScope,
        isAuthenticated: Boolean(token && user),
        completeW3OAuth2Login,
        loginWithDevToken,
        logout,
        refreshMe,
        hasPermission,
        isContact,
        isSecondaryContact,
        isTertiaryContact,
      }}
    >
      {children}
    </RoleContext.Provider>
  )
}
