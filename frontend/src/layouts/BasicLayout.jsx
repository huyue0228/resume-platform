import { useEffect, useState } from 'react'
import { Link, Outlet, useNavigate, useLocation } from 'react-router-dom'
import { ProLayout } from '@ant-design/pro-components'
import { Dropdown, Tag } from 'antd'
import { UserOutlined } from '@ant-design/icons'
import { APP_NAME } from '../appBrand'
import { useRole, ROLES } from '../contexts/roleState'
import { canAccessRoute } from '../routePermissions'
import BrandLogo from '../components/BrandLogo'
import QuickNavigation from '../components/QuickNavigation'
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
  const [openKeys, setOpenKeys] = useState(() => [...new Set(['/data', ...defaultOpenKeys(location.pathname)])])
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
        <Link
          className="srf-menu-item-link"
          to={item.path}
          aria-label={item.name}
          aria-current={pathname === item.path ? 'page' : undefined}
        >
          {dom}
        </Link>
      )}
      avatarProps={{
        icon: <UserOutlined />,
        title: (
          <span>
            {user?.username || ROLES[role]?.label}
            <Tag color={isContact ? 'orange' : 'default'} style={{ marginLeft: 8 }}>
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
      <div className="workspace-topbar">
        <span className="workspace-context"><span className="workspace-context-mark" />招聘工作空间</span>
        <QuickNavigation routes={route.routes} />
      </div>
      <Outlet />
    </ProLayout>
  )
}
