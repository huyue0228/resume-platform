from django.db import migrations


PERMISSION_DEFINITIONS = [
    ("resume__view", "查看简历"),
    ("resume__import", "导入简历"),
    ("resume__manual_assign", "手动分配"),
    ("job__view", "查看岗位"),
    ("job__manage", "维护岗位"),
    ("school__view", "查看院校"),
    ("school__manage", "维护院校"),
    ("department__view", "查看部门接口人"),
    ("department__manage", "维护部门接口人"),
    ("attempt__view_all", "查看全部分配"),
    ("attempt__view_department", "查看所属部门分配"),
    ("attempt__dispatch", "下发部门"),
    ("attempt__transfer_department", "转派部门"),
    ("attempt__feedback", "提交反馈"),
    ("attempt__export", "导出简历"),
    ("pipeline__run", "运行处理流程"),
    ("pipeline__view", "查看处理记录"),
    ("analytics__view", "查看招聘分析"),
    ("settings__manage_config", "配置项管理"),
    ("settings__manage_permissions", "用户权限管理"),
    ("settings__manage_ai_connection", "AI 模型连接与 Prompt 管理"),
]


ROLE_PERMISSION_CODENAMES = {
    "管理员": [codename for codename, _ in PERMISSION_DEFINITIONS],
    "HR": [
        "resume__view",
        "resume__import",
        "resume__manual_assign",
        "job__view",
        "job__manage",
        "school__view",
        "school__manage",
        "department__view",
        "department__manage",
        "attempt__view_all",
        "attempt__dispatch",
        "attempt__transfer_department",
        "attempt__export",
        "pipeline__run",
        "pipeline__view",
        "analytics__view",
        "settings__manage_config",
    ],
    "二级接口人": [
        "attempt__view_department",
        "attempt__transfer_department",
        "attempt__feedback",
        "attempt__export",
    ],
    "三级接口人": [
        "attempt__view_department",
        "attempt__feedback",
        "attempt__export",
    ],
}

# 0008 在全新数据库中只会为内置角色写入这些部门收件箱权限。此时角色虽已
# 存在，却还没有获得完整默认权限。仅识别这些精确集合，避免把已有环境中
# 管理员手工缩减过的权限再次补满。
DEPARTMENT_INBOX_BOOTSTRAP_CODENAMES = {
    "管理员": {
        "attempt__view_department",
        "attempt__transfer_department",
    },
    "HR": {"attempt__transfer_department"},
    "二级接口人": {
        "attempt__view_department",
        "attempt__transfer_department",
        "attempt__feedback",
        "attempt__export",
    },
    "三级接口人": {
        "attempt__view_department",
        "attempt__feedback",
        "attempt__export",
    },
}


def restore_builtin_role_permissions(apps, schema_editor):
    ContentType = apps.get_model("contenttypes", "ContentType")
    Permission = apps.get_model("auth", "Permission")
    Group = apps.get_model("auth", "Group")

    content_type, _ = ContentType.objects.get_or_create(
        app_label="accounts", model="user"
    )
    permissions = {}
    for codename, name in PERMISSION_DEFINITIONS:
        permissions[codename], _ = Permission.objects.update_or_create(
            content_type=content_type,
            codename=codename,
            defaults={"name": name},
        )

    for role_name, codenames in ROLE_PERMISSION_CODENAMES.items():
        group, created = Group.objects.get_or_create(name=role_name)
        if created:
            group.permissions.set(
                [permissions[codename] for codename in codenames]
            )
            continue

        registered_codenames = set(
            group.permissions.filter(content_type=content_type).values_list(
                "codename", flat=True
            )
        )
        if registered_codenames == DEPARTMENT_INBOX_BOOTSTRAP_CODENAMES[role_name]:
            group.permissions.add(
                *(permissions[codename] for codename in codenames)
            )


class Migration(migrations.Migration):
    dependencies = [
        ("accounts", "0008_department_inbox_permissions"),
    ]

    operations = [
        migrations.RunPython(
            restore_builtin_role_permissions,
            reverse_code=migrations.RunPython.noop,
        ),
    ]
