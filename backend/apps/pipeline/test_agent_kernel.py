from datetime import datetime, timezone
from unittest.mock import Mock, patch

from django.test import SimpleTestCase, override_settings
from pydantic import ValidationError

from apps.pipeline.agent_kernel.client import AgentKernelClient
from apps.pipeline.agent_kernel.contracts import (
    AgentActionProposalV1,
    CaseEnvelopeV2,
)
from apps.pipeline.agent_kernel.gateway import is_agent_ready
from apps.pipeline.agent_kernel.policy import validate_proposal
from apps.pipeline.ai.structured_output import AIServiceError


def envelope_payload():
    return {
        "protocol_version": "resume-agent/v2",
        "task_id": "task-1",
        "idempotency_key": "idem-1",
        "pin": {
            "pin_id": "pin-1",
            "kernel_build": "test-build",
            "protocol_version": "resume-agent/v2",
            "toolset_version": "resume-tool-providers/v2",
            "result_schema_version": "resume-screening/v1",
            "policy_version": "django-policy-gate/v1",
            "instruction_version": "resume-screening-kernel/v1",
            "model_config_revision": "model-v1",
        },
        "constraints": {
            "workflow_revision": 2,
            "volunteer_rank": 1,
            "policies": ["只处理当前志愿"],
        },
        "candidate_reference": {"highest_major": "计算机科学"},
        "current_volunteer": {"position_name": "后端工程师"},
        "current_job": {
            "position_name": "后端工程师",
            "responsibilities": "开发可靠的后端服务",
            "department_name": "平台部",
        },
        "resume": {
            "checksum": "a" * 64,
            "text": "项目经历：负责 Go 服务开发和性能优化。",
        },
        "model": {
            "api_style": "chat_json",
            "base_url": "https://model.example.test/v1",
            "model_name": "test-model",
            "structured_output_mode": "json_object",
            "timeout_seconds": 30,
            "retry_count": 1,
        },
        "budget": {
            "max_turns": 4,
            "max_tool_calls": 8,
            "max_duration_seconds": 60,
        },
    }


def proposal_payload():
    now = datetime.now(timezone.utc).isoformat()
    return {
        "proposal_version": "agent-action-proposal/v1",
        "task_id": "task-1",
        "pin_id": "pin-1",
        "action": "review",
        "evaluation": {
            "profile": {
                "major_direction": "后端开发",
                "educations": [],
                "projects": [],
                "internships": [],
                "skills": ["Go"],
                "certificates": [],
                "summary": "具备后端开发经历",
                "risk_flags": [],
            },
            "decision": {
                "recommendation": "review",
                "score_breakdown": {
                    "major_match": 0.8,
                    "skills_match": 0.8,
                    "experience_evidence": 0.7,
                    "job_requirement": 0.8,
                    "resume_quality": 0.7,
                },
                "summary": "建议复核",
                "reason": "具备相关经验",
                "evidence": ["负责 Go 服务开发和性能优化"],
                "risks": [],
                "ai_specialist_match": False,
                "ai_specialist_confidence": 0,
                "ai_specialist_evidence": [],
            },
        },
        "safe_trace": {
            "trace_id": "trace-1",
            "kernel_build": "test-build",
            "started_at": now,
            "finished_at": now,
            "turns": 2,
            "tool_call_count": 1,
            "tool_calls": [
                {
                    "name": "resume.read_sections",
                    "status": "success",
                    "duration_ms": 1,
                    "item_count": 1,
                }
            ],
            "input_tokens": 100,
            "output_tokens": 50,
            "status": "completed",
        },
    }


