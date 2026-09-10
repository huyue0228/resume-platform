import { useEffect, useRef, useState } from 'react'
import {
  ModalForm,
  PageContainer,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
} from '@ant-design/pro-components'
import { Button, message, Popconfirm, Space, Tag } from 'antd'
import {
  createContact,
  deleteContact,
  fetchContactFilterOptions,
  fetchContacts,
  fetchDepartments,
  updateContact,
} from '../api/services'
import ImportButton from '../components/ImportButton'
import SmartDataTable from '../components/SmartDataTable'
import { useRole } from '../contexts/roleState'

const IMPORT_FIELDS = [
  { key: 'contacts', label: '部门人员授权信息 (.xlsx/.xls/.csv)', accept: '.xlsx,.xls,.csv' },
]

export default function DepartmentsPage() {
  const actionRef = useRef()
  const role = useRole()
  const canManageContacts = Boolean(role?.hasPermission?.('department.manage'))
  const canImportContacts = Boolean(role?.hasPermission?.('resume.import'))
  const [contactModal, setContactModal] = useState({ open: false, record: null })
  const [departments, setDepartments] = useState([])

  useEffect(() => {
    if (!canManageContacts) return
    fetchDepartments({ page_size: 500 })
      .then(({ data }) => setDepartments(data?.results || []))
      .catch(() => setDepartments([]))
  }, [canManageContacts])

  const handleDelete = async (record) => {
    try {
      await deleteContact(record.id)
      message.success('已删除')
      actionRef.current?.reload()
      actionRef.current?.reloadOptions()
    } catch (error) {
      message.error(error?.response?.data?.detail || '删除失败')
    }
  }

  const departmentOptions = departments
    .filter((department) => [1, 2].includes(department.level))
    .map((department) => ({
      label: `${department.name}（${{ 1: '一级部门', 2: '二级部门' }[department.level]}）`,
      value: department.id,
    }))

  const handleSave = async (values) => {
    const isTertiary = values.contact_level === 'tertiary'
    const body = {
      name: values.name?.trim(),
      employee_no: values.employee_no?.trim(),
      email: values.email?.trim().toLowerCase(),
      department: values.department,
      contact_level: values.contact_level,
      can_delegate: isTertiary ? false : Boolean(values.can_delegate),
      is_active: Boolean(values.is_active),
    }
    if (contactModal.record) {
      await updateContact(contactModal.record.id, body)
    } else {
      await createContact(body)
    }
    message.success('部门授权已保存')
    setContactModal({ open: false, record: null })
    actionRef.current?.reload()
    actionRef.current?.reloadOptions()
    return true
  }

  const baseColumns = [
    {
      title: '姓名',
      dataIndex: 'name',
      fixed: 'left',
      width: 120,
      filter: { type: 'text', param: 'name', pinyin: true, placeholder: '筛选姓名/拼音' },
    },
    {
      title: '工号',
      dataIndex: 'employee_no',
      width: 140,
      filter: { type: 'text', param: 'employee_no', placeholder: '筛选工号' },
    },
    {
      title: '邮箱',
      dataIndex: 'email',
      width: 220,
      ellipsis: true,
      filter: { type: 'text', param: 'email', placeholder: '筛选邮箱' },
    },
    {
      title: '所属部门',
      dataIndex: 'department_name',
      width: 160,
      ellipsis: true,
      filter: { type: 'select', param: 'department_in', multiple: true, options: 'department' },
    },
    {
      title: '部门层级',
      dataIndex: 'department_level',
      width: 100,
      filter: { type: 'select', param: 'department_level', options: [
        { label: '一级部门', value: '1' },
        { label: '二级部门', value: '2' },
      ] },
      render: (value) => ({ 1: '一级部门', 2: '二级部门' }[value] || '-'),
    },
    {
      title: '角色',
      dataIndex: 'contact_level',
      width: 120,
      filter: { type: 'select', param: 'contact_level', options: [
        { label: '二级部门HR', value: 'secondary_hr' },
        { label: '接口人', value: 'secondary' },
        { label: '简历筛选人', value: 'tertiary' },
      ] },
      render: (value) => ({ secondary_hr: '二级部门HR', secondary: '接口人', tertiary: '简历筛选人' }[value] || '-'),
    },
    {
      title: '可转派',
      dataIndex: 'can_delegate',
      width: 90,
      filter: { type: 'select', param: 'can_delegate', options: [
        { label: '是', value: 'true' },
        { label: '否', value: 'false' },
      ] },
      render: (_, record) =>
        ['secondary_hr', 'secondary'].includes(record.contact_level) && record.can_delegate ? (
          <Tag color="green">是</Tag>
        ) : (
          '-'
        ),
    },
    {
      title: '状态',
      dataIndex: 'is_active',
      width: 90,
      filter: { type: 'select', param: 'is_active', options: [
        { label: '启用', value: 'true' },
        { label: '停用', value: 'false' },
      ] },
      render: (value) =>
        value ? <Tag color="green">启用</Tag> : <Tag color="default">停用</Tag>,
    },
    canManageContacts && {
      title: '操作',
      valueType: 'option',
      fixed: 'right',
      width: 130,
      render: (_, record) => (
        <Space>
          <a onClick={() => setContactModal({ open: true, record })}>编辑</a>
          <Popconfirm
            title="撤销部门授权"
            description="仅移除该条部门角色授权，保留账号、其他部门授权和处理记录。"
            okText="删除"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            onConfirm={() => handleDelete(record)}
          >
            <a style={{ color: '#cf1322' }}>删除</a>
          </Popconfirm>
        </Space>
      ),
    },
  ].filter(Boolean)
  return (
    <PageContainer
      title="部门人员授权"
      content="同一工号可兼任多个部门和角色，每个部门、角色一条授权；可转派和启用状态分别设置。"
    >
      <SmartDataTable
        tableId="contacts"
        stickyPagination
        actionRef={actionRef}
        rowKey="id"
        columns={baseColumns}
        request={fetchContacts}
        filterOptionsRequest={fetchContactFilterOptions}
        toolBarRender={() => [
          canManageContacts && (
            <Button
              key="create"
              type="primary"
              onClick={() => setContactModal({ open: true, record: null })}
            >
              新增部门授权
            </Button>
          ),
          canImportContacts && (
            <ImportButton
              key="import"
              buttonText="导入人员授权"
              title="导入部门人员授权信息"
              fields={IMPORT_FIELDS}
              templateType="contacts"
              templateFilename="部门人员授权标准模板.xlsx"
              onDone={() => {
                actionRef.current?.reload()
                actionRef.current?.reloadOptions()
              }}
            />
          ),
        ].filter(Boolean)}
      />
      {canManageContacts && (
        <ModalForm
          title={contactModal.record ? '编辑部门授权' : '新增部门授权'}
          open={contactModal.open}
          modalProps={{
            destroyOnHidden: true,
            onCancel: () => setContactModal({ open: false, record: null }),
          }}
          initialValues={
            contactModal.record || { contact_level: 'secondary', can_delegate: true, is_active: true }
          }
          onFinish={handleSave}
        >
          <ProFormText
            name="name"
            label="姓名"
            rules={[{ required: true, whitespace: true, message: '请输入姓名' }]}
          />
          <ProFormText
            name="employee_no"
            label="工号"
            disabled={Boolean(contactModal.record)}
            extra="兼任其他部门时可新增一条相同工号的授权；姓名和邮箱由同一账号共用。"
            rules={[{ required: true, whitespace: true, message: '请输入工号' }]}
          />
          <ProFormText
            name="email"
            label="邮箱"
            rules={[
              { required: true, whitespace: true, message: '请输入邮箱' },
              { type: 'email', message: '请输入有效邮箱' },
            ]}
          />
          <ProFormSelect
            name="department"
            label="所属部门"
            showSearch
            options={departmentOptions}
            rules={[{ required: true, message: '请选择所属部门' }]}
          />
          <ProFormSelect
            name="contact_level"
            label="角色"
            options={[
              { label: '二级部门HR', value: 'secondary_hr' },
              { label: '接口人', value: 'secondary' },
              { label: '简历筛选人', value: 'tertiary' },
            ]}
            extra="二级部门HR按部门授权，一级部门HR为全局角色，请在用户管理中设置；接口人管理部门收件箱；简历筛选人仅处理转派给自己的简历。"
            rules={[{ required: true, message: '请选择角色' }]}
          />
          <ProFormSwitch name="can_delegate" label="允许转派" />
          <ProFormSwitch name="is_active" label="启用" />
        </ModalForm>
      )}
    </PageContainer>
  )
}
