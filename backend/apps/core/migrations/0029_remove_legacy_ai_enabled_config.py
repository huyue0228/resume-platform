from django.db import migrations


def remove_legacy_ai_enabled(apps, schema_editor):
    Config = apps.get_model("core", "Config")
    Config.objects.filter(key="ai_enabled").delete()


class Migration(migrations.Migration):
    dependencies = [
        ("core", "0028_agentdispatchdecision_ai_specialist_confidence_and_more"),
    ]

    operations = [
        migrations.RunPython(remove_legacy_ai_enabled, migrations.RunPython.noop),
    ]
