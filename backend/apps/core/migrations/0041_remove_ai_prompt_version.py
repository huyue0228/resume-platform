from django.db import migrations


class Migration(migrations.Migration):
    dependencies = [("core", "0040_agent_kernel_runtime_boundary")]

    operations = [migrations.DeleteModel(name="AIPromptVersion")]
