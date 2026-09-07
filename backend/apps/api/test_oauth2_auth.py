import base64
import hashlib
from unittest.mock import Mock, patch
from urllib.parse import parse_qs, urlparse

from django.contrib.auth.models import Group
from django.test import TestCase, override_settings
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults


OAUTH2_SETTINGS = {
    "W3_OAUTH2_ENABLED": True,
    "W3_OAUTH2_CLIENT_ID": "resume-client",
    "W3_OAUTH2_CLIENT_SECRET": "client-secret",
    "W3_OAUTH2_AUTHORIZE_URL": "https://w3.example.com/oauth2/authorize",
    "W3_OAUTH2_TOKEN_URL": "https://w3.example.com/oauth2/token",
    "W3_OAUTH2_USERINFO_URL": "https://w3.example.com/oauth2/userinfo",
    "W3_OAUTH2_REDIRECT_URI": "https://resume.example.com/api/auth/w3/callback/",
    "W3_OAUTH2_FRONTEND_CALLBACK_URL": "/login",
    "W3_OAUTH2_SCOPE": "profile employee_no email",
    "W3_OAUTH2_EMPLOYEE_NO_FIELD": "identity.employeeNo",
    "W3_OAUTH2_EMAIL_FIELD": "identity.email",
    "W3_OAUTH2_CLIENT_AUTH_METHOD": "client_secret_basic",
    "W3_OAUTH2_USE_PKCE": True,
    "W3_OAUTH2_TIMEOUT_SECONDS": 5,
    "W3_OAUTH2_TRANSACTION_TTL_SECONDS": 300,
}


