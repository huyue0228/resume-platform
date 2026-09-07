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
  { key: 'contacts', label: '部门接口人信息 (.xlsx/.xls/.csv)', accept: '.xlsx,.xls,.csv' },
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
    .filter((department) => department.level === 2 || department.level === 3)
    .map((department) => ({
      label: `${department.name}（${department.level === 3 ? '三级部门' : '二级部门'}）`,
      value: department.id,
    }))

  const handleSave = async (values) => {
    const department = departments.find((item) => item.id === values.department)
    const isTertiary = department?.level === 3
    const body = {
      name: values.name?.trim(),
      employee_no: values.employee_no?.trim(),
      email: values.email?.trim().toLowerCase(),
      department: values.department,
      contact_level: isTertiary ? 'tertiary' : 'secondary',
      can_delegate: isTertiary ? false : Boolean(values.can_delegate),
      is_active: Boolean(values.is_active),
    }
    if (contactModal.record) {
      await updateContact(contactModal.record.id, body)
    } else {
      await createContact(body)
    }
    message.success('接口人已保存')
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
        { label: '二级部门', value: '2' },
        { label: '三级部门', value: '3' },
      ] },
      render: (value) => (value === 3 ? '三级部门' : value === 2 ? '二级部门' : '-'),
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
        record.contact_level === 'secondary' && record.can_delegate ? (
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
            title="删除接口人"
            description="将删除该接口人及绑定用户；既有处理日志仍保留操作者快照。"
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
      title="部门接口人"
      content="二级/三级部门接口人名单，可导入维护；同部门接口人共享部门收件箱，按部门范围处理简历。"
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
              新增接口人
            </Button>
          ),
          canImportContacts && (
            <ImportButton
              key="import"
              buttonText="导入接口人"
              title="导入部门接口人信息"
              fields={IMPORT_FIELDS}
              templateType="contacts"
              templateFilename="部门接口人标准模板.xlsx"
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
          title={contactModal.record ? '编辑接口人' : '新增接口人'}
          open={contactModal.open}
          modalProps={{
            destroyOnHidden: true,
            onCancel: () => setContactModal({ open: false, record: null }),
          }}
          initialValues={
            contactModal.record || { can_delegate: true, is_active: true }
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
          <ProFormSwitch name="can_delegate" label="允许转派" />
          <ProFormSwitch name="is_active" label="启用" />
        </ModalForm>
      )}
    </PageContainer>
  )
}
