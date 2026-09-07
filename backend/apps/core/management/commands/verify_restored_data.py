import json
import os

from django.conf import settings
from django.core.management.base import BaseCommand, CommandError
from django.db import connection

from apps.accounts.models import User
from apps.core import models as m
from apps.ingestion.sources import RESUME_SUBDIR


class Command(BaseCommand):
    help = "校验恢复后的核心表、外键约束和简历文件完整性。"

    def add_arguments(self, parser):
        parser.add_argument("--json", action="store_true", dest="as_json")

    def handle(self, *args, **options):
        with connection.cursor() as cursor:
            connection.check_constraints()
            cursor.execute("SELECT 1")

        missing_files = []
        for resume in m.Resume.objects.exclude(resume_file="").only(
            "apply_id", "resume_file"
        ):
            filename = os.path.basename(resume.resume_file)
            path = os.path.join(settings.MEDIA_ROOT, RESUME_SUBDIR, filename)
            if not os.path.isfile(path):
                missing_files.append(resume.apply_id)

        statistics = {
            "users": User.objects.count(),
            "configs": m.Config.objects.count(),
            "candidates": m.Candidate.objects.count(),
            "resumes": m.Resume.objects.count(),
            "workflows": m.CandidateWorkflow.objects.count(),
            "assignment_attempts": m.AssignmentAttempt.objects.count(),
            "processing_runs": m.ProcessingRun.objects.count(),
            "missing_resume_files": len(missing_files),
        }
        output = json.dumps(statistics, ensure_ascii=False, sort_keys=True)
        if options["as_json"]:
            self.stdout.write(output)
        else:
            self.stdout.write(self.style.SUCCESS(output))

        if missing_files:
            preview = ", ".join(missing_files[:10])
            raise CommandError(
                f"发现 {len(missing_files)} 条缺失简历文件，应聘ID示例：{preview}"
            )
