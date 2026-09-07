import importlib
from io import StringIO

from django.apps import apps as django_apps
from django.contrib.auth.hashers import make_password
from django.core.management import CommandError, call_command
from django.test import TestCase, override_settings
from rest_framework.authtoken.models import Token
from rest_framework.test import APIClient

from apps.accounts.models import User


class UserPasswordPolicyTests(TestCase):
    def test_create_user_and_set_password_keep_password_unusable(self):
        user = User.objects.create_user(username="password-policy", password="secret")

        self.assertFalse(user.has_usable_password())
        self.assertFalse(user.check_password("secret"))

        user.set_password("another-secret")
        user.save(update_fields=["password"])
        user.refresh_from_db()

        self.assertFalse(user.has_usable_password())
        self.assertFalse(user.check_password("another-secret"))

    def test_save_invalidates_directly_assigned_usable_hash(self):
        user = User(username="direct-password")
        user.password = make_password("secret")

        user.save()
        user.refresh_from_db()

        self.assertFalse(user.has_usable_password())

    def test_data_migration_invalidates_existing_hash_and_is_irreversible(self):
        user = User.objects.create(username="legacy-password")
        User.objects.filter(pk=user.pk).update(password=make_password("legacy-secret"))
        migration = importlib.import_module(
            "apps.accounts.migrations.0007_invalidate_user_passwords"
        )

        migration.invalidate_user_passwords(django_apps, None)
        user.refresh_from_db()

        self.assertFalse(user.has_usable_password())
        self.assertIsNone(migration.Migration.operations[0].reverse_code)


class IssueDevTokenCommandTests(TestCase):
    def setUp(self):
        self.user = User.objects.create_user(username="DEV100", is_active=True)

    @override_settings(DEBUG=True)
    def test_command_reissues_token_and_revokes_previous_token(self):
        old_token = Token.objects.create(user=self.user)
        stdout = StringIO()

        call_command("issue_dev_token", username=self.user.username, stdout=stdout)

        issued_key = stdout.getvalue().strip()
        self.assertTrue(issued_key)
        self.assertFalse(Token.objects.filter(key=old_token.key).exists())
        self.assertEqual(Token.objects.get(user=self.user).key, issued_key)
        client = APIClient()
        client.credentials(HTTP_AUTHORIZATION=f"Token {issued_key}")
        self.assertEqual(client.get("/api/me/").status_code, 200)

    @override_settings(DEBUG=False)
    def test_command_rejects_non_debug_environment(self):
        with self.assertRaisesMessage(CommandError, "仅允许在 DEBUG=True"):
            call_command("issue_dev_token", username=self.user.username)

    @override_settings(DEBUG=True)
    def test_command_rejects_missing_or_inactive_account(self):
        self.user.is_active = False
        self.user.save(update_fields=["is_active"])

        with self.assertRaisesMessage(CommandError, "账号已停用"):
            call_command("issue_dev_token", username=self.user.username)
        with self.assertRaisesMessage(CommandError, "账号不存在"):
            call_command("issue_dev_token", username="MISSING")
