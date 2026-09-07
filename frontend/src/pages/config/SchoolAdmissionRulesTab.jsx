import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ModalForm,
  ProFormDigit,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
} from '@ant-design/pro-components'
import { Button, Popconfirm, Space, Tag, message } from 'antd'
import {
  createSchoolTagRule,
  deleteSchoolTagRule,
  fetchSchoolTags,
  fetchSchoolTagRules,
  updateSchoolTagRule,
} from '../../api/services'
import { useModalRecord } from './useModalRecord'
import SchoolTagBadge from '../../components/SchoolTagBadge'
import SmartDataTable from '../../components/SmartDataTable'

function tagIds(tags) {
  return (tags || []).map((tag) => tag.id).filter(Boolean)
}

function TagList({ tags = [] }) {
  if (!tags.length) return '-'
  return (
    <Space wrap>
      {tags.map((tag) => (
        <SchoolTagBadge value={tag.name} key={tag.id} />
      ))}
    </Space>
  )
}

const EDUCATION_OPTIONS = [
  { value: 'associate', label: '大专' },
  { value: 'bachelor', label: '本科' },
  { value: 'master', label: '硕士' },
  { value: 'doctor', label: '博士' },
]
const EDUCATION_LABELS = Object.fromEntries(
  EDUCATION_OPTIONS.map((item) => [item.value, item.label]),
)

export default function SchoolAdmissionRulesTab() {
  const actionRef = useRef()
  const modal = useModalRecord()
  const [schoolTags, setSchoolTags] = useState([])

  const loadSchoolTags = async () => {
    const { data } = await fetchSchoolTags({ page_size: 500, is_active: 'true' })
    setSchoolTags(data?.results || [])
  }

  useEffect(() => {
    loadSchoolTags()
  }, [])

  const schoolTagOptions = useMemo(
    () =>
      schoolTags.map((tag) => ({
        label: tag.name,
        value: tag.id,
      })),
    [schoolTags],
  )

  const saveRule = async (values) => {
    const body = {
      name: values.name,
      priority: values.priority ?? 0,
      is_active: Boolean(values.is_active),
      first_degree_tag_ids: values.first_degree_tag_ids || [],
      highest_degree_tag_ids: values.highest_degree_tag_ids || [],
      allowed_highest_educations: values.allowed_highest_educations || [],
    }
    if (modal.record) {
      await updateSchoolTagRule(modal.record.id, body)
      message.success('规则已保存')
    } else {
      await createSchoolTagRule(body)
      message.success('规则已创建')
    }
    modal.close()
    actionRef.current?.reload()
    return true
  }

  const baseColumns = [
    {
      title: '规则名称',
      dataIndex: 'name',
      width: 180,
      fixed: 'left',
      filter: { type: 'text', param: 'name', placeholder: '筛选规则名称' },
    },
    { title: '优先级', dataIndex: 'priority', width: 90, filter: { type: 'text', param: 'priority', placeholder: '筛选优先级' } },
    {
      title: '状态',
      dataIndex: 'is_active',
      width: 90,
      filter: { type: 'select', param: 'is_active', options: [
        { value: 'true', label: '启用' },
        { value: 'false', label: '停用' },
      ] },
      render: (value) =>
        value ? <Tag color="green">启用</Tag> : <Tag color="default">停用</Tag>,
    },
    {
      title: '第一学历允许标签',
      dataIndex: 'first_degree_tags',
      render: (tags) => <TagList tags={tags} />,
    },
    {
      title: '最高学历允许标签',
      dataIndex: 'highest_degree_tags',
      render: (tags) => <TagList tags={tags} />,
    },
    {
      title: '允许最高学历',
      dataIndex: 'allowed_highest_educations',
      render: (values = []) => values.length
        ? <Space wrap>{values.map((value) => <Tag key={value}>{EDUCATION_LABELS[value] || value}</Tag>)}</Space>
        : '不限',
    },
    {
      title: '操作',
      valueType: 'option',
      width: 130,
      fixed: 'right',
      render: (_, record) => (
        <Space>
          <a onClick={() => modal.open(record)}>编辑</a>
          <Popconfirm
            title="删除院校规则"
            description="删除后不会影响历史分配记录中的规则快照。"
            okText="删除"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            onConfirm={async () => {
              await deleteSchoolTagRule(record.id)
              message.success('已删除')
              actionRef.current?.reload()
            }}
          >
            <a style={{ color: '#cf1322' }}>删除</a>
          </Popconfirm>
        </Space>
      ),
    },
  ]
  const initialValues = modal.record
    ? {
        ...modal.record,
        first_degree_tag_ids: tagIds(modal.record.first_degree_tags),
        highest_degree_tag_ids: tagIds(modal.record.highest_degree_tags),
      }
    : {
        is_active: true,
        priority: 0,
        first_degree_tag_ids: [],
        highest_degree_tag_ids: [],
        allowed_highest_educations: [],
      }

  return (
    <>
      <SmartDataTable
        tableId="school-admission-rules"
        stickyPagination
        actionRef={actionRef}
        rowKey="id"
        columns={baseColumns}
        request={fetchSchoolTagRules}
        toolBarRender={() => [
          <Button key="create" type="primary" onClick={() => modal.open()}>
            新增规则
          </Button>,
        ]}
      />
      <ModalForm
        key={modal.record?.id || 'create-rule'}
        title={modal.record ? '编辑院校准入规则' : '新增院校准入规则'}
        open={modal.visible}
        width={560}
        modalProps={{
          destroyOnHidden: true,
          onCancel: modal.close,
        }}
        initialValues={initialValues}
        onFinish={saveRule}
      >
        <ProFormText
          name="name"
          label="规则名称"
          rules={[{ required: true, message: '请输入规则名称' }]}
        />
        <ProFormDigit name="priority" label="优先级" min={0} fieldProps={{ precision: 0 }} />
        <ProFormSwitch name="is_active" label="是否启用" />
        <ProFormSelect
          name="first_degree_tag_ids"
          label="第一学历允许标签"
          mode="multiple"
          options={schoolTagOptions}
          placeholder="选择第一学历允许标签"
          rules={[{ required: true, message: '请至少配置一个第一学历标签' }]}
        />
        <ProFormSelect
          name="highest_degree_tag_ids"
          label="最高学历允许标签"
          mode="multiple"
          options={schoolTagOptions}
          placeholder="选择最高学历允许标签"
          rules={[{ required: true, message: '请至少配置一个最高学历标签' }]}
        />
        <ProFormSelect
          name="allowed_highest_educations"
          label="允许最高学历"
          mode="multiple"
          options={EDUCATION_OPTIONS}
          placeholder="不选择表示不限最高学历"
        />
      </ModalForm>
    </>
  )
}