class AgentKernelContractTests(SimpleTestCase):
    def test_case_envelope_forbids_business_database_identifiers(self):
        payload = envelope_payload()
        payload["candidate_reference"]["candidate_id"] = 123

        with self.assertRaises(ValidationError):
            CaseEnvelopeV2.model_validate(payload)

    def test_policy_accepts_only_pinned_verifiable_proposal(self):
        envelope = CaseEnvelopeV2.model_validate(envelope_payload())
        proposal = AgentActionProposalV1.model_validate(proposal_payload())

        output = validate_proposal(envelope, proposal)

        self.assertEqual(output.decision.recommendation, "review")

    def test_policy_rejects_unverifiable_evidence(self):
        envelope = CaseEnvelopeV2.model_validate(envelope_payload())
        payload = proposal_payload()
        payload["evaluation"]["decision"]["evidence"] = ["简历中不存在的经历"]
        proposal = AgentActionProposalV1.model_validate(payload)

        with self.assertRaises(AIServiceError) as raised:
            validate_proposal(envelope, proposal)

        self.assertEqual(raised.exception.code, "agent_evidence_invalid")

    @override_settings(
        AGENT_KERNEL_BUILD="test-build",
        AGENT_KERNEL_TOKEN="kernel-token",
    )
    @patch("apps.pipeline.agent_kernel.client.httpx.get")
    def test_health_requires_matching_runtime_contract(self, mock_get):
        response = Mock()
        response.raise_for_status.return_value = None
        response.json.return_value = {
            "ok": True,
            "build": "test-build",
            "protocol_version": "resume-agent/v2",
            "toolset_version": "resume-tool-providers/v2",
            "result_schema_version": "resume-screening/v1",
            "instruction_version": "resume-screening-kernel/v1",
            "extra_health_field": "allowed",
            "task_protocol_version": "resume-analysis/v1",
            "task_toolset_version": "resume-job-match-tools/v1",
            "task_result_version": "resume-job-match/v1",
        }
        mock_get.return_value = response

        self.assertTrue(AgentKernelClient().is_ready())

        response.json.return_value["build"] = "stale-build"
        self.assertFalse(AgentKernelClient().is_ready())

    @override_settings(
        AGENT_KERNEL_MODE="remote",
        AGENT_KERNEL_BUILD="test-build",
        AGENT_KERNEL_TOKEN="kernel-token",
    )
    @patch("apps.pipeline.agent_kernel.gateway.AgentKernelClient.is_ready")
    @patch("apps.pipeline.agent_kernel.gateway.ai_config.is_ai_available")
    def test_remote_readiness_requires_model_and_kernel(
        self, mock_ai_available, mock_kernel_ready
    ):
        mock_ai_available.return_value = True
        mock_kernel_ready.return_value = False

        self.assertFalse(is_agent_ready())

    @override_settings(AGENT_KERNEL_TOKEN="kernel-token")
    @patch("apps.pipeline.agent_kernel.client.httpx.post")
    def test_model_key_is_forwarded_only_in_header(self, mock_post):
        response = Mock(status_code=200)
        response.json.return_value = proposal_payload()
        mock_post.return_value = response
        envelope = CaseEnvelopeV2.model_validate(envelope_payload())

        AgentKernelClient(base_url="http://kernel.test").evaluate(
            envelope,
            model_api_key="model-secret",
        )

        kwargs = mock_post.call_args.kwargs
        self.assertEqual(kwargs["headers"]["X-Model-API-Key"], "model-secret")
        self.assertNotIn("model-secret", str(kwargs["json"]))

    @override_settings(AGENT_KERNEL_TOKEN="kernel-token")
    @patch("apps.pipeline.agent_kernel.client.httpx.post")
    def test_kernel_failure_preserves_only_safe_trace(self, mock_post):
        response = Mock(status_code=422)
        response.json.return_value = {
            "ok": False,
            "code": "agent_evidence_invalid",
            "detail": "internal detail is ignored",
            "safe_trace": {
                "trace_id": "trace-failed",
                "status": "failed",
                "tool_calls": [],
            },
        }
        mock_post.return_value = response

        with self.assertRaises(AIServiceError) as raised:
            AgentKernelClient(base_url="http://kernel.test").evaluate(
                CaseEnvelopeV2.model_validate(envelope_payload())
            )

        self.assertEqual(raised.exception.code, "agent_evidence_invalid")
        self.assertEqual(raised.exception.safe_trace["trace_id"], "trace-failed")
        self.assertNotIn("internal detail", raised.exception.message)
