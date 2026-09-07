import json
from types import SimpleNamespace
from unittest.mock import Mock, patch

from django.test import TestCase, override_settings

from apps.core import models as m
from apps.pipeline import tasks
from apps.pipeline.ai import school_province


class _Slot:
    def __init__(self):
        self.released = False

    def release(self, *_args, **_kwargs):
        self.released = True


class SchoolProvinceAIServiceTests(TestCase):
    def test_structured_output_only_keeps_requested_school_and_supported_province(self):
        content = json.dumps(
            {
                "schools": [
                    {"name": "北京大学", "province": "北京市"},
                    {"name": "南京大学", "province": "江苏省"},
                    {"name": "模型新增大学", "province": "广东"},
                ]
            },
            ensure_ascii=False,
        )
        client = SimpleNamespace(
            chat=SimpleNamespace(
                completions=SimpleNamespace(
                    create=Mock(
                        return_value=SimpleNamespace(
                            choices=[
                                SimpleNamespace(
                                    message=SimpleNamespace(content=content)
                                )
                            ]
                        )
                    )
                )
            )
        )
        model_config = SimpleNamespace(
            api_style="chat_json",
            model_name="test-model",
            base_url="https://model.internal/v1",
            api_key="",
        )
        runtime_config = SimpleNamespace(
            retry_count=0,
            retry_backoff_seconds=0,
            timeout_seconds=30,
            concurrency=2,
        )
        with patch.object(
            school_province.ai_config,
            "get_ai_model_config",
            return_value=model_config,
        ), patch.object(
            school_province.ai_config,
            "get_ai_runtime_config",
            return_value=runtime_config,
        ), patch.object(
            school_province,
            "_get_openai_client",
            return_value=client,
        ), patch.object(
            school_province.concurrency,
            "acquire_slot",
            return_value=_Slot(),
        ):
            result = school_province.infer_school_provinces(
                ["北京大学", "南京大学"]
            )

        self.assertEqual(result, {"北京大学": "北京", "南京大学": "江苏"})

    def test_invalid_school_schema_is_corrected_once(self):
        create = Mock(
            side_effect=[
                SimpleNamespace(
                    choices=[
                        SimpleNamespace(
                            message=SimpleNamespace(content='{"unexpected": []}')
                        )
                    ]
                ),
                SimpleNamespace(
                    choices=[
                        SimpleNamespace(
                            message=SimpleNamespace(
                                content='{"schools": [{"name": "北京大学", "province": "北京"}]}'
                            )
                        )
                    ]
                ),
            ]
        )
        client = SimpleNamespace(
            chat=SimpleNamespace(completions=SimpleNamespace(create=create))
        )
        model_config = SimpleNamespace(
            api_style="chat_json",
            model_name="test-model",
            base_url="https://model.internal/v1",
            api_key="",
        )
        runtime_config = SimpleNamespace(
            retry_count=0,
            retry_backoff_seconds=0,
            timeout_seconds=30,
            concurrency=2,
        )
        with patch.object(
            school_province.ai_config,
            "get_ai_model_config",
            return_value=model_config,
        ), patch.object(
            school_province.ai_config,
            "get_ai_runtime_config",
            return_value=runtime_config,
        ), patch.object(
            school_province,
            "_get_openai_client",
            return_value=client,
        ), patch.object(
            school_province.concurrency,
            "acquire_slot",
            side_effect=[_Slot(), _Slot()],
        ):
            result = school_province.infer_school_provinces(["北京大学"])

        self.assertEqual(result, {"北京大学": "北京"})
        self.assertEqual(create.call_count, 2)
        correction = create.call_args_list[1].kwargs["messages"][0]["content"]
        self.assertIn("schools:missing", correction)


class SchoolProvinceTaskTests(TestCase):
    @patch("apps.pipeline.tasks.ai_config.is_ai_available", return_value=True)
    @patch("apps.pipeline.tasks.school_province.infer_school_provinces")
    def test_task_only_updates_still_blank_provinces(self, infer, _available):
        blank = m.School.objects.create(name="北京大学", province="")
        manual = m.School.objects.create(name="南京大学", province="安徽")
        another_blank = m.School.objects.create(name="浙江大学", province="")
        infer.return_value = {
            "北京大学": "北京",
            "南京大学": "江苏",
            "浙江大学": "浙江",
        }

        result = tasks.enrich_school_provinces_task.run(
            [blank.id, manual.id, another_blank.id]
        )

        blank.refresh_from_db()
        manual.refresh_from_db()
        another_blank.refresh_from_db()
        self.assertEqual(blank.province, "北京")
        self.assertEqual(manual.province, "安徽")
        self.assertEqual(another_blank.province, "浙江")
        self.assertEqual(result["updated"], 2)
        infer.assert_called_once_with(["北京大学", "浙江大学"])

    @patch("apps.pipeline.tasks.ai_config.is_ai_available", return_value=True)
    @patch(
        "apps.pipeline.tasks.school_province.infer_school_provinces",
        side_effect=RuntimeError("provider error"),
    )
    def test_task_failure_does_not_raise_or_change_school(self, _infer, _available):
        school = m.School.objects.create(name="未知大学", province="")

        result = tasks.enrich_school_provinces_task.run([school.id])

        school.refresh_from_db()
        self.assertEqual(school.province, "")
        self.assertEqual(result["status"], "partial")
        self.assertEqual(result["failed_batches"], 1)

    @patch("apps.pipeline.tasks.ai_config.is_ai_available", return_value=True)
    @patch("apps.pipeline.tasks.school_province.infer_school_provinces")
    def test_task_uses_prompt_version_frozen_at_submission(
        self, infer, _available
    ):
        school = m.School.objects.create(name="北京大学", province="")
        infer.return_value = {"北京大学": "北京"}

        tasks.enrich_school_provinces_task.run(
            [school.id],
            "resume-screening-v2",
        )

        infer.assert_called_once_with(
            ["北京大学"],
            prompt_version="resume-screening-v2",
        )

    @override_settings(CELERY_TASK_ALWAYS_EAGER=True)
    @patch.object(tasks._LOCAL_ENRICHMENT_EXECUTOR, "submit")
    def test_eager_mode_submits_local_background_thread(self, submit):
        result = tasks.submit_school_province_enrichment([1, 2])

        self.assertEqual(result, {"backend": "local_thread", "task_id": ""})
        submit.assert_called_once_with(
            tasks._run_local_school_province_enrichment,
            [1, 2],
            "kernel-instructions-v1",
        )
