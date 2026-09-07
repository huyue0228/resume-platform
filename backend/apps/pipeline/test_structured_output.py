import json
from types import SimpleNamespace
from unittest.mock import Mock, patch

from django.test import SimpleTestCase, TestCase

from apps.pipeline import ai_config
from apps.core import models as m
from apps.pipeline.ai.schemas import ResumeScreeningOutput
from apps.pipeline.ai.structured_output import (
    AIServiceError,
    call_structured_model,
    probe_structured_output_mode,
)


def valid_payload():
    return {
        "profile": {
            "major_direction": "计算机",
            "educations": [],
            "projects": [],
            "internships": [],
            "skills": ["Python"],
            "certificates": [],
            "summary": "具备开发基础",
            "risk_flags": [],
        },
        "decision": {
            "recommendation": "review",
            "score_breakdown": {
                "major_match": 0.8,
                "skills_match": 0.8,
                "experience_evidence": 0.6,
                "job_requirement": 0.7,
                "resume_quality": 0.7,
            },
            "summary": "建议复核",
            "reason": "信息有限",
            "evidence": ["Python"],
            "risks": [],
            "ai_specialist_match": False,
            "ai_specialist_confidence": 0,
            "ai_specialist_evidence": [],
        },
    }


class _Slot:
    def __init__(self):
        self.released = False
        self.calls = []

    def release(self, outcome, *, retry_after=0):
        self.released = True
        self.calls.append((outcome, retry_after))


