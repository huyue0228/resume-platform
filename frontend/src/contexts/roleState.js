import { createContext, useContext } from 'react'

export const RoleContext = createContext(null)

export const ROLES = {
  hr: { label: '一级部门HR' },
  admin: { label: '管理员' },
  primary_hr: { label: '一级部门HR' },
  secondary_hr: { label: '二级部门HR' },
  secondary_contact: { label: '接口人' },
  tertiary_contact: { label: '简历筛选人' },
}

export function useRole() {
  return useContext(RoleContext)
}
