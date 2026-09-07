import { createContext, useContext } from 'react'

export const RoleContext = createContext(null)

export const ROLES = {
  hr: { label: 'HR' },
  admin: { label: '管理员' },
  secondary_contact: { label: '二级接口人' },
  tertiary_contact: { label: '三级接口人' },
}

export function useRole() {
  return useContext(RoleContext)
}
