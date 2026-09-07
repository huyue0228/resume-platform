import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ModalForm,
  PageContainer,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
} from '@ant-design/pro-components'
import { Button, Modal, Popconfirm, Space, Tabs, Tag, Tree, message } from 'antd'
import {
  createRole,
  createUser,
  deleteUser,
  fetchContacts,
  fetchPermissionTree,
  fetchRoles,
  fetchUsers,
  updateRole,
  updateUser,
} from '../api/services'
import SmartDataTable from '../components/SmartDataTable'
import { useRole } from '../contexts/roleState'

const ROLE_VALUE_ENUM = {
  admin: { text: '管理员' },
  hr: { text: 'HR' },
  secondary_contact: { text: '二级接口人' },
  tertiary_contact: { text: '三级接口人' },
}

function treeData(permissionTree) {
  return permissionTree.map((module) => ({
    title: module.name,
    key: module.code,
    selectable: false,
    children: module.children.map((item) => ({
      title: `${item.name}（${item.code}）`,
      key: item.code,
    })),
  }))
}

function leafCodes(permissionTree) {
  return permissionTree.flatMap((module) => module.children.map((item) => item.code))
}

export default function UsersPage() {
  const { refreshMe } = useRole()
  const userActionRef = useRef()
  const roleActionRef = useRef()
  const [roles, setRoles] = useState([])
  const [contacts, setContacts] = useState([])
  const [permissionTree, setPermissionTree] = useState([])
  const [userModal, setUserModal] = useState({ open: false, record: null })
  const [roleModal, setRoleModal] = useState({ open: false, record: null })
  const [permissionModalOpen, setPermissionModalOpen] = useState(false)
  const [activeRole, setActiveRole] = useState(null)
  const [checkedPermissions, setCheckedPermissions] = useState([])
  const [savingPermissions, setSavingPermissions] = useState(false)

  const loadOptions = async () => {
    const [roleResp, contactResp, permissionResp] = await Promise.all([
      fetchRoles({ page_size: 200 }),
      fetchContacts({ page_size: 500 }),
      fetchPermissionTree(),
    ])
    setRoles(roleResp.data?.results || [])
    setContacts(contactResp.data?.results || [])
    setPermissionTree(permissionResp.data || [])
  }

  useEffect(() => {
    loadOptions()
  }, [])

  const roleOptions = roles.map((role) => ({ label: role.name, value: role.id }))
  const contactOptions = contacts.map((contact) => ({
    label: `${contact.name}（${contact.employee_no} / ${contact.department_name || '未绑定部门'}）`,
    value: contact.id,
  }))
  const allLeafCodes = useMemo(() => leafCodes(permissionTree), [permissionTree])

  const userBaseColumns = [
    {
      title: '用户名',
      dataIndex: 'username',
      width: 150,
      fixed: 'left',
      filter: { type: 'text', param: 'username', placeholder: '筛选用户名' },
    },
    {
      title: '邮箱',
      dataIndex: 'email',
      width: 220,
      ellipsis: true,
      filter: { type: 'text', param: 'email', placeholder: '筛选邮箱' },
    },
    {
      title: '角色类型',
      dataIndex: 'role',
      width: 130,
      valueEnum: ROLE_VALUE_ENUM,
      filter: { type: 'select', param: 'role', options: Object.entries(ROLE_VALUE_ENUM).map(([value, item]) => ({ value, label: item.text })) },
    },
    {
      title: 'RBAC 角色',
      dataIndex: 'roles',
      search: false,
      width: 200,
      filter: { type: 'select', param: 'roles_in', multiple: true, options: roles.map((role) => ({ value: role.name, label: role.name })) },
      render: (_, record) => (
        <Space wrap>
          {(record.roles || []).map((name) => (
            <Tag color="blue" key={name}>
              {name}
            </Tag>
          ))}
        </Space>
      ),
    },
    {
      title: '绑定接口人',
      dataIndex: 'contact_name',
      width: 160,
      search: false,
      filter: { type: 'text', param: 'contact_name', pinyin: true, placeholder: '筛选接口人/拼音' },
    },
    {
      title: '状态',
      dataIndex: 'is_active',
      width: 90,
      search: false,
      filter: { type: 'select', param: 'is_active', options: [
        { value: 'true', label: '启用' },
        { value: 'false', label: '停用' },
      ] },
      render: (value) =>
        value ? <Tag color="green">启用</Tag> : <Tag color="default">停用</Tag>,
    },
    {
      title: '操作',
      valueType: 'option',
      width: 170,
      render: (_, record) => (
        record.is_protected ? (
          <Tag color="gold">内置保护</Tag>
        ) : (
          <Space>
            <a onClick={() => setUserModal({ open: true, record })}>编辑</a>
            <a
              onClick={async () => {
                await updateUser(record.id, { is_active: !record.is_active })
                message.success(record.is_active ? '已停用' : '已启用')
                userActionRef.current?.reload()
              }}
            >
              {record.is_active ? '停用' : '启用'}
            </a>
            <Popconfirm
              title="删除用户"
              description="删除后账号、Token 和角色绑定会清理；若绑定接口人，将同步删除接口人，历史记录仅保留快照。"
              okText="删除"
              okButtonProps={{ danger: true }}
              onConfirm={async () => {
                await deleteUser(record.id)
                message.success('用户已删除')
                await loadOptions()
                userActionRef.current?.reload()
              }}
            >
              <a style={{ color: '#cf1322' }}>删除</a>
            </Popconfirm>
          </Space>
        )
      ),
    },
  ]

  const roleBaseColumns = [
    {
      title: '角色名称',
      dataIndex: 'name',
      width: 180,
      filter: { type: 'text', param: 'name', placeholder: '筛选角色名称' },
    },
    {
      title: '权限数',
      dataIndex: 'permissions',
      search: false,
      width: 100,
      render: (_, record) => record.permissions?.length || 0,
    },
    {
      title: '操作',
      valueType: 'option',
      width: 160,
      render: (_, record) => (
        <Space>
          <a onClick={() => setRoleModal({ open: true, record })}>改名</a>
          <a
            onClick={() => {
              setActiveRole(record)
              setCheckedPermissions(record.permissions || [])
              setPermissionModalOpen(true)
            }}
          >
            配置权限
          </a>
        </Space>
      ),
    },
  ]
  const saveRolePermissions = async () => {
    if (!activeRole) return
    setSavingPermissions(true)
    try {
      const { data } = await updateRole(activeRole.id, {
        permission_codes: checkedPermissions.filter((code) => allLeafCodes.includes(code)),
      })
      setActiveRole(data)
      roleActionRef.current?.reload()
      await Promise.all([loadOptions(), refreshMe()])
      message.success('角色权限已保存')
      setPermissionModalOpen(false)
    } finally {
      setSavingPermissions(false)
    }
  }

  return (
    <PageContainer title="用户权限" content="维护系统用户、RBAC 角色与后端预置权限点绑定。">
      <Tabs
        items={[
          {
            key: 'users',
            label: '用户管理',
            children: (
              <SmartDataTable
                tableId="users"
                stickyPagination
                actionRef={userActionRef}
                rowKey="id"
                columns={userBaseColumns}
                request={fetchUsers}
                toolBarRender={() => [
                  <Button
                    key="create"
                    type="primary"
                    onClick={() => setUserModal({ open: true, record: null })}
                  >
                    新增用户
                  </Button>,
                ]}
              />
            ),
          },
          {
            key: 'roles',
            label: '角色管理',
            children: (
              <SmartDataTable
                tableId="roles"
                stickyPagination
                actionRef={roleActionRef}
                rowKey="id"
                columns={roleBaseColumns}
                request={fetchRoles}
                toolBarRender={() => [
                  <Button
                    key="create"
                    type="primary"
                    onClick={() => setRoleModal({ open: true, record: null })}
                  >
                    新增角色
                  </Button>,
                ]}
              />
            ),
          },
        ]}
      />

      <Modal
        title={`配置权限：${activeRole?.name || ''}`}
        open={permissionModalOpen}
        destroyOnHidden
        onCancel={() => {
          setPermissionModalOpen(false)
          setActiveRole(null)
          setCheckedPermissions([])
        }}
        footer={[
          <Button key="cancel" onClick={() => setPermissionModalOpen(false)}>
            取消
          </Button>,
          <Button
            key="save"
            type="primary"
            disabled={!activeRole}
            loading={savingPermissions}
            onClick={saveRolePermissions}
          >
            保存权限
          </Button>,
        ]}
      >
        <Tree
          checkable
          treeData={treeData(permissionTree)}
          checkedKeys={checkedPermissions}
          onCheck={(keys) => setCheckedPermissions(keys)}
        />
      </Modal>

      <ModalForm
        title={userModal.record ? '编辑用户' : '新增用户'}
        open={userModal.open}
        modalProps={{ destroyOnHidden: true, onCancel: () => setUserModal({ open: false, record: null }) }}
        initialValues={
          userModal.record
            ? {
                ...userModal.record,
                role_ids: roles
                  .filter((role) => userModal.record.roles?.includes(role.name))
                  .map((role) => role.id),
              }
            : { is_active: true, role: 'hr' }
        }
        onFinish={async (values) => {
          const body = { ...values, contact: values.contact || null }
          if (userModal.record) {
            await updateUser(userModal.record.id, body)
          } else {
            await createUser(body)
          }
          message.success('用户已保存')
          setUserModal({ open: false, record: null })
          userActionRef.current?.reload()
          return true
        }}
      >
        <ProFormText name="username" label="用户名" rules={[{ required: true }]} />
        <ProFormText
          name="email"
          label="邮箱"
          rules={[
            { required: true, whitespace: true, message: '请输入邮箱' },
            { type: 'email', message: '请输入有效邮箱' },
          ]}
        />
        <ProFormSelect
          name="role"
          label="角色类型"
          valueEnum={ROLE_VALUE_ENUM}
          rules={[{ required: true }]}
        />
        <ProFormSelect
          name="role_ids"
          label="RBAC 角色"
          mode="multiple"
          options={roleOptions}
          rules={[{ required: true }]}
        />
        <ProFormSelect
          name="contact"
          label="绑定接口人"
          showSearch
          options={contactOptions}
        />
        <ProFormSwitch name="is_active" label="启用" />
      </ModalForm>

      <ModalForm
        title={roleModal.record ? '编辑角色' : '新增角色'}
        open={roleModal.open}
        modalProps={{ destroyOnHidden: true, onCancel: () => setRoleModal({ open: false, record: null }) }}
        initialValues={roleModal.record || {}}
        onFinish={async (values) => {
          if (roleModal.record) {
            await updateRole(roleModal.record.id, values)
          } else {
            await createRole(values)
          }
          message.success('角色已保存')
          setRoleModal({ open: false, record: null })
          roleActionRef.current?.reload()
          await loadOptions()
          return true
        }}
      >
        <ProFormText name="name" label="角色名称" rules={[{ required: true }]} />
      </ModalForm>
    </PageContainer>
  )
}
