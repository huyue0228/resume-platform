from django.contrib.auth.models import Group
from django.test import TestCase
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults
from apps.core import models as m


class MajorDictionaryApiTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.client = APIClient()
        self.admin = User.objects.create_user(
            username="admin-major-dictionary",
            password="pass",
            role=User.ROLE_ADMIN,
        )
        self.admin.groups.add(Group.objects.get(name="管理员"))
        self.client.force_authenticate(self.admin)

    def test_major_category_crud_endpoint_creates_dictionary_category(self):
        response = self.client.post(
            "/api/major-categories/",
            {
                "code": "CS_SOFTWARE",
                "name": "计算机与软件类",
                "description": "计算机、软件、大数据、人工智能等方向",
                "is_active": True,
                "sort_order": 10,
            },
            format="json",
        )

        self.assertEqual(response.status_code, 201)
        self.assertEqual(response.data["code"], "CS_SOFTWARE")
        self.assertEqual(response.data["alias_count"], 0)
        self.assertTrue(
            m.MajorCategory.objects.filter(
                code="CS_SOFTWARE", name="计算机与软件类"
            ).exists()
        )

    def test_major_alias_normalizes_name_and_exposes_category_name(self):
        category = m.MajorCategory.objects.create(
            code="CS_SOFTWARE", name="计算机与软件类", sort_order=10
        )

        response = self.client.post(
            "/api/major-aliases/",
            {
                "category": category.id,
                "name": " 软件 工程 ",
                "match_type": m.MajorAlias.MATCH_EXACT,
                "source": m.MajorAlias.SOURCE_USER,
                "note": "人工补充",
                "is_active": True,
            },
            format="json",
        )

        self.assertEqual(response.status_code, 201)
        self.assertEqual(response.data["category_name"], "计算机与软件类")
        self.assertEqual(response.data["normalized_name"], "软件工程")
        alias = m.MajorAlias.objects.get()
        self.assertEqual(alias.normalized_name, "软件工程")

    def test_major_alias_rejects_duplicate_normalized_name_in_same_category_and_match_type(self):
        category = m.MajorCategory.objects.create(
            code="CS_SOFTWARE", name="计算机与软件类", sort_order=10
        )
        m.MajorAlias.objects.create(
            category=category,
            name="软件工程",
            normalized_name="软件工程",
            match_type=m.MajorAlias.MATCH_EXACT,
            source=m.MajorAlias.SOURCE_BUILTIN,
        )

        create_response = self.client.post(
            "/api/major-aliases/",
            {
                "category": category.id,
                "name": " 软件 工程 ",
                "match_type": m.MajorAlias.MATCH_EXACT,
                "source": m.MajorAlias.SOURCE_USER,
                "is_active": True,
            },
            format="json",
        )
        other = m.MajorAlias.objects.create(
            category=category,
            name="软件开发",
            normalized_name="软件开发",
            match_type=m.MajorAlias.MATCH_CONTAINS,
            source=m.MajorAlias.SOURCE_USER,
        )
        update_response = self.client.patch(
            f"/api/major-aliases/{other.id}/",
            {"name": "软件 工程", "match_type": m.MajorAlias.MATCH_EXACT},
            format="json",
        )

        self.assertEqual(create_response.status_code, 400)
        self.assertIn("已存在", str(create_response.data))
        self.assertEqual(update_response.status_code, 400)
        self.assertIn("已存在", str(update_response.data))
        self.assertEqual(m.MajorAlias.objects.count(), 2)

    def test_major_category_with_aliases_cannot_be_deleted_directly(self):
        category = m.MajorCategory.objects.create(
            code="CS_SOFTWARE", name="计算机与软件类", sort_order=10
        )
        m.MajorAlias.objects.create(
            category=category,
            name="软件工程",
            normalized_name="软件工程",
            match_type=m.MajorAlias.MATCH_EXACT,
            source=m.MajorAlias.SOURCE_BUILTIN,
        )

        response = self.client.delete(f"/api/major-categories/{category.id}/")

        self.assertEqual(response.status_code, 400)
        self.assertIn("需先删除或迁移别名", response.data["detail"])
        self.assertTrue(m.MajorCategory.objects.filter(id=category.id).exists())

    def test_major_dictionary_header_filters(self):
        category = m.MajorCategory.objects.create(
            code="CS_SOFTWARE",
            name="计算机与软件类",
            description="软件与人工智能方向",
            is_active=True,
        )
        alias = m.MajorAlias.objects.create(
            category=category,
            name="软件工程",
            normalized_name="软件工程",
            match_type=m.MajorAlias.MATCH_EXACT,
            source=m.MajorAlias.SOURCE_USER,
            note="人工补充",
            is_active=True,
        )

        category_response = self.client.get(
            "/api/major-categories/",
            {"code": "CS", "description": "人工智能", "is_active": "true"},
        )
        alias_response = self.client.get(
            "/api/major-aliases/",
            {
                "category": category.id,
                "name": "软件",
                "match_type": "exact",
                "source": "user",
                "note": "补充",
                "is_active": "true",
            },
        )

        self.assertEqual(category_response.status_code, 200)
        self.assertEqual([item["id"] for item in category_response.data["results"]], [category.id])
        self.assertEqual(alias_response.status_code, 200)
        self.assertEqual([item["id"] for item in alias_response.data["results"]], [alias.id])
