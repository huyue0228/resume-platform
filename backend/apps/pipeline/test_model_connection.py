"""平台连接探测回归；不再维护 Python 简历筛选基线。"""
from dataclasses import replace
from types import SimpleNamespace
from unittest.mock import Mock, patch
from django.test import TestCase
from apps.core import models as m
from apps.pipeline import ai_config
from apps.pipeline.ai import model_connection as service
from apps.pipeline.ai.schemas import ConnectionProbeOutput


class ModelConnectionTests(TestCase):
    def setUp(self):
        service.close_cached_ai_clients()
        self.addCleanup(service.close_cached_ai_clients)
        ai_config.save_ai_connection_config(dict(api_style="responses", model_name="test", base_url="https://model.internal/v1", api_key="test-key"))

    def test_openai_client_disables_ssl_verification(self):
        http_client = Mock()
        client = SimpleNamespace(
            responses=SimpleNamespace(
                parse=Mock(
                    return_value=SimpleNamespace(output_parsed=ConnectionProbeOutput(ok=True))
                )
            )
        )

        with patch.object(service.httpx, "Client", return_value=http_client) as httpx_client, patch(
            "openai.OpenAI", return_value=client
        ) as openai_client:
            service.test_model_connection()

        httpx_client.assert_called_once_with(verify=False)
        self.assertIs(openai_client.call_args.kwargs["http_client"], http_client)

    def test_openai_client_is_reused_for_same_worker_connection(self):
        http_client = Mock()
        parse = Mock(
            return_value=SimpleNamespace(output_parsed=ConnectionProbeOutput(ok=True))
        )
        client = SimpleNamespace(
            responses=SimpleNamespace(parse=parse)
        )

        with patch.object(service.httpx, "Client", return_value=http_client) as httpx_client, patch(
            "openai.OpenAI", return_value=client
        ) as openai_client:
            service.test_model_connection()
            service.test_model_connection()

        httpx_client.assert_called_once_with(verify=False)
        openai_client.assert_called_once()
        self.assertEqual(parse.call_count, 2)

    def test_client_cache_key_tracks_connection_but_not_model_name(self):
        model_config = ai_config.get_ai_model_config()
        runtime_config = ai_config.get_ai_runtime_config()
        original = service._client_cache_key(model_config, runtime_config)

        self.assertEqual(
            service._client_cache_key(
                replace(model_config, model_name="another-model"), runtime_config
            ),
            original,
        )
        for changed in [
            replace(model_config, api_style="chat_json"),
            replace(model_config, base_url="https://another-model.internal/v1"),
            replace(model_config, api_key="another-key"),
        ]:
            self.assertNotEqual(
                service._client_cache_key(changed, runtime_config), original
            )
        self.assertNotEqual(
            service._client_cache_key(
                model_config, replace(runtime_config, timeout_seconds=120)
            ),
            original,
        )

    def test_cached_clients_are_closed_and_cleared(self):
        client = Mock()
        http_client = Mock()
        service._CLIENT_CACHE["test"] = (client, http_client)

        service.close_cached_ai_clients()

        client.close.assert_called_once_with()
        self.assertEqual(service._CLIENT_CACHE, {})

    def test_model_discovery_returns_sorted_model_ids_without_auth_header(self):
        response = Mock()
        response.raise_for_status.return_value = None
        response.json.return_value = {
            "data": [{"id": "glm-4.7"}, {"id": "deepseek-v4"}, {"id": "glm-4.7"}]
        }
        m.Config.objects.filter(key="ai_connection_api_key").delete()

        with patch.object(service.httpx, "get", return_value=response) as get:
            models = service.list_available_models(base_url="https://model.internal/v1")

        self.assertEqual(models, ["deepseek-v4", "glm-4.7"])
        self.assertNotIn("Authorization", get.call_args.kwargs["headers"])
        self.assertEqual(get.call_args.args[0], "https://model.internal/v1/models")

    def test_model_discovery_uses_new_token_without_returning_it(self):
        response = Mock()
        response.raise_for_status.return_value = None
        response.json.return_value = {"data": [{"id": "deepseek-v4"}]}

        with patch.object(service.httpx, "get", return_value=response) as get:
            models = service.list_available_models(
                base_url="https://model.internal/v1/", api_key="new-secret"
            )

        self.assertEqual(models, ["deepseek-v4"])
        self.assertEqual(
            get.call_args.kwargs["headers"]["Authorization"], "Bearer new-secret"
        )

    def test_model_discovery_reuses_saved_token_when_new_token_is_blank(self):
        response = Mock()
        response.raise_for_status.return_value = None
        response.json.return_value = {"data": [{"id": "glm-4.7"}]}

        with patch.object(service.httpx, "get", return_value=response) as get:
            models = service.list_available_models(
                base_url="https://model.internal/v1", api_key="   "
            )

        self.assertEqual(models, ["glm-4.7"])
        self.assertEqual(
            get.call_args.kwargs["headers"]["Authorization"], "Bearer test-key"
        )

    def test_model_discovery_never_forwards_saved_token_to_changed_base_url(self):
        response = Mock()
        response.raise_for_status.return_value = None
        response.json.return_value = {"data": [{"id": "deepseek-v4"}]}

        with patch.object(service.httpx, "get", return_value=response) as get:
            models = service.list_available_models(
                base_url="https://another-model.internal/v1", api_key=""
            )

        self.assertEqual(models, ["deepseek-v4"])
        self.assertNotIn("Authorization", get.call_args.kwargs["headers"])

    def test_model_discovery_maps_http_auth_failure_to_safe_message(self):
        request = service.httpx.Request("GET", "https://model.internal/v1/models")
        response = service.httpx.Response(401, request=request)
        error = service.httpx.HTTPStatusError(
            "Authorization: Bearer secret", request=request, response=response
        )

        with patch.object(service.httpx, "get", side_effect=error):
            with self.assertRaises(service.AIServiceError) as captured:
                service.list_available_models(base_url="https://model.internal/v1")

        self.assertEqual(captured.exception.code, "ai_connection_error")
        self.assertIn("认证失败", captured.exception.message)
        self.assertNotIn("secret", captured.exception.message)
