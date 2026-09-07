import json
import os
import tempfile
from io import StringIO

from django.core.management import call_command
from django.core.management.base import CommandError
from django.test import TestCase, override_settings

from apps.core import models as m
from apps.ingestion.sources import RESUME_SUBDIR


class VerifyRestoredDataCommandTests(TestCase):
    def setUp(self):
        self.media_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.media_dir.cleanup)
        self.candidate = m.Candidate.objects.create(
            identity_hash="restore-verify-candidate",
            name="恢复校验候选人",
            phone="13800009999",
        )
        self.resume = m.Resume.objects.create(
            candidate=self.candidate,
            apply_id="RESTORE-001",
            resume_file="restore.pdf",
        )

    def test_reports_core_counts_when_database_and_media_are_consistent(self):
        with override_settings(MEDIA_ROOT=self.media_dir.name):
            resume_dir = os.path.join(self.media_dir.name, RESUME_SUBDIR)
            os.makedirs(resume_dir, exist_ok=True)
            with open(os.path.join(resume_dir, "restore.pdf"), "wb") as file_obj:
                file_obj.write(b"%PDF-restore-test")

            stdout = StringIO()
            call_command("verify_restored_data", "--json", stdout=stdout)

        payload = json.loads(stdout.getvalue())
        self.assertEqual(payload["candidates"], 1)
        self.assertEqual(payload["resumes"], 1)
        self.assertEqual(payload["missing_resume_files"], 0)

    def test_fails_when_a_resume_file_is_missing(self):
        with override_settings(MEDIA_ROOT=self.media_dir.name):
            with self.assertRaisesMessage(CommandError, "缺失简历文件"):
                call_command("verify_restored_data", "--json")
