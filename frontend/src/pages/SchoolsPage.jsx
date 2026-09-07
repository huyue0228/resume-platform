import { useRef, useState } from 'react'
import {
  ModalForm,
  PageContainer,
  ProFormSelect,
  ProFormText,
} from '@ant-design/pro-components'
import { Button, Space, message } from 'antd'
import {
  createSchool,
  fetchSchoolFilterOptions,
  fetchSchools,
  updateSchool,
} from '../api/services'
import ImportButton from '../components/ImportButton'
import SchoolTagBadge from '../components/SchoolTagBadge'
import SmartDataTable from '../components/SmartDataTable'
import { useRole } from '../contexts/roleState'

const IMPORT_FIELDS = [
  { key: 'schools', label: '院校分类 (.xlsx/.xls/.csv)', accept: '.xlsx,.xls,.csv' },
]

export default function SchoolsPage() {
  const actionRef = useRef()
  const { hasPermission } = useRole()
  const [schoolModal, setSchoolModal] = useState({ open: false, record: null })
  const canManageSchools = hasPermission('school.manage')
  const canImportSchools = hasPermission('resume.import')

  const baseColumns = [
    {
      title: '学校',
      dataIndex: 'name',
      fixed: 'left',
      width: 220,
      ellipsis: true,
      filter: { type: 'text', param: 'name', pinyin: true, placeholder: '筛选学校/拼音' },
    },
    {
      title: '院校标签',
      dataIndex: 'platform',
      width: 160,
      filter: { type: 'select', param: 'platform_in', multiple: true, options: 'platform' },
      render: (_, r) => {
        const value = r.school_tag_name || r.platform
        return value ? <SchoolTagBadge value={value} /> : '-'
      },
    },
    canManageSchools && {
      title: '操作',
      valueType: 'option',
      fixed: 'right',
      width: 80,
      render: (_, record) => (
        <Space>
          <a onClick={() => setSchoolModal({ open: true, record })}>编辑</a>
        </Space>
      ),
    },
  ].filter(Boolean)
  return (
    <PageContainer
      title="院校清单"
      content="维护院校与单个院校标签。"
    >
      <SmartDataTable
        tableId="schools"
        stickyPagination
        actionRef={actionRef}
        rowKey="id"
        columns={baseColumns}
        request={fetchSchools}
        filterOptionsRequest={fetchSchoolFilterOptions}
        toolBarRender={() => [
          canManageSchools && (
            <Button
              key="create"
              type="primary"
              onClick={() => setSchoolModal({ open: true, record: null })}
            >
              新增院校
            </Button>
          ),
          canImportSchools && (
            <ImportButton
              key="import"
              buttonText="导入院校"
              title="导入院校分类"
              fields={IMPORT_FIELDS}
              templateType="schools"
              templateFilename="院校分类标准模板.xlsx"
              onDone={() => {
                actionRef.current?.reload()
                actionRef.current?.reloadOptions()
              }}
            />
          ),
        ].filter(Boolean)}
      />
      {canManageSchools && (
        <ModalForm
          title={schoolModal.record ? '编辑院校' : '新增院校'}
          open={schoolModal.open}
          modalProps={{
            destroyOnHidden: true,
            onCancel: () => setSchoolModal({ open: false, record: null }),
          }}
          initialValues={schoolModal.record || {}}
          onFinish={async (values) => {
            const body = {
              name: values.name?.trim(),
              school_tag: values.school_tag || null,
            }
            if (schoolModal.record) {
              await updateSchool(schoolModal.record.id, body)
            } else {
              await createSchool(body)
            }
            message.success('院校已保存')
            setSchoolModal({ open: false, record: null })
            actionRef.current?.reload()
            actionRef.current?.reloadOptions()
            return true
          }}
        >
          <ProFormText
            name="name"
            label="学校"
            rules={[{ required: true, whitespace: true, message: '请输入学校名称' }]}
          />
          <ProFormSelect
            name="school_tag"
            label="院校标签"
            allowClear
            showSearch
            request={async () => {
              const { data } = await fetchSchoolFilterOptions()
              return data?.school_tag || []
            }}
          />
        </ModalForm>
      )}
    </PageContainer>
  )
}
