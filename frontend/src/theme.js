export const appTheme = {
  token: {
    colorPrimary: '#176b58',
    colorSuccess: '#16a34a',
    colorWarning: '#d97706',
    colorError: '#dc2626',
    colorInfo: '#176b58',
    colorInfoBg: '#eef5f0',
    colorInfoBorder: '#d9e6dc',
    colorBgLayout: '#f7f8fa',
    colorBgContainer: '#ffffff',
    colorText: '#202c29',
    colorTextSecondary: '#697571',
    colorBorder: '#dce2df',
    colorBorderSecondary: '#e7ebe9',
    colorFillAlter: '#f7f9f8',
    borderRadius: 8,
    borderRadiusLG: 12,
    controlHeight: 36,
    fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
    fontSize: 14,
    boxShadowTertiary: '0 2px 5px rgba(25, 46, 38, 0.025)',
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
      borderRadiusLG: 12,
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
      borderColor: '#e7ebe9',
      headerBg: '#f7f9f8',
      headerColor: '#697571',
      headerSplitColor: '#e7ebe9',
      rowHoverBg: '#f2f7f4',
    },
    Tabs: {
      itemSelectedColor: '#176b58',
      itemHoverColor: '#176b58',
      inkBarColor: '#176b58',
    },
    Tag: {
      borderRadiusSM: 999,
    },
  },
}

export const appLayoutToken = {
  bgLayout: '#f7f8fa',
  sider: {
    colorBgCollapsedButton: '#ffffff',
    colorTextCollapsedButton: '#697571',
    colorTextCollapsedButtonHover: '#176b58',
    colorMenuBackground: '#f0f3f1',
    colorBgMenuItemCollapsedElevated: '#ffffff',
    colorMenuItemDivider: '#dce2df',
    colorBgMenuItemHover: '#e6ece8',
    colorBgMenuItemActive: '#e0ece5',
    colorBgMenuItemSelected: '#e0ece5',
    colorTextMenuSelected: '#155440',
    colorTextMenuItemHover: '#202c29',
    colorTextMenuActive: '#155440',
    colorTextMenu: '#55645d',
    colorTextMenuSecondary: '#77847c',
    colorTextMenuTitle: '#202c29',
    colorTextSubMenuSelected: '#155440',
    paddingInlineLayoutMenu: 12,
    paddingBlockLayoutMenu: 8,
    menuHeight: 40,
  },
  header: {
    colorBgHeader: '#ffffff',
    colorBgScrollHeader: '#ffffff',
    colorHeaderTitle: '#1f2937',
    colorBgMenuItemHover: '#f3f4f6',
    colorBgMenuElevated: '#ffffff',
    colorBgMenuItemSelected: '#eaf3ed',
    colorTextMenuSelected: '#176b58',
    colorTextMenuActive: '#176b58',
    colorTextMenu: '#4b5563',
    colorTextMenuSecondary: '#6b7280',
    colorBgRightActionsItemHover: '#f3f4f6',
    colorTextRightActionsItem: '#374151',
    heightLayoutHeader: 64,
  },
  pageContainer: {
    colorBgPageContainer: '#f7f8fa',
    colorBgPageContainerFixed: '#ffffff',
    paddingInlinePageContainerContent: 32,
    paddingBlockPageContainerContent: 24,
  },
}

export const appLayoutSettings = {
  layout: 'side',
  siderWidth: 232,
  siderMenuType: 'sub',
  fixedHeader: true,
  fixSiderbar: true,
}

// 菜单与内容区使用同一浅色语义，业务状态色不受侧栏主题影响。
export const appSiderMenuProps = {
  theme: 'light',
}
