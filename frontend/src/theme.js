export const appTheme = {
  token: {
    colorPrimary: '#4f46e5',
    colorSuccess: '#16a34a',
    colorWarning: '#d97706',
    colorError: '#dc2626',
    colorInfo: '#4f46e5',
    colorBgLayout: '#f5f7fb',
    colorBgContainer: '#ffffff',
    colorText: '#1f2937',
    colorTextSecondary: '#6b7280',
    colorBorder: '#dfe3ea',
    colorBorderSecondary: '#e5e7eb',
    colorFillAlter: '#f8fafc',
    borderRadius: 8,
    borderRadiusLG: 14,
    controlHeight: 34,
    fontSize: 14,
    boxShadowTertiary: '0 1px 3px rgba(16, 24, 40, 0.06), 0 1px 2px rgba(16, 24, 40, 0.04)',
  },
  components: {
    Alert: {
      borderRadiusLG: 10,
    },
    Button: {
      borderRadius: 8,
      primaryShadow: 'none',
    },
    Card: {
      borderRadiusLG: 14,
      headerBg: '#ffffff',
      headerFontSize: 14,
      headerHeight: 52,
      bodyPadding: 18,
    },
    Drawer: {
      colorBgElevated: '#ffffff',
    },
    Menu: {
      itemBorderRadius: 8,
      itemMarginInline: 10,
    },
    Modal: {
      borderRadiusLG: 14,
    },
    Table: {
      borderColor: '#e5e7eb',
      headerBg: '#f8fafc',
      headerColor: '#64748b',
      headerSplitColor: '#e5e7eb',
      rowHoverBg: '#fafbff',
    },
    Tabs: {
      itemSelectedColor: '#4f46e5',
      itemHoverColor: '#4f46e5',
      inkBarColor: '#4f46e5',
    },
    Tag: {
      borderRadiusSM: 999,
    },
  },
}

export const appLayoutToken = {
  bgLayout: '#f5f7fb',
  sider: {
    colorBgCollapsedButton: '#111827',
    colorTextCollapsedButton: '#cbd5e1',
    colorTextCollapsedButtonHover: '#ffffff',
    colorMenuBackground: '#111827',
    colorBgMenuItemCollapsedElevated: '#1f2937',
    colorMenuItemDivider: '#263244',
    colorBgMenuItemHover: '#1f2937',
    colorBgMenuItemActive: '#4f46e5',
    colorBgMenuItemSelected: '#4f46e5',
    colorTextMenuSelected: '#ffffff',
    colorTextMenuItemHover: '#ffffff',
    colorTextMenuActive: '#ffffff',
    colorTextMenu: '#cbd5e1',
    colorTextMenuSecondary: '#94a3b8',
    colorTextMenuTitle: '#ffffff',
    colorTextSubMenuSelected: '#ffffff',
    paddingInlineLayoutMenu: 14,
    paddingBlockLayoutMenu: 8,
    menuHeight: 40,
  },
  header: {
    colorBgHeader: '#ffffff',
    colorBgScrollHeader: '#ffffff',
    colorHeaderTitle: '#1f2937',
    colorBgMenuItemHover: '#f3f4f6',
    colorBgMenuElevated: '#ffffff',
    colorBgMenuItemSelected: '#eef2ff',
    colorTextMenuSelected: '#4f46e5',
    colorTextMenuActive: '#4f46e5',
    colorTextMenu: '#4b5563',
    colorTextMenuSecondary: '#6b7280',
    colorBgRightActionsItemHover: '#f3f4f6',
    colorTextRightActionsItem: '#374151',
    heightLayoutHeader: 64,
  },
  pageContainer: {
    colorBgPageContainer: '#f5f7fb',
    colorBgPageContainerFixed: '#ffffff',
    paddingInlinePageContainerContent: 28,
    paddingBlockPageContainerContent: 22,
  },
}

export const appLayoutSettings = {
  layout: 'side',
  siderWidth: 220,
  siderMenuType: 'sub',
  fixedHeader: true,
  fixSiderbar: true,
}

// 仅侧栏菜单使用深色语义；不要设置 ProLayout.navTheme，避免内容区 Tag 继承深色主题。
export const appSiderMenuProps = {
  theme: 'dark',
}
