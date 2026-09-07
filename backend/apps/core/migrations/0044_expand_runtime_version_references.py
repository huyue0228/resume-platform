"""内部版本标识允许 SHA-256 和独立组件版本；不修改现有业务数据。"""
from django.db import migrations, models


class Migration(migrations.Migration):
    dependencies = [("core", "0043_agentdispatchdecision_kernel_result")]
    operations = [
        migrations.AlterField(model_name=model, name=field, field=models.CharField(blank=True, max_length=128))
        for model, fields in (
            ("agentdispatchdecision", ("decision_version", "kernel_build", "prompt_version", "toolset_version")),
            ("processingrun", ("decision_version", "kernel_build", "model_config_revision", "policy_version", "prompt_version", "toolset_version")),
        )
        for field in fields
    ]
