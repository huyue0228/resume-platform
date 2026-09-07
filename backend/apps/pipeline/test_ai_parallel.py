from types import SimpleNamespace
from unittest.mock import patch

from celery.exceptions import Retry
from django.test import SimpleTestCase, TestCase, override_settings

from apps.core import models as m
from apps.pipeline import ai_config, runner
from apps.pipeline.ai import concurrency
from apps.pipeline.services import allocate
from apps.pipeline.tasks import dispatch_ai_run_task


class _FakeRedis:
    """只实现控制器测试所需的 Lua 语义；生产原子性由 Redis 脚本提供。"""

    def __init__(self):
        self.limit = 0
        self.successes = 0
        self.in_flight = set()
        self.blocked_until = 0

    def eval(self, script, _key_count, _leases_key, _state_key, *args):
        if script == concurrency._ACQUIRE_SCRIPT:
            now, ceiling, lease_id, _expires_at = args
            self.limit = self.limit or min(2, int(ceiling))
            if now < self.blocked_until or len(self.in_flight) >= self.limit:
                return [0, self.limit, len(self.in_flight), 50]
            self.in_flight.add(lease_id)
            return [1, self.limit, len(self.in_flight), 0]
        now, ceiling, outcome, retry_after, backoff, lease_id = args
        self.in_flight.discard(lease_id)
        if outcome == "success":
            self.successes += 1
            if self.successes >= self.limit and self.limit < int(ceiling):
                self.limit = min(int(ceiling), self.limit * 2)
                self.successes = 0
        elif outcome == "rate_limit":
            self.limit = max(1, int(self.limit * 0.8))
            self.successes = 0
            self.blocked_until = now + max(float(retry_after), float(backoff), 1)
        return [self.limit, len(self.in_flight)]


@override_settings(CELERY_TASK_ALWAYS_EAGER=False)
class AdaptiveConcurrencyTests(SimpleTestCase):
    def setUp(self):
        self.model = SimpleNamespace(
            api_style="responses",
            base_url="https://model.internal/v1",
            model_name="test-model",
            api_key="secret",
        )
        self.runtime = SimpleNamespace(
            concurrency=8,
            timeout_seconds=60,
            retry_backoff_seconds=1,
        )

    def test_clean_windows_grow_and_rate_limit_reduces_shared_limit(self):
        fake = _FakeRedis()
        with patch.object(concurrency, "_redis_client", return_value=fake):
            first = concurrency.acquire_slot(self.model, self.runtime)
            first.release("success")
            second = concurrency.acquire_slot(self.model, self.runtime)
            second.release("success")
            self.assertEqual(fake.limit, 4)

            limited = concurrency.acquire_slot(self.model, self.runtime)
            limited.release("rate_limit", retry_after=2)

        self.assertEqual(fake.limit, 3)
        self.assertGreater(fake.blocked_until, 0)

    def test_resource_key_is_model_scoped_and_never_contains_secret(self):
        leases, state = concurrency._resource_key(self.model)

        self.assertIn("srf:ai-limit", leases)
        self.assertIn("srf:ai-limit", state)
        self.assertNotIn("secret", leases)
        other = SimpleNamespace(**{**self.model.__dict__, "model_name": "other"})
        self.assertNotEqual(concurrency._resource_key(other), (leases, state))


