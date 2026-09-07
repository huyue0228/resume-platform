from django.conf import settings
from django.db import migrations, models
import django.db.models.deletion


class Migration(migrations.Migration):
    dependencies = [
        ("core", "0015_processingrun_failure_count_and_more"),
    ]

    operations = [
        migrations.RenameField("resumeprofile", "parsed_text", "raw_text"),
        migrations.RenameField("resumeprofile", "education", "education_experiences"),
        migrations.RenameField("resumeprofile", "projects", "project_experiences"),
        migrations.RenameField("resumeprofile", "internships", "internship_experiences"),
        migrations.RenameField("resumeprofile", "risk_flags", "profile_risk_flags"),
        migrations.RenameField("resumeprofile", "parser_version", "parse_model"),
        migrations.RenameField("processingrun", "failure_count", "failed_count"),
        migrations.AddField(
            model_name="processingrun",
            name="archive_count",
            field=models.PositiveIntegerField(default=0),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="celery_group_id",
            field=models.CharField(blank=True, max_length=64),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="chunk_done",
            field=models.PositiveIntegerField(blank=True, null=True),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="chunk_errors",
            field=models.JSONField(blank=True, default=list),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="chunk_failed",
            field=models.PositiveIntegerField(blank=True, null=True),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="chunk_size",
            field=models.PositiveIntegerField(blank=True, null=True),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="chunk_total",
            field=models.PositiveIntegerField(blank=True, null=True),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="decision_version",
            field=models.CharField(blank=True, max_length=32),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="dispatch_count",
            field=models.PositiveIntegerField(default=0),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="error",
            field=models.TextField(blank=True),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="model_name",
            field=models.CharField(blank=True, max_length=64),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="prompt_version",
            field=models.CharField(blank=True, max_length=32),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="review_count",
            field=models.PositiveIntegerField(default=0),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="undone_at",
            field=models.DateTimeField(blank=True, null=True),
        ),
        migrations.AddField(
            model_name="processingrun",
            name="undone_by",
            field=models.ForeignKey(
                blank=True,
                null=True,
                on_delete=django.db.models.deletion.SET_NULL,
                related_name="processing_runs_undone",
                to=settings.AUTH_USER_MODEL,
            ),
        ),
    ]
