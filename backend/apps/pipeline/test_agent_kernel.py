"""公开协议握手、安全失败与独立版本升级的客户端回归。"""
from unittest.mock import patch
import httpx
from django.test import SimpleTestCase, override_settings
from apps.pipeline.agent_kernel.client import AgentKernelClient
from apps.pipeline.agent_kernel.gateway import validate_runtime
from apps.pipeline.errors import AIServiceError
from resume_contracts.fixtures import capabilities_fixture

@override_settings(AGENT_KERNEL_TOKEN="kernel-token", AGENT_KERNEL_BUILD="", AGENT_KERNEL_ALLOW_MOCK=False)
class KernelCapabilitiesTests(SimpleTestCase):
    def response(self, payload=None, status=200):
        return httpx.Response(status, json=payload or {}, request=httpx.Request("GET", "http://kernel/v2/capabilities"))

    def test_future_internal_versions_do_not_require_platform_upgrade(self):
        caps = capabilities_fixture(kernel_build="release-2040", toolset_version="tools/v999", instruction_version="sha256:" + "f"*64)
        with patch("httpx.get", return_value=self.response(caps.model_dump())) as get:
            self.assertEqual(AgentKernelClient().capabilities(), caps)
        self.assertTrue(get.call_args.args[0].endswith("/v2/capabilities"))
        self.assertEqual(get.call_args.kwargs["headers"], {"X-Agent-Kernel-Token": "kernel-token"})

    def test_incompatible_public_protocol_is_rejected(self):
        for field in ("protocol_version", "result_schema_version"):
            payload = capabilities_fixture().model_dump()
            payload[field] = "incompatible/v999"
            with self.subTest(field=field), patch("httpx.get", return_value=self.response(payload)):
                with self.assertRaises(AIServiceError) as caught:
                    AgentKernelClient().capabilities()
                self.assertEqual(caught.exception.code, "agent_protocol_incompatible")

    def test_internal_version_cannot_be_blank(self):
        payload = capabilities_fixture().model_dump()
        payload["instruction_version"] = " "
        with patch("httpx.get", return_value=self.response(payload)):
            self.assertFalse(AgentKernelClient().is_ready())

    def test_auth_failure_never_exposes_server_body(self):
        with patch("httpx.get", return_value=self.response({"detail": "sk-secret"}, 401)):
            with self.assertRaises(AIServiceError) as caught:
                AgentKernelClient().capabilities()
        self.assertNotIn("sk-secret", str(caught.exception))

    @override_settings(AGENT_KERNEL_BUILD="pinned-build")
    def test_explicit_operator_build_lock_is_enforced(self):
        with patch("httpx.get", return_value=self.response(capabilities_fixture().model_dump())):
            self.assertFalse(AgentKernelClient().is_ready())

    def test_mock_cannot_be_used_as_production_kernel(self):
        with patch("httpx.get", return_value=self.response(capabilities_fixture(mock=True).model_dump())):
            self.assertFalse(AgentKernelClient().is_ready())

    @override_settings(AGENT_KERNEL_ROLLOUT="shadow")
    def test_old_rollout_is_not_supported(self):
        with self.assertRaises(AIServiceError):
            validate_runtime()

    def test_removed_runtime_cannot_be_imported(self):
        from importlib.util import find_spec
        self.assertIsNone(find_spec("apps.pipeline.ai.service"))
        self.assertIsNone(find_spec("apps.pipeline.agent_kernel.legacy_baseline"))
        self.assertIsNone(find_spec("apps.pipeline.agent_kernel.contracts"))

    def test_error_text_rejects_database_nul(self):
        self.assertEqual(AIServiceError("test", "bad" + chr(0) + "value").message, "badvalue")

    def test_execute_errors_are_safe_and_preserve_conflict_semantics(self):
        from types import SimpleNamespace
        from resume_contracts.fixtures import request_fixture
        envelope = SimpleNamespace(budget=SimpleNamespace(max_duration_seconds=600))
        for code in ("kernel_version_unavailable", "idempotency_conflict", "agent_budget_exhausted"):
            response = httpx.Response(409 if "conflict" in code or "version" in code else 422,
                json={"code": code, "detail": "secret-key"}, request=httpx.Request("POST", "http://kernel"))
            with self.subTest(code=code), patch("apps.pipeline.agent_kernel.wire.analysis_request", return_value=request_fixture()), patch("httpx.post", return_value=response) as post:
                with self.assertRaises(AIServiceError) as caught:
                    AgentKernelClient().execute(envelope, model_api_key="model-secret")
                self.assertEqual(caught.exception.code, code)
                self.assertNotIn("secret", str(caught.exception))
                self.assertNotIn("model-secret", str(post.call_args.kwargs["json"]))
