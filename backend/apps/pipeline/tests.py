from unittest.mock import patch
from types import SimpleNamespace

from apps.pipeline.test_support import KernelTestCase, analysis_response
from django.test import TestCase
from django.db import transaction
from django.db.models.deletion import ProtectedError
from django.utils import timezone

from apps.accounts.models import User
from apps.core import models as m, system_status
from apps.pipeline import ai_config, runner
from apps.pipeline.errors import AIServiceError
from apps.pipeline.services import allocate, classify_school


class AllocationDesignContractTests(KernelTestCase):
    def setUp(self):
        ai_config.save_ai_connection_config(
            {
                "api_style": "responses",
                "model_name": "gpt-test",
                "base_url": "https://model.internal/v1",
                "api_key": "test-key",
            }
        )
        self.department = m.Department.objects.create(name="技术部", level=2)
        ai_config.mark_ai_connection_tested()
        self.candidate = m.Candidate.objects.create(
            identity_hash="candidate-1",
            name="张三",
            phone="13800000000",
            first_degree_platform="平台A",
            highest_degree_platform="平台A",
        )
        self.resume = m.Resume.objects.create(
            candidate=self.candidate,
            apply_id="A1001",
            position_name="后端工程师",
            volunteer_rank=1,
            resume_file="张三（A1001）.pdf",
        )
        self.job = m.Job.objects.create(
            department=self.department,
            public_name="后端工程师",
            position_name="后端工程师",
            category="技术类",
            responsibilities="负责后端服务开发和性能优化。",
            headcount=1,
        )

    def _ai_result(self, confidence=0.78):
        profile, _ = m.ResumeProfile.objects.get_or_create(
            resume=self.resume,
            defaults={"parse_status": "parsed", "raw_text": "候选人简历正文"},
        )
        breakdown = {
            "major_match": confidence,
            "skills_match": confidence,
            "experience_evidence": confidence,
            "job_requirement": confidence,
            "resume_quality": confidence,
        }
        decision = SimpleNamespace(
            recommendation="dispatch",
            summary="结构化 AI 建议",
            reason="简历证据与当前志愿匹配",
            evidence=["项目经历支持岗位要求"],
            risks=[],
        )
        return SimpleNamespace(
            profile=profile,
            output=SimpleNamespace(
                decision=decision,
                profile=SimpleNamespace(risk_flags=[]),
            ),
            job=self.job,
            department=self.department,
            confidence=confidence,
            score_breakdown=breakdown,
        )

    def test_parent_department_with_children_cannot_be_deleted(self):
        primary_department = m.Department.objects.create(name="研发中心", level=1)
        m.Department.objects.create(
            name="研发二部", level=2, parent=primary_department
        )

        with self.assertRaises(ProtectedError):
            primary_department.delete()

    def test_rule_allocation_passes_school_gate_when_no_active_rules(self):
        message = allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.assertIn("已生成 1 条候选人分配尝试", message)
        self.assertEqual(attempt.source, m.AssignmentAttempt.SOURCE_RULE)
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_PENDING_DISPATCH)
        self.assertIsNone(attempt.matched_rule)
        self.assertEqual(self.candidate.workflow.status, m.CandidateWorkflow.STATUS_IN_PROGRESS)

    def test_job_model_rejects_tertiary_department(self):
        primary = m.Department.objects.create(name="研发中心", level=1)
        self.department.parent = primary
        self.department.save(update_fields=["parent"])
        tertiary = m.Department.objects.create(
            name="平台研发组", level=3, parent=self.department
        )
        self.job.department = tertiary
        with self.assertRaisesRegex(ValueError, "岗位必须绑定二级部门"):
            self.job.save(update_fields=["department"])

    def test_resume_process_freezes_scope_and_exposes_agent_stages(self):
        user = User.objects.create_user(username="hr-run-owner", password="pass")
        run = runner.create_run(
            "resume_process",
            scope={"candidate_ids": [self.candidate.id], "source": "resume_import"},
            created_by=user,
        )

        self.assertEqual(run.scope, {"source": "resume_import"})
        self.assertEqual(run.scope_summary["candidate_count"], 1)
        self.assertEqual(
            list(run.scope_items.values_list("candidate_id", flat=True)), [self.candidate.id]
        )
        self.assertEqual(
            list(run.stages.values_list("step", flat=True)),
            ["step1", "step2", "step3", "step4"],
        )

        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ):
            runner.execute_run(run.id)

        run.refresh_from_db()
        self.assertEqual(run.status, "success")
        self.assertEqual(run.created_by, user)
        self.assertTrue(run.last_heartbeat_at)
        self.assertEqual(
            list(run.stages.values_list("status", flat=True)),
            ["success", "success", "success", "success"],
        )

    def test_runner_has_no_mode_parameter(self):
        with self.assertRaisesRegex(TypeError, "unexpected keyword argument 'mode'"):
            runner.create_run(
                "step2",
                mode="rule",
                scope={"candidate_ids": [self.candidate.id]},
            )

    def test_explicit_ai_run_freezes_mode_at_submission_time(self):
        run = runner.create_run(
            "step2",
            scope={"candidate_ids": [self.candidate.id]},
        )

        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ):
            runner.execute_run(run.id)

        run.refresh_from_db()
        self.assertEqual(run.mode, "ai")
        self.assertEqual(
            list(m.AssignmentAttempt.objects.values_list("source", flat=True)),
            [m.AssignmentAttempt.SOURCE_AI],
        )

    def test_force_reprocess_skips_candidate_changed_after_submit(self):
        workflow = m.CandidateWorkflow.objects.create(candidate=self.candidate)
        run = runner.create_run(
            "step2",
            scope={"candidate_ids": [self.candidate.id], "force_reprocess": True},
        )
        workflow.status = m.CandidateWorkflow.STATUS_IN_PROGRESS
        workflow.save(update_fields=["status"])

        runner.execute_run(run.id)

        scope_item = run.scope_items.get(candidate=self.candidate)
        self.assertEqual(scope_item.status, "skipped_manual_change")
        self.assertIsNotNone(scope_item.finished_at)
        run.refresh_from_db()
        self.assertEqual(run.success_count, 0)
        self.assertEqual(run.skipped_count, 1)
        self.assertEqual(run.stages.get(step="step2").skipped_count, 1)
        self.assertFalse(m.AssignmentAttempt.objects.exists())

    def test_force_reprocess_retries_current_volunteer_instead_of_advancing(self):
        second_resume = m.Resume.objects.create(
            candidate=self.candidate,
            apply_id="A1002",
            position_name="产品经理",
            volunteer_rank=2,
        )
        second_department = m.Department.objects.create(name="产品部", level=2)
        m.Job.objects.create(
            department=second_department,
            public_name="产品经理",
            position_name="产品经理",
            category="产品类",
        )
        m.CandidateWorkflow.objects.create(
            candidate=self.candidate,
            status=m.CandidateWorkflow.STATUS_ARCHIVED,
            current_resume=self.resume,
            current_rank=1,
            archive_reason=m.CandidateWorkflow.ARCHIVE_JOB_NOT_MATCHED,
        )
        run = runner.create_run(
            "step2",
            scope={"candidate_ids": [self.candidate.id], "force_reprocess": True},
        )

        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ):
            runner.execute_run(run.id)

        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.resume, self.resume)
        second_resume.refresh_from_db()
        self.assertIsNone(second_resume.job)

    def test_scoped_reprocess_classifies_school_tags_before_school_gate(self):
        target_tag = m.SchoolTag.objects.create(code="TARGET", name="目标院校")
        m.School.objects.create(name="南京大学", school_tag=target_tag)
        rule = m.SchoolTagRule.objects.create(name="目标院校", priority=1, is_active=True)
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=target_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_FIRST,
        )
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=target_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_HIGHEST,
        )
        self.candidate.first_degree_school = "南京大学"
        self.candidate.highest_degree_school = "南京大学"
        self.candidate.first_degree_platform = ""
        self.candidate.highest_degree_platform = ""
        self.candidate.first_degree_tag = None
        self.candidate.highest_degree_tag = None
        self.candidate.save(
            update_fields=[
                "first_degree_school",
                "highest_degree_school",
                "first_degree_platform",
                "highest_degree_platform",
                "first_degree_tag",
                "highest_degree_tag",
            ]
        )

        message = allocate.run(mode="rule", scope={"system_statuses": ["raw"]})

        self.candidate.refresh_from_db()
        self.assertEqual(self.candidate.first_degree_tag, target_tag)
        self.assertEqual(self.candidate.highest_degree_tag, target_tag)
        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.matched_rule, rule)
        self.assertEqual(self.candidate.workflow.status, m.CandidateWorkflow.STATUS_IN_PROGRESS)
        self.assertIn("已生成 1 条候选人分配尝试", message)

    def test_school_name_normalization_is_consistent_for_admission_and_all_tags(self):
        target_tag = m.SchoolTag.objects.create(code="TARGET", name="目标院校")
        m.School.objects.create(name="Example University", school_tag=target_tag)
        rule = m.SchoolTagRule.objects.create(
            name="目标院校", priority=1, is_active=True
        )
        for degree_type in (
            m.SchoolTagRuleTag.DEGREE_FIRST,
            m.SchoolTagRuleTag.DEGREE_HIGHEST,
        ):
            m.SchoolTagRuleTag.objects.create(
                rule=rule,
                school_tag=target_tag,
                degree_type=degree_type,
            )
        self.candidate.first_degree_school = "  example   university  "
        self.candidate.highest_degree_school = "EXAMPLE\tUNIVERSITY"
        self.candidate.first_degree_tag = None
        self.candidate.highest_degree_tag = None
        self.candidate.save(
            update_fields=[
                "first_degree_school",
                "highest_degree_school",
                "first_degree_tag",
                "highest_degree_tag",
            ]
        )

        allocate.run(mode="rule", scope={"system_statuses": ["raw"]})

        self.candidate.refresh_from_db()
        self.assertEqual(self.candidate.first_degree_tag, target_tag)
        self.assertEqual(self.candidate.highest_degree_tag, target_tag)
        self.assertEqual(
            list(self.candidate.school_tags.values_list("id", flat=True)),
            [target_tag.id],
        )
        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.matched_rule, rule)
        self.assertEqual(
            self.candidate.workflow.status,
            m.CandidateWorkflow.STATUS_IN_PROGRESS,
        )

    def test_scoped_reprocess_preserves_existing_school_tags_and_platforms(self):
        existing_tag = m.SchoolTag.objects.create(code="MANUAL", name="人工确认标签")
        mapped_tag = m.SchoolTag.objects.create(code="TARGET", name="清单目标院校")
        m.School.objects.create(name="南京大学", school_tag=mapped_tag)
        self.candidate.first_degree_school = "南京大学"
        self.candidate.highest_degree_school = "南京大学"
        self.candidate.first_degree_tag = existing_tag
        self.candidate.highest_degree_tag = existing_tag
        self.candidate.first_degree_platform = "人工确认平台"
        self.candidate.highest_degree_platform = "人工确认平台"
        self.candidate.save(
            update_fields=[
                "first_degree_school",
                "highest_degree_school",
                "first_degree_tag",
                "highest_degree_tag",
                "first_degree_platform",
                "highest_degree_platform",
            ]
        )

        allocate.run(mode="rule", scope={"candidate_ids": [self.candidate.id]})

        self.candidate.refresh_from_db()
        self.assertEqual(self.candidate.first_degree_tag, existing_tag)
        self.assertEqual(self.candidate.highest_degree_tag, existing_tag)
        self.assertEqual(self.candidate.first_degree_platform, "人工确认平台")
        self.assertEqual(self.candidate.highest_degree_platform, "人工确认平台")

    def test_school_classification_fills_missing_tags_without_overwriting_platforms(self):
        mapped_tag = m.SchoolTag.objects.create(code="TARGET", name="清单目标院校")
        m.School.objects.create(name="南京大学", school_tag=mapped_tag)
        self.candidate.first_degree_school = "南京大学"
        self.candidate.highest_degree_school = "南京大学"
        self.candidate.first_degree_tag = None
        self.candidate.highest_degree_tag = None
        self.candidate.first_degree_platform = "人工确认平台"
        self.candidate.highest_degree_platform = "人工确认平台"
        self.candidate.save(
            update_fields=[
                "first_degree_school",
                "highest_degree_school",
                "first_degree_tag",
                "highest_degree_tag",
                "first_degree_platform",
                "highest_degree_platform",
            ]
        )

        classify_school.classify_candidates([self.candidate], overwrite=False)

        self.candidate.refresh_from_db()
        self.assertEqual(self.candidate.first_degree_tag, mapped_tag)
        self.assertEqual(self.candidate.highest_degree_tag, mapped_tag)
        self.assertEqual(self.candidate.first_degree_platform, "人工确认平台")
        self.assertEqual(self.candidate.highest_degree_platform, "人工确认平台")

    def test_school_outside_imported_list_is_always_non_target(self):
        m.SchoolTag.objects.create(
            code="DEFAULT_TARGET", name="默认目标院校", is_default=True, is_active=True
        )
        self.candidate.first_degree_school = "未在清单中的学校"
        self.candidate.highest_degree_school = "另一所未在清单中的学校"
        self.candidate.save(
            update_fields=["first_degree_school", "highest_degree_school"]
        )

        classify_school.classify_candidates([self.candidate])

        self.candidate.refresh_from_db()
        self.assertEqual(self.candidate.first_degree_tag.code, "NON_TARGET")
        self.assertEqual(self.candidate.highest_degree_tag.code, "NON_TARGET")

    def test_candidate_collects_tags_from_all_current_resume_education_experiences(self):
        target_a = m.SchoolTag.objects.create(code="TARGET_A", name="目标院校A")
        target_b = m.SchoolTag.objects.create(code="TARGET_B", name="目标院校B")
        m.School.objects.create(name="本科大学", school_tag=target_a)
        m.School.objects.create(name="硕士大学", school_tag=target_b)
        self.candidate.first_degree_school = "本科大学"
        self.candidate.highest_degree_school = "硕士大学"
        self.candidate.save(
            update_fields=["first_degree_school", "highest_degree_school"]
        )
        m.ResumeProfile.objects.create(
            resume=self.resume,
            education_experiences=[
                {"school_name": "本科大学", "degree": "本科"},
                {"school_name": "硕士大学", "degree": "硕士"},
                {"school_name": "未收录学院", "degree": "交换经历"},
            ],
        )

        classify_school.classify_candidates([self.candidate])

        self.assertEqual(m.School.objects.get(name="本科大学").school_tag, target_a)
        self.assertEqual(
            set(self.candidate.school_tags.values_list("code", flat=True)),
            {"TARGET_A", "TARGET_B", "NON_TARGET"},
        )

    def test_rule_allocation_does_not_skip_current_volunteer_on_major_mismatch(self):
        self.candidate.highest_major = "计算机科学与技术"
        self.candidate.save(update_fields=["highest_major"])
        m.JobMajor.objects.create(job=self.job, major="电气工程")
        next_resume = m.Resume.objects.create(
            candidate=self.candidate,
            apply_id="A1002",
            position_name="产品经理",
            volunteer_rank=2,
            resume_file="张三（A1002）.pdf",
        )
        next_job = m.Job.objects.create(
            department=self.department,
            public_name="产品经理",
            position_name="产品经理",
            category="产品类",
            headcount=1,
        )
        m.JobMajor.objects.create(job=next_job, major="计算机")

        allocate.run(mode="rule")

        self.assertFalse(m.AssignmentAttempt.objects.exists())
        self.candidate.workflow.refresh_from_db()
        self.resume.refresh_from_db()
        next_resume.refresh_from_db()
        self.assertEqual(self.candidate.workflow.current_resume, self.resume)
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_JOB_NOT_MATCHED,
        )
        self.assertIn("专业", self.candidate.workflow.archive_detail)
        self.assertIsNone(next_resume.job)

    def test_rule_allocation_allows_department_without_active_contacts(self):
        fallback_department = m.Department.objects.create(name="产品部", level=2)
        fallback_resume = m.Resume.objects.create(
            candidate=self.candidate,
            apply_id="A1002",
            position_name="产品经理",
            volunteer_rank=2,
            resume_file="张三（A1002）.pdf",
        )
        m.Job.objects.create(
            department=fallback_department,
            public_name="产品经理",
            position_name="产品经理",
            category="产品类",
            headcount=1,
        )

        allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.candidate.workflow.refresh_from_db()
        self.resume.refresh_from_db()
        fallback_resume.refresh_from_db()
        self.assertEqual(
            self.candidate.workflow.status,
            m.CandidateWorkflow.STATUS_IN_PROGRESS,
        )
        self.assertEqual(self.candidate.workflow.current_resume, self.resume)
        self.assertEqual(self.candidate.workflow.current_rank, 1)
        self.assertEqual(attempt.initial_department, self.department)
        self.assertEqual(attempt.current_department, self.department)
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_PENDING_DISPATCH)
        self.assertEqual(self.candidate.workflow.block_reason, "")
        self.assertEqual(self.candidate.workflow.block_detail, "")
        self.assertEqual(self.resume.job, self.job)
        self.assertIsNone(fallback_resume.job)
        created_event = attempt.handling_events.get(
            event_type=m.AssignmentHandlingEvent.EVENT_ATTEMPT_CREATED
        )
        self.assertEqual(
            created_event.metadata["initial_department_id"], self.department.id
        )

    def test_rule_archive_detail_identifies_missing_job_requirement(self):
        self.job.is_active = False
        self.job.save(update_fields=["is_active"])

        allocate.run(mode="rule")

        self.candidate.workflow.refresh_from_db()
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_JOB_NOT_MATCHED,
        )
        self.assertIn("第1志愿", self.candidate.workflow.archive_detail)
        self.assertIn("后端工程师", self.candidate.workflow.archive_detail)
        self.assertIn("岗位需求中未配置", self.candidate.workflow.archive_detail)

    def test_rule_archive_detail_identifies_missing_secondary_department(self):
        self.job.department = None
        self.job.save(update_fields=["department"])

        allocate.run(mode="rule")

        self.candidate.workflow.refresh_from_db()
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_DEPARTMENT_NOT_FOUND,
        )
        self.assertIn("后端工程师", self.candidate.workflow.archive_detail)
        self.assertIn("未配置有效二级部门", self.candidate.workflow.archive_detail)

    def test_rule_allocation_clears_previous_block_when_valid_attempt_is_created(self):
        workflow = m.CandidateWorkflow.objects.create(
            candidate=self.candidate,
            status=m.CandidateWorkflow.STATUS_IN_PROGRESS,
            current_resume=self.resume,
            current_rank=1,
            block_reason=m.CandidateWorkflow.BLOCK_JOB_HC_EXHAUSTED,
            block_detail="旧阻塞原因",
        )

        allocate._create_attempt(
            workflow=workflow,
            resume=self.resume,
            initial_department=self.department,
            source=m.AssignmentAttempt.SOURCE_RULE,
            mode="rule",
            match_reason="测试分配",
        )

        workflow.refresh_from_db()
        self.assertEqual(workflow.block_reason, "")
        self.assertEqual(workflow.block_detail, "")

    def test_rule_allocation_matches_major_category_dictionary_before_name_fallback(self):
        self.candidate.highest_major = "软件工程"
        self.candidate.save(update_fields=["highest_major"])
        m.JobMajor.objects.create(job=self.job, major="计算机类")
        category = m.MajorCategory.objects.create(
            code="CS_SOFTWARE",
            name="计算机与软件类",
            is_active=True,
        )
        m.MajorAlias.objects.create(
            category=category,
            name="软件工程",
            normalized_name="软件工程",
            match_type=m.MajorAlias.MATCH_EXACT,
            source=m.MajorAlias.SOURCE_BUILTIN,
            is_active=True,
        )
        m.MajorAlias.objects.create(
            category=category,
            name="计算机类",
            normalized_name="计算机类",
            match_type=m.MajorAlias.MATCH_EXACT,
            source=m.MajorAlias.SOURCE_BUILTIN,
            is_active=True,
        )

        allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.resume, self.resume)
        self.assertEqual(attempt.resume.job, self.job)
        self.assertIn("专业大类匹配", attempt.match_reason)
        self.assertIn("计算机与软件类", attempt.match_reason)

    def test_rule_allocation_allows_wildcard_required_major(self):
        m.JobMajor.objects.create(job=self.job, major="不限专业")

        allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.resume, self.resume)
        self.assertEqual(attempt.resume.job, self.job)
        self.assertIn("需求专业为不限", attempt.match_reason)

    def test_rule_allocation_does_not_treat_related_major_as_default_wildcard(self):
        self.candidate.highest_major = "软件工程"
        self.candidate.save(update_fields=["highest_major"])
        m.JobMajor.objects.create(job=self.job, major="相关专业")
        general = m.MajorCategory.objects.create(
            code="OTHER_GENERAL",
            name="其他通用类",
            is_active=False,
        )
        m.MajorAlias.objects.create(
            category=general,
            name="相关专业",
            normalized_name="相关专业",
            match_type=m.MajorAlias.MATCH_CONTAINS,
            source=m.MajorAlias.SOURCE_BUILTIN,
            is_active=True,
        )

        allocate.run(mode="rule")

        self.assertFalse(m.AssignmentAttempt.objects.exists())
        self.candidate.workflow.refresh_from_db()
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_JOB_NOT_MATCHED,
        )

    def test_rule_allocation_allows_job_without_required_majors(self):
        self.candidate.highest_major = "材料科学与工程"
        self.candidate.save(update_fields=["highest_major"])

        allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.resume, self.resume)
        self.assertEqual(attempt.resume.job, self.job)

    def test_rule_allocation_prefers_same_entity_job(self):
        self.resume.entity = "GW"
        self.resume.save(update_fields=["entity"])
        self.job.entity = "YLS"
        self.job.save(update_fields=["entity"])
        same_entity_job = m.Job.objects.create(
            entity="GW",
            department=self.department,
            public_name="后端工程师",
            position_name="后端工程师",
            category="技术类",
            headcount=1,
        )

        allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.resume.job, same_entity_job)

    def test_rule_allocation_uses_stable_job_match_priority(self):
        self.job.public_name = "后端"
        self.job.position_name = "后端"
        self.job.save(update_fields=["public_name", "position_name"])
        exact_position_job = m.Job.objects.create(
            department=self.department,
            public_name="服务端研发",
            position_name="后端工程师",
            category="技术类",
            headcount=1,
        )
        exact_public_job = m.Job.objects.create(
            department=self.department,
            public_name="后端工程师",
            position_name="后端研发",
            category="技术类",
            headcount=1,
        )

        allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.resume.job, exact_public_job)
        self.assertNotEqual(attempt.resume.job, exact_position_job)
        self.assertIn("岗位名精确命中对外发布名称", attempt.match_reason)

    def test_rule_allocation_records_explainable_match_reason(self):
        self.candidate.highest_major = "计算机科学与技术"
        self.candidate.save(update_fields=["highest_major"])
        m.JobMajor.objects.create(job=self.job, major="计算机")

        allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.assertIn("院校准入", attempt.match_reason)
        self.assertIn("第1志愿", attempt.match_reason)
        self.assertIn("岗位名精确命中", attempt.match_reason)
        self.assertIn("专业匹配", attempt.match_reason)
        self.assertIn("分配至技术部", attempt.match_reason)

    def test_rule_allocation_matches_school_tag_rule_links(self):
        first_tag = m.SchoolTag.objects.create(code="A", name="平台A")
        highest_tag = m.SchoolTag.objects.create(code="A_PLUS", name="平台A+")
        self.candidate.first_degree_tag = first_tag
        self.candidate.highest_degree_tag = highest_tag
        self.candidate.save(update_fields=["first_degree_tag", "highest_degree_tag"])
        rule = m.SchoolTagRule.objects.create(name="目标院校", priority=1, is_active=True)
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=first_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_FIRST,
        )
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=highest_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_HIGHEST,
        )

        allocate.run(mode="rule")

        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.matched_rule, rule)

    def test_school_gate_requires_allowed_highest_education_in_same_rule(self):
        first_tag = m.SchoolTag.objects.create(code="EDU_FIRST", name="第一标签")
        highest_tag = m.SchoolTag.objects.create(code="EDU_HIGH", name="最高标签")
        self.candidate.first_degree_tag = first_tag
        self.candidate.highest_degree_tag = highest_tag
        self.candidate.highest_education = m.Candidate.EDUCATION_MASTER
        self.candidate.save(
            update_fields=["first_degree_tag", "highest_degree_tag", "highest_education"]
        )
        bachelor_rule = m.SchoolTagRule.objects.create(
            name="仅本科", priority=1, is_active=True
        )
        master_rule = m.SchoolTagRule.objects.create(
            name="允许硕士", priority=2, is_active=True
        )
        for rule in [bachelor_rule, master_rule]:
            m.SchoolTagRuleTag.objects.create(
                rule=rule,
                school_tag=first_tag,
                degree_type=m.SchoolTagRuleTag.DEGREE_FIRST,
            )
            m.SchoolTagRuleTag.objects.create(
                rule=rule,
                school_tag=highest_tag,
                degree_type=m.SchoolTagRuleTag.DEGREE_HIGHEST,
            )
        m.SchoolTagRuleEducation.objects.create(
            rule=bachelor_rule, education=m.Candidate.EDUCATION_BACHELOR
        )
        m.SchoolTagRuleEducation.objects.create(
            rule=master_rule, education=m.Candidate.EDUCATION_MASTER
        )

        allocate.run(mode="rule")

        self.assertEqual(m.AssignmentAttempt.objects.get().matched_rule, master_rule)

    def test_missing_highest_education_blocks_ai_before_model_call(self):
        first_tag = m.SchoolTag.objects.create(code="MISS_FIRST", name="第一标签")
        highest_tag = m.SchoolTag.objects.create(code="MISS_HIGH", name="最高标签")
        self.candidate.first_degree_tag = first_tag
        self.candidate.highest_degree_tag = highest_tag
        self.candidate.highest_education = ""
        self.candidate.save(
            update_fields=["first_degree_tag", "highest_degree_tag", "highest_education"]
        )
        rule = m.SchoolTagRule.objects.create(name="限制学历", priority=1, is_active=True)
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=first_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_FIRST,
        )
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=highest_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_HIGHEST,
        )
        m.SchoolTagRuleEducation.objects.create(
            rule=rule, education=m.Candidate.EDUCATION_MASTER
        )

        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume"
        ) as screen_resume:
            allocate.run(mode="ai")

        screen_resume.assert_not_called()
        self.candidate.workflow.refresh_from_db()
        self.assertIn("最高学历缺失", self.candidate.workflow.archive_detail)

    def test_disallowed_highest_education_records_explicit_archive_detail(self):
        first_tag = m.SchoolTag.objects.create(code="NO_FIRST", name="第一标签")
        highest_tag = m.SchoolTag.objects.create(code="NO_HIGH", name="最高标签")
        self.candidate.first_degree_tag = first_tag
        self.candidate.highest_degree_tag = highest_tag
        self.candidate.highest_education = m.Candidate.EDUCATION_ASSOCIATE
        self.candidate.save(
            update_fields=["first_degree_tag", "highest_degree_tag", "highest_education"]
        )
        rule = m.SchoolTagRule.objects.create(name="仅本科", priority=1, is_active=True)
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=first_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_FIRST,
        )
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=highest_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_HIGHEST,
        )
        m.SchoolTagRuleEducation.objects.create(
            rule=rule, education=m.Candidate.EDUCATION_BACHELOR
        )

        allocate.run(mode="rule")

        self.candidate.workflow.refresh_from_db()
        self.assertIn("不在", self.candidate.workflow.archive_detail)
        self.assertIn("允许范围", self.candidate.workflow.archive_detail)

    def test_school_gate_does_not_fall_back_to_legacy_platform_text(self):
        first_tag = m.SchoolTag.objects.create(code="A", name="平台A")
        highest_tag = m.SchoolTag.objects.create(code="A_PLUS", name="平台A+")
        rule = m.SchoolTagRule.objects.create(name="目标院校", priority=1, is_active=True)
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=first_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_FIRST,
        )
        m.SchoolTagRuleTag.objects.create(
            rule=rule,
            school_tag=highest_tag,
            degree_type=m.SchoolTagRuleTag.DEGREE_HIGHEST,
        )

        allocate.run(mode="rule")

        self.assertFalse(m.AssignmentAttempt.objects.exists())
        self.candidate.workflow.refresh_from_db()
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_SCHOOL_RULE_NOT_MATCHED,
        )

    def test_ai_allocation_uses_configured_review_threshold(self):
        m.Config.objects.create(key="ai_dispatch_threshold", value=0.8)
        m.Config.objects.create(key="ai_review_threshold", value=0.5)

        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ):
            allocate.run(mode="ai")

        attempt = m.AssignmentAttempt.objects.get()
        decision = m.AgentDispatchDecision.objects.get()
        self.assertEqual(attempt.source, m.AssignmentAttempt.SOURCE_AI)
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_PENDING_REVIEW)
        self.assertTrue(attempt.review_required)
        self.assertEqual(decision.recommendation, m.AgentDispatchDecision.RECOMMEND_REVIEW)
        self.assertGreaterEqual(decision.confidence_score, 0.5)
        self.assertLess(decision.confidence_score, 0.8)
        self.assertEqual(decision.recommended_department, self.department)

    def test_ai_allocation_only_passes_current_volunteer_job_to_model(self):
        unrelated_job = m.Job.objects.create(
            department=self.department,
            public_name="产品经理",
            position_name="产品经理",
            category="产品类",
            headcount=1,
        )
        m.JobMajor.objects.create(job=unrelated_job, major="工商管理")

        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ) as mocked:
            allocate.run(mode="ai")

        mocked.assert_called_once()
        self.assertEqual(mocked.call_args.args[0], self.resume)
        self.assertEqual(mocked.call_args.args[1], self.job)

    def test_direct_ai_allocation_ignores_rule_major_filter(self):
        self.candidate.highest_major = "计算机科学与技术"
        self.candidate.save(update_fields=["highest_major"])
        m.JobMajor.objects.create(job=self.job, major="电气工程")

        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ) as screen_resume:
            allocate.run(mode="ai")

        screen_resume.assert_called_once()
        attempt = m.AssignmentAttempt.objects.get()
        decision = m.AgentDispatchDecision.objects.get()
        self.resume.refresh_from_db()
        self.candidate.workflow.refresh_from_db()
        self.assertEqual(attempt.source, m.AssignmentAttempt.SOURCE_AI)
        self.assertEqual(decision.error_code, "")
        self.assertEqual(self.resume.category_mode, "ai")
        self.assertNotIn("major_not_matched", self.resume.category_reason)
        self.assertNotIn("major_not_matched", self.candidate.workflow.archive_detail)

    def test_ai_current_job_lookup_does_not_load_unrelated_job_majors(self):
        for index in range(5):
            unrelated_job = m.Job.objects.create(
                department=self.department,
                public_name=f"无关岗位{index}",
                position_name=f"无关岗位{index}",
                category="其它类",
                headcount=1,
            )
            m.JobMajor.objects.create(job=unrelated_job, major=f"无关专业{index}")

        with self.assertNumQueries(1):
            job_pool, _mapping = allocate._mapped_job_pool(self.resume, mode="ai")
            current_job = job_pool[0]
        with self.assertNumQueries(1):
            from apps.pipeline.agent_kernel.task_contracts import job_content
            context = job_content(current_job)

        self.assertEqual(current_job, self.job)
        self.assertEqual(context["public_name"], self.job.public_name)
        self.assertNotIn("id", context)
        self.assertEqual(context["required_majors"], [])

    def test_ai_failure_keeps_current_volunteer_and_never_skips_to_next(self):
        m.Resume.objects.create(
            candidate=self.candidate,
            apply_id="A1002",
            position_name="测试工程师",
            volunteer_rank=2,
            resume_file="张三（A1002）.pdf",
        )
        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            side_effect=AIServiceError("llm_error", "模型服务不可用"),
        ) as mocked:
            allocate.run(mode="ai")

        self.candidate.workflow.refresh_from_db()
        self.assertEqual(mocked.call_count, 1)
        self.assertEqual(mocked.call_args.args[0], self.resume)
        self.assertEqual(self.candidate.workflow.current_resume, self.resume)
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_AGENT_NO_RECOMMENDATION,
        )

    def test_ai_archive_detail_identifies_missing_secondary_department(self):
        self.job.department = None
        self.job.save(update_fields=["department"])
        with patch("apps.pipeline.services.allocate.agent_gateway.evaluate_resume") as mocked:
            allocate.run(mode="ai")

        self.candidate.workflow.refresh_from_db()
        decision = m.AgentDispatchDecision.objects.get()
        mocked.assert_not_called()
        self.assertEqual(decision.error_code, "reference_not_found")
        self.assertIn("后端工程师", decision.error_message)
        self.assertIn("未配置有效二级部门", self.candidate.workflow.archive_detail)

    def test_ai_archive_detail_identifies_missing_job_requirement(self):
        self.job.is_active = False
        self.job.save(update_fields=["is_active"])
        with patch("apps.pipeline.services.allocate.agent_gateway.evaluate_resume") as mocked:
            allocate.run(mode="ai")

        self.candidate.workflow.refresh_from_db()
        decision = m.AgentDispatchDecision.objects.get()
        mocked.assert_not_called()
        self.assertEqual(decision.error_code, "guardrail_blocked")
        self.assertIn("后端工程师", decision.error_message)
        self.assertIn("岗位需求中未配置", self.candidate.workflow.archive_detail)

    def test_ai_allocation_allows_department_without_active_contacts(self):
        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ) as mocked:
            allocate.run(mode="ai")

        attempt = m.AssignmentAttempt.objects.get()
        decision = m.AgentDispatchDecision.objects.get()
        mocked.assert_called_once()
        self.assertEqual(attempt.initial_department, self.department)
        self.assertEqual(attempt.current_department, self.department)
        self.assertEqual(decision.recommended_department, self.department)
        self.assertEqual(decision.error_code, "")

    def test_cancel_ai_review_archives_when_no_other_active_attempt(self):
        m.Config.objects.create(key="ai_dispatch_threshold", value=0.8)
        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ):
            allocate.run(mode="ai")
        attempt = m.AssignmentAttempt.objects.get()

        allocate.cancel_attempt(attempt, "hr_cancelled_review")

        attempt.refresh_from_db()
        self.candidate.workflow.refresh_from_db()
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_CANCELLED)
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_AGENT_NO_RECOMMENDATION,
        )

    def test_manual_direct_to_tertiary_records_two_department_events(self):
        tertiary_department = m.Department.objects.create(
            name="平台组", level=3, parent=self.department
        )
        user = User.objects.create_user(username="hr-direct", password="pass")

        attempt = allocate.manual_assign(self.resume, tertiary_department, user=user)

        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_DISPATCHED)
        self.assertEqual(attempt.initial_department, self.department)
        self.assertEqual(attempt.current_department, tertiary_department)
        self.assertEqual(attempt.initial_department_name_snapshot, "技术部")
        self.assertEqual(attempt.current_department_name_snapshot, "平台组")
        events = list(attempt.handling_events.all())
        self.assertEqual(
            [event.event_type for event in events],
            [
                m.AssignmentHandlingEvent.EVENT_ATTEMPT_CREATED,
                m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED,
                m.AssignmentHandlingEvent.EVENT_DEPARTMENT_TRANSFERRED,
            ],
        )
        self.assertEqual(events[1].to_department, self.department)
        self.assertFalse(events[1].is_system_auto)
        self.assertEqual(events[2].from_department, self.department)
        self.assertEqual(events[2].to_department, tertiary_department)
        self.assertTrue(events[2].is_system_auto)

    def test_dispatch_attempt_records_department_event_snapshots(self):
        allocate.run(mode="rule")
        attempt = m.AssignmentAttempt.objects.get()
        user = User.objects.create_user(username="hr-snapshot", password="pass")

        allocate.dispatch_attempt(attempt, user=user)

        attempt.refresh_from_db()
        dispatch_event = attempt.handling_events.get(
            event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED
        )
        self.assertEqual(attempt.initial_department_name_snapshot, "技术部")
        self.assertEqual(attempt.current_department_name_snapshot, "技术部")
        self.assertEqual(dispatch_event.to_department_name_snapshot, "技术部")
        self.assertEqual(dispatch_event.actor_username_snapshot, "hr-snapshot")
        self.assertEqual(
            dispatch_event.metadata["welink"]["skipped_reason"],
            "welink_disabled",
        )

    def test_dispatch_to_department_without_active_contact_is_not_blocked(self):
        m.Config.objects.update_or_create(
            key="welink_enabled", defaults={"value": True}
        )
        allocate.run(mode="rule")
        attempt = m.AssignmentAttempt.objects.get()

        allocate.dispatch_attempt(attempt)

        attempt.refresh_from_db()
        dispatch_event = attempt.handling_events.get(
            event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED
        )
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_DISPATCHED)
        self.assertEqual(dispatch_event.metadata["welink"]["recipient_count"], 0)
        self.assertEqual(
            dispatch_event.metadata["welink"]["skipped_reason"],
            "no_active_recipient",
        )

    def test_transfer_attempt_updates_current_department_and_preserves_initial(self):
        target_department = m.Department.objects.create(name="产品部", level=2)
        allocate.run(mode="rule")
        attempt = m.AssignmentAttempt.objects.get()
        allocate.dispatch_attempt(attempt)

        allocate.transfer_attempt(attempt, target_department, note="业务调整")

        attempt.refresh_from_db()
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_DISPATCHED)
        self.assertEqual(attempt.initial_department, self.department)
        self.assertEqual(attempt.current_department, target_department)
        self.assertEqual(attempt.current_department_name_snapshot, "产品部")
        transfer_event = attempt.handling_events.get(
            event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_TRANSFERRED
        )
        self.assertEqual(transfer_event.from_department, self.department)
        self.assertEqual(transfer_event.to_department, target_department)
        self.assertEqual(transfer_event.note, "业务调整")

    def test_stale_transfer_loses_after_current_department_changes(self):
        first_target = m.Department.objects.create(name="产品部", level=2)
        stale_target = m.Department.objects.create(name="市场部", level=2)
        allocate.run(mode="rule")
        attempt = m.AssignmentAttempt.objects.get()
        allocate.dispatch_attempt(attempt)
        first_writer = m.AssignmentAttempt.objects.get(pk=attempt.pk)
        stale_writer = m.AssignmentAttempt.objects.get(pk=attempt.pk)

        allocate.transfer_attempt(first_writer, first_target)
        with self.assertRaisesRegex(
            allocate.AttemptStateChanged, "当前接收部门已变更"
        ):
            allocate.transfer_attempt(stale_writer, stale_target)

        attempt.refresh_from_db()
        self.assertEqual(attempt.current_department, first_target)
        self.assertEqual(
            attempt.handling_events.filter(
                event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_TRANSFERRED
            ).count(),
            1,
        )

    def test_stale_feedback_loses_after_department_transfer(self):
        target_department = m.Department.objects.create(name="产品部", level=2)
        allocate.run(mode="rule")
        attempt = m.AssignmentAttempt.objects.get()
        allocate.dispatch_attempt(attempt)
        transfer_writer = m.AssignmentAttempt.objects.get(pk=attempt.pk)
        stale_feedback_writer = m.AssignmentAttempt.objects.get(pk=attempt.pk)

        allocate.transfer_attempt(transfer_writer, target_department)
        with self.assertRaisesRegex(
            allocate.AttemptStateChanged, "当前接收部门已变更"
        ):
            allocate.submit_feedback(
                stale_feedback_writer,
                m.AssignmentAttempt.FEEDBACK_PASSED,
            )

        attempt.refresh_from_db()
        self.assertEqual(attempt.current_department, target_department)
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_DISPATCHED)
        self.assertIsNone(attempt.feedback_at)
        self.assertFalse(
            attempt.handling_events.filter(
                event_type__in=[
                    m.AssignmentHandlingEvent.EVENT_FEEDBACK_PASSED,
                    m.AssignmentHandlingEvent.EVENT_FEEDBACK_REJECTED,
                ]
            ).exists()
        )

    def test_intermediate_event_department_cannot_be_deleted(self):
        intermediate_department = m.Department.objects.create(name="中间部门", level=2)
        final_department = m.Department.objects.create(name="最终部门", level=2)
        allocate.run(mode="rule")
        attempt = m.AssignmentAttempt.objects.get()
        attempt = allocate.dispatch_attempt(attempt)
        attempt = allocate.transfer_attempt(attempt, intermediate_department)
        allocate.transfer_attempt(attempt, final_department)

        with self.assertRaises(ProtectedError):
            intermediate_department.delete()

    def test_department_rejection_advances_to_next_volunteer(self):
        second_resume = m.Resume.objects.create(
            candidate=self.candidate,
            apply_id="A1002",
            position_name="后端工程师",
            volunteer_rank=2,
            resume_file="张三（A1002）.pdf",
        )
        allocate.run(mode="rule")
        first_attempt = m.AssignmentAttempt.objects.get()
        allocate.dispatch_attempt(first_attempt)

        with patch(
            "apps.pipeline.agent_kernel.client.AgentKernelClient.execute",
            side_effect=analysis_response,
        ), self.captureOnCommitCallbacks(execute=True), patch(
            "apps.pipeline.services.allocate.agent_gateway.is_agent_ready", return_value=True,
        ):
            allocate.submit_feedback(
                first_attempt,
            m.AssignmentAttempt.FEEDBACK_REJECTED,
            "二级判断不匹配",
            reason_code=m.AssignmentAttempt.REJECTION_REASON_KEY_CAPABILITY_MISMATCH,
        )

        first_attempt.refresh_from_db()
        next_attempt = m.AssignmentAttempt.objects.exclude(pk=first_attempt.pk).get()
        self.assertEqual(first_attempt.status, m.AssignmentAttempt.STATUS_REJECTED)
        self.assertEqual(first_attempt.feedback_note, "二级判断不匹配")
        self.assertEqual(
            first_attempt.feedback_reason_code,
            m.AssignmentAttempt.REJECTION_REASON_KEY_CAPABILITY_MISMATCH,
        )
        self.assertEqual(first_attempt.feedback_reason_label_snapshot, "关键能力不匹配")
        feedback_event = first_attempt.handling_events.get(
            event_type=m.AssignmentHandlingEvent.EVENT_FEEDBACK_REJECTED
        )
        self.assertEqual(
            feedback_event.metadata["reason_code"],
            m.AssignmentAttempt.REJECTION_REASON_KEY_CAPABILITY_MISMATCH,
        )
        self.assertEqual(next_attempt.resume, second_resume)
        self.assertEqual(next_attempt.status, m.AssignmentAttempt.STATUS_PENDING_DISPATCH)

    def test_rejected_feedback_requires_reason_and_other_requires_note(self):
        allocate.run(mode="rule")
        attempt = m.AssignmentAttempt.objects.get()
        allocate.dispatch_attempt(attempt)

        with self.assertRaisesMessage(ValueError, "必须选择有效的反馈原因"):
            allocate.submit_feedback(
                attempt,
                m.AssignmentAttempt.FEEDBACK_REJECTED,
            )
        with self.assertRaisesMessage(ValueError, "必须填写备注"):
            allocate.submit_feedback(
                attempt,
                m.AssignmentAttempt.FEEDBACK_REJECTED,
                reason_code=m.AssignmentAttempt.REJECTION_REASON_OTHER,
            )

        attempt.refresh_from_db()
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_DISPATCHED)
        self.assertFalse(
            attempt.handling_events.filter(
                event_type=m.AssignmentHandlingEvent.EVENT_FEEDBACK_REJECTED
            ).exists()
        )

    def test_ai_model_versions_come_from_backend_config(self):
        ai_config.save_ai_connection_config(
            {
                "api_style": "responses",
                "model_name": "gpt-test",
                "base_url": "https://model.internal/v1",
                "api_key": "test-key",
            }
        )
        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ):
            allocate.run(mode="ai")

        decision = m.AgentDispatchDecision.objects.get()
        self.assertEqual(decision.model_name, "gpt-test")
        self.assertEqual(decision.prompt_version, "")  # 未提供实际 Kernel pin 的测试结果不能伪造版本。
        self.assertEqual(decision.decision_version, "")

    def test_ai_runtime_config_uses_database_overrides(self):
        m.Config.objects.create(key="ai_timeout_seconds", value=120)
        m.Config.objects.create(key="ai_retry_count", value=3)

        config = ai_config.get_ai_runtime_config()

        self.assertEqual(config.timeout_seconds, 120)
        self.assertEqual(config.retry_count, 3)
        self.assertEqual(config.dispatch_threshold, 0.75)

    def test_ai_allocation_records_pdf_missing_without_assignment_attempt(self):
        self.resume.resume_file = ""
        self.resume.save(update_fields=["resume_file"])
        run = m.ProcessingRun.objects.create(step="step2", mode="ai")
        m.ProcessingRunJobCapacity.objects.create(
            run=run,
            job=self.job,
            headcount_snapshot=self.job.headcount,
            capacity=self.job.headcount,
        )

        allocate.run(mode="ai", processing_run=run)

        self.assertFalse(m.AssignmentAttempt.objects.exists())
        decision = m.AgentDispatchDecision.objects.get()
        self.assertEqual(decision.processing_run, run)
        self.assertIsNone(decision.recommendation)
        self.assertIsNone(decision.confidence_score)
        self.assertEqual(decision.error_code, "agent_snapshot_unavailable")
        self.assertIn("冻结快照", decision.error_message)
        self.assertEqual(decision.resume, self.resume)
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_AGENT_NO_RECOMMENDATION,
        )
        self.assertEqual(self.candidate.workflow.current_resume, self.resume)
        self.assertEqual(self.candidate.workflow.current_rank, 1)

    def test_rerun_cancels_pending_review_ai_attempts(self):
        m.Config.objects.create(key="ai_dispatch_threshold", value=0.8)
        m.Config.objects.create(key="ai_review_threshold", value=0.5)
        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ):
            allocate.run(mode="ai")
        first_attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(first_attempt.status, m.AssignmentAttempt.STATUS_PENDING_REVIEW)

        with patch(
            "apps.pipeline.services.allocate.agent_gateway.evaluate_resume",
            return_value=self._ai_result(),
        ):
            allocate.run(mode="ai")

        first_attempt.refresh_from_db()
        self.assertEqual(first_attempt.status, m.AssignmentAttempt.STATUS_CANCELLED)
        self.assertEqual(first_attempt.cancel_reason, m.AssignmentAttempt.CANCEL_RERUN)
        self.assertEqual(
            m.AssignmentAttempt.objects.filter(
                status=m.AssignmentAttempt.STATUS_PENDING_REVIEW
            ).count(),
            1,
        )

    def test_archived_workflow_keeps_last_attempted_resume_when_job_not_matched(self):
        self.job.is_active = False
        self.job.save(update_fields=["is_active"])

        allocate.run(mode="rule")

        self.candidate.workflow.refresh_from_db()
        self.assertEqual(
            self.candidate.workflow.status,
            m.CandidateWorkflow.STATUS_ARCHIVED,
        )
        self.assertEqual(
            self.candidate.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_JOB_NOT_MATCHED,
        )
        self.assertEqual(self.candidate.workflow.current_resume, self.resume)
        self.assertEqual(self.candidate.workflow.current_rank, 1)

    def test_scoped_reprocess_reopens_only_selected_system_statuses(self):
        allocate.run(mode="rule")
        passed_attempt = m.AssignmentAttempt.objects.get()
        allocate.dispatch_attempt(passed_attempt)
        tertiary_department = m.Department.objects.create(
            name="技术三部", level=3, parent=self.department
        )
        passed_attempt = allocate.transfer_attempt(passed_attempt, tertiary_department)
        allocate.submit_feedback(passed_attempt, m.AssignmentAttempt.FEEDBACK_PASSED)
        self.candidate.workflow.refresh_from_db()
        self.assertEqual(self.candidate.workflow.status, m.CandidateWorkflow.STATUS_PASSED)

        other_candidate = m.Candidate.objects.create(
            identity_hash="candidate-unselected",
            name="李四",
            phone="13900000000",
            first_degree_platform="平台A",
            highest_degree_platform="平台A",
        )
        m.Resume.objects.create(
            candidate=other_candidate,
            apply_id="B1001",
            position_name="后端工程师",
            volunteer_rank=1,
            resume_file="李四（B1001）.pdf",
        )
        filtered_passed_candidate = m.Candidate.objects.create(
            identity_hash="candidate-filtered-passed",
            name="王五",
            phone="13700000000",
            first_degree_platform="平台A",
            highest_degree_platform="平台A",
        )
        filtered_passed_resume = m.Resume.objects.create(
            candidate=filtered_passed_candidate,
            apply_id="C1001",
            position_name="后端工程师",
            volunteer_rank=1,
            resume_file="王五（C1001）.pdf",
        )
        m.CandidateWorkflow.objects.create(
            candidate=filtered_passed_candidate,
            status=m.CandidateWorkflow.STATUS_PASSED,
            current_resume=filtered_passed_resume,
            current_rank=1,
        )
        same_name_raw_candidate = m.Candidate.objects.create(
            identity_hash="candidate-same-name-raw",
            name="张三",
            phone="13600000000",
            first_degree_platform="平台A",
            highest_degree_platform="平台A",
        )
        m.Resume.objects.create(
            candidate=same_name_raw_candidate,
            apply_id="D1001",
            position_name="后端工程师",
            volunteer_rank=1,
            resume_file="张三（D1001）.pdf",
        )

        message = allocate.run(
            mode="rule",
            scope={
                "system_statuses": ["screening_passed"],
                "candidate_filters": {"name": "张三"},
            },
        )

        self.candidate.workflow.refresh_from_db()
        self.assertEqual(self.candidate.workflow.status, m.CandidateWorkflow.STATUS_IN_PROGRESS)
        self.assertIsNone(self.candidate.workflow.passed_attempt_id)
        self.assertEqual(
            m.AssignmentAttempt.objects.filter(workflow=self.candidate.workflow).count(),
            2,
        )
        self.assertFalse(hasattr(other_candidate, "workflow"))
        filtered_passed_candidate.workflow.refresh_from_db()
        self.assertEqual(
            filtered_passed_candidate.workflow.status,
            m.CandidateWorkflow.STATUS_PASSED,
        )
        self.assertFalse(
            m.AssignmentAttempt.objects.filter(
                workflow=filtered_passed_candidate.workflow
            ).exists()
        )
        self.assertFalse(hasattr(same_name_raw_candidate, "workflow"))
        self.assertIn("已生成 1 条候选人分配尝试", message)

    def test_force_reprocess_selected_reopens_all_system_statuses(self):
        status_candidates = [("raw", self.candidate, self.resume)]

        def create_candidate(code, *, job_category=""):
            candidate = m.Candidate.objects.create(
                identity_hash=f"candidate-force-{code}",
                name=f"候选人-{code}",
                phone=f"1390000{len(status_candidates):04d}",
                first_degree_platform="平台A",
                highest_degree_platform="平台A",
            )
            resume = m.Resume.objects.create(
                candidate=candidate,
                apply_id=f"FORCE-{code}",
                position_name="后端工程师",
                volunteer_rank=1,
                job_category=job_category,
                resume_file=f"{code}.pdf",
            )
            status_candidates.append((code, candidate, resume))
            return candidate, resume

        archived, archived_resume = create_candidate(
            "archived", job_category="技术类"
        )
        pending_reallocation, pending_reallocation_resume = create_candidate(
            "pending_reallocation", job_category="技术类"
        )
        pending_review, pending_review_resume = create_candidate("pending_review")
        pending_dispatch, pending_dispatch_resume = create_candidate("pending_dispatch")
        pending_screening, pending_screening_resume = create_candidate(
            "pending_screening"
        )
        passed, passed_resume = create_candidate("screening_passed")
        rejected, rejected_resume = create_candidate("screening_rejected")

        def create_attempt(candidate, resume, status, *, workflow_status="in_progress"):
            workflow = m.CandidateWorkflow.objects.create(
                candidate=candidate,
                status=workflow_status,
                current_resume=resume,
                current_rank=1,
            )
            attempt = m.AssignmentAttempt.objects.create(
                workflow=workflow,
                resume=resume,
                attempt_no=1,
                source=m.AssignmentAttempt.SOURCE_RULE,
                status=status,
                initial_department=self.department,
                current_department=self.department,
            )
            if workflow_status == m.CandidateWorkflow.STATUS_PASSED:
                workflow.passed_attempt = attempt
                workflow.save(update_fields=["passed_attempt"])
            return workflow, attempt

        m.CandidateWorkflow.objects.create(
            candidate=archived,
            status=m.CandidateWorkflow.STATUS_IN_PROGRESS,
            current_resume=archived_resume,
            current_rank=1,
            started_at=timezone.now(),
        )
        m.CandidateWorkflow.objects.create(
            candidate=pending_reallocation,
            status=m.CandidateWorkflow.STATUS_IN_PROGRESS,
            current_resume=pending_reallocation_resume,
            current_rank=1,
            started_at=timezone.now(),
            block_reason=m.CandidateWorkflow.BLOCK_JOB_HC_EXHAUSTED,
            block_detail="当前任务岗位 HC 容量已用尽",
        )
        create_attempt(
            pending_review,
            pending_review_resume,
            m.AssignmentAttempt.STATUS_PENDING_REVIEW,
        )
        create_attempt(
            pending_dispatch,
            pending_dispatch_resume,
            m.AssignmentAttempt.STATUS_PENDING_DISPATCH,
        )
        create_attempt(
            pending_screening,
            pending_screening_resume,
            m.AssignmentAttempt.STATUS_DISPATCHED,
        )
        create_attempt(
            passed,
            passed_resume,
            m.AssignmentAttempt.STATUS_PASSED,
            workflow_status=m.CandidateWorkflow.STATUS_PASSED,
        )
        create_attempt(
            rejected,
            rejected_resume,
            m.AssignmentAttempt.STATUS_REJECTED,
            workflow_status=m.CandidateWorkflow.STATUS_ARCHIVED,
        )

        actual_statuses = {
            code: system_status.candidate_system_status(
                m.Candidate.objects.prefetch_related(
                    "resumes", "workflow__attempts"
                ).get(pk=candidate.pk)
            )
            for code, candidate, _resume in status_candidates
        }
        self.assertEqual(set(actual_statuses.values()), set(system_status.LABELS))

        candidate_ids = [candidate.id for _code, candidate, _resume in status_candidates]
        allocate.run(
            mode="rule",
            scope={"candidate_ids": candidate_ids, "force_reprocess": True},
        )

        self.assertEqual(
            m.CandidateWorkflow.objects.filter(
                candidate_id__in=candidate_ids,
                dispatch_strategy="rule",
            ).count(),
            8,
        )
        for _code, candidate, _resume in status_candidates:
            candidate.workflow.refresh_from_db()
            self.assertEqual(
                candidate.workflow.status, m.CandidateWorkflow.STATUS_IN_PROGRESS
            )

    def test_force_reprocess_preserves_history_and_cancels_unfeedbacked_auto_attempts(self):
        allocate.run(mode="rule")
        passed_attempt = m.AssignmentAttempt.objects.get()
        allocate.dispatch_attempt(passed_attempt)
        allocate.submit_feedback(
            passed_attempt,
            m.AssignmentAttempt.FEEDBACK_PASSED,
            "业务确认通过",
        )
        workflow = self.candidate.workflow
        workflow.archive_reason = m.CandidateWorkflow.ARCHIVE_ALL_REJECTED
        workflow.archive_detail = "旧归档原因"
        workflow.block_reason = m.CandidateWorkflow.BLOCK_JOB_HC_EXHAUSTED
        workflow.block_detail = "旧阻塞原因"
        workflow.completed_at = timezone.now()
        workflow.save(
            update_fields=[
                "archive_reason",
                "archive_detail",
                "block_reason",
                "block_detail",
                "completed_at",
            ]
        )
        old_decision = m.AgentDispatchDecision.objects.create(
            workflow=workflow,
            resume=self.resume,
            recommendation=m.AgentDispatchDecision.RECOMMEND_REVIEW,
            summary="历史 AI 决策",
        )
        pending_auto = m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=self.resume,
            attempt_no=2,
            source=m.AssignmentAttempt.SOURCE_AI,
            status=m.AssignmentAttempt.STATUS_PENDING_REVIEW,
            initial_department=self.department,
            current_department=self.department,
        )
        manual_attempt = m.AssignmentAttempt.objects.create(
            workflow=workflow,
            resume=self.resume,
            attempt_no=3,
            source=m.AssignmentAttempt.SOURCE_MANUAL,
            status=m.AssignmentAttempt.STATUS_PENDING_DISPATCH,
            initial_department=self.department,
            current_department=self.department,
        )

        allocate.run(
            mode="rule",
            scope={"candidate_ids": [self.candidate.id], "force_reprocess": True},
        )

        workflow.refresh_from_db()
        passed_attempt.refresh_from_db()
        pending_auto.refresh_from_db()
        manual_attempt.refresh_from_db()
        self.assertEqual(workflow.status, m.CandidateWorkflow.STATUS_IN_PROGRESS)
        self.assertIsNone(workflow.passed_attempt_id)
        self.assertEqual(workflow.archive_reason, "")
        self.assertEqual(workflow.archive_detail, "")
        self.assertEqual(workflow.block_reason, "")
        self.assertEqual(workflow.block_detail, "")
        self.assertIsNone(workflow.completed_at)
        self.assertEqual(passed_attempt.feedback_result, m.AssignmentAttempt.FEEDBACK_PASSED)
        self.assertEqual(passed_attempt.feedback_note, "业务确认通过")
        self.assertEqual(pending_auto.status, m.AssignmentAttempt.STATUS_CANCELLED)
        self.assertEqual(pending_auto.cancel_reason, m.AssignmentAttempt.CANCEL_RERUN)
        self.assertEqual(manual_attempt.status, m.AssignmentAttempt.STATUS_PENDING_DISPATCH)
        self.assertTrue(m.AgentDispatchDecision.objects.filter(pk=old_decision.pk).exists())
        self.assertEqual(workflow.attempts.count(), 4)

    def test_resume_import_scope_does_not_reopen_terminal_workflow(self):
        allocate.run(mode="rule")
        passed_attempt = m.AssignmentAttempt.objects.get()
        allocate.dispatch_attempt(passed_attempt)
        allocate.submit_feedback(passed_attempt, m.AssignmentAttempt.FEEDBACK_PASSED)
        workflow = self.candidate.workflow
        passed_attempt_count = workflow.attempts.count()

        allocate.run(
            mode="rule",
            scope={
                "candidate_ids": [self.candidate.id],
                "source": "resume_import",
            },
        )

        workflow.refresh_from_db()
        passed_attempt.refresh_from_db()
        self.assertEqual(workflow.status, m.CandidateWorkflow.STATUS_PASSED)
        self.assertEqual(workflow.passed_attempt, passed_attempt)
        self.assertEqual(workflow.attempts.count(), passed_attempt_count)
        self.assertEqual(passed_attempt.status, m.AssignmentAttempt.STATUS_PASSED)


