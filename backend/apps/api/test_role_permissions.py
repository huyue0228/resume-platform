from importlib import import_module

from django.apps import apps as django_apps
from django.contrib.auth.models import Group, Permission
from django.test import TestCase, override_settings
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import (
    ROLE_PERMISSION_CODES,
    ensure_rbac_defaults,
    permission_codename,
    user_permission_codes,
)


@override_settings(
    REST_FRAMEWORK={
        "DEFAULT_PERMISSION_CLASSES": [
            "rest_framework.permissions.IsAuthenticated"
        ],
        "DEFAULT_AUTHENTICATION_CLASSES": [
            "rest_framework.authentication.TokenAuthentication"
        ],
        "DEFAULT_PAGINATION_CLASS": "rest_framework.pagination.PageNumberPagination",
        "PAGE_SIZE": 20,
    }
)
class RolePermissionPersistenceTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.client = APIClient()
        self.admin = User.objects.create_user(
            username="role-admin",
            email="role-admin@example.com",
            role=User.ROLE_ADMIN,
            is_superuser=True,
        )
        self.client.force_authenticate(self.admin)

    def _run_department_permission_migrations(self):
        migration_0008 = import_module(
            "apps.accounts.migrations.0008_department_inbox_permissions"
        )
        migration_0009 = import_module(
            "apps.accounts.migrations.0009_restore_builtin_role_permissions"
        )
        migration_0008.migrate_department_inbox_permissions(django_apps, None)
        migration_0009.restore_builtin_role_permissions(django_apps, None)

    def test_permission_save_changes_effective_permissions_and_survives_reinitialization(self):
        role = Group.objects.get(name="HR")
        user = User.objects.create_user(
            username="custom-hr",
            email="custom-hr@example.com",
            role=User.ROLE_HR,
        )
        user.groups.add(role)
        selected_codes = ["resume.view", "settings.manage_ai_connection"]

        response = self.client.patch(
            f"/api/roles/{role.id}/",
            {"permission_codes": selected_codes},
            format="json",
        )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["permissions"], sorted(selected_codes))
        user = User.objects.get(pk=user.pk)
        self.assertEqual(user_permission_codes(user), set(selected_codes))

        ensure_rbac_defaults()
        role.refresh_from_db()
        self.assertEqual(
            {
                permission.codename.replace("__", ".")
                for permission in role.permissions.all()
            },
            set(selected_codes),
        )

    def test_permission_save_recreates_missing_registered_permission(self):
        role = Group.objects.get(name="HR")
        Permission.objects.filter(
            content_type__app_label="accounts",
            content_type__model="user",
            codename=permission_codename("settings.manage_ai_connection"),
        ).delete()

        response = self.client.patch(
            f"/api/roles/{role.id}/",
            {"permission_codes": ["settings.manage_ai_connection"]},
            format="json",
        )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(
            response.data["permissions"], ["settings.manage_ai_connection"]
        )

    def test_permission_save_rejects_unknown_code(self):
        role = Group.objects.get(name="HR")

        response = self.client.patch(
            f"/api/roles/{role.id}/",
            {"permission_codes": ["unknown.permission"]},
            format="json",
        )

        self.assertEqual(response.status_code, 400)
        self.assertIn("unknown.permission", str(response.data))

    def test_new_builtin_role_receives_complete_default_permissions(self):
        Group.objects.filter(name="三级接口人").delete()

        ensure_rbac_defaults()

        role = Group.objects.get(name="三级接口人")
        self.assertEqual(
            {
                permission.codename.replace("__", ".")
                for permission in role.permissions.all()
            },
            set(ROLE_PERMISSION_CODES["三级接口人"]),
        )

    def test_restore_migration_preserves_existing_role_customization(self):
        role = Group.objects.get(name="HR")
        selected_codes = {
            "resume.view",
            "attempt.transfer_department",
            "settings.manage_ai_connection",
        }
        role.permissions.set(
            Permission.objects.filter(
                content_type__app_label="accounts",
                content_type__model="user",
                codename__in=[permission_codename(code) for code in selected_codes],
            )
        )

        migration = import_module(
            "apps.accounts.migrations.0009_restore_builtin_role_permissions"
        )
        migration.restore_builtin_role_permissions(django_apps, None)

        self.assertEqual(
            {
                permission.codename.replace("__", ".")
                for permission in role.permissions.all()
            },
            selected_codes,
        )

    def test_department_permission_migrations_preserve_custom_reduction(self):
        role = Group.objects.get(name="HR")
        selected_codes = {"resume.view", "settings.manage_ai_connection"}
        role.permissions.set(
            Permission.objects.filter(
                content_type__app_label="accounts",
                content_type__model="user",
                codename__in=[permission_codename(code) for code in selected_codes],
            )
        )

        self._run_department_permission_migrations()

        self.assertEqual(
            {
                permission.codename.replace("__", ".")
                for permission in role.permissions.all()
            },
            selected_codes | {"attempt.transfer_department"},
        )

    def test_department_permission_migrations_upgrade_previous_role_defaults(self):
        content_type = Permission.objects.get(
            content_type__app_label="accounts",
            content_type__model="user",
            codename=permission_codename("attempt.export"),
        ).content_type
        current_defaults = {
            role_name: set(codes)
            for role_name, codes in ROLE_PERMISSION_CODES.items()
        }
        previous_defaults = {
            "管理员": current_defaults["管理员"]
            - {"attempt.view_department", "attempt.transfer_department"}
            | {
                "attempt.view_received",
                "attempt.view_assigned",
                "attempt.assign_sub_contact",
            },
            "HR": current_defaults["HR"] - {"attempt.transfer_department"},
            "二级接口人": {
                "attempt.view_received",
                "attempt.assign_sub_contact",
                "attempt.export",
            },
            "三级接口人": {
                "attempt.view_assigned",
                "attempt.feedback",
                "attempt.export",
            },
        }
        for role_name, codes in previous_defaults.items():
            previous_permissions = []
            for code in codes:
                codename = permission_codename(code)
                permission, _ = Permission.objects.get_or_create(
                    content_type=content_type,
                    codename=codename,
                    defaults={"name": codename},
                )
                previous_permissions.append(permission)
            Group.objects.get(name=role_name).permissions.set(previous_permissions)

        self._run_department_permission_migrations()

        for role_name, expected_codes in current_defaults.items():
            self.assertEqual(
                {
                    permission.codename.replace("__", ".")
                    for permission in Group.objects.get(
                        name=role_name
                    ).permissions.all()
                },
                expected_codes,
            )

    def test_restore_migration_repairs_department_inbox_only_bootstrap_role(self):
        role = Group.objects.get(name="HR")
        role.permissions.set(
            Permission.objects.filter(
                content_type__app_label="accounts",
                content_type__model="user",
                codename=permission_codename("attempt.transfer_department"),
            )
        )

        migration = import_module(
            "apps.accounts.migrations.0009_restore_builtin_role_permissions"
        )
        migration.restore_builtin_role_permissions(django_apps, None)

        self.assertEqual(
            {
                permission.codename.replace("__", ".")
                for permission in role.permissions.filter(
                    content_type__app_label="accounts",
                    content_type__model="user",
                )
            },
            set(ROLE_PERMISSION_CODES["HR"]),
        )

    def test_restore_migration_initializes_missing_builtin_role_defaults(self):
        Group.objects.filter(name="三级接口人").delete()

        migration = import_module(
            "apps.accounts.migrations.0009_restore_builtin_role_permissions"
        )
        migration.restore_builtin_role_permissions(django_apps, None)

        role = Group.objects.get(name="三级接口人")
        self.assertEqual(
            {
                permission.codename.replace("__", ".")
                for permission in role.permissions.all()
            },
            set(ROLE_PERMISSION_CODES["三级接口人"]),
        )
