from django.db import migrations


class Migration(migrations.Migration):
    dependencies = [
        ("core", "0038_candidate_school_tags"),
    ]

    operations = [
        migrations.RemoveField(
            model_name="processingrun",
            name="undone_by",
        ),
        migrations.RemoveField(
            model_name="processingrun",
            name="undone_at",
        ),
        migrations.DeleteModel(
            name="ImportSnapshot",
        ),
    ]
