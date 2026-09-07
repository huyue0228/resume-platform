from pathlib import Path
from tempfile import TemporaryDirectory
from io import BytesIO
import zipfile

from django.contrib.auth.models import Group
from django.test import TestCase
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults
from apps.core import models as m


class CandidateDeleteApiTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.client = APIClient()
        self.hr = User.objects.create_user(
            username="hr-candidate-delete",
            password="pass",
            role=User.ROLE_HR,
        )
        self.hr.groups.add(Group.objects.get(name="HR"))
        self.client.force_authenticate(self.hr)

    def _candidate_with_workflow(self, suffix):
        candidate = m.Candidate.objects.create(
            identity_hash=f"candidate-delete-{suffix}",
            name=f"候选人{suffix}",
            phone=f"1380000{suffix:04d}",
        )
        resume = m.Resume.objects.create(
            candidate=candidate,
            apply_id=f"DELETE-{suffix}",
            position_name="后端工程师",
            volunteer_rank=1,
        )
        workflow = m.CandidateWorkflow.objects.create(
            candidate=candidate,
            status=m.CandidateWorkflow.STATUS_IN_PROGRESS,
            current_resume=resume,
            current_rank=1,
        )
        department = m.Department.objects.create(
            name=f"删除测试部门{suffix}", level=2
        )
        workflow._test_department = department
        return candidate, resume, workflow

    def test_delete_candidate_without_protected_history(self):
        candidate, _, _ = self._candidate_with_workflow(1)

        response = self.client.delete(f"/api/candidates/{candidate.id}/")

        self.assertEqual(response.status_code, 204)
        self.assertFalse(m.Candidate.objects.filter(id=candidate.id).exists())

    def test_delete_candidate_with_assignment_attempt_returns_409(self):
        candidate, resume, workflow = self._candidate_with_workflow(2)
        attempt = m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=resume,
            attempt_no=1,
            source=m.AssignmentAttempt.SOURCE_RULE,
            status=m.AssignmentAttempt.STATUS_PENDING_DISPATCH,
            initial_department=workflow._test_department,
            current_department=workflow._test_department,
        )

        response = self.client.delete(f"/api/candidates/{candidate.id}/")

        self.assertEqual(response.status_code, 409)
        self.assertIn("分配尝试", response.data["detail"])
        self.assertTrue(m.Candidate.objects.filter(id=candidate.id).exists())
        self.assertTrue(m.AssignmentAttempt.objects.filter(id=attempt.id).exists())

    def test_delete_candidate_with_ai_decision_returns_409(self):
        candidate, resume, workflow = self._candidate_with_workflow(3)
        decision = m.AgentDispatchDecision.objects.create(
            workflow=workflow,
            resume=resume,
            recommendation=m.AgentDispatchDecision.RECOMMEND_REVIEW,
        )

        response = self.client.delete(f"/api/candidates/{candidate.id}/")

        self.assertEqual(response.status_code, 409)
        self.assertIn("AI 决策", response.data["detail"])
        self.assertTrue(m.Candidate.objects.filter(id=candidate.id).exists())
        self.assertTrue(m.AgentDispatchDecision.objects.filter(id=decision.id).exists())


class ContactCandidateExportApiTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.client = APIClient()
        department = m.Department.objects.create(name="导出测试部门", level=2)
        self.contact = m.Contact.objects.create(
            name="导出测试接口人",
            employee_no="EXPORT-L2",
            department=department,
            contact_level=m.Contact.LEVEL_SECONDARY,
        )
        self.user = User.objects.create_user(
            username="contact-candidate-export",
            password="pass",
            role=User.ROLE_SECONDARY_CONTACT,
            contact=self.contact,
        )
        self.user.groups.add(Group.objects.get(name="二级接口人"))
        self.client.force_authenticate(self.user)

    def _visible_candidate(self, suffix, name, filename):
        candidate = m.Candidate.objects.create(
            identity_hash=f"contact-export-{suffix}",
            name=name,
            phone=f"1390000{suffix:04d}",
        )
        resume = m.Resume.objects.create(
            candidate=candidate,
            apply_id=f"CONTACT-EXPORT-{suffix}",
            position_name="测试岗位",
            volunteer_rank=1,
            resume_file=filename,
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
            initial_department=self.contact.department,
            current_department=self.contact.department,
        )
        return candidate

    def test_contact_selected_export_respects_candidate_ids(self):
        with TemporaryDirectory() as media_root:
            resume_dir = Path(media_root) / "resumes"
            resume_dir.mkdir()
            (resume_dir / "候选人甲.txt").write_text("selected resume", encoding="utf-8")
            (resume_dir / "候选人乙.txt").write_text("other resume", encoding="utf-8")
            selected = self._visible_candidate(1, "候选人甲", "候选人甲.txt")
            self._visible_candidate(2, "候选人乙", "候选人乙.txt")

            with self.settings(MEDIA_ROOT=media_root):
                response = self.client.get(
                    "/api/candidates/export/", {"ids": str(selected.id)}
                )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response["X-Export-Count"], "1")
        self.assertEqual(response["X-Export-Candidate-Count"], "1")
        with zipfile.ZipFile(BytesIO(response.content)) as archive:
            self.assertEqual(
                archive.read("简历文件/候选人甲.txt").decode("utf-8"),
                "selected resume",
            )

    def test_contact_filtered_export_respects_candidate_filters(self):
        with TemporaryDirectory() as media_root:
            resume_dir = Path(media_root) / "resumes"
            resume_dir.mkdir()
            (resume_dir / "候选人甲.txt").write_text("filtered resume", encoding="utf-8")
            (resume_dir / "候选人乙.txt").write_text("other resume", encoding="utf-8")
            self._visible_candidate(3, "候选人甲", "候选人甲.txt")
            self._visible_candidate(4, "候选人乙", "候选人乙.txt")

            with self.settings(MEDIA_ROOT=media_root):
                response = self.client.get(
                    "/api/candidates/export/", {"name": "候选人甲"}
                )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response["X-Export-Count"], "1")
        self.assertEqual(response["X-Export-Candidate-Count"], "1")
        with zipfile.ZipFile(BytesIO(response.content)) as archive:
            self.assertEqual(
                archive.read("简历文件/候选人甲.txt").decode("utf-8"),
                "filtered resume",
            )
