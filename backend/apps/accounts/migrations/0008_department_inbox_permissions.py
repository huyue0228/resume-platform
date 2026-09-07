from django.db import migrations


def migrate_department_inbox_permissions(apps, schema_editor):
    ContentType = apps.get_model("contenttypes", "ContentType")
    Permission = apps.get_model("auth", "Permission")
    Group = apps.get_model("auth", "Group")

    content_type, _ = ContentType.objects.get_or_create(
        app_label="accounts", model="user"
    )
    legacy_codenames = [
        "attempt__view_received",
        "attempt__view_assigned",
        "attempt__assign_sub_contact",
    ]
    Permission.objects.filter(
        content_type=content_type,
        codename__in=legacy_codenames,
    ).delete()

    permissions = {}
    for codename, name in [
        ("attempt__view_department", "查看所属部门分配"),
        ("attempt__transfer_department", "转派部门"),
    ]:
        permissions[codename], _ = Permission.objects.update_or_create(
            content_type=content_type,
            codename=codename,
            defaults={"name": name},
        )

    feedback, _ = Permission.objects.get_or_create(
        content_type=content_type,
        codename="attempt__feedback",
        defaults={"name": "提交反馈"},
    )
    export, _ = Permission.objects.get_or_create(
        content_type=content_type,
        codename="attempt__export",
        defaults={"name": "导出简历"},
    )

    role_additions = {
        "管理员": list(permissions.values()),
        "HR": [permissions["attempt__transfer_department"]],
        "二级接口人": [
            permissions["attempt__view_department"],
            permissions["attempt__transfer_department"],
            feedback,
            export,
        ],
        "三级接口人": [
            permissions["attempt__view_department"],
            feedback,
            export,
        ],
    }
    for role_name, additions in role_additions.items():
        group, _ = Group.objects.get_or_create(name=role_name)
        group.permissions.add(*additions)


class Migration(migrations.Migration):
    dependencies = [
        ("accounts", "0007_invalidate_user_passwords"),
    ]

    operations = [
        migrations.RunPython(
            migrate_department_inbox_permissions,
            reverse_code=migrations.RunPython.noop,
        ),
    ]