class StructuredOutputTests(SimpleTestCase):
    def setUp(self):
        self.model_config = SimpleNamespace(
            api_style="chat_json",
            model_name="test-model",
            base_url="https://model.internal/v1",
            api_key="",
        )
        self.runtime_config = SimpleNamespace(
            retry_count=0,
            retry_backoff_seconds=0,
            timeout_seconds=30,
            concurrency=2,
        )
        self.messages = [
            {"role": "system", "content": "只输出 JSON"},
            {"role": "user", "content": "分析测试简历"},
        ]

    def _chat_response(self, content=None, *, parsed=None, finish_reason=None, refusal=None):
        return SimpleNamespace(
            choices=[
                SimpleNamespace(
                    finish_reason=finish_reason,
                    message=SimpleNamespace(
                        content=content,
                        parsed=parsed,
                        refusal=refusal,
                    ),
                )
            ]
        )

    def _call_chat_compat(self, create, *, retry_count=0, run_id=7):
        runtime = SimpleNamespace(
            **{**self.runtime_config.__dict__, "retry_count": retry_count}
        )
        slots = [_Slot() for _ in range(2 + retry_count)]
        client = SimpleNamespace(
            chat=SimpleNamespace(completions=SimpleNamespace(create=create))
        )
        with patch(
            "apps.pipeline.ai.structured_output.ai_config.get_structured_output_mode",
            return_value=ai_config.STRUCTURED_OUTPUT_MODE_JSON_COMPAT,
        ), patch(
            "apps.pipeline.ai.structured_output.concurrency.acquire_slot",
            side_effect=slots,
        ), patch(
            "apps.pipeline.ai.structured_output.concurrency.record_retry"
        ) as record_retry, patch(
            "apps.pipeline.ai.structured_output.concurrency.record_rate_limit"
        ) as record_rate_limit, patch(
            "apps.pipeline.ai.structured_output.concurrency.retry_delay",
            return_value=0,
        ):
            result = call_structured_model(
                client=client,
                model_config=self.model_config,
                runtime_config=runtime,
                messages=self.messages,
                schema_model=ResumeScreeningOutput,
                processing_run_id=run_id,
            )
        return result, record_retry, record_rate_limit, slots

    def test_compat_mode_accepts_single_outer_json_code_fence(self):
        content = f"```json\n{json.dumps(valid_payload(), ensure_ascii=False)}\n```"
        create = Mock(return_value=self._chat_response(content))

        result, record_retry, _record_rate_limit, slots = self._call_chat_compat(create)

        self.assertEqual(result.decision.recommendation, "review")
        record_retry.assert_not_called()
        self.assertEqual(slots[0].calls, [("success", 0)])

    def test_schema_failure_is_corrected_once_without_echoing_invalid_value(self):
        invalid = valid_payload()
        invalid["decision"]["recommendation"] = "private-invalid-value"
        create = Mock(
            side_effect=[
                self._chat_response(json.dumps(invalid, ensure_ascii=False)),
                self._chat_response(json.dumps(valid_payload(), ensure_ascii=False)),
            ]
        )

        result, record_retry, _record_rate_limit, _slots = self._call_chat_compat(create)

        self.assertEqual(result.decision.recommendation, "review")
        self.assertEqual(create.call_count, 2)
        record_retry.assert_called_once_with(7)
        correction = create.call_args_list[1].kwargs["messages"][0]["content"]
        self.assertIn("decision.recommendation:literal_error", correction)
        self.assertNotIn("private-invalid-value", correction)

    def test_untrusted_extra_field_name_is_redacted_from_correction(self):
        invalid = valid_payload()
        invalid["候选人手机号13800000000"] = True
        create = Mock(
            side_effect=[
                self._chat_response(json.dumps(invalid, ensure_ascii=False)),
                self._chat_response(json.dumps(valid_payload(), ensure_ascii=False)),
            ]
        )

        self._call_chat_compat(create)

        correction = create.call_args_list[1].kwargs["messages"][0]["content"]
        self.assertIn("<field>:extra_forbidden", correction)
        self.assertNotIn("13800000000", correction)

    def test_second_schema_failure_stops_after_one_correction(self):
        invalid = valid_payload()
        invalid.pop("decision")
        create = Mock(
            return_value=self._chat_response(json.dumps(invalid, ensure_ascii=False))
        )

        with self.assertRaises(AIServiceError) as captured:
            self._call_chat_compat(create)

        self.assertEqual(captured.exception.code, "ai_invalid_output")
        self.assertIn("decision", captured.exception.message)
        self.assertIn("纠错 1 次", captured.exception.message)
        self.assertEqual(create.call_count, 2)

    def test_empty_and_truncated_outputs_share_one_repair_budget(self):
        create = Mock(
            side_effect=[
                self._chat_response("", finish_reason="length"),
                self._chat_response(json.dumps(valid_payload(), ensure_ascii=False)),
            ]
        )

        result, record_retry, _record_rate_limit, _slots = self._call_chat_compat(create)

        self.assertEqual(result.profile.major_direction, "计算机")
        record_retry.assert_called_once_with(7)
        correction = create.call_args_list[1].kwargs["messages"][0]["content"]
        self.assertIn("被截断", correction)

    def test_refusal_does_not_trigger_structure_repair(self):
        create = Mock(
            return_value=self._chat_response(
                None, finish_reason="content_filter", refusal="refused"
            )
        )

        with self.assertRaises(AIServiceError) as captured:
            self._call_chat_compat(create)

        self.assertEqual(captured.exception.code, "ai_invalid_output")
        self.assertIn("拒绝", captured.exception.message)
        self.assertEqual(create.call_count, 1)

    def test_transport_and_structure_retries_are_not_multiplied(self):
        class ProviderRateLimit(Exception):
            status_code = 429
            response = SimpleNamespace(status_code=429, headers={"retry-after": "0"})

        create = Mock(
            side_effect=[
                ProviderRateLimit("secret"),
                self._chat_response("not-json"),
                self._chat_response(json.dumps(valid_payload(), ensure_ascii=False)),
            ]
        )

        result, record_retry, record_rate_limit, _slots = self._call_chat_compat(
            create, retry_count=1
        )

        self.assertEqual(result.decision.recommendation, "review")
        self.assertEqual(create.call_count, 3)
        self.assertEqual(record_retry.call_count, 2)
        record_rate_limit.assert_called_once_with(7)

    def test_strict_responses_mode_returns_typed_result(self):
        model_config = SimpleNamespace(**{**self.model_config.__dict__, "api_style": "responses"})
        parse = Mock(
            return_value=SimpleNamespace(
                status="completed",
                output=[],
                output_parsed=ResumeScreeningOutput.model_validate(valid_payload()),
            )
        )
        client = SimpleNamespace(responses=SimpleNamespace(parse=parse))
        with patch(
            "apps.pipeline.ai.structured_output.ai_config.get_structured_output_mode",
            return_value=ai_config.STRUCTURED_OUTPUT_MODE_STRICT,
        ), patch(
            "apps.pipeline.ai.structured_output.concurrency.acquire_slot",
            return_value=_Slot(),
        ):
            result = call_structured_model(
                client=client,
                model_config=model_config,
                runtime_config=self.runtime_config,
                messages=self.messages,
                schema_model=ResumeScreeningOutput,
            )

        self.assertEqual(result.profile.skills, ["Python"])
        self.assertIs(parse.call_args.kwargs["text_format"], ResumeScreeningOutput)

    def test_probe_prefers_strict_schema(self):
        parse = Mock(
            return_value=self._chat_response(
                parsed=ResumeScreeningOutput.model_validate(valid_payload())
            )
        )
        create = Mock()
        client = SimpleNamespace(
            chat=SimpleNamespace(
                completions=SimpleNamespace(parse=parse, create=create)
            )
        )

        mode = probe_structured_output_mode(
            client=client,
            model_config=self.model_config,
            messages=self.messages,
            schema_model=ResumeScreeningOutput,
        )

        self.assertEqual(mode, ai_config.STRUCTURED_OUTPUT_MODE_STRICT)
        create.assert_not_called()

    def test_probe_only_falls_back_on_explicit_format_rejection(self):
        class UnsupportedFormat(Exception):
            status_code = 422

        parse = Mock(side_effect=UnsupportedFormat("unsupported"))
        create = Mock(
            return_value=self._chat_response(
                json.dumps(valid_payload(), ensure_ascii=False)
            )
        )
        client = SimpleNamespace(
            chat=SimpleNamespace(
                completions=SimpleNamespace(parse=parse, create=create)
            )
        )

        mode = probe_structured_output_mode(
            client=client,
            model_config=self.model_config,
            messages=self.messages,
            schema_model=ResumeScreeningOutput,
        )

        self.assertEqual(mode, ai_config.STRUCTURED_OUTPUT_MODE_JSON_COMPAT)
        create.assert_called_once()

    def test_probe_does_not_hide_auth_failure_with_compatibility_mode(self):
        class AuthenticationFailure(Exception):
            status_code = 401

        parse = Mock(side_effect=AuthenticationFailure("secret token"))
        create = Mock()
        client = SimpleNamespace(
            chat=SimpleNamespace(
                completions=SimpleNamespace(parse=parse, create=create)
            )
        )

        with self.assertRaises(AIServiceError) as captured:
            probe_structured_output_mode(
                client=client,
                model_config=self.model_config,
                messages=self.messages,
                schema_model=ResumeScreeningOutput,
            )

        self.assertEqual(captured.exception.code, "ai_connection_error")
        self.assertNotIn("secret", captured.exception.message)
        create.assert_not_called()

    def test_all_schema_objects_reject_extra_fields(self):
        payload = valid_payload()
        payload["unexpected"] = True
        with self.assertRaises(ValueError):
            ResumeScreeningOutput.model_validate(payload)

        payload = valid_payload()
        payload["profile"]["projects"] = [
            {
                "name": "项目",
                "role": "开发",
                "period": "2025",
                "description": "描述",
                "evidence": "证据",
                "unexpected": True,
            }
        ]
        with self.assertRaises(ValueError):
            ResumeScreeningOutput.model_validate(payload)


