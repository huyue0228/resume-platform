import { useEffect, useState } from 'react'
import { Outlet, useNavigate, useLocation } from 'react-router-dom'
import { ProLayout } from '@ant-design/pro-components'
import { Dropdown, Tag } from 'antd'
import { UserOutlined } from '@ant-design/icons'
import { APP_NAME } from '../appBrand'
import { useRole, ROLES } from '../contexts/roleState'
import { canAccessRoute } from '../routePermissions'
import BrandLogo from '../components/BrandLogo'
import { allRoute } from './menuRoutes'
import { appLayoutSettings, appLayoutToken, appSiderMenuProps } from '../theme'
import useUsagePageView from '../utils/useUsagePageView'

const ROOT_MENU_KEYS = ['/data', '/system']

function pathMatchesGroup(pathname, route) {
  if (!route.routes) return false
  return route.routes.some((child) => child.path === pathname)
}

function defaultOpenKeys(pathname) {
  return allRoute.routes
    .filter((route) => ROOT_MENU_KEYS.includes(route.path) && pathMatchesGroup(pathname, route))
    .map((route) => route.path)
}

function filterRoutesByPermission(routes, hasPermission) {
  const keepRoute = (route) => {
    return canAccessRoute(route.path, hasPermission)
  }
  const next = routes
    .map((route) => {
      const children = route.routes
        ? filterRoutesByPermission(route.routes, hasPermission)
        : undefined
      return { ...route, routes: children }
    })
    .filter((route) => {
      if (route.routes) return route.routes.length > 0
      return keepRoute(route)
    })
  return next
}

export default function BasicLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const { role, roles, user, logout, hasPermission, isContact } = useRole()
  const [pathname, setPathname] = useState(location.pathname)
  const [openKeys, setOpenKeys] = useState(() => defaultOpenKeys(location.pathname))
  useUsagePageView(location.pathname)

  useEffect(() => {
    setPathname(location.pathname)
    const activeParentKeys = defaultOpenKeys(location.pathname)
    if (activeParentKeys.length) {
      setOpenKeys((keys) => [...new Set([...keys, ...activeParentKeys])])
    }
  }, [location.pathname])

  const handleLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  const route = {
    ...allRoute,
    routes: filterRoutesByPermission(allRoute.routes, hasPermission),
  }

  return (
    <ProLayout
      {...appLayoutSettings}
      className="srf-app-layout"
      title={APP_NAME}
      logo={<BrandLogo size={28} />}
      token={appLayoutToken}
      route={route}
      location={{ pathname }}
      onPageChange={(loc) => setPathname(loc?.pathname || location.pathname)}
      menuProps={{
        ...appSiderMenuProps,
        openKeys,
        onOpenChange: setOpenKeys,
      }}
      menuItemRender={(item, dom) => (
        <div
          className="srf-menu-item-link"
          onClick={() => {
            setPathname(item.path)
            navigate(item.path)
          }}
        >
          {dom}
        </div>
      )}
      avatarProps={{
        icon: <UserOutlined />,
        title: (
          <span>
            {user?.username || ROLES[role]?.label}
            <Tag color={isContact ? 'orange' : 'blue'} style={{ marginLeft: 8 }}>
              {roles?.[0] || ROLES[role]?.label || '用户'}
            </Tag>
          </span>
        ),
        size: 'small',
        render: (_, avatar) => (
          <Dropdown
            menu={{
              items: [
                {
                  key: 'profile',
                  label: `${user?.first_name || user?.username || '当前用户'}`,
                  disabled: true,
                },
                { type: 'divider' },
                { key: 'logout', label: '退出登录' },
              ],
              onClick: ({ key }) => {
                if (key === 'logout') handleLogout()
              },
            }}
          >
            {avatar}
          </Dropdown>
        ),
      }}
    >
      <Outlet />
    </ProLayout>
  )
}
