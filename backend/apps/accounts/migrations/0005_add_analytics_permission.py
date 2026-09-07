from django.db import migrations


PERMISSION_CODENAME = "analytics__view"
PERMISSION_NAME = "查看招聘分析"
DEFAULT_GROUPS = ("管理员", "HR")


def add_analytics_permission(apps, schema_editor):
    ContentType = apps.get_model("contenttypes", "ContentType")
    Permission = apps.get_model("auth", "Permission")
    Group = apps.get_model("auth", "Group")
    content_type, _ = ContentType.objects.get_or_create(
        app_label="accounts", model="user"
    )
    permission, _ = Permission.objects.update_or_create(
        content_type=content_type,
        codename=PERMISSION_CODENAME,
        defaults={"name": PERMISSION_NAME},
    )
    for group in Group.objects.filter(name__in=DEFAULT_GROUPS):
        group.permissions.add(permission)


class Migration(migrations.Migration):
    dependencies = [
        ("accounts", "0004_add_ai_connection_permission"),
    ]

    operations = [
        migrations.RunPython(add_analytics_permission, migrations.RunPython.noop),
    ]
