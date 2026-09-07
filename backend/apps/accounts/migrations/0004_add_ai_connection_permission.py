from django.db import migrations


PERMISSION_CODENAME = "settings__manage_ai_connection"
PERMISSION_NAME = "AI 模型连接管理"


def add_ai_connection_permission(apps, schema_editor):
    ContentType = apps.get_model("contenttypes", "ContentType")
    Permission = apps.get_model("auth", "Permission")
    Group = apps.get_model("auth", "Group")
    content_type, _ = ContentType.objects.get_or_create(app_label="accounts", model="user")
    permission, _ = Permission.objects.update_or_create(
        content_type=content_type,
        codename=PERMISSION_CODENAME,
        defaults={"name": PERMISSION_NAME},
    )
    try:
        Group.objects.get(name="管理员").permissions.add(permission)
    except Group.DoesNotExist:
        pass


class Migration(migrations.Migration):

    dependencies = [
        ("accounts", "0003_alter_user_role"),
    ]

    operations = [
        migrations.RunPython(add_ai_connection_permission, migrations.RunPython.noop),
    ]
