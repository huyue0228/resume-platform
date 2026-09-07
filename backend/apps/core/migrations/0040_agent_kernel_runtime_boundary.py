from django.db import migrations, models


class Migration(migrations.Migration):
    dependencies = [
        ("core", "0039_remove_import_undo"),
    ]

    operations = [
        migrations.AddField(
            model_name="agentdispatchdecision",
            name="kernel_build",
            field=models.CharField(blank=True, max_length=64),
        ),
        migrations.AddField(
            model_name="agentdispatchdecision",
            name="kernel_pin_id",
            field=models.CharField(blank=True, db_index=True, max_length=64),
        ),
        migrations.AddField(
            model_name="agentdispatchdecision",
            name="protocol_version",
            field=models.CharField(blank=True, max_length=32),
        ),
        migrations.AddField(
            model_name="agentdispatchdecision",
            name="safe_trace",
            field=models.JSONField(blank=True, default=dict),
        ),
        migrations.AddField(
            model_name="agentdispatchdecision",
            name="toolset_version",
            field=models.CharField(blank=True, max_length=48),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="kernel_build",
            field=models.CharField(blank=True, max_length=64),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="model_config_revision",
            field=models.CharField(blank=True, max_length=64),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="pin_id",
            field=models.CharField(blank=True, db_index=True, max_length=64),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="policy_version",
            field=models.CharField(blank=True, max_length=32),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="protocol_version",
            field=models.CharField(blank=True, max_length=32),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="result_schema_version",
            field=models.CharField(blank=True, max_length=32),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="toolset_version",
            field=models.CharField(blank=True, max_length=48),
        ),
        migrations.AlterField(
            model_name="candidateworkflow",
            name="dispatch_strategy",
            field=models.CharField(default="ai", max_length=16),
        ),
        migrations.AlterField(
            model_name="assignmentattempt",
            name="source",
            field=models.CharField(
                choices=[
                    ("rule", "规则分配"),
                    ("ai", "AI 分配"),
                    ("manual", "手动强制分配"),
                ],
                default="ai",
                max_length=16,
            ),
        ),
        migrations.AlterField(
            model_name="processingrun",
            name="mode",
            field=models.CharField(default="ai", max_length=8),
        ),
    ]