class StructuredOutputConfigTests(TestCase):
    def test_capability_mode_is_persisted_and_invalidated_with_connection(self):
        ai_config.save_ai_connection_config(
            {
                "api_style": "chat_json",
                "model_name": "model-a",
                "base_url": "https://model.internal/v1",
                "api_key": "",
            }
        )
        self.assertEqual(
            ai_config.get_ai_connection_status()["structured_output_mode"],
            ai_config.STRUCTURED_OUTPUT_MODE_LEGACY,
        )

        ai_config.mark_ai_connection_tested(
            structured_output_mode=ai_config.STRUCTURED_OUTPUT_MODE_STRICT
        )
        self.assertEqual(
            ai_config.get_ai_connection_status()["structured_output_mode"],
            ai_config.STRUCTURED_OUTPUT_MODE_STRICT,
        )

        ai_config.save_ai_connection_config(
            {
                "api_style": "chat_json",
                "model_name": "model-b",
                "base_url": "https://model.internal/v1",
                "api_key": "",
            }
        )
        self.assertFalse(
            m.Config.objects.filter(
                key=ai_config.AI_CONNECTION_STRUCTURED_OUTPUT_MODE_KEY
            ).exists()
        )
        self.assertEqual(
            ai_config.get_ai_connection_status()["structured_output_mode"],
            ai_config.STRUCTURED_OUTPUT_MODE_LEGACY,
        )
