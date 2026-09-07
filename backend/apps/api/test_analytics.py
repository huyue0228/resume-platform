import json
from datetime import datetime, timedelta

from django.core.cache import cache
from django.test import TestCase
from django.utils import timezone
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults
from apps.core import models as m
from apps.pipeline.services import allocate


class RecruitmentAnalyticsApiTests(TestCase):
    def setUp(self):
        cache.clear()
        ensure_rbac_defaults()
        self.hr = User.objects.create_user(username="analytics-hr", password="pass")
        self.hr.groups.add(self.hr.groups.model.objects.get(name="HR"))
        self.no_access = User.objects.create_user(username="analytics-contact")
        self.no_access.groups.add(
            self.no_access.groups.model.objects.get(name="二级接口人")
        )
        self.client = APIClient()

        self.primary_department = m.Department.objects.create(name="科技中心", level=1)
        self.department = m.Department.objects.create(
            name="产品研发", level=2, parent=self.primary_department
        )
        self.job = m.Job.objects.create(
            entity="GW",
            department=self.department,
            public_name="软件工程师",
            category="技术",
        )
        self.tag = m.SchoolTag.objects.create(code="TARGET", name="目标院校")
        now = timezone.now()
        imported_at = now - timedelta(days=2)

        candidate_1 = m.Candidate.objects.create(
            identity_hash="analytics-candidate-1",
            name="候选人一",
            phone="13800000001",
            highest_education=m.Candidate.EDUCATION_MASTER,
            highest_degree_tag=self.tag,
        )
        resume_1 = m.Resume.objects.create(
            candidate=candidate_1,
            apply_id="AN-001",
            entity="GW",
            position_name="软件工程师",
            job=self.job,
            job_category="技术",
            volunteer_rank=1,
        )
        m.Resume.objects.filter(pk=resume_1.pk).update(imported_at=imported_at)
        workflow_1 = m.CandidateWorkflow.objects.create(
            candidate=candidate_1,
            current_resume=resume_1,
            status=m.CandidateWorkflow.STATUS_PASSED,
        )
        attempt = m.AssignmentAttempt.objects.create(
            workflow=workflow_1,
            resume=resume_1,
            attempt_no=1,
            source=m.AssignmentAttempt.SOURCE_RULE,
            status=m.AssignmentAttempt.STATUS_PASSED,
            initial_department=self.department,
            current_department=self.department,
            initial_department_name_snapshot="产品研发",
            current_department_name_snapshot="产品研发",
            dispatched_at=imported_at + timedelta(hours=4),
            feedback_at=imported_at + timedelta(hours=10),
            feedback_result=m.AssignmentAttempt.FEEDBACK_PASSED,
        )
        m.AssignmentAttempt.objects.filter(pk=attempt.pk).update(
            created_at=imported_at + timedelta(hours=2)
        )
        attempt.refresh_from_db()
        self._create_event(
            attempt,
            m.AssignmentHandlingEvent.EVENT_ATTEMPT_CREATED,
            imported_at + timedelta(hours=2),
        )
        self._create_event(
            attempt,
            m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED,
            imported_at + timedelta(hours=4),
            to_department=self.department,
        )
        self._create_event(
            attempt,
            m.AssignmentHandlingEvent.EVENT_FEEDBACK_PASSED,
            imported_at + timedelta(hours=10),
            from_department=self.department,
        )
        self.passed_attempt = attempt

        candidate_2 = m.Candidate.objects.create(
            identity_hash="analytics-candidate-2",
            name="候选人二",
            phone="13800000002",
            highest_education=m.Candidate.EDUCATION_BACHELOR,
            highest_degree_tag=self.tag,
        )
        resume_2 = m.Resume.objects.create(
            candidate=candidate_2,
            apply_id="AN-002",
            entity="YLS",
            position_name="产品经理",
            job_category="产品",
            volunteer_rank=1,
        )
        m.Resume.objects.filter(pk=resume_2.pk).update(imported_at=imported_at)
        workflow_2 = m.CandidateWorkflow.objects.create(
            candidate=candidate_2,
            current_resume=resume_2,
            status=m.CandidateWorkflow.STATUS_ARCHIVED,
            archive_reason=m.CandidateWorkflow.ARCHIVE_AGENT_NO_RECOMMENDATION,
        )
        m.AgentDispatchDecision.objects.create(
            workflow=workflow_2,
            resume=resume_2,
            recommendation=m.AgentDispatchDecision.RECOMMEND_REVIEW,
            special_route_applied=True,
        )
        m.AgentDispatchDecision.objects.create(
            workflow=workflow_2,
            resume=resume_2,
            error_code="llm_timeout",
            error_message="模型调用超时",
        )

        old_candidate = m.Candidate.objects.create(
            identity_hash="analytics-old-candidate",
            name="范围外候选人",
            phone="13800000003",
        )
        old_resume = m.Resume.objects.create(
            candidate=old_candidate,
            apply_id="AN-OLD",
            entity="GW",
        )
        m.Resume.objects.filter(pk=old_resume.pk).update(
            imported_at=now - timedelta(days=60)
        )

    def _create_event(self, attempt, event_type, occurred_at, **kwargs):
        event = m.AssignmentHandlingEvent.objects.create(
            attempt=attempt,
            event_type=event_type,
            **kwargs,
        )
        m.AssignmentHandlingEvent.objects.filter(pk=event.pk).update(
            occurred_at=occurred_at
        )
        event.refresh_from_db()
        return event

    def test_requires_analytics_permission(self):
        self.client.force_authenticate(self.no_access)
        response = self.client.get("/api/analytics/recruitment-overview/")
        self.assertEqual(response.status_code, 403)

    def test_returns_cohort_metrics_distributions_and_methodology(self):
        self.client.force_authenticate(self.hr)
        response = self.client.get("/api/analytics/recruitment-overview/")

        self.assertEqual(response.status_code, 200)
        payload = response.json()
        self.assertEqual(payload["summary"]["resume_count"], 2)
        self.assertEqual(payload["summary"]["candidate_count"], 2)
        self.assertEqual(payload["summary"]["classified_count"], 2)
        self.assertEqual(payload["summary"]["allocated_count"], 1)
        self.assertEqual(payload["summary"]["dispatched_count"], 1)
        self.assertEqual(payload["summary"]["feedback_count"], 1)
        self.assertEqual(payload["summary"]["passed_count"], 1)
        self.assertEqual(payload["summary"]["archived_count"], 1)
        self.assertEqual(payload["conversion"]["passed_rate"], 50.0)
        self.assertEqual(payload["average_hours"]["to_allocation"], 2.0)
        self.assertEqual(
            payload["primary_department_ranking"],
            [
                {
                    "key": self.primary_department.id,
                    "label": "科技中心",
                    "count": 1,
                }
            ],
        )
        self.assertEqual(payload["department_ranking"][0]["label"], "产品研发")
        self.assertEqual(payload["ai_error_distribution"][0]["key"], "llm_timeout")
        self.assertEqual(
            payload["ai_recommendation_distribution"],
            [{"key": "review", "label": "人工复核", "count": 1}],
        )
        self.assertEqual(
            payload["methodology"]["cohort"],
            "Resume.imported_at 落在所选日期范围内的投递记录",
        )

    def test_supports_entity_and_source_filters(self):
        self.client.force_authenticate(self.hr)
        response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"entity": "GW", "source": "rule"},
        )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["summary"]["candidate_count"], 1)
        self.assertEqual(response.json()["source_distribution"][0]["key"], "rule")

    def test_returns_overall_and_department_handling_speed(self):
        self.client.force_authenticate(self.hr)

        response = self.client.get("/api/analytics/recruitment-overview/")

        self.assertEqual(response.status_code, 200)
        speed = response.json()["handling_speed"]
        self.assertEqual(
            speed["overall"]["hr_dispatch_hours"],
            {"avg": 2.0, "median": 2.0, "p90": 2.0, "sample_count": 1},
        )
        self.assertEqual(
            speed["overall"]["department_processing_hours"],
            {"avg": 6.0, "median": 6.0, "p90": 6.0, "sample_count": 1},
        )
        self.assertEqual(
            speed["overall"]["total_feedback_hours"],
            {"avg": 6.0, "median": 6.0, "p90": 6.0, "sample_count": 1},
        )
        self.assertEqual(speed["overall"]["pending_count"], 0)
        self.assertIsNone(speed["overall"]["max_pending_age_hours"])
        self.assertEqual(speed["departments"][0]["department_id"], self.department.id)
        self.assertEqual(
            speed["departments"][0]["processing_hours"]["sample_count"], 1
        )

    def test_automatic_transfer_excludes_the_outgoing_instant_segment(self):
        sub_department = m.Department.objects.create(
            name="平台研发组", level=3, parent=self.department
        )
        dispatch_event = self.passed_attempt.handling_events.get(
            event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED
        )
        transfer_at = dispatch_event.occurred_at + timedelta(minutes=1)
        self._create_event(
            self.passed_attempt,
            m.AssignmentHandlingEvent.EVENT_DEPARTMENT_TRANSFERRED,
            transfer_at,
            from_department=self.department,
            to_department=sub_department,
            is_system_auto=True,
        )
        m.AssignmentAttempt.objects.filter(pk=self.passed_attempt.pk).update(
            current_department=sub_department,
            current_department_name_snapshot=sub_department.name,
        )
        cache.clear()
        self.client.force_authenticate(self.hr)

        response = self.client.get("/api/analytics/recruitment-overview/")

        self.assertEqual(response.status_code, 200)
        payload = response.json()
        departments = payload["handling_speed"]["departments"]
        self.assertEqual(
            [item["department_id"] for item in departments], [sub_department.id]
        )
        self.assertEqual(departments[0]["processing_hours"]["sample_count"], 1)
        self.assertEqual(
            departments[0]["primary_department_id"], self.primary_department.id
        )
        self.assertEqual(
            payload["primary_department_ranking"][0]["key"],
            self.primary_department.id,
        )
        self.assertEqual(
            payload["department_ranking"],
            [
                {
                    "key": self.department.id,
                    "label": self.department.name,
                    "count": 1,
                }
            ],
        )
        self.assertNotIn(
            sub_department.id,
            {
                item["value"]
                for item in payload["filter_options"]["departments"]
            },
        )

        secondary_filtered = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"department_id": self.department.id},
        )
        tertiary_filtered = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"department_id": sub_department.id},
        )
        self.assertEqual(
            secondary_filtered.json()["summary"]["candidate_count"], 1
        )
        self.assertEqual(
            tertiary_filtered.json()["summary"]["candidate_count"], 0
        )

        drilldown = self.client.get(
            "/api/candidates/",
            self._drilldown_params("department", [str(self.department.id)]),
        )
        self.assertEqual(
            {item["name"] for item in drilldown.json()["results"]},
            {"候选人一"},
        )

    def test_dispatched_attempt_contributes_pending_age(self):
        workflow = m.CandidateWorkflow.objects.get(
            candidate__identity_hash="analytics-candidate-2"
        )
        resume = m.Resume.objects.get(apply_id="AN-002")
        attempt = m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=resume,
            attempt_no=1,
            source=m.AssignmentAttempt.SOURCE_MANUAL,
            status=m.AssignmentAttempt.STATUS_DISPATCHED,
            initial_department=self.department,
            current_department=self.department,
        )
        entered_at = timezone.now() - timedelta(hours=3)
        self._create_event(
            attempt,
            m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED,
            entered_at,
            to_department=self.department,
        )
        self.client.force_authenticate(self.hr)

        response = self.client.get("/api/analytics/recruitment-overview/")

        self.assertEqual(response.status_code, 200)
        overall = response.json()["handling_speed"]["overall"]
        self.assertEqual(overall["pending_count"], 1)
        self.assertGreaterEqual(overall["max_pending_age_hours"], 3.0)
        self.assertLess(overall["max_pending_age_hours"], 3.1)
        department = next(
            item
            for item in response.json()["handling_speed"]["departments"]
            if item["department_id"] == self.department.id
        )
        self.assertEqual(department["pending_count"], 1)

    def _drilldown_params(self, dimension, values=None, **filters):
        params = {
            "analytics_date_from": (timezone.localdate() - timedelta(days=29)).isoformat(),
            "analytics_date_to": timezone.localdate().isoformat(),
            "analytics_dimension": dimension,
        }
        if values is not None:
            params["analytics_values"] = json.dumps(values, ensure_ascii=False)
            params["analytics_value_labels"] = json.dumps(
                ["" for _ in values], ensure_ascii=False
            )
        params.update(
            {f"analytics_{key}": value for key, value in filters.items()}
        )
        return params

    def test_candidate_list_drilldown_reuses_dashboard_scope_and_dimensions(self):
        self.client.force_authenticate(self.hr)
        cases = [
            ("candidate", None, {"候选人一", "候选人二"}),
            ("classified", None, {"候选人一", "候选人二"}),
            ("allocated", None, {"候选人一"}),
            ("dispatched", None, {"候选人一"}),
            ("feedback", None, {"候选人一"}),
            ("passed", None, {"候选人一"}),
            ("archived", None, {"候选人二"}),
            ("source", ["rule"], {"候选人一"}),
            ("ai_recommendation", ["review"], {"候选人二"}),
            ("ai_error", ["llm_timeout"], {"候选人二"}),
            ("job", [str(self.job.id)], {"候选人一"}),
            (
                "primary_department",
                [str(self.primary_department.id)],
                {"候选人一"},
            ),
            ("department", [str(self.department.id)], {"候选人一"}),
            ("school_tag", [str(self.tag.id)], {"候选人一", "候选人二"}),
            ("education", [m.Candidate.EDUCATION_MASTER], {"候选人一"}),
            (
                "archive_reason",
                [m.CandidateWorkflow.ARCHIVE_AGENT_NO_RECOMMENDATION],
                {"候选人二"},
            ),
        ]

        for dimension, values, expected_names in cases:
            with self.subTest(dimension=dimension):
                response = self.client.get(
                    "/api/candidates/",
                    self._drilldown_params(dimension, values),
                )
                self.assertEqual(response.status_code, 200)
                self.assertEqual(
                    {item["name"] for item in response.json()["results"]},
                    expected_names,
                )

        filtered = self.client.get(
            "/api/candidates/",
            self._drilldown_params(
                "candidate",
                primary_department_id=self.primary_department.id,
            ),
        )
        self.assertEqual(
            {item["name"] for item in filtered.json()["results"]},
            {"候选人一"},
        )

    def test_pipeline_scope_preserves_dashboard_drilldown(self):
        params = self._drilldown_params("source", ["rule"])

        candidate_ids = list(
            allocate.candidate_ids_for_scope({"candidate_filters": params})
        )

        self.assertEqual(
            candidate_ids,
            [
                m.Candidate.objects.get(
                    identity_hash="analytics-candidate-1"
                ).id
            ],
        )

    def test_rejection_reason_drilldown_uses_stable_reason_code(self):
        candidate = m.Candidate.objects.get(identity_hash="analytics-candidate-2")
        workflow = m.CandidateWorkflow.objects.get(candidate=candidate)
        resume = m.Resume.objects.get(apply_id="AN-002")
        note = "补充说明不会作为下钻条件"
        reason_code = m.AssignmentAttempt.REJECTION_REASON_KEY_CAPABILITY_MISMATCH
        m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=resume,
            attempt_no=1,
            source=m.AssignmentAttempt.SOURCE_MANUAL,
            status=m.AssignmentAttempt.STATUS_REJECTED,
            initial_department=self.department,
            current_department=self.department,
            feedback_result=m.AssignmentAttempt.FEEDBACK_REJECTED,
            feedback_reason_code=reason_code,
            feedback_reason_label_snapshot="关键能力不匹配",
            feedback_note=note,
        )
        self.client.force_authenticate(self.hr)
        overview = self.client.get("/api/analytics/recruitment-overview/").json()
        row = overview["rejection_reason_distribution"][0]

        self.assertEqual(row["key"], reason_code)
        self.assertEqual(row["label"], "关键能力不匹配")
        self.assertNotIn(note, row["key"])
        response = self.client.get(
            "/api/candidates/",
            self._drilldown_params("rejection_reason", [row["key"]]),
        )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(
            {item["name"] for item in response.json()["results"]},
            {"候选人二"},
        )

    def test_candidate_list_rejects_invalid_dashboard_drilldown(self):
        self.client.force_authenticate(self.hr)
        response = self.client.get(
            "/api/candidates/",
            {"analytics_dimension": "unknown"},
        )

        self.assertEqual(response.status_code, 400)
        self.assertIn("analytics_dimension", response.json()["detail"])

    def test_primary_department_filter_options_and_ranking(self):
        unrelated_primary = m.Department.objects.create(name="市场中心", level=1)
        self.client.force_authenticate(self.hr)

        matching_response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"primary_department_id": self.primary_department.id},
        )
        unrelated_response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"primary_department_id": unrelated_primary.id},
        )

        self.assertEqual(matching_response.status_code, 200)
        payload = matching_response.json()
        self.assertEqual(payload["summary"]["candidate_count"], 1)
        self.assertEqual(
            payload["primary_department_ranking"][0]["key"],
            self.primary_department.id,
        )
        self.assertIn(
            {"value": self.primary_department.id, "label": "科技中心"},
            payload["filter_options"]["primary_departments"],
        )
        self.assertIn(
            {
                "value": self.department.id,
                "label": "产品研发",
                "parent_id": self.primary_department.id,
            },
            payload["filter_options"]["departments"],
        )
        self.assertEqual(unrelated_response.status_code, 200)
        self.assertEqual(
            unrelated_response.json()["summary"]["candidate_count"], 0
        )

    def test_department_filter_and_ranking_use_latest_non_cancelled_attempt(self):
        candidate = m.Candidate.objects.get(identity_hash="analytics-candidate-1")
        resume = m.Resume.objects.get(apply_id="AN-001")
        workflow = m.CandidateWorkflow.objects.get(candidate=candidate)
        latest_primary_department = m.Department.objects.create(
            name="最新一级部门", level=1
        )
        latest_department = m.Department.objects.create(
            name="最新统计归属部门",
            level=2,
            parent=latest_primary_department,
        )
        m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=resume,
            attempt_no=2,
            source=m.AssignmentAttempt.SOURCE_MANUAL,
            status=m.AssignmentAttempt.STATUS_DISPATCHED,
            initial_department=latest_department,
            current_department=latest_department,
            initial_department_name_snapshot="最新统计归属部门",
            current_department_name_snapshot="最新统计归属部门",
        )
        m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=resume,
            attempt_no=3,
            source=m.AssignmentAttempt.SOURCE_RULE,
            status=m.AssignmentAttempt.STATUS_CANCELLED,
            initial_department=self.department,
            current_department=self.department,
            initial_department_name_snapshot="产品研发",
            current_department_name_snapshot="产品研发",
        )
        self.client.force_authenticate(self.hr)

        old_response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"department_id": self.department.id},
        )
        latest_response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"department_id": latest_department.id},
        )
        latest_primary_response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"primary_department_id": latest_primary_department.id},
        )

        self.assertEqual(old_response.status_code, 200)
        self.assertEqual(old_response.json()["summary"]["candidate_count"], 0)
        self.assertEqual(latest_response.status_code, 200)
        self.assertEqual(latest_primary_response.status_code, 200)
        self.assertEqual(
            latest_primary_response.json()["summary"]["candidate_count"], 1
        )
        payload = latest_response.json()
        self.assertEqual(payload["summary"]["candidate_count"], 1)
        self.assertEqual(
            payload["department_ranking"],
            [
                {
                    "key": latest_department.id,
                    "label": "最新统计归属部门",
                    "count": 1,
                }
            ],
        )
        self.assertEqual(payload["source_distribution"][0]["key"], "manual")

    def test_job_filter_and_ranking_use_current_effective_resume(self):
        candidate = m.Candidate.objects.get(identity_hash="analytics-candidate-1")
        current_job = m.Job.objects.create(
            entity="GW",
            department=self.department,
            public_name="当前有效岗位",
            category="技术",
        )
        current_resume = m.Resume.objects.create(
            candidate=candidate,
            apply_id="AN-CURRENT",
            entity="GW",
            position_name="当前有效岗位",
            job=current_job,
            job_category="技术",
            volunteer_rank=2,
        )
        m.Resume.objects.filter(pk=current_resume.pk).update(
            imported_at=timezone.now() - timedelta(days=60)
        )
        m.CandidateWorkflow.objects.filter(candidate=candidate).update(
            current_resume=current_resume
        )
        self.client.force_authenticate(self.hr)

        response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"job_id": current_job.id},
        )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["summary"]["candidate_count"], 1)
        self.assertEqual(response.json()["job_ranking"][0]["label"], "当前有效岗位")

    def test_only_current_volunteer_attempt_contributes_to_assignment_metrics(self):
        candidate = m.Candidate.objects.get(identity_hash="analytics-candidate-1")
        workflow = m.CandidateWorkflow.objects.get(candidate=candidate)
        historical_resume = m.Resume.objects.get(apply_id="AN-001")
        current_primary = m.Department.objects.create(name="当前志愿一级", level=1)
        current_department = m.Department.objects.create(
            name="当前志愿二级", level=2, parent=current_primary
        )
        current_resume = m.Resume.objects.create(
            candidate=candidate,
            apply_id="AN-NEW-CURRENT",
            entity="GW",
            position_name="当前志愿岗位",
            job_category="技术",
            volunteer_rank=2,
        )
        m.Resume.objects.filter(pk=current_resume.pk).update(
            imported_at=historical_resume.imported_at
        )
        workflow.current_resume = current_resume
        workflow.save(update_fields=["current_resume"])
        attempt = m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=current_resume,
            attempt_no=2,
            source=m.AssignmentAttempt.SOURCE_MANUAL,
            status=m.AssignmentAttempt.STATUS_PASSED,
            initial_department=current_department,
            current_department=current_department,
        )
        created_at = historical_resume.imported_at + timedelta(hours=3)
        m.AssignmentAttempt.objects.filter(pk=attempt.pk).update(created_at=created_at)
        attempt.refresh_from_db()
        self._create_event(
            attempt,
            m.AssignmentHandlingEvent.EVENT_ATTEMPT_CREATED,
            created_at,
        )
        self._create_event(
            attempt,
            m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED,
            created_at + timedelta(hours=1),
            to_department=current_department,
        )
        self._create_event(
            attempt,
            m.AssignmentHandlingEvent.EVENT_FEEDBACK_PASSED,
            created_at + timedelta(hours=3),
            from_department=current_department,
        )
        self.client.force_authenticate(self.hr)

        response = self.client.get("/api/analytics/recruitment-overview/")

        self.assertEqual(response.status_code, 200)
        payload = response.json()
        self.assertEqual(payload["summary"]["resume_count"], 3)
        self.assertEqual(payload["summary"]["allocated_count"], 1)
        self.assertEqual(
            payload["source_distribution"],
            [{"key": "manual", "label": "手动强制分配", "count": 1}],
        )
        self.assertEqual(
            payload["department_ranking"],
            [
                {
                    "key": current_department.id,
                    "label": current_department.name,
                    "count": 1,
                }
            ],
        )
        self.assertEqual(
            payload["handling_speed"]["overall"]["hr_dispatch_hours"][
                "sample_count"
            ],
            1,
        )
        self.assertEqual(
            payload["handling_speed"]["overall"]["department_processing_hours"][
                "sample_count"
            ],
            1,
        )

    def test_missing_current_resume_uses_existing_volunteer_fallback(self):
        workflow = m.CandidateWorkflow.objects.get(
            candidate__identity_hash="analytics-candidate-1"
        )
        workflow.current_resume = None
        workflow.save(update_fields=["current_resume"])
        self.client.force_authenticate(self.hr)

        response = self.client.get("/api/analytics/recruitment-overview/")

        self.assertEqual(response.status_code, 200)
        payload = response.json()
        self.assertEqual(
            payload["source_distribution"],
            [{"key": "rule", "label": "规则分配", "count": 1}],
        )
        self.assertEqual(
            payload["department_ranking"][0]["key"], self.department.id
        )

    def test_school_tag_filter_uses_highest_tag_then_first_tag_fallback(self):
        candidate = m.Candidate.objects.get(identity_hash="analytics-candidate-1")
        candidate.first_degree_tag = self.tag
        candidate.highest_degree_tag = None
        candidate.save(update_fields=["first_degree_tag", "highest_degree_tag"])
        self.client.force_authenticate(self.hr)

        response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"school_tag_id": self.tag.id},
        )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["summary"]["candidate_count"], 2)
        self.assertEqual(response.json()["school_tag_ranking"][0]["count"], 2)

    def test_rejects_invalid_or_overlong_date_range(self):
        self.client.force_authenticate(self.hr)
        response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"date_from": "2025-01-01", "date_to": "2026-12-31"},
        )
        self.assertEqual(response.status_code, 400)
        self.assertIn("366", response.json()["detail"])

        response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"date_from": "not-a-date"},
        )
        self.assertEqual(response.status_code, 400)

        response = self.client.get(
            "/api/analytics/recruitment-overview/",
            {"primary_department_id": "not-an-id"},
        )
        self.assertEqual(response.status_code, 400)
        self.assertIn("primary_department_id", response.json()["detail"])

    def test_response_is_cached_for_identical_filters(self):
        self.client.force_authenticate(self.hr)
        first = self.client.get("/api/analytics/recruitment-overview/")
        self.assertEqual(first.status_code, 200)
        m.Resume.objects.filter(apply_id="AN-002").delete()

        second = self.client.get("/api/analytics/recruitment-overview/")
        self.assertEqual(second.json()["summary"]["resume_count"], 2)
