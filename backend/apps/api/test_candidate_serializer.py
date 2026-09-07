from types import SimpleNamespace
from unittest.mock import patch

from django.contrib.auth.models import Group
from django.db import connection
from django.test import TestCase
from django.test.utils import CaptureQueriesContext
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults, user_permission_codes
from apps.api import serializers
from apps.core import models as m


class CandidateSerializerCachingTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.department = m.Department.objects.create(name="序列化测试部门", level=2)
        self.contact = m.Contact.objects.create(
            name="序列化测试接口人",
            employee_no="SERIALIZER001",
            department=self.department,
            contact_level=m.Contact.LEVEL_SECONDARY,
        )
        self.user = User.objects.create_user(
            username="SERIALIZER001",
            password="pass",
            role=User.ROLE_SECONDARY_CONTACT,
            contact=self.contact,
        )
        self.user.groups.add(Group.objects.get(name="二级接口人"))
        self.client = APIClient()
        self.client.force_authenticate(self.user)
        self.candidate = self._create_visible_candidate("one")

    def _create_visible_candidate(self, code):
        candidate = m.Candidate.objects.create(
            identity_hash=f"candidate-serializer-cache-{code}",
            name=f"序列化候选人{code}",
            phone=f"1380000{len(code):04d}",
        )
        resume = m.Resume.objects.create(
            candidate=candidate,
            apply_id=f"SERIALIZER-APPLY-{code}",
            position_name="测试岗位",
            volunteer_rank=1,
        )
        workflow = m.CandidateWorkflow.objects.create(
            candidate=candidate,
            status=m.CandidateWorkflow.STATUS_IN_PROGRESS,
            current_resume=resume,
            current_rank=1,
        )
        m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=resume,
            attempt_no=1,
            source=m.AssignmentAttempt.SOURCE_RULE,
            status=m.AssignmentAttempt.STATUS_DISPATCHED,
            initial_department=self.department,
            current_department=self.department,
        )
        return candidate

    def test_reuses_permissions_and_visible_attempt_across_candidate_fields(self):
        request = SimpleNamespace(user=self.user)
        with (
            patch(
                "apps.api.serializers.user_permission_codes",
                wraps=serializers.user_permission_codes,
            ) as permission_codes,
            patch(
                "apps.api.serializers.visible_candidate_attempt",
                wraps=serializers.visible_candidate_attempt,
            ) as visible_attempt,
        ):
            data = serializers.CandidateSerializer(
                self.candidate, context={"request": request}
            ).data

        self.assertEqual(data["phone"], "")
        self.assertEqual(data["current_attempt"]["status"], "dispatched")
        self.assertEqual(permission_codes.call_count, 1)
        self.assertEqual(visible_attempt.call_count, 1)

    def test_candidate_list_query_count_does_not_scale_with_rows(self):
        user_permission_codes(self.user)

        with CaptureQueriesContext(connection) as first_queries:
            first_response = self.client.get("/api/candidates/")
        self.assertEqual(first_response.status_code, 200)

        self._create_visible_candidate("two")
        with CaptureQueriesContext(connection) as second_queries:
            second_response = self.client.get("/api/candidates/")

        self.assertEqual(second_response.status_code, 200)
        self.assertEqual(second_response.data["count"], 2)
        self.assertLessEqual(len(second_queries), len(first_queries))

    def test_full_candidate_list_query_count_does_not_scale_with_rows(self):
        admin = User.objects.create_user(
            username="serializer-admin", password="pass", role=User.ROLE_ADMIN
        )
        admin.groups.add(Group.objects.get(name="管理员"))
        user_permission_codes(admin)
        self.client.force_authenticate(admin)

        with CaptureQueriesContext(connection) as first_queries:
            first_response = self.client.get("/api/candidates/")
        self.assertEqual(first_response.status_code, 200)

        self._create_visible_candidate("full-two")
        with CaptureQueriesContext(connection) as second_queries:
            second_response = self.client.get("/api/candidates/")

        self.assertEqual(second_response.status_code, 200)
        self.assertEqual(second_response.data["count"], 2)
        self.assertLessEqual(len(second_queries), len(first_queries))
