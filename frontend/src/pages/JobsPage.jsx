import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ModalForm,
  PageContainer,
  ProFormDigit,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
  ProFormTextArea,
} from '@ant-design/pro-components'
import { Button, Popconfirm, Space, Tag, message } from 'antd'
import {
  createJob,
  deleteJob,
  exportJobs,
  fetchDepartments,
  fetchJobFilterOptions,
  fetchJobs,
  updateJob,
} from '../api/services'
import ImportButton from '../components/ImportButton'
import { useRole } from '../contexts/roleState'
import SmartDataTable from '../components/SmartDataTable'
import { downloadBlobFromResponse } from '../utils/download'

const IMPORT_FIELDS = [
  { key: 'jobs', label: '校招岗位分类及专业要求 (.xlsx/.xls/.csv)', accept: '.xlsx,.xls,.csv' },
]

export default function JobsPage() {
  const actionRef = useRef()
  const { hasPermission } = useRole()
  const [jobModal, setJobModal] = useState({ open: false, record: null })
  const [departments, setDepartments] = useState([])
  const [exporting, setExporting] = useState(false)

  useEffect(() => {
    fetchDepartments({ page_size: 500 })
      .then(({ data }) => setDepartments(data?.results || []))
      .catch(() => setDepartments([]))
  }, [])

  const canManageJobs = hasPermission('job.manage')
  const canImportJobs = hasPermission('resume.import')
  const departmentOptions = useMemo(() => {
    const departmentsById = new Map(
      departments.map((department) => [department.id, department]),
    )
    const pathLabel = (department) => {
      const path = []
      const visited = new Set()
      let current = department
      while (current && !visited.has(current.id)) {
        visited.add(current.id)
        path.unshift(current.name)
        current = current.parent ? departmentsById.get(current.parent) : null
      }
      return path.filter(Boolean).join(' / ')
    }
    return departments
      .filter((department) => department.level === 2)
      .map((department) => ({
        label: pathLabel(department),
        value: department.id,
      }))
      .sort((left, right) => left.label.localeCompare(right.label, 'zh-CN'))
  }, [departments])
  const baseColumns = [
    {
      title: '招聘主体',
      dataIndex: 'entity',
      width: 100,
      filter: { type: 'select', param: 'entity_in', multiple: true, options: 'entity' },
    },
    {
      title: '一级部门',
      dataIndex: 'primary_department_name',
      width: 140,
      filter: {
        type: 'select',
        param: 'primary_department_name_in',
        multiple: true,
        options: 'primary_department_name',
      },
      render: (value) => value || '-',
    },
    {
      title: '二级部门',
      dataIndex: 'secondary_department_name',
      width: 140,
      filter: {
        type: 'select',
        param: 'secondary_department_name_in',
        multiple: true,
        options: 'secondary_department_name',
      },
      render: (value) => value || '-',
    },
    {
      title: '对外名称',
      dataIndex: 'public_name',
      fixed: 'left',
      width: 160,
      ellipsis: true,
      filter: { type: 'select', param: 'public_name_in', multiple: true, options: 'public_name' },
    },
    {
      title: '职位名称',
      dataIndex: 'position_name',
      width: 160,
      ellipsis: true,
      filter: { type: 'select', param: 'position_name_in', multiple: true, options: 'position_name' },
    },
    {
      title: '岗位类别',
      dataIndex: 'category',
      width: 120,
      filter: { type: 'select', param: 'category_in', multiple: true, options: 'category' },
    },
    {
      title: '工作职责',
      dataIndex: 'responsibilities',
      width: 280,
      ellipsis: { showTitle: true },
      filter: { type: 'text', param: 'responsibilities', placeholder: '筛选工作职责' },
      render: (value) => value || '-',
    },
    {
      title: '岗位族',
      dataIndex: 'job_family',
      width: 110,
      filter: { type: 'select', param: 'job_family_in', multiple: true, options: 'job_family' },
    },
    {
      title: '工作地点',
      dataIndex: 'location',
      width: 110,
      filter: { type: 'select', param: 'location_in', multiple: true, options: 'location' },
    },
    {
      title: '学历要求',
      dataIndex: 'education',
      width: 100,
      filter: { type: 'select', param: 'education_in', multiple: true, options: 'education' },
    },
    {
      title: '需求专业',
      dataIndex: 'majors',
      width: 180,
      search: false,
      ellipsis: true,
      render: (_, record) => record.majors?.join('、') || '-',
    },
    {
      title: 'HC',
      dataIndex: 'headcount',
      width: 70,
      filter: { type: 'text', param: 'headcount', placeholder: '筛选HC' },
    },
    {
      title: '对外发布',
      dataIndex: 'is_public',
      width: 90,
      filter: { type: 'select', param: 'is_public', options: [
        { label: '是', value: 'true' },
        { label: '否', value: 'false' },
      ] },
      render: (_, r) =>
        r.is_public ? <Tag color="green">是</Tag> : <Tag>否</Tag>,
    },
    canManageJobs && {
      title: '操作',
      valueType: 'option',
      width: 120,
      fixed: 'right',
      render: (_, record) => (
        <Space>
          <a onClick={() => setJobModal({ open: true, record })}>编辑</a>
          <Popconfirm
            title="删除岗位"
            description="删除后岗位将停用，历史投递和分配记录仍可追溯。"
            okText="删除"
            okButtonProps={{ danger: true }}
            onConfirm={async () => {
              await deleteJob(record.id)
              message.success('岗位已删除')
              actionRef.current?.reload()
              actionRef.current?.reloadOptions()
            }}
          >
            <a style={{ color: '#cf1322' }}>删除</a>
          </Popconfirm>
        </Space>
      ),
    },
  ].filter(Boolean)
  return (
    <PageContainer title="岗位需求" content="校招岗位分类及专业要求，可导入维护。">
      <SmartDataTable
        tableId="jobs"
        stickyPagination
        actionRef={actionRef}
        rowKey="id"
        columns={baseColumns}
        request={fetchJobs}
        filterOptionsRequest={fetchJobFilterOptions}
        toolBarRender={() =>
          [
            canManageJobs && (
              <Button
                key="create"
                type="primary"
                onClick={() => setJobModal({ open: true, record: null })}
              >
                新增岗位
              </Button>
            ),
            canImportJobs && (
              <ImportButton
                key="import"
                buttonText="导入岗位"
                title="导入岗位分类及专业要求"
                fields={IMPORT_FIELDS}
                templateType="jobs"
                templateFilename="岗位标准模板.xlsx"
                onDone={() => {
                  actionRef.current?.reload()
                  actionRef.current?.reloadOptions()
                }}
              />
            ),
            <Button
              key="download"
              loading={exporting}
              onClick={async () => {
                setExporting(true)
                try {
                  const response = await exportJobs(actionRef.current?.getFilters?.() || {})
                  downloadBlobFromResponse(response, '职位清单.xlsx')
                  const count = response.headers?.['x-export-count']
                  message.success(count ? `已下载 ${count} 条职位` : '职位清单已下载')
                } catch {
                  // API 客户端已统一展示后端错误；这里只负责恢复按钮状态。
                } finally {
                  setExporting(false)
                }
              }}
            >
              下载职位清单
            </Button>,
          ].filter(Boolean)
        }
      />
      <ModalForm
        title={jobModal.record ? '编辑岗位' : '新增岗位'}
        open={jobModal.open}
        modalProps={{
          destroyOnHidden: true,
          onCancel: () => setJobModal({ open: false, record: null }),
        }}
        initialValues={
          jobModal.record
            ? {
                ...jobModal.record,
                major_names: jobModal.record.majors?.join('、') || '',
              }
            : { is_public: true, headcount: 0 }
        }
        onFinish={async (values) => {
          const majorNames =
            values.major_names
              ?.split(/[、,，;；\n]/)
              .map((item) => item.trim())
              .filter(Boolean) || []
          const body = {
            ...values,
            department: values.department || null,
            major_names: majorNames,
          }
          if (jobModal.record) {
            await updateJob(jobModal.record.id, body)
          } else {
            await createJob(body)
          }
          message.success('岗位已保存')
          setJobModal({ open: false, record: null })
          actionRef.current?.reload()
          actionRef.current?.reloadOptions()
          return true
        }}
      >
        <ProFormText name="entity" label="招聘主体" placeholder="如 GW / YLS" />
        <ProFormText name="public_name" label="对外名称" />
        <ProFormText name="position_name" label="职位名称" rules={[{ required: true }]} />
        <ProFormText name="category" label="岗位类别" />
        <ProFormText name="job_family" label="岗位族" />
        <ProFormSelect
          name="department"
          label="所属二级部门"
          showSearch
          options={departmentOptions}
          rules={[{ required: true, message: '请选择所属二级部门' }]}
        />
        <ProFormText name="location" label="工作地点" />
        <ProFormText name="education" label="学历要求" />
        <ProFormTextArea
          name="responsibilities"
          label="工作职责"
          rules={[{ required: true, whitespace: true, message: '请输入工作职责' }]}
          fieldProps={{
            autoSize: { minRows: 4, maxRows: 10 },
            showCount: true,
          }}
          placeholder="详细描述岗位负责内容，AI 将结合简历项目、实习和技能进行匹配分析"
        />
        <ProFormText
          name="major_names"
          label="需求专业"
          placeholder="多个专业可用顿号、逗号、分号或换行分隔"
        />
        <ProFormDigit name="headcount" label="HC" min={0} fieldProps={{ precision: 0 }} />
        <ProFormSwitch name="is_public" label="对外发布" />
      </ModalForm>
    </PageContainer>
  )
}
