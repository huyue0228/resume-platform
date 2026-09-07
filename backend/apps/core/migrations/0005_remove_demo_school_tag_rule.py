from django.db import migrations


def remove_demo_rule(apps, schema_editor):
    SchoolTagRule = apps.get_model("core", "SchoolTagRule")
    SchoolTagRule.objects.filter(
        name="Demo 默认目标院校",
        first_degree_tags=["平台A", "平台B", "平台C"],
        highest_degree_tags=["平台A", "平台B", "平台C"],
    ).delete()


class Migration(migrations.Migration):
    dependencies = [
        ("core", "0004_seed_demo_school_tag_rule"),
    ]

    operations = [
        migrations.RunPython(remove_demo_rule, migrations.RunPython.noop),
    ]
