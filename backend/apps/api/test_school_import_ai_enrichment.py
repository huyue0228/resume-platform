from io import BytesIO
from unittest.mock import patch

from django.contrib.auth.models import Group
from django.core.files.uploadedfile import SimpleUploadedFile
from django.test import TestCase
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults
from apps.ingestion.tabular_imports import build_import_template_workbook


class SchoolImportAIEnrichmentApiTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.client = APIClient()
        self.hr = User.objects.create_user(
            username="school-import-ai", password="pass", role=User.ROLE_HR
        )
        self.hr.groups.add(Group.objects.get(name="HR"))
        self.client.force_authenticate(self.hr)

    def _upload(self):
        output = BytesIO()
        build_import_template_workbook("schools").save(output)
        return self.client.post(
            "/api/import/",
            {
                "schools": SimpleUploadedFile(
                    "院校.xlsx",
                    output.getvalue(),
                    content_type=(
                        "application/vnd.openxmlformats-officedocument."
                        "spreadsheetml.sheet"
                    ),
                )
            },
            format="multipart",
        )

    @patch("apps.api.views.submit_school_province_enrichment")
    @patch("apps.api.views.ai_config.is_ai_available", return_value=True)
    @patch("apps.api.views.import_files")
    def test_import_queues_background_enrichment_for_blank_provinces(
        self,
        import_files,
        _available,
        submit,
    ):
        import_files.return_value = {
            "schools": 2,
            "_candidate_ids": [],
            "_school_ids_missing_province": [10, 20],
            "_warnings": [],
        }
        submit.return_value = {"backend": "celery", "task_id": "task-1"}

        response = self._upload()

        self.assertEqual(response.status_code, 200)
        submit.assert_called_once_with([10, 20])
        self.assertEqual(
            response.data["detail"], "导入完成，已提交 2 所院校省份后台补全"
        )
        self.assertEqual(
            response.data["school_province_enrichment"],
            {
                "status": "queued",
                "school_count": 2,
                "backend": "celery",
                "task_id": "task-1",
            },
        )
        self.assertNotIn("_school_ids_missing_province", response.data["counts"])

    @patch("apps.api.views.submit_school_province_enrichment")
    @patch("apps.api.views.ai_config.is_ai_available", return_value=False)
    @patch("apps.api.views.import_files")
    def test_import_skips_enrichment_when_ai_is_unavailable(
        self,
        import_files,
        _available,
        submit,
    ):
        import_files.return_value = {
            "schools": 1,
            "_candidate_ids": [],
            "_school_ids_missing_province": [10],
            "_warnings": [],
        }

        response = self._upload()

        self.assertEqual(response.status_code, 200)
        submit.assert_not_called()
        self.assertEqual(
            response.data["school_province_enrichment"],
            {"status": "ai_unavailable", "school_count": 1},
        )

    @patch(
        "apps.api.views.submit_school_province_enrichment",
        side_effect=RuntimeError("broker unavailable"),
    )
    @patch("apps.api.views.ai_config.is_ai_available", return_value=True)
    @patch("apps.api.views.import_files")
    def test_queue_failure_does_not_fail_import(
        self,
        import_files,
        _available,
        _submit,
    ):
        import_files.return_value = {
            "schools": 1,
            "_candidate_ids": [],
            "_school_ids_missing_province": [10],
            "_warnings": [],
        }

        response = self._upload()

        self.assertEqual(response.status_code, 200)
        self.assertEqual(
            response.data["school_province_enrichment"],
            {"status": "queue_failed", "school_count": 1},
        )
