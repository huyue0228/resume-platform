import {
  ApartmentOutlined,
  BankOutlined,
  BarChartOutlined,
  ControlOutlined,
  DatabaseOutlined,
  ProfileOutlined,
  SafetyCertificateOutlined,
  ScheduleOutlined,
  SettingOutlined,
  TeamOutlined,
} from '@ant-design/icons'

export const allRoute = {
  path: '/',
  routes: [
    {
      path: '/analytics',
      name: '数据看板',
      icon: <BarChartOutlined />,
    },
    {
      path: '/processing-tasks',
      name: '处理任务',
      icon: <ScheduleOutlined />,
    },
    {
      path: '/data',
      name: '数据管理',
      icon: <DatabaseOutlined />,
      routes: [
        { path: '/resumes', name: '简历库', icon: <ProfileOutlined /> },
        { path: '/jobs', name: '岗位需求', icon: <ApartmentOutlined /> },
        { path: '/schools', name: '院校清单', icon: <BankOutlined /> },
        { path: '/departments', name: '部门接口人', icon: <TeamOutlined /> },
      ],
    },
    {
      path: '/system',
      name: '系统设置',
      icon: <SettingOutlined />,
      routes: [
        { path: '/config', name: '配置项', icon: <ControlOutlined /> },
        { path: '/ai-connection', name: 'AI 模型连接', icon: <ControlOutlined /> },
        { path: '/users', name: '用户权限', icon: <SafetyCertificateOutlined /> },
      ],
    },
  ],
}