class JobCapacityAllocationTests(KernelTestCase):
    def setUp(self):
        ai_config.save_ai_connection_config(
            {
                "api_style": "responses",
                "model_name": "gpt-test",
                "base_url": "https://model.internal/v1",
                "api_key": "test-key",
            }
        )
        self.department_a = m.Department.objects.create(name="研发一部", level=2)
        ai_config.mark_ai_connection_tested()
        self.department_b = m.Department.objects.create(name="研发二部", level=2)
        self.job_a = m.Job.objects.create(
            entity="GW", department=self.department_a,
            public_name="软件开发工程师", position_name="软件开发",
            category="技术类", responsibilities="研发", headcount=2,
        )
        self.job_b = m.Job.objects.create(
            entity="GW", department=self.department_b,
            public_name="研发岗位", position_name="软件开发",
            category="技术类", responsibilities="研发", headcount=1,
        )

    def _candidate(self, index):
        candidate = m.Candidate.objects.create(
            identity_hash=f"hc-candidate-{index}", name=f"候选人{index}",
            phone=f"1381000{index:04d}",
        )
        resume = m.Resume.objects.create(
            candidate=candidate, apply_id=f"HC-{index}", entity="GW",
            position_name="软件开发工程师", volunteer_rank=1,
        )
        return candidate, resume

    def _agent_result(self, resume, job, *, department=None, **_kwargs):
        profile, _ = m.ResumeProfile.objects.get_or_create(
            resume=resume,
            defaults={"parse_status": "parsed", "raw_text": "候选人简历正文"},
        )
        decision = SimpleNamespace(
            recommendation="dispatch",
            summary="建议下发",
            reason="简历证据与当前岗位匹配",
            evidence=["候选人简历正文"],
            risks=[],
            ai_specialist_match=False,
            ai_specialist_confidence=0,
            ai_specialist_evidence=[],
        )
        return SimpleNamespace(
            profile=profile,
            output=SimpleNamespace(
                decision=decision,
                profile=SimpleNamespace(risk_flags=[]),
            ),
            job=job,
            department=department,
            confidence=0.9,
            score_breakdown={
                "major_match": 0.9,
                "skills_match": 0.9,
                "experience_evidence": 0.9,
                "job_requirement": 0.9,
                "resume_quality": 0.9,
            },
        )

    @patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute")
    def test_run_snapshot_distributes_by_hc_and_exhausted_candidate_reenters_new_run(
        self, mock_evaluate
    ):
        mock_evaluate.side_effect = analysis_response
        candidates = [self._candidate(index)[0] for index in range(1, 5)]
        run = runner.create_run(
            "step2",
            scope={"candidate_ids": [candidate.id for candidate in candidates]},
        )

        runner.execute_run(run.id)

        capacities = {
            item.job_id: (item.capacity, item.used_count)
            for item in run.job_capacities.all()
        }
        run.refresh_from_db()
        self.assertEqual(capacities[self.job_a.id], (2, 2), (run.message, list(run.scope_items.values("status", "result_message", "reason_code"))))
        self.assertEqual(capacities[self.job_b.id], (1, 1))
        assigned_job_ids = list(
            m.Resume.objects.filter(candidate__in=candidates[:3])
            .order_by("candidate_id").values_list("job_id", flat=True)
        )
        self.assertEqual(
            assigned_job_ids, [self.job_a.id, self.job_a.id, self.job_b.id]
        )

        exhausted = candidates[3]
        self.assertFalse(exhausted.workflow.attempts.exists())
        self.assertEqual(
            system_status.candidate_system_status(exhausted),
            system_status.PENDING_REALLOCATION,
        )
        self.assertEqual(
            run.scope_items.get(candidate=exhausted).reason_code,
            "job_hc_exhausted",
        )

        rerun = runner.create_run(
            "step2",
            scope={"candidate_ids": [exhausted.id], "force_reprocess": True},
        )
        runner.execute_run(rerun.id)
        self.assertEqual(exhausted.workflow.attempts.count(), 1)
        self.assertEqual(
            system_status.candidate_system_status(exhausted),
            system_status.PENDING_DISPATCH,
        )

    @patch("apps.pipeline.services.allocate.agent_gateway.evaluate_resume")
    def test_capacity_release_and_manual_assignment_bypass(self, mock_evaluate):
        mock_evaluate.side_effect = self._agent_result
        self.job_b.is_active = False
        self.job_b.save(update_fields=["is_active"])
        first, _first_resume = self._candidate(10)
        run = runner.create_run(
            "step2", scope={"candidate_ids": [first.id]}
        )
        runner.execute_run(run.id)
        attempt = first.workflow.attempts.get()
        capacity = attempt.capacity_reservation
        self.assertEqual(capacity.used_count, 1)

        allocate.cancel_attempt(attempt)
        capacity.refresh_from_db()
        attempt.refresh_from_db()
        self.assertEqual(capacity.used_count, 0)
        self.assertIsNotNone(attempt.capacity_released_at)

        _second, second_resume = self._candidate(11)
        manual_attempt = allocate.manual_assign(second_resume, self.department_a)
        self.assertIsNone(manual_attempt.capacity_reservation_id)

    def test_agent_result_never_switches_to_a_job_it_did_not_evaluate(self):
        candidate, resume = self._candidate(12)
        run = runner.create_run("step2", scope={"candidate_ids": [candidate.id]})
        capacity_a = run.job_capacities.get(job=self.job_a)
        capacity_a.used_count = capacity_a.capacity
        capacity_a.save(update_fields=["used_count"])
        workflow = m.CandidateWorkflow.objects.create(
            candidate=candidate,
            status=m.CandidateWorkflow.STATUS_IN_PROGRESS,
            current_resume=resume,
            current_rank=1,
        )
        workflow._processing_run = run

        with transaction.atomic():
            attempt = allocate._apply_ai_result(
                workflow,
                resume,
                matched_rule=None,
                result=self._agent_result(
                    resume,
                    self.job_a,
                    department=self.department_a,
                ),
            )

        workflow.refresh_from_db()
        resume.refresh_from_db()
        decision = workflow.agent_decisions.get()
        self.assertIsNone(attempt)
        self.assertEqual(workflow.block_reason, m.CandidateWorkflow.BLOCK_JOB_HC_EXHAUSTED)
        self.assertEqual(resume.job, self.job_a)
        self.assertEqual(decision.evaluated_job, self.job_a)
        self.assertEqual(decision.recommended_job, self.job_a)
        self.assertEqual(run.job_capacities.get(job=self.job_b).used_count, 0)

    def test_ambiguous_and_missing_internal_position_are_archived_with_codes(self):
        self.job_b.public_name = "软件开发工程师"
        self.job_b.position_name = "另一个内部职位"
        self.job_b.save(update_fields=["public_name", "position_name"])
        ambiguous, _resume = self._candidate(20)
        ambiguous_run = runner.create_run(
            "step2", scope={"candidate_ids": [ambiguous.id]}
        )
        runner.execute_run(ambiguous_run.id)
        self.assertEqual(
            ambiguous.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_JOB_MAPPING_AMBIGUOUS,
        )
        self.assertEqual(
            ambiguous_run.scope_items.get(candidate=ambiguous).reason_code,
            "job_mapping_ambiguous",
        )

        self.job_b.is_active = False
        self.job_b.save(update_fields=["is_active"])
        self.job_a.position_name = ""
        self.job_a.save(update_fields=["position_name"])
        missing, _resume = self._candidate(21)
        missing_run = runner.create_run(
            "step2", scope={"candidate_ids": [missing.id]}
        )
        runner.execute_run(missing_run.id)
        self.assertEqual(
            missing.workflow.archive_reason,
            m.CandidateWorkflow.ARCHIVE_INTERNAL_POSITION_NAME_MISSING,
        )
        self.assertEqual(
            missing_run.scope_items.get(candidate=missing).reason_code,
            "internal_position_name_missing",
        )
