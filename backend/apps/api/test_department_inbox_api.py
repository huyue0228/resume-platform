from django.contrib.auth.models import Group
from django.test import TestCase
from rest_framework.test import APIClient
from unittest.mock import patch

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults
from apps.core import models as m


class DepartmentInboxApiTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.primary_a = m.Department.objects.create(name="一级甲", level=1)
        self.secondary_a = m.Department.objects.create(
            name="二级甲", level=2, parent=self.primary_a
        )
        self.secondary_a_other = m.Department.objects.create(
            name="二级甲二", level=2, parent=self.primary_a
        )
        self.tertiary_a = m.Department.objects.create(
            name="三级甲", level=3, parent=self.secondary_a
        )
        self.tertiary_a_other = m.Department.objects.create(
            name="三级甲二", level=3, parent=self.secondary_a
        )
        self.primary_b = m.Department.objects.create(name="一级乙", level=1)
        self.secondary_b = m.Department.objects.create(
            name="二级乙", level=2, parent=self.primary_b
        )
        self.tertiary_b = m.Department.objects.create(
            name="三级乙", level=3, parent=self.secondary_b
        )

        self.secondary_contact = self._contact(
            "L2-A", self.secondary_a, m.Contact.LEVEL_SECONDARY
        )
        self.secondary_peer = self._contact(
            "L2-A-PEER", self.secondary_a, m.Contact.LEVEL_SECONDARY
        )
        self.tertiary_contact = self._contact(
            "L3-A", self.tertiary_a, m.Contact.LEVEL_TERTIARY
        )
        self.secondary_user = self._user(
            self.secondary_contact, "二级接口人"
        )
        self.secondary_peer_user = self._user(
            self.secondary_peer, "二级接口人"
        )
        self.tertiary_user = self._user(self.tertiary_contact, "三级接口人")
        self.hr = User.objects.create_user(username="DEPT-HR", password="pass")
        self.hr.groups.add(Group.objects.get(name="HR"))
        self.client = APIClient()

    def _contact(self, employee_no, department, level):
        return m.Contact.objects.create(
            name=employee_no,
            employee_no=employee_no,
            email=f"{employee_no.lower()}@example.com",
            department=department,
            contact_level=level,
            is_active=True,
        )

    def _user(self, contact, group_name):
        user = User.objects.create_user(
            username=contact.employee_no,
            password="pass",
            contact=contact,
        )
        user.groups.add(Group.objects.get(name=group_name))
        return user

    def _attempt(self, suffix, current_department, status=None):
        candidate = m.Candidate.objects.create(
            identity_hash=f"department-inbox-{suffix}",
            name=f"候选人{suffix}",
            phone=f"138{int(suffix):08d}",
        )
        resume = m.Resume.objects.create(
            candidate=candidate,
            apply_id=f"DEPT-{suffix}",
            position_name="研发工程师",
            volunteer_rank=1,
        )
        workflow = m.CandidateWorkflow.objects.create(
            candidate=candidate,
            status=m.CandidateWorkflow.STATUS_IN_PROGRESS,
            current_resume=resume,
            current_rank=1,
        )
        hierarchy_department = (
            current_department.parent
            if current_department.level == 3
            else current_department
        )
        attempt = m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=resume,
            attempt_no=1,
            source=m.AssignmentAttempt.SOURCE_RULE,
            status=status or m.AssignmentAttempt.STATUS_DISPATCHED,
            initial_department=hierarchy_department,
            current_department=current_department,
        )
        return candidate, attempt

    def _candidate_ids(self, user):
        self.client.force_authenticate(user)
        response = self.client.get("/api/candidates/")
        self.assertEqual(response.status_code, 200)
        return {item["id"] for item in response.data["results"]}

    def test_department_visibility_is_shared_and_pending_is_hidden(self):
        own, _ = self._attempt("1", self.secondary_a)
        child, _ = self._attempt("2", self.tertiary_a)
        sibling_child, _ = self._attempt("3", self.tertiary_a_other)
        foreign, _ = self._attempt("4", self.secondary_b)
        pending, _ = self._attempt(
            "5", self.secondary_a, m.AssignmentAttempt.STATUS_PENDING_DISPATCH
        )

        expected_secondary = {own.id, child.id, sibling_child.id}
        self.assertEqual(self._candidate_ids(self.secondary_user), expected_secondary)
        self.assertEqual(
            self._candidate_ids(self.secondary_peer_user), expected_secondary
        )
        self.assertEqual(self._candidate_ids(self.tertiary_user), {child.id})
        self.assertNotIn(foreign.id, expected_secondary)
        self.assertNotIn(pending.id, expected_secondary)

    def test_cross_primary_transfer_allows_department_without_contacts(self):
        candidate, attempt = self._attempt("10", self.secondary_a)
        self.client.force_authenticate(self.secondary_user)

        response = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/transfer/",
            {"target_department_id": self.secondary_b.id, "note": "跨一级转派"},
            format="json",
        )

        self.assertEqual(response.status_code, 200)
        attempt.refresh_from_db()
        self.assertEqual(attempt.current_department_id, self.secondary_b.id)
        event = attempt.handling_events.get(
            event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_TRANSFERRED
        )
        self.assertEqual(event.from_department_id, self.secondary_a.id)
        self.assertEqual(event.to_department_id, self.secondary_b.id)
        self.assertEqual(event.actor_id, self.secondary_user.id)
        self.assertNotIn(candidate.id, self._candidate_ids(self.secondary_user))

        new_contact = self._contact(
            "L2-B-LATE", self.secondary_b, m.Contact.LEVEL_SECONDARY
        )
        new_user = self._user(new_contact, "二级接口人")
        self.assertIn(candidate.id, self._candidate_ids(new_user))

    def test_transfer_options_follow_actor_scope(self):
        _, attempt = self._attempt("20", self.secondary_a)
        self.client.force_authenticate(self.secondary_user)
        response = self.client.get(
            f"/api/workflow-attempts/{attempt.id}/transfer-options/"
        )
        self.assertEqual(response.status_code, 200)
        option_ids = {item["id"] for item in response.data["results"]}
        self.assertIn(self.secondary_b.id, option_ids)
        self.assertIn(self.tertiary_a.id, option_ids)
        self.assertNotIn(self.tertiary_b.id, option_ids)

        self.client.force_authenticate(self.hr)
        response = self.client.get(
            f"/api/workflow-attempts/{attempt.id}/transfer-options/"
        )
        self.assertEqual(response.status_code, 200)
        self.assertIn(
            self.tertiary_b.id,
            {item["id"] for item in response.data["results"]},
        )

    def test_department_with_children_cannot_be_deleted_or_reparented(self):
        self.client.force_authenticate(self.hr)

        delete_response = self.client.delete(
            f"/api/departments/{self.secondary_a.id}/"
        )
        self.assertEqual(delete_response.status_code, 400)
        self.assertEqual(delete_response.data["detail"], "存在下级部门不可删除")

        update_response = self.client.patch(
            f"/api/departments/{self.secondary_a.id}/",
            {"parent": self.primary_b.id},
            format="json",
        )
        self.assertEqual(update_response.status_code, 400)
        self.assertIn("detail", update_response.data)
        self.secondary_a.refresh_from_db()
        self.assertEqual(self.secondary_a.parent_id, self.primary_a.id)

    def test_referenced_department_cannot_change_structure_or_be_deleted(self):
        _, attempt = self._attempt("21", self.secondary_a_other)
        self.client.force_authenticate(self.hr)

        update_response = self.client.patch(
            f"/api/departments/{self.secondary_a_other.id}/",
            {"parent": self.primary_b.id},
            format="json",
        )
        self.assertEqual(update_response.status_code, 400)
        self.assertIn("detail", update_response.data)

        delete_response = self.client.delete(
            f"/api/departments/{self.secondary_a_other.id}/"
        )
        self.assertEqual(delete_response.status_code, 400)
        self.assertEqual(delete_response.data["detail"], "部门已有业务引用不可删除")
        self.assertTrue(m.AssignmentAttempt.objects.filter(pk=attempt.pk).exists())

    def test_secondary_contact_without_delegate_flag_cannot_transfer(self):
        _, attempt = self._attempt("21", self.secondary_a)
        self.secondary_contact.can_delegate = False
        self.secondary_contact.save(update_fields=["can_delegate"])
        self.client.force_authenticate(self.secondary_user)

        options = self.client.get(
            f"/api/workflow-attempts/{attempt.id}/transfer-options/"
        )
        transfer = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/transfer/",
            {"target_department_id": self.secondary_b.id},
            format="json",
        )
        bulk = self.client.post(
            "/api/candidates/bulk-transfer/",
            {
                "candidate_ids": [attempt.workflow.candidate_id],
                "target_department_id": self.secondary_b.id,
            },
            format="json",
        )
        self.assertEqual(options.status_code, 403)
        self.assertEqual(transfer.status_code, 403)
        self.assertEqual(bulk.status_code, 403)

    def test_feedback_requires_exact_department_and_structured_reason(self):
        _, attempt = self._attempt("30", self.tertiary_a)
        self.client.force_authenticate(self.secondary_user)
        response = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/feedback/",
            {"result": "rejected", "reason_code": "major_background_mismatch"},
            format="json",
        )
        self.assertEqual(response.status_code, 400)
        self.assertIn("当前接收部门", response.data["detail"])

        self.client.force_authenticate(self.tertiary_user)
        response = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/feedback/",
            {"result": "rejected", "reason_code": "other"},
            format="json",
        )
        self.assertEqual(response.status_code, 400)
        self.assertIn("必须填写备注", response.data["detail"])

        response = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/feedback/",
            {"result": "rejected", "reason_code": "other", "note": "补充原因"},
            format="json",
        )
        self.assertEqual(response.status_code, 200)
        attempt.refresh_from_db()
        self.assertEqual(attempt.feedback_reason_code, "other")
        self.assertEqual(attempt.feedback_reason_label_snapshot, "其他")

        duplicate = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/feedback/",
            {"result": "passed"},
            format="json",
        )
        self.assertEqual(duplicate.status_code, 409)

    def test_feedback_validation_for_passed_and_fixed_rejection_reason(self):
        _, passed_attempt = self._attempt("31", self.tertiary_a)
        self.client.force_authenticate(self.tertiary_user)
        invalid_passed = self.client.post(
            f"/api/workflow-attempts/{passed_attempt.id}/feedback/",
            {"result": "passed", "reason_code": "major_background_mismatch"},
            format="json",
        )
        self.assertEqual(invalid_passed.status_code, 400)
        self.assertIn("通过反馈不能填写", invalid_passed.data["detail"])

        _, rejected_attempt = self._attempt("32", self.tertiary_a)
        rejected = self.client.post(
            f"/api/workflow-attempts/{rejected_attempt.id}/feedback/",
            {"result": "rejected", "reason_code": "major_background_mismatch"},
            format="json",
        )
        self.assertEqual(rejected.status_code, 200)
        rejected_attempt.refresh_from_db()
        self.assertEqual(
            rejected_attempt.feedback_reason_label_snapshot,
            "专业背景不匹配",
        )

    def test_feedback_reasons_and_handling_events_are_read_only(self):
        _, attempt = self._attempt("40", self.secondary_a)
        m.AssignmentHandlingEvent.objects.create(
            attempt=attempt,
            event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED,
            to_department=self.secondary_a,
            actor=self.hr,
        )
        self.client.force_authenticate(self.secondary_user)
        reasons = self.client.get("/api/workflow-attempts/feedback-reasons/")
        self.assertEqual(reasons.status_code, 200)
        self.assertEqual(len(reasons.data["results"]), 6)
        self.assertIn(
            {"value": "key_capability_mismatch", "label": "关键能力不匹配"},
            reasons.data["results"],
        )
        events = self.client.get(
            f"/api/workflow-attempts/{attempt.id}/handling-events/"
        )
        self.assertEqual(events.status_code, 200)
        self.assertEqual(events.data["results"][0]["event_type"], "department_dispatched")
        self.assertIn("duration_since_previous_seconds", events.data["results"][0])

    def test_bulk_transfer_keeps_duplicate_input_as_skipped(self):
        candidate, attempt = self._attempt("50", self.secondary_a)
        terminal, terminal_attempt = self._attempt(
            "51", self.secondary_a, m.AssignmentAttempt.STATUS_PASSED
        )
        self.client.force_authenticate(self.secondary_user)
        response = self.client.post(
            "/api/candidates/bulk-transfer/",
            {
                "candidate_ids": [candidate.id, candidate.id, terminal.id],
                "target_department_id": self.secondary_b.id,
                "note": "批量转派",
            },
            format="json",
        )
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["total"], 3)
        self.assertEqual(response.data["transferred"], 1)
        self.assertEqual(response.data["skipped"], 2)
        self.assertEqual(response.data["failed"], 0)
        self.assertTrue(response.data["batch_operation_id"])
        attempt.refresh_from_db()
        self.assertEqual(attempt.current_department_id, self.secondary_b.id)
        self.assertEqual(terminal_attempt.current_department_id, self.secondary_a.id)

    def test_me_exposes_department_scope(self):
        self.client.force_authenticate(self.secondary_user)
        response = self.client.get("/api/me/")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["data_scope"]["type"], "department")
        self.assertEqual(
            response.data["data_scope"]["department_id"], self.secondary_a.id
        )
        self.assertTrue(response.data["data_scope"]["include_descendants"])
        self.assertEqual(
            set(response.data["data_scope"]["department_ids"]),
            {self.secondary_a.id, self.tertiary_a.id, self.tertiary_a_other.id},
        )

    def test_current_department_filters_accept_multiple_ids(self):
        candidate_a, _ = self._attempt("60", self.tertiary_a)
        candidate_a_other, _ = self._attempt("61", self.secondary_a_other)
        candidate_b, _ = self._attempt("62", self.secondary_b)
        self.client.force_authenticate(self.hr)

        response = self.client.get(
            "/api/candidates/",
            {
                "current_department_id": (
                    f"{self.tertiary_a.id},{self.secondary_b.id}"
                )
            },
        )
        self.assertEqual(response.status_code, 200)
        self.assertEqual(
            {item["id"] for item in response.data["results"]},
            {candidate_a.id, candidate_b.id},
        )

        response = self.client.get(
            "/api/candidates/",
            {
                "current_primary_department_id": (
                    f"{self.primary_a.id},{self.primary_b.id}"
                )
            },
        )
        self.assertEqual(response.status_code, 200)
        self.assertEqual(
            {item["id"] for item in response.data["results"]},
            {candidate_a.id, candidate_a_other.id, candidate_b.id},
        )

        options = self.client.get("/api/candidates/filter-options/")
        self.assertEqual(options.status_code, 200)
        self.assertIn(
            self.primary_a.id,
            {
                item["value"]
                for item in options.data["current_primary_department"]
            },
        )

    def test_historical_department_cannot_infer_later_application(self):
        candidate, old_attempt = self._attempt(
            "70", self.secondary_a, m.AssignmentAttempt.STATUS_REJECTED
        )
        old_job = m.Job.objects.create(
            entity="旧主体",
            department=self.secondary_a,
            category="旧类别",
            public_name="旧岗位",
            position_name="旧岗位",
        )
        old_attempt.resume.job = old_job
        old_attempt.resume.entity = "旧主体"
        old_attempt.resume.position_name = "旧岗位"
        old_attempt.resume.job_category = "旧类别"
        old_attempt.resume.save(
            update_fields=["job", "entity", "position_name", "job_category"]
        )
        new_job = m.Job.objects.create(
            entity="秘密主体",
            department=self.secondary_b,
            category="秘密类别",
            public_name="秘密岗位",
            position_name="秘密岗位",
        )
        new_resume = m.Resume.objects.create(
            candidate=candidate,
            apply_id="SECRET-NEW-APPLY",
            entity="秘密主体",
            position_name="秘密岗位",
            job_category="秘密类别",
            volunteer_rank=2,
            job=new_job,
        )
        workflow = old_attempt.workflow
        new_attempt = m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=new_resume,
            attempt_no=2,
            source=m.AssignmentAttempt.SOURCE_AI,
            status=m.AssignmentAttempt.STATUS_PASSED,
            initial_department=self.secondary_b,
            current_department=self.secondary_b,
            feedback_reason_code="key_capability_mismatch",
            feedback_reason_label_snapshot="关键能力不匹配",
        )
        workflow.current_resume = new_resume
        workflow.current_rank = 2
        workflow.status = m.CandidateWorkflow.STATUS_PASSED
        workflow.passed_attempt = new_attempt
        workflow.archive_reason = m.CandidateWorkflow.ARCHIVE_ALL_REJECTED
        workflow.archive_detail = "秘密归档详情"
        workflow.save(
            update_fields=[
                "current_resume",
                "current_rank",
                "status",
                "passed_attempt",
                "archive_reason",
                "archive_detail",
                "updated_at",
            ]
        )
        run = m.ProcessingRun.objects.create(step="step2", status="completed")
        m.ProcessingRunScopeItem.objects.create(
            run=run,
            candidate=candidate,
            status="success",
            result_type=m.ProcessingRunScopeItem.RESULT_COMPLETED,
            reason_code="secret_processing_reason",
            result_message="秘密处理结果",
        )

        self.client.force_authenticate(self.secondary_user)
        visible = self.client.get("/api/candidates/")
        self.assertEqual(visible.status_code, 200)
        row = visible.data["results"][0]
        self.assertEqual(row["current_apply_id"], old_attempt.resume.apply_id)
        self.assertEqual(row["current_department_id"], self.secondary_a.id)
        self.assertEqual(row["workflow_status"], m.CandidateWorkflow.STATUS_IN_PROGRESS)
        self.assertEqual(row["archive_reason"], "")
        self.assertEqual(row["archive_detail"], "")
        self.assertEqual(row["processing_result"], "")
        self.assertEqual(row["reason_code"], "")

        hidden_queries = [
            {"search": candidate.phone},
            {"phone": candidate.phone},
            {"search": "SECRET-NEW-APPLY"},
            {"current_apply_id": "SECRET-NEW-APPLY"},
            {"current_entity": "秘密主体"},
            {"current_position_name": "秘密岗位"},
            {"current_job_category": "秘密类别"},
            {"job_department_name": self.secondary_b.name},
            {"current_department_id": self.secondary_b.id},
            {"current_primary_department_id": self.primary_b.id},
            {"feedback_reason_code": "key_capability_mismatch"},
            {"system_status": "screening_passed"},
        ]
        for query in hidden_queries:
            response = self.client.get("/api/candidates/", query)
            self.assertEqual(response.status_code, 200, query)
            self.assertEqual(response.data["count"], 0, query)

        visible_filter = self.client.get(
            "/api/candidates/", {"current_position_name": "旧岗位"}
        )
        self.assertEqual(visible_filter.data["count"], 1)
        rejected_filter = self.client.get(
            "/api/candidates/", {"system_status": "screening_rejected"}
        )
        self.assertEqual(rejected_filter.data["count"], 1)

        options = self.client.get("/api/candidates/filter-options/")
        self.assertEqual(options.status_code, 200)
        self.assertIn(
            "旧岗位",
            {item["value"] for item in options.data["current_position_name"]},
        )
        self.assertNotIn(
            "秘密岗位",
            {item["value"] for item in options.data["current_position_name"]},
        )
        self.assertNotIn(
            self.secondary_b.id,
            {item["value"] for item in options.data["current_department"]},
        )

        for forbidden in [
            {"analytics_primary_department_id": self.primary_b.id},
            {"processing_run_id": run.id},
            {"workflow_status": m.CandidateWorkflow.STATUS_PASSED},
            {"reason_code": "secret_processing_reason"},
        ]:
            response = self.client.get("/api/candidates/", forbidden)
            self.assertEqual(response.status_code, 400, forbidden)

    def test_attempt_and_timeline_hide_ai_and_recipient_identifiers(self):
        _, attempt = self._attempt("80", self.secondary_a)
        decision = m.AgentDispatchDecision.objects.create(
            workflow=attempt.workflow,
            resume=attempt.resume,
            recommendation=m.AgentDispatchDecision.RECOMMEND_DISPATCH,
            recommended_department=self.secondary_a,
            summary="HR 可见的 AI 摘要",
        )
        attempt.agent_decision = decision
        attempt.save(update_fields=["agent_decision"])
        m.AssignmentHandlingEvent.objects.create(
            attempt=attempt,
            event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED,
            to_department=self.secondary_a,
            metadata={
                "welink": {
                    "enabled": True,
                    "recipient_count": 2,
                    "recipient_ids": [11, 12],
                    "recipient_employee_nos": ["SECRET-A", "SECRET-B"],
                    "delivery_status": "stubbed",
                    "skipped_reason": "",
                    "error": "",
                }
            },
        )
        self.client.force_authenticate(self.secondary_user)

        detail = self.client.get(f"/api/workflow-attempts/{attempt.id}/")
        self.assertEqual(detail.status_code, 200)
        self.assertIsNone(detail.data["agent_decision"])
        self.assertIsNone(detail.data["agent_decision_summary"])
        metadata = detail.data["handling_events"][0]["metadata"]["welink"]
        self.assertEqual(metadata["recipient_count"], 2)
        self.assertNotIn("recipient_ids", metadata)
        self.assertNotIn("recipient_employee_nos", metadata)

        transfer = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/transfer/",
            {"target_department_id": self.secondary_b.id},
            format="json",
        )
        self.assertEqual(transfer.status_code, 200)
        self.assertIsNone(transfer.data["agent_decision"])
        new_contact = self._contact(
            "L2-B-TIMELINE", self.secondary_b, m.Contact.LEVEL_SECONDARY
        )
        new_user = self._user(new_contact, "二级接口人")
        self.client.force_authenticate(new_user)
        cross_department_detail = self.client.get(
            f"/api/workflow-attempts/{attempt.id}/"
        )
        cross_metadata = cross_department_detail.data["handling_events"][0][
            "metadata"
        ]["welink"]
        self.assertNotIn("recipient_ids", cross_metadata)
        self.assertNotIn("recipient_employee_nos", cross_metadata)

        feedback = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/feedback/",
            {"result": "passed"},
            format="json",
        )
        self.assertEqual(feedback.status_code, 200)
        self.assertIsNone(feedback.data["agent_decision"])
        self.assertIsNone(feedback.data["agent_decision_summary"])

    def test_malformed_department_child_does_not_expand_secondary_scope(self):
        malformed = m.Department.objects.create(
            name="畸形二级子节点", level=2, parent=self.secondary_a
        )
        hidden, _ = self._attempt("90", malformed)
        self.assertNotIn(hidden.id, self._candidate_ids(self.secondary_user))

    def test_department_api_enforces_tree_shape_and_cycles(self):
        self.client.force_authenticate(self.hr)
        invalid_primary = self.client.post(
            "/api/departments/",
            {"name": "非法一级", "level": 1, "parent": self.primary_a.id},
            format="json",
        )
        missing_primary = self.client.post(
            "/api/departments/",
            {"name": "无父二级", "level": 2},
            format="json",
        )
        invalid_tertiary = self.client.post(
            "/api/departments/",
            {"name": "非法三级", "level": 3, "parent": self.primary_a.id},
            format="json",
        )
        self.assertEqual(invalid_primary.status_code, 400)
        self.assertEqual(missing_primary.status_code, 400)
        self.assertEqual(invalid_tertiary.status_code, 400)

        valid = self.client.post(
            "/api/departments/",
            {"name": "合法二级", "level": 2, "parent": self.primary_a.id},
            format="json",
        )
        self.assertEqual(valid.status_code, 201)
        self_reference = self.client.patch(
            f"/api/departments/{self.secondary_a.id}/",
            {"parent": self.secondary_a.id},
            format="json",
        )
        self.assertEqual(self_reference.status_code, 400)

        loop_l2 = m.Department.objects.create(
            name="环二级", level=2, parent=self.primary_b
        )
        self.primary_b.parent = loop_l2
        self.primary_b.save(update_fields=["parent"])
        cycle = self.client.post(
            "/api/departments/",
            {"name": "循环二级", "level": 2, "parent": self.primary_b.id},
            format="json",
        )
        self.assertEqual(cycle.status_code, 400)

    def test_stale_transfer_response_is_409_and_keeps_first_target(self):
        _, attempt = self._attempt("91", self.secondary_a)
        stale_attempt = m.AssignmentAttempt.objects.get(pk=attempt.pk)
        self.client.force_authenticate(self.secondary_user)
        first = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/transfer/",
            {"target_department_id": self.secondary_a_other.id},
            format="json",
        )
        self.assertEqual(first.status_code, 200)
        with patch(
            "apps.api.views.AssignmentAttemptViewSet.get_object",
            return_value=stale_attempt,
        ):
            stale = self.client.post(
                f"/api/workflow-attempts/{attempt.id}/transfer/",
                {"target_department_id": self.secondary_b.id},
                format="json",
            )
        self.assertEqual(stale.status_code, 409)
        attempt.refresh_from_db()
        self.assertEqual(attempt.current_department_id, self.secondary_a_other.id)

    def test_breaking_api_rejects_legacy_fields_and_aliases(self):
        candidate, attempt = self._attempt("92", self.secondary_a)
        self.client.force_authenticate(self.secondary_user)
        legacy_department_filter = self.client.get(
            "/api/workflow-attempts/", {"department": self.secondary_a.id}
        )
        legacy_primary_filter = self.client.get(
            "/api/workflow-attempts/", {"primary_department_id": self.primary_a.id}
        )
        legacy_transfer = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/transfer/",
            {
                "target_department_id": self.secondary_b.id,
                "department_id": self.secondary_b.id,
            },
            format="json",
        )
        legacy_feedback = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/feedback/",
            {"feedback_result": "passed", "feedback_note": "旧字段"},
            format="json",
        )
        legacy_bulk = self.client.post(
            "/api/candidates/bulk-transfer/",
            {
                "candidate_ids": [candidate.id],
                "target_department_id": self.secondary_b.id,
                "department_id": self.secondary_b.id,
            },
            format="json",
        )
        for response in [
            legacy_department_filter,
            legacy_primary_filter,
            legacy_transfer,
            legacy_feedback,
            legacy_bulk,
        ]:
            self.assertEqual(response.status_code, 400)

        self.client.force_authenticate(self.hr)
        legacy_manual = self.client.post(
            f"/api/resumes/{attempt.resume_id}/manual-assign/",
            {
                "target_department_id": self.secondary_b.id,
                "contact_id": self.secondary_contact.id,
            },
            format="json",
        )
        legacy_transfer_manual = self.client.post(
            f"/api/workflow-attempts/{attempt.id}/transfer-to-manual/",
            {
                "target_department_id": self.secondary_b.id,
                "secondary_contact_id": self.secondary_contact.id,
            },
            format="json",
        )
        self.assertEqual(legacy_manual.status_code, 400)
        self.assertEqual(legacy_transfer_manual.status_code, 400)
        attempt.refresh_from_db()
        self.assertEqual(attempt.current_department_id, self.secondary_a.id)
