from django.contrib.auth.hashers import make_password
from django.db import migrations


def invalidate_user_passwords(apps, schema_editor):
    User = apps.get_model("accounts", "User")
    User.objects.all().update(password=make_password(None))


class Migration(migrations.Migration):
    dependencies = [
        ("accounts", "0006_create_protected_w3_admin"),
    ]

    operations = [
        migrations.RunPython(invalidate_user_passwords),
    ]
