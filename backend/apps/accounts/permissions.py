from django.contrib.auth.models import Group, Permission
from django.contrib.contenttypes.models import ContentType
from rest_framework.permissions import BasePermission

from apps.accounts.models import User


PERMISSION_TREE = [
    {
        "code": "resume",
        "name": "简历库",
        "children": [
            {"code": "resume.view", "name": "查看简历"},
            {"code": "resume.import", "name": "导入简历"},
            {"code": "resume.manual_assign", "name": "手动分配"},
        ],
    },
    {
        "code": "master_data",
        "name": "主数据",
        "children": [
            {"code": "job.view", "name": "查看岗位"},
            {"code": "job.manage", "name": "维护岗位"},
            {"code": "school.view", "name": "查看院校"},
            {"code": "school.manage", "name": "维护院校"},
            {"code": "department.view", "name": "查看部门接口人"},
            {"code": "department.manage", "name": "维护部门接口人"},
        ],
    },
    {
        "code": "attempt",
        "name": "分配尝试",
        "children": [
            {"code": "attempt.view_all", "name": "查看全部分配"},
            {"code": "attempt.view_department", "name": "查看所属部门分配"},
            {"code": "attempt.dispatch", "name": "下发部门"},
            {"code": "attempt.transfer_department", "name": "转派部门"},
            {"code": "attempt.feedback", "name": "提交反馈"},
            {"code": "attempt.export", "name": "导出简历"},
        ],
    },
    {
        "code": "pipeline",
        "name": "处理流水线",
        "children": [
            {"code": "pipeline.run", "name": "运行处理流程"},
            {"code": "pipeline.view", "name": "查看处理记录"},
        ],
    },
    {
        "code": "analytics",
        "name": "招聘分析",
        "children": [
            {"code": "analytics.view", "name": "查看招聘分析"},
        ],
    },
    {
        "code": "settings",
        "name": "系统设置",
        "children": [
            {"code": "settings.manage_config", "name": "配置项管理"},
            {"code": "settings.manage_permissions", "name": "用户权限管理"},
            {
                "code": "settings.manage_ai_connection",
                "name": "AI 模型连接管理",
            },
        ],
    },
]

ROLE_PERMISSION_CODES = {
    "管理员": [
        child["code"] for module in PERMISSION_TREE for child in module["children"]
    ],
    "HR": [
        "resume.view",
        "resume.import",
        "resume.manual_assign",
        "job.view",
        "job.manage",
        "school.view",
        "school.manage",
        "department.view",
        "department.manage",
        "attempt.view_all",
        "attempt.dispatch",
        "attempt.transfer_department",
        "attempt.export",
        "pipeline.run",
        "pipeline.view",
        "analytics.view",
        "settings.manage_config",
    ],
    "二级接口人": [
        "attempt.view_department",
        "attempt.transfer_department",
        "attempt.feedback",
        "attempt.export",
    ],
    "三级接口人": [
        "attempt.view_department",
        "attempt.feedback",
        "attempt.export",
    ],
}


def permission_codename(code):
    return code.replace(".", "__")


def permission_code(codename):
    return codename.replace("__", ".")


def all_permission_codes():
    return [child["code"] for module in PERMISSION_TREE for child in module["children"]]


def ensure_permission_definitions():
    content_type = ContentType.objects.get_for_model(User)
    permissions = {}
    for module in PERMISSION_TREE:
        for item in module["children"]:
            permission, _ = Permission.objects.update_or_create(
                content_type=content_type,
                codename=permission_codename(item["code"]),
                defaults={"name": item["name"]},
            )
            permissions[item["code"]] = permission
    return permissions


def ensure_rbac_defaults():
    permissions = ensure_permission_definitions()

    for role_name, codes in ROLE_PERMISSION_CODES.items():
        group, created = Group.objects.get_or_create(name=role_name)
        if created:
            group.permissions.set([permissions[code] for code in codes])


def user_permission_codes(user):
    if not user or not user.is_authenticated:
        return set()
    if user.is_superuser:
        return set(all_permission_codes())
    codenames = set(
        user.get_all_permissions()
    )
    prefix = f"{User._meta.app_label}."
    known_codes = set(all_permission_codes())
    return {
        permission_code(value[len(prefix):])
        for value in codenames
        if value.startswith(prefix)
        and permission_code(value[len(prefix):]) in known_codes
    }


def has_permission_code(user, code):
    return code in user_permission_codes(user)


def user_role_names(user):
    if not user or not user.is_authenticated:
        return []
    return list(user.groups.order_by("name").values_list("name", flat=True))


class HasPermissionCode(BasePermission):
    """DRF permission class using the stable dotted permission codes exposed to UI."""

    def has_permission(self, request, view):
        if not request.user or not request.user.is_authenticated:
            return False
        code = getattr(view, "permission_code", None)
        by_action = getattr(view, "permission_codes_by_action", {})
        if getattr(view, "action", None) in by_action:
            code = by_action[view.action]
        if code is None:
            return True
        if isinstance(code, (list, tuple, set)):
            return any(has_permission_code(request.user, item) for item in code)
        return has_permission_code(request.user, code)