@override_settings(**OAUTH2_SETTINGS)
class W3OAuth2ApiTests(TestCase):
    def setUp(self):
        self.client = APIClient()
        ensure_rbac_defaults()
        self.user = User.objects.create_user(
            username="E10001",
            email="e10001@example.com",
            password="local-pass",
            role=User.ROLE_HR,
        )
        self.user.groups.add(Group.objects.get(name="HR"))

    def _start(self):
        response = self.client.get("/api/auth/w3/start/")
        self.assertEqual(response.status_code, 302)
        params = parse_qs(urlparse(response.url).query)
        return response, params["state"][0]

    def test_status_exposes_only_login_capabilities(self):
        response = self.client.get("/api/auth/w3/status/")

        self.assertEqual(response.status_code, 200)
        self.assertEqual(
            response.data,
            {
                "enabled": True,
                "ready": True,
                "debug_token_login_enabled": False,
                "start_url": "/api/auth/w3/start/",
            },
        )
        self.assertNotIn("client_id", response.data)
        self.assertEqual(response["Cache-Control"], "no-store")

    @override_settings(DEBUG=True, W3_OAUTH2_ENABLED=False)
    def test_disabled_status_and_start_do_not_begin_authorization(self):
        status_response = self.client.get("/api/auth/w3/status/")
        start_response = self.client.get("/api/auth/w3/start/")

        self.assertFalse(status_response.data["enabled"])
        self.assertFalse(status_response.data["ready"])
        self.assertTrue(status_response.data["debug_token_login_enabled"])
        self.assertIsNone(status_response.data["start_url"])
        self.assertEqual(start_response.status_code, 503)

    @override_settings(DEBUG=False, W3_OAUTH2_ENABLED=False)
    def test_production_status_never_enables_debug_token_login(self):
        response = self.client.get("/api/auth/w3/status/")

        self.assertFalse(response.data["ready"])
        self.assertFalse(response.data["debug_token_login_enabled"])

    @override_settings(W3_OAUTH2_AUTHORIZE_URL="")
    def test_incomplete_configuration_is_not_reported_as_ready(self):
        response = self.client.get("/api/auth/w3/status/")

        self.assertTrue(response.data["enabled"])
        self.assertFalse(response.data["ready"])

    @override_settings(W3_OAUTH2_FRONTEND_CALLBACK_URL="https://other.example/login")
    def test_external_frontend_callback_is_rejected(self):
        status_response = self.client.get("/api/auth/w3/status/")
        start_response = self.client.get("/api/auth/w3/start/")

        self.assertFalse(status_response.data["ready"])
        self.assertEqual(start_response.status_code, 503)

    @override_settings(W3_OAUTH2_FRONTEND_CALLBACK_URL="/login?oauth2=success")
    def test_frontend_callback_cannot_preseed_reserved_status(self):
        response = self.client.get("/api/auth/w3/status/")

        self.assertFalse(response.data["ready"])

    @override_settings(
        W3_OAUTH2_REDIRECT_URI="https://resume.example.com/oauth/callback/"
    )
    def test_registered_redirect_uri_must_use_the_fixed_callback_path(self):
        response = self.client.get("/api/auth/w3/status/")

        self.assertFalse(response.data["ready"])

    def test_start_uses_exact_redirect_uri_state_and_s256_pkce(self):
        response, state_value = self._start()
        params = parse_qs(urlparse(response.url).query)
        transaction_data = self.client.session["w3_oauth2_transaction"]
        verifier = transaction_data["verifier"]
        expected_challenge = (
            base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest())
            .rstrip(b"=")
            .decode()
        )

        self.assertEqual(params["response_type"], ["code"])
        self.assertEqual(params["client_id"], ["resume-client"])
        self.assertEqual(
            params["redirect_uri"],
            ["https://resume.example.com/api/auth/w3/callback/"],
        )
        self.assertEqual(params["scope"], ["profile employee_no email"])
        self.assertEqual(params["state"], [state_value])
        self.assertEqual(params["code_challenge_method"], ["S256"])
        self.assertEqual(params["code_challenge"], [expected_challenge])
        self.assertEqual(response["Cache-Control"], "no-store")
        self.assertEqual(response["Referrer-Policy"], "no-referrer")

    @patch("apps.accounts.oauth2.httpx.get")
    @patch("apps.accounts.oauth2.httpx.post")
    def test_callback_maps_employee_number_and_complete_is_one_time(
        self, token_post, userinfo_get
    ):
        _, state_value = self._start()
        verifier = self.client.session["w3_oauth2_transaction"]["verifier"]
        token_post.return_value = self._json_response(
            {"access_token": "provider-access-token", "token_type": "Bearer"}
        )
        userinfo_get.return_value = self._json_response(
            {
                "identity": {
                    "employeeNo": "E10001",
                    "email": "E10001@EXAMPLE.COM",
                },
                "name": "测试用户",
            }
        )

        callback = self.client.get(
            "/api/auth/w3/callback/",
            {"code": "authorization-code", "state": state_value},
        )

        self.assertEqual(callback.status_code, 302)
        self.assertEqual(callback.url, "/login?oauth2=success")
        token_kwargs = token_post.call_args.kwargs
        self.assertEqual(token_kwargs["auth"], ("resume-client", "client-secret"))
        self.assertEqual(token_kwargs["data"]["code"], "authorization-code")
        self.assertEqual(token_kwargs["data"]["code_verifier"], verifier)
        self.assertEqual(
            token_kwargs["data"]["redirect_uri"],
            "https://resume.example.com/api/auth/w3/callback/",
        )
        self.assertEqual(
            userinfo_get.call_args.kwargs["params"],
            {
                "access_token": "provider-access-token",
                "scope": "profile employee_no email",
                "client_id": "resume-client",
            },
        )
        self.assertEqual(
            userinfo_get.call_args.kwargs["headers"],
            {"Accept": "application/json"},
        )

        complete = self.client.post("/api/auth/w3/complete/", {}, format="json")
        repeated = self.client.post("/api/auth/w3/complete/", {}, format="json")

        self.assertEqual(complete.status_code, 200)
        self.assertEqual(complete.data["user"]["username"], "E10001")
        self.assertIn("token", complete.data)
        self.assertEqual(complete["Cache-Control"], "no-store")
        self.assertEqual(repeated.status_code, 400)

    @override_settings(
        W3_OAUTH2_EMPLOYEE_NO_FIELD="employeeNumber",
        W3_OAUTH2_EMAIL_FIELD="email",
    )
    @patch("apps.accounts.oauth2.httpx.get")
    @patch("apps.accounts.oauth2.httpx.post")
    def test_callback_accepts_current_w3_userinfo_fields_and_preserves_leading_zero(
        self, token_post, userinfo_get
    ):
        _, state_value = self._start()
        token_post.return_value = self._json_response({"access_token": "token"})
        userinfo_get.return_value = self._json_response(
            {
                "tenantId": "tenant-1",
                "uuid": "account-uuid",
                "globalUserID": "global-user-id",
                "email": "HUYUE2@UEASCEND.COM",
                "employeeNumber": "012358",
            }
        )

        callback = self.client.get(
            "/api/auth/w3/callback/",
            {"code": "authorization-code", "state": state_value},
        )
        complete = self.client.post("/api/auth/w3/complete/", {}, format="json")

        self.assertEqual(callback.status_code, 302)
        self.assertEqual(callback.url, "/login?oauth2=success")
        self.assertEqual(complete.status_code, 200)
        self.assertEqual(complete.data["user"]["username"], "012358")
        self.assertEqual(
            complete.data["user"]["email"],
            "huyue2@ueascend.com",
        )

    @patch("apps.accounts.oauth2.httpx.get")
    @patch("apps.accounts.oauth2.httpx.post")
    def test_invalid_state_is_rejected_before_provider_calls(
        self, token_post, userinfo_get
    ):
        self._start()

        response = self.client.get(
            "/api/auth/w3/callback/", {"code": "code", "state": "wrong-state"}
        )

        self.assertEqual(response.status_code, 302)
        self.assertEqual(response.url, "/login?oauth2_error=state_invalid")
        token_post.assert_not_called()
        userinfo_get.assert_not_called()
        self.assertNotIn("w3_oauth2_transaction", self.client.session)

    @patch("apps.accounts.oauth2.httpx.get")
    @patch("apps.accounts.oauth2.httpx.post")
    def test_unknown_employee_number_does_not_create_account(
        self, token_post, userinfo_get
    ):
        _, state_value = self._start()
        token_post.return_value = self._json_response({"access_token": "token"})
        userinfo_get.return_value = self._json_response(
            {
                "identity": {
                    "employeeNo": "UNKNOWN",
                    "email": "unknown@example.com",
                }
            }
        )

        response = self.client.get(
            "/api/auth/w3/callback/", {"code": "code", "state": state_value}
        )

        self.assertEqual(response.status_code, 302)
        self.assertEqual(response.url, "/login?oauth2_error=account_not_found")
        self.assertFalse(User.objects.filter(username="UNKNOWN").exists())

    @patch("apps.accounts.oauth2.httpx.get")
    @patch("apps.accounts.oauth2.httpx.post")
    def test_matching_employee_number_with_wrong_email_is_rejected(
        self, token_post, userinfo_get
    ):
        _, state_value = self._start()
        token_post.return_value = self._json_response({"access_token": "token"})
        userinfo_get.return_value = self._json_response(
            {
                "identity": {
                    "employeeNo": "E10001",
                    "email": "other@example.com",
                }
            }
        )

        response = self.client.get(
            "/api/auth/w3/callback/", {"code": "code", "state": state_value}
        )

        self.assertEqual(response.url, "/login?oauth2_error=account_not_found")

    @patch("apps.accounts.oauth2.httpx.get")
    @patch("apps.accounts.oauth2.httpx.post")
    def test_missing_email_is_rejected_before_local_account_lookup(
        self, token_post, userinfo_get
    ):
        _, state_value = self._start()
        token_post.return_value = self._json_response({"access_token": "token"})
        userinfo_get.return_value = self._json_response(
            {"identity": {"employeeNo": "E10001"}}
        )

        response = self.client.get(
            "/api/auth/w3/callback/", {"code": "code", "state": state_value}
        )

        self.assertEqual(response.url, "/login?oauth2_error=email_missing")

    def test_local_password_login_and_django_admin_routes_are_removed(self):
        response = self.client.post(
            "/api/auth/login/",
            {"username": "E10001", "password": "local-pass"},
            format="json",
        )
        admin_response = self.client.get("/admin/")

        self.assertEqual(response.status_code, 404)
        self.assertEqual(admin_response.status_code, 404)

    @staticmethod
    def _json_response(payload):
        response = Mock()
        response.raise_for_status.return_value = None
        response.json.return_value = payload
        return response
