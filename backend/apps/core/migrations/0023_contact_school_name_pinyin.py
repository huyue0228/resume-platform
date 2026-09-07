from django.db import migrations, models


def populate_name_pinyin(apps, schema_editor):
    from apps.core.name_pinyin import name_to_pinyin

    for model_name in ["Contact", "School"]:
        model = apps.get_model("core", model_name)
        for record in model.objects.all().iterator(chunk_size=500):
            record.name_pinyin, record.name_pinyin_initials = name_to_pinyin(record.name)
            record.save(update_fields=["name_pinyin", "name_pinyin_initials"])


class Migration(migrations.Migration):
    dependencies = [("core", "0022_alter_candidateworkflow_archive_reason")]

    operations = [
        migrations.AddField(
            model_name="contact",
            name="name_pinyin",
            field=models.CharField(blank=True, max_length=128),
        ),
        migrations.AddField(
            model_name="contact",
            name="name_pinyin_initials",
            field=models.CharField(blank=True, max_length=32),
        ),
        migrations.AddField(
            model_name="school",
            name="name_pinyin",
            field=models.CharField(blank=True, max_length=256),
        ),
        migrations.AddField(
            model_name="school",
            name="name_pinyin_initials",
            field=models.CharField(blank=True, max_length=64),
        ),
        migrations.RunPython(populate_name_pinyin, migrations.RunPython.noop),
        migrations.AddIndex(
            model_name="contact",
            index=models.Index(fields=["name"], name="core_contac_name_8f709f_idx"),
        ),
        migrations.AddIndex(
            model_name="contact",
            index=models.Index(fields=["name_pinyin"], name="core_contac_name_pi_d5e4e6_idx"),
        ),
        migrations.AddIndex(
            model_name="contact",
            index=models.Index(fields=["name_pinyin_initials"], name="core_contac_name_pi_03b11a_idx"),
        ),
        migrations.AddIndex(
            model_name="school",
            index=models.Index(fields=["name"], name="core_school_name_f5e185_idx"),
        ),
        migrations.AddIndex(
            model_name="school",
            index=models.Index(fields=["name_pinyin"], name="core_school_name_pi_5339d1_idx"),
        ),
        migrations.AddIndex(
            model_name="school",
            index=models.Index(fields=["name_pinyin_initials"], name="core_school_name_pi_0703cb_idx"),
        ),
    ]
