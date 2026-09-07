from django.db import migrations, models


class Migration(migrations.Migration):

    dependencies = [
        ("core", "0029_remove_legacy_ai_enabled_config"),
    ]

    operations = [
        migrations.AddField(
            model_name="job",
            name="responsibilities",
            field=models.TextField(blank=True, default="", help_text="工作职责"),
        ),
    ]
