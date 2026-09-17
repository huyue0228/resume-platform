import { editDemandReception, editTableRecord } from '../api/bulkEdit'

const active = { name: 'is_active', label: '状态', type: 'select', options: [{ value: true, label: '启用' }, { value: false, label: '停用' }] }
const text = (name, label) => ({ name, label, clearValue: '', normalize: (value) => value?.trim() })
const selection = (name, label, options) => ({ name, label, type: 'select', options })
const config = (resource, title, fields, extra = {}) => ({ title, fields, update: editTableRecord(resource), ...extra })

export const jobBulkEdit = (departments, receptionOptions) => config('jobs', '批量编辑岗位', [
  text('entity', '招聘主体'), selection('department', '所属部门', departments),
  { name: 'position_name', label: '职位名称', normalize: (value) => value?.trim() },
  text('public_name', '对外名称'), text('category', '岗位类别'), text('job_family', '岗位族'),
  text('location', '工作地点'), text('education', '学历要求'),
  { name: 'responsibilities', label: '工作职责', type: 'textarea', normalize: (value) => value?.trim() },
  { name: 'major_names', label: '需求专业（整体替换）', type: 'textarea', clearValue: [], normalize: (value) => [...new Set((value || '').split(/[、,，;；\n]/).map((item) => item.trim()).filter(Boolean))], help: '多个专业用逗号、顿号或换行分隔。' },
  { name: 'headcount', label: 'HC（计划人数）', type: 'number', min: 0 },
  { name: 'is_public', label: '对外发布', type: 'boolean' },
], {
  getLabel: (row) => [row.position_name, row.department_name, row.public_name].filter(Boolean).join(' / '),
  actions: [{
    title: '批量接收设置',
    fields: [{ ...selection('reception_state', '接收状态', receptionOptions), always: true }, { name: 'reason', label: '调整原因', type: 'textarea', always: true, normalize: (value) => value?.trim() }],
    description: '接收状态控制双 Agent 分配是否继续推荐候选人；已分配的记录保持原有流程。',
    validate: (changes) => !changes.reception_state || !changes.reason ? '请选择接收状态并填写调整原因' : '',
    update: editDemandReception,
  }],
})

export const schoolBulkEdit = (tags) => config('schools', '批量编辑院校', [
  { ...selection('school_tag', '院校标签', tags), clearValue: null },
])

export const contactBulkEdit = (departments) => config('contacts', '批量编辑部门授权', [
  selection('department', '所属部门', departments),
  selection('contact_level', '角色', [{ value: 'secondary_hr', label: '二级部门HR' }, { value: 'secondary', label: '接口人' }, { value: 'tertiary', label: '简历筛选人' }]),
  { name: 'can_delegate', label: '允许转派', type: 'boolean' }, active,
], {
  description: '姓名、工号和邮箱请通过人员姓名进入单条编辑。简历筛选人不允许转派；同一工号、部门、角色不能重复授权。',
  getLabel: (row) => [row.name, row.employee_no, row.department_name].filter(Boolean).join(' / '),
  update: (row, changes) => editTableRecord('contacts')(row, (changes.contact_level || row.contact_level) === 'tertiary' ? { ...changes, can_delegate: false } : changes),
  validate: (changes, records) => changes.can_delegate === true && records.some((row) => (changes.contact_level || row.contact_level) === 'tertiary') ? '简历筛选人不允许转派，请调整所选角色或“允许转派”设置' : '',
})

export const userBulkEdit = (roles, onComplete) => config('users', '批量编辑用户', [
  active,
  { name: 'role_ids', label: 'RBAC 角色（整体替换）', type: 'multiple', options: roles, clearValue: [], help: '会替换这些用户的角色绑定；部门范围的授权请在部门人员授权中维护。' },
], { disabled: (row) => row.is_protected, onComplete })

export const roleBulkEdit = (permissionTree, onComplete) => config('roles', '批量配置角色权限', (records) => [{
  name: 'permission_codes', label: '权限（整体替换）', type: 'multiple', clearValue: [],
  options: permissionTree.flatMap((module) => module.children.map((item) => ({ value: item.code, label: `${module.name} / ${item.name}` }))).filter((option) => records.every((row) => !row.allowed_permission_codes || row.allowed_permission_codes.includes(option.value))),
  help: '仅可选所有已选角色共同允许的权限，保存后替换这些角色的权限集合。',
}], { onComplete })

export const schoolTagBulkEdit = () => config('school-tags', '批量编辑院校标签', [active])
export const admissionBulkEdit = (tags, educations) => config('school-tag-rules', '批量编辑院校准入规则', [
  { name: 'priority', label: '优先级', type: 'number', min: 0 }, active,
  { name: 'first_degree_tag_ids', label: '第一学历允许标签（整体替换）', type: 'multiple', options: tags },
  { name: 'highest_degree_tag_ids', label: '最高学历允许标签（整体替换）', type: 'multiple', options: tags },
  { name: 'allowed_highest_educations', label: '允许最高学历（整体替换）', type: 'multiple', options: educations, clearValue: [], clearLabel: '不限最高学历' },
], {
  validate: (changes, records) => records.some((record) => (changes.is_active ?? record.is_active) && (
    !(changes.first_degree_tag_ids || record.first_degree_tags || []).length || !(changes.highest_degree_tag_ids || record.highest_degree_tags || []).length
  )) ? '启用的规则必须同时配置第一学历和最高学历允许标签' : '',
})