class AIParallelPipelineTests(TestCase):
    def setUp(self):
        ai_config.save_ai_connection_config(
            {
                "api_style": "responses",
                "model_name": "gpt-test",
                "base_url": "https://model.internal/v1",
                "api_key": "test-key",
            }
        )
        self.department = m.Department.objects.create(name="并发技术部", level=2)
        self.job = m.Job.objects.create(
            department=self.department,
            public_name="后端工程师",
            position_name="后端工程师",
            category="技术类",
            responsibilities="负责后端服务开发和稳定性建设。",
            headcount=100,
        )

    def _candidate(self, index):
        candidate = m.Candidate.objects.create(
            identity_hash=f"parallel-{index}",
            name=f"候选人{index}",
            phone=f"138000{index:05d}",
        )
        resume = m.Resume.objects.create(
            candidate=candidate,
            apply_id=f"PAR-{index}",
            position_name="后端工程师",
            volunteer_rank=1,
            resume_file=f"候选人{index}.pdf",
        )
        return candidate, resume

    def _ai_result(self, resume, *, specialist_confidence=0):
        profile = m.ResumeProfile.objects.create(
            resume=resume,
            parse_status="parsed",
            raw_text="候选人简历正文",
        )
        output = SimpleNamespace(
            decision=SimpleNamespace(
                recommendation="dispatch",
                summary="建议下发",
                reason="岗位匹配",
                evidence=["项目经历"],
                risks=[],
                ai_specialist_match=specialist_confidence > 0,
                ai_specialist_confidence=specialist_confidence,
                ai_specialist_evidence=(
                    ["参与企业知识库 RAG 检索增强项目"]
                    if specialist_confidence > 0
                    else []
                ),
            ),
            profile=SimpleNamespace(risk_flags=[]),
        )
        return SimpleNamespace(
            profile=profile,
            output=output,
            job=self.job,
            department=self.department,
            confidence=0.9,
            score_breakdown={},
        )

    def test_manual_revision_change_during_model_call_discards_ai_result(self):
        candidate, resume = self._candidate(1)
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )

        def change_workflow(*_args, **_kwargs):
            workflow = m.CandidateWorkflow.objects.get(candidate=candidate)
            workflow.status = m.CandidateWorkflow.STATUS_IN_PROGRESS
            workflow.save(update_fields=["status"])
            return self._ai_result(resume)

        with patch.object(
            allocate.ai_service,
            "screen_resume",
            side_effect=change_workflow,
        ):
            runner.execute_run(run.id)

        run.refresh_from_db()
        item = run.scope_items.get()
        self.assertEqual(item.status, "skipped_manual_change")
        self.assertEqual(run.skipped_count, 1)
        self.assertFalse(m.AssignmentAttempt.objects.exists())

    def test_eager_ai_run_uses_candidate_task_counters_and_default_ceiling(self):
        candidate, resume = self._candidate(88)
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )

        with patch.object(
            allocate.ai_service,
            "screen_resume",
            return_value=self._ai_result(resume),
        ):
            runner.execute_run(run.id)

        run.refresh_from_db()
        stage = run.stages.get(step="step4")
        self.assertEqual(run.status, "success")
        self.assertEqual(run.ai_concurrency_limit, 8)
        self.assertEqual(run.ai_effective_concurrency, 1)
        self.assertEqual((run.chunk_size, run.chunk_total, run.chunk_done), (1, 1, 1))
        self.assertEqual((run.processed_count, run.success_count), (1, 1))
        self.assertEqual((stage.processed_count, stage.success_count), (1, 1))
        self.assertEqual(m.AssignmentAttempt.objects.get().source, "ai")

    def test_formal_ai_run_ignores_rule_major_filter_and_persists_ai_result(self):
        candidate, resume = self._candidate(188)
        candidate.highest_major = "计算机科学与技术"
        candidate.save(update_fields=["highest_major"])
        m.JobMajor.objects.create(job=self.job, major="电气工程")
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )

        with patch.object(
            allocate.ai_service,
            "screen_resume",
            return_value=self._ai_result(resume),
        ) as screen_resume:
            runner.execute_run(run.id)

        screen_resume.assert_called_once()
        item = run.scope_items.get()
        resume.refresh_from_db()
        workflow = m.CandidateWorkflow.objects.get(candidate=candidate)
        self.assertEqual((item.result_type, item.reason_code), ("completed", "ai_dispatched"))
        self.assertEqual(resume.category_mode, "ai")
        self.assertTrue(m.AgentDispatchDecision.objects.filter(workflow=workflow).exists())
        self.assertTrue(m.AssignmentAttempt.objects.filter(workflow=workflow, source="ai").exists())
        self.assertNotEqual(item.reason_code, "major_not_matched")
        self.assertNotIn("major_not_matched", workflow.archive_detail)

    def test_only_timeout_is_failed_and_rate_limit_needs_attention(self):
        timeout_candidate, _ = self._candidate(89)
        limited_candidate, _ = self._candidate(90)
        run = runner.create_run(
            "step2",
            scope={"candidate_ids": [timeout_candidate.id, limited_candidate.id]},
        )

        def fail_by_candidate(resume, *_args, **_kwargs):
            if resume.candidate_id == timeout_candidate.id:
                raise allocate.ai_service.AIServiceError("llm_timeout", "模型超时")
            raise allocate.ai_service.AIServiceError("ai_rate_limited", "模型限流")

        with patch.object(
            allocate.ai_service,
            "screen_resume",
            side_effect=fail_by_candidate,
        ):
            runner.execute_run(run.id)

        run.refresh_from_db()
        timeout_item = run.scope_items.get(candidate=timeout_candidate)
        limited_item = run.scope_items.get(candidate=limited_candidate)
        self.assertEqual(
            (timeout_item.result_type, timeout_item.reason_code),
            (m.ProcessingRunScopeItem.RESULT_FAILED, "llm_timeout"),
        )
        self.assertEqual(
            (limited_item.result_type, limited_item.reason_code),
            (m.ProcessingRunScopeItem.RESULT_NEEDS_ATTENTION, "ai_rate_limited"),
        )
        self.assertEqual((run.failed_count, run.needs_attention_count), (1, 1))
        self.assertEqual(run.status, "partial_failed")

    def test_rule_precheck_business_gap_is_completed_without_calling_ai(self):
        candidate, _resume = self._candidate(91)
        self.job.is_active = False
        self.job.save(update_fields=["is_active"])
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )

        with patch.object(allocate.ai_service, "screen_resume") as screen:
            runner.execute_run(run.id)

        item = run.scope_items.get()
        self.assertEqual(
            (item.result_type, item.reason_code),
            (m.ProcessingRunScopeItem.RESULT_COMPLETED, "job_not_found"),
        )
        screen.assert_not_called()

    def test_missing_job_responsibility_is_needs_attention_without_model_work(self):
        candidate, _resume = self._candidate(96)
        self.job.responsibilities = ""
        self.job.save(update_fields=["responsibilities"])
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )

        with patch.object(allocate.ai_service, "_extract_pdf") as extract_pdf, patch.object(
            allocate.ai_service, "_call_model"
        ) as call_model:
            runner.execute_run(run.id)

        run.refresh_from_db()
        item = run.scope_items.get()
        self.assertEqual(
            (item.result_type, item.reason_code),
            (
                m.ProcessingRunScopeItem.RESULT_NEEDS_ATTENTION,
                "job_responsibility_missing",
            ),
        )
        self.assertEqual(run.needs_attention_count, 1)
        self.assertEqual(run.status, "needs_attention")
        extract_pdf.assert_not_called()
        call_model.assert_not_called()

    def test_school_gate_uses_education_reason_only_for_education_restriction(self):
        non_target = m.SchoolTag.objects.create(
            code="NON_TARGET", name="非目标院校"
        )
        rule = m.SchoolTagRule.objects.create(name="仅硕士", is_active=True)
        for degree_type in [
            m.SchoolTagRuleTag.DEGREE_FIRST,
            m.SchoolTagRuleTag.DEGREE_HIGHEST,
        ]:
            m.SchoolTagRuleTag.objects.create(
                rule=rule,
                school_tag=non_target,
                degree_type=degree_type,
            )
        m.SchoolTagRuleEducation.objects.create(
            rule=rule,
            education=m.Candidate.EDUCATION_MASTER,
        )
        candidate, _resume = self._candidate(94)
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )

        runner.execute_run(run.id)

        item = run.scope_items.get()
        self.assertEqual(
            (item.result_type, item.reason_code),
            (m.ProcessingRunScopeItem.RESULT_COMPLETED, "education_not_eligible"),
        )

    def test_school_gate_uses_school_reason_for_tag_mismatch(self):
        target = m.SchoolTag.objects.create(code="TARGET_ONLY", name="目标院校")
        rule = m.SchoolTagRule.objects.create(name="目标院校规则", is_active=True)
        for degree_type in [
            m.SchoolTagRuleTag.DEGREE_FIRST,
            m.SchoolTagRuleTag.DEGREE_HIGHEST,
        ]:
            m.SchoolTagRuleTag.objects.create(
                rule=rule,
                school_tag=target,
                degree_type=degree_type,
            )
        candidate, _resume = self._candidate(95)
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )

        runner.execute_run(run.id)

        item = run.scope_items.get()
        self.assertEqual(
            (item.result_type, item.reason_code),
            (m.ProcessingRunScopeItem.RESULT_COMPLETED, "school_not_eligible"),
        )

    def test_special_route_threshold_is_strict_and_creates_force_assign_chain(self):
        tertiary_department = m.Department.objects.create(
            name="AI 专项组", level=3, parent=self.department
        )
        for key, value in {
            "ai_special_route_enabled": True,
            "ai_special_route_threshold": 0.9,
            "ai_special_route_secondary_department_id": self.department.id,
            "ai_special_route_tertiary_department_id": tertiary_department.id,
        }.items():
            m.Config.objects.update_or_create(key=key, defaults={"value": value})

        boundary_candidate, boundary_resume = self._candidate(92)
        routed_candidate, routed_resume = self._candidate(93)
        run = runner.create_run(
            "step2",
            scope={"candidate_ids": [boundary_candidate.id, routed_candidate.id]},
        )

        def result_by_candidate(resume, *_args, **_kwargs):
            return self._ai_result(
                resume,
                specialist_confidence=(0.9 if resume == boundary_resume else 0.91),
            )

        with patch.object(
            allocate.ai_service,
            "screen_resume",
            side_effect=result_by_candidate,
        ):
            runner.execute_run(run.id)

        boundary_attempt = m.AssignmentAttempt.objects.get(
            workflow__candidate=boundary_candidate
        )
        routed_attempt = m.AssignmentAttempt.objects.get(
            workflow__candidate=routed_candidate
        )
        self.assertEqual(boundary_attempt.route_code, "")
        self.assertEqual(
            boundary_attempt.status, m.AssignmentAttempt.STATUS_PENDING_DISPATCH
        )
        self.assertEqual(routed_attempt.route_code, "ai_special_route")
        self.assertEqual(routed_attempt.source, m.AssignmentAttempt.SOURCE_AI)
        self.assertEqual(
            routed_attempt.status, m.AssignmentAttempt.STATUS_DISPATCHED
        )
        self.assertEqual(routed_attempt.initial_department, self.department)
        self.assertEqual(routed_attempt.current_department, tertiary_department)
        self.assertEqual(
            list(
                routed_attempt.handling_events.values_list("event_type", flat=True)
            ),
            [
                m.AssignmentHandlingEvent.EVENT_ATTEMPT_CREATED,
                m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED,
                m.AssignmentHandlingEvent.EVENT_DEPARTMENT_TRANSFERRED,
            ],
        )
        routed_event = routed_attempt.handling_events.get(
            event_type=m.AssignmentHandlingEvent.EVENT_DEPARTMENT_TRANSFERRED
        )
        self.assertEqual(routed_event.from_department, self.department)
        self.assertEqual(routed_event.to_department, tertiary_department)
        self.assertTrue(routed_event.is_system_auto)
        self.assertEqual(
            routed_attempt.special_route_config_snapshot["version"],
            ai_config.get_ai_special_route_config().snapshot()["version"],
        )
        routed_decision = m.AgentDispatchDecision.objects.get(
            workflow__candidate=routed_candidate
        )
        self.assertTrue(routed_decision.special_route_applied)
        self.assertEqual(routed_decision.processing_run, run)
        self.assertEqual(
            run.scope_items.get(candidate=routed_candidate).reason_code,
            "ai_dispatched",
        )

    def test_unavailable_special_route_target_falls_back_to_normal_ai(self):
        for key, value in {
            "ai_special_route_enabled": True,
            "ai_special_route_threshold": 0.9,
            "ai_special_route_secondary_department_id": self.department.id,
            "ai_special_route_tertiary_department_id": 999999,
        }.items():
            m.Config.objects.update_or_create(key=key, defaults={"value": value})

        candidate, resume = self._candidate(97)
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )

        with patch.object(
            allocate.ai_service,
            "screen_resume",
            return_value=self._ai_result(resume, specialist_confidence=0.96),
        ):
            runner.execute_run(run.id)

        attempt = m.AssignmentAttempt.objects.get(workflow__candidate=candidate)
        decision = m.AgentDispatchDecision.objects.get(workflow__candidate=candidate)
        item = run.scope_items.get(candidate=candidate)
        self.assertEqual(attempt.route_code, "")
        self.assertEqual(attempt.status, m.AssignmentAttempt.STATUS_PENDING_DISPATCH)
        self.assertEqual(decision.error_code, "")
        self.assertFalse(decision.special_route_applied)
        self.assertEqual(
            decision.special_route_config_snapshot["fallback_code"],
            "target_unavailable",
        )
        self.assertEqual(
            (item.result_type, item.reason_code),
            (m.ProcessingRunScopeItem.RESULT_COMPLETED, "ai_dispatched"),
        )

    @override_settings(CELERY_TASK_ALWAYS_EAGER=False)
    def test_dispatcher_only_queues_four_times_the_configured_ceiling(self):
        candidates = [self._candidate(index)[0] for index in range(2, 42)]
        run = runner.create_run(
            "step2",
            scope={"candidate_ids": [candidate.id for candidate in candidates]},
        )
        run.status = "running"
        run.started_at = run.created_at
        run.save(update_fields=["status", "started_at"])
        runner.prepare_ai_stage(run, {})

        with patch(
            "apps.pipeline.tasks.process_ai_scope_item_task.apply_async"
        ) as publish:
            result = dispatch_ai_run_task.run(run.id)

        self.assertEqual(result, "queued:32")
        self.assertEqual(publish.call_count, 32)
        self.assertEqual(run.scope_items.filter(status="queued").count(), 32)
        self.assertEqual(run.scope_items.filter(status="pending").count(), 8)

    @override_settings(CELERY_TASK_ALWAYS_EAGER=False)
    def test_dispatch_publish_failure_rolls_back_queued_state(self):
        candidate, _resume = self._candidate(99)
        run = runner.create_run(
            "step2", scope={"candidate_ids": [candidate.id]}
        )
        run.status = "running"
        run.save(update_fields=["status"])
        runner.prepare_ai_stage(run, {})

        with patch(
            "apps.pipeline.tasks.process_ai_scope_item_task.apply_async",
            side_effect=RuntimeError("broker unavailable"),
        ), self.assertRaises(Retry):
            dispatch_ai_run_task.run(run.id)

        self.assertEqual(run.scope_items.get().status, "pending")
