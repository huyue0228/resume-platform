from django.contrib.auth.hashers import make_password
from django.db import migrations


PROTECTED_ADMIN_USERNAME = "012358"
PROTECTED_ADMIN_EMAIL = "huyue2@ueascend.com"


def create_protected_w3_admin(apps, schema_editor):
    User = apps.get_model("accounts", "User")
    Group = apps.get_model("auth", "Group")

    conflicting_email = User.objects.filter(
        email__iexact=PROTECTED_ADMIN_EMAIL
    ).exclude(username=PROTECTED_ADMIN_USERNAME)
    if conflicting_email.exists():
        raise RuntimeError(
            f"邮箱 {PROTECTED_ADMIN_EMAIL} 已被其它账号占用，无法创建内置管理员"
        )

    user, _ = User.objects.update_or_create(
        username=PROTECTED_ADMIN_USERNAME,
        defaults={
            "email": PROTECTED_ADMIN_EMAIL,
            "password": make_password(None),
            "role": "admin",
            "contact": None,
            "is_active": True,
            "is_staff": True,
            "is_superuser": True,
        },
    )
    admin_group, _ = Group.objects.get_or_create(name="管理员")
    user.groups.set([admin_group])


class Migration(migrations.Migration):
    dependencies = [
        ("accounts", "0005_add_analytics_permission"),
    ]

    operations = [
        migrations.RunPython(
            create_protected_w3_admin,
            migrations.RunPython.noop,
        ),
    ]
