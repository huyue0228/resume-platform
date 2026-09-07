"""候选人级 Kernel 的业务回归：冻结范围、完整排名和人工流程。"""
from copy import deepcopy
from unittest.mock import patch

from apps.pipeline.test_support import KernelTestCase
from django.test import TestCase, override_settings

from apps.core import models as m
from apps.pipeline import ai_config, runner
from apps.pipeline.agent_kernel import matching
from apps.pipeline.agent_kernel.task_contracts import TaskResultV1, freeze_case, build_task
from apps.pipeline.ai.structured_output import AIServiceError
from apps.pipeline.services import allocate


@override_settings(AGENT_KERNEL_ROLLOUT="enforced", AGENT_KERNEL_TOKEN="test", AGENT_KERNEL_DOCUMENT_SIGNING_KEY="test")
class CandidateKernelTests(KernelTestCase):
    def setUp(self):
        ai_config.save_ai_connection_config(dict(api_style="responses", model_name="test", base_url="https://model.internal/v1", api_key="test-key"))
        self.department = m.Department.objects.create(name="开发部", level=2)
        self.candidate = m.Candidate.objects.create(identity_hash="test-person", name="测试", phone="13800000000", household_province="北京")
        self.resume = m.Resume.objects.create(candidate=self.candidate, apply_id="A1", entity="GW", position_name="软件", volunteer_rank=1)
        self.job = m.Job.objects.create(entity="GW", department=self.department, public_name="软件", position_name="开发", responsibilities="服务开发", headcount=1)
        self.job2 = m.Job.objects.create(entity="GW", department=self.department, public_name="软件", position_name="开发", responsibilities="软件测试", headcount=1)
        self.workflow = m.CandidateWorkflow.objects.create(candidate=self.candidate, current_resume=self.resume, current_rank=1)

    def response(self, envelope, **kwargs):
        snapshot = envelope.snapshot
        ref = next(v["ref"] for v in snapshot["volunteers"] if not v["rejected"])
        jobs = snapshot["jobs"]
        refs = [j["ref"] for j in jobs]
        evidence = [dict(quote="负责后端服务开发与测试工作", page=1, start_line=1, end_line=1)]
        dimensions = {key: .8 for key in ["major_match", "skills_match", "experience_evidence", "job_requirement", "resume_quality"]}
        matches = [dict(job_ref=j, dimensions=dimensions, confidence=.8, evidence=evidence,
                        risks=[], reason="经历支持岗位职责", score=.8, rank=i+1) for i,j in enumerate(sorted(refs))]
        return TaskResultV1.model_validate(dict(protocol_version=envelope.protocol_version, task_id=envelope.task_id,
            idempotency_key=envelope.idempotency_key, pin=envelope.pin.model_dump(), workflow_revision=snapshot["workflow"]["revision"],
            deterministic=dict(volunteer_order=[v["ref"] for v in snapshot["volunteers"]], current_volunteer_ref=ref,
                current_rank=1, admission_passed=True, admission_rule_ref="", job_refs=refs, status="ready"),
            profile=dict(source_text="负责后端服务开发与测试工作", claims=[dict(kind="project", summary="后端服务开发", evidence=evidence)], risks=[]),
            matches=matches,
            manifest=dict(input_hash="a"*64, resume_checksum="", covered_jobs=refs,
                          tool_versions={}, warnings=[], terminal_state="DONE", ocr_pages=0),
            safe_trace=dict(turns=3)))

    def test_full_pipeline_ranking_and_live_capacity(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        frozen = run.scope_items.get().kernel_snapshot
        best_ref = sorted(frozen["job_ids"])[0]
        best_id = frozen["job_ids"][best_ref]
        m.Job.objects.filter(pk=best_id).update(headcount=0)
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response) as execute:
            runner.execute_run(run.pk)
        run.refresh_from_db()
        self.assertEqual(run.status, "success", run.error)
        attempt = m.AssignmentAttempt.objects.get()
        self.assertNotEqual(attempt.agent_decision.recommended_job_id, best_id)
        self.assertEqual(execute.call_count, 1)
        self.assertEqual(len(attempt.agent_decision.kernel_result["matches"]), 2)
        self.assertEqual(sum(run.job_capacities.values_list("used_count", flat=True)), 1)

    def test_result_rejects_missing_coverage_and_wrong_task(self):
        frozen = freeze_case(self.candidate)
        envelope = build_task(frozen, ai_config.get_ai_model_config(), self.workflow.revision, task_id="test")
        result = self.response(envelope)
        matching.validate_result(envelope, result, frozen, self.resume)
        result.matches.pop()
        with self.assertRaises(AIServiceError):
            matching.validate_result(envelope, result, frozen, self.resume)
        result = self.response(envelope); result.task_id = "other"
        with self.assertRaises(AIServiceError):
            matching.validate_result(envelope, result, frozen, self.resume)

    def test_school_rejection_never_calls_kernel(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        item=run.scope_items.get()
        item.kernel_snapshot["snapshot"]["admission_rules"]=[dict(ref="test-rule",priority=0,first_tag_refs=[],highest_tag_refs=[],educations=[])]
        item.save(update_fields=["kernel_snapshot"])
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute") as execute:
            runner.execute_run(run.pk)
        self.assertFalse(execute.called)
        self.assertFalse(m.AssignmentAttempt.objects.exists())
    def test_no_old_model_fallback_on_enforced_failure(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=AIServiceError("agent_kernel_unavailable", "不可用")):
            runner.execute_run(run.pk)
        self.assertEqual(run.scope_items.get().result_type, "needs_attention")
        self.assertFalse(m.AssignmentAttempt.objects.exists())

    def test_live_job_change_blocks_commit(self):
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response):
            result = matching.evaluate(self.resume, self.job)
        self.job.responsibilities = "岗位要求已修改"; self.job.save()
        with self.assertRaises(AIServiceError):
            matching.validate_live_jobs(result)
        self.assertFalse(m.ResumeProfile.objects.exists())

    @override_settings(AGENT_KERNEL_ROLLOUT="review_only")
    def test_review_lane_does_not_reserve_until_hr_confirmation(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response):
            runner.execute_run(run.pk)
        attempt = m.AssignmentAttempt.objects.get()
        self.assertEqual(attempt.status, "pending_review")
        self.assertIsNone(attempt.capacity_reservation_id)
        allocate.confirm_review(attempt)
        self.assertEqual(sum(run.job_capacities.values_list("used_count", flat=True)), 1)

    def test_snapshot_retains_job_content_and_opaque_refs(self):
        first = freeze_case(self.candidate)
        second = freeze_case(self.candidate)
        self.assertEqual(first["job_ids"], second["job_ids"])
        self.assertTrue(all(len(key)==64 for key in first["job_ids"]))
        original = deepcopy(first["snapshot"])
        self.job.responsibilities = "新的职责"; self.job.save()
        self.assertEqual(first["snapshot"], original)

    def test_public_http_payload_only_contains_admitted_scope(self):
        from unittest.mock import Mock
        from resume_contracts.fixtures import request_fixture, response_fixture
        from resume_contracts.models import AnalysisRequestV1
        from apps.pipeline.agent_kernel.client import AgentKernelClient
        frozen=freeze_case(self.candidate)
        frozen["snapshot"]["volunteers"][0]["artifact"]=request_fixture().scope.artifact.model_dump()
        frozen["snapshot"]["jobs"].append(dict(frozen["snapshot"]["jobs"][0],ref="foreign",entity="YLS"))
        envelope=build_task(frozen,ai_config.get_ai_model_config(),self.workflow.revision,task_id="wire-test")
        def respond(_url,**kwargs):
            payload=kwargs["json"]
            self.assertNotIn("snapshot",payload)
            self.assertNotIn("model_api_key",payload)
            parsed=AnalysisRequestV1.model_validate(payload)
            self.assertEqual(len(parsed.scope.jobs),2)
            self.assertNotIn("capacity",payload["scope"]["jobs"][0])
            result=response_fixture(parsed);result["manifest"]["warnings"]=[]
            return Mock(status_code=200,json=lambda:result)
        with patch("apps.pipeline.agent_kernel.client.httpx.post",side_effect=respond):
            result=AgentKernelClient().execute(envelope)
        matching.validate_result(envelope,result,frozen,self.resume)

    @override_settings(AGENT_KERNEL_ROLLOUT="review_only")
    def test_review_confirmation_rejects_changed_job(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response):
            runner.execute_run(run.pk)
        attempt = m.AssignmentAttempt.objects.get()
        m.Job.objects.filter(pk=attempt.agent_decision.recommended_job_id).update(responsibilities="岗位要求已调整")
        with self.assertRaises(allocate.AttemptStateChanged):
            allocate.confirm_review(attempt)
        attempt.refresh_from_db()
        self.assertEqual(attempt.status, "pending_review")
        self.assertIsNone(attempt.capacity_reservation_id)

    def test_manual_revision_change_discards_analysis_without_profile_write(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        def respond(envelope, **kwargs):
            result = self.response(envelope)
            workflow = m.CandidateWorkflow.objects.get(pk=self.workflow.pk)
            workflow.save(update_fields=["updated_at"])
            return result
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=respond):
            runner.execute_run(run.pk)
        self.assertEqual(run.scope_items.get().status, "skipped_manual_change")
        self.assertFalse(m.ResumeProfile.objects.exists())
        self.assertFalse(m.AssignmentAttempt.objects.exists())

    def test_capacity_only_change_reuses_existing_analysis(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response):
            runner.execute_run(run.pk)
        self.job.headcount = 2; self.job.save()
        rerun = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response) as execute:
            runner.execute_run(rerun.pk)
        self.assertEqual(execute.call_count, 0)  # 准入在平台本地完成，复用不访问内核。
        result = rerun.scope_items.get().kernel_result
        self.assertTrue(result["manifest"]["reused_from_task_id"])
        self.assertEqual(result["safe_trace"]["turns"], 0)

    def test_analysis_projection_uses_frozen_names_and_omits_raw_body(self):
        from apps.pipeline.agent_kernel.presentation import decision_analysis
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response):
            runner.execute_run(run.pk)
        decision = m.AgentDispatchDecision.objects.get()
        brief = decision_analysis(decision)
        self.assertEqual(brief["match_count"], 2)
        self.assertNotIn("matches", brief)
        detailed = decision_analysis(decision, detailed=True)
        self.assertNotIn("source_text", detailed["profile"])
        self.assertEqual(detailed["matches"][0]["department_name"], "开发部")

    def test_external_knowledge_prevents_unversioned_cache_reuse(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response):
            runner.execute_run(run.pk)
        item = run.scope_items.get()
        item.kernel_result["manifest"]["tool_versions"]["mcp.taxonomy.lookup"] = "1"
        item.save(update_fields=["kernel_result"])
        rerun = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response) as execute:
            runner.execute_run(rerun.pk)
        self.assertEqual(execute.call_count, 1)
        self.assertFalse(rerun.scope_items.get().kernel_result["manifest"].get("reused_from_task_id"))

    def test_rejection_creates_new_frozen_task_without_model_inside_transaction(self):
        second = m.Resume.objects.create(candidate=self.candidate, apply_id="A2", entity="GW", position_name="软件", volunteer_rank=2)
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response):
            runner.execute_run(run.pk)
        attempt = allocate.dispatch_attempt(m.AssignmentAttempt.objects.get())
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute") as model, self.captureOnCommitCallbacks(execute=False) as callbacks:
            allocate.submit_feedback(attempt, "rejected", note="岗位不适合", reason_code="other")
        self.assertFalse(model.called)
        self.assertEqual(len(callbacks), 1)
        with patch("apps.pipeline.tasks.execute_runs_sequence_task", return_value=[]):
            callbacks[0]()
        next_run = m.ProcessingRun.objects.latest("pk")
        self.assertNotEqual(next_run.pk, run.pk)
        self.assertEqual(next_run.scope["retry_resume_id"], second.pk)
        frozen = next_run.scope_items.get().kernel_snapshot
        rejected = [v for v in frozen["snapshot"]["volunteers"] if v["rejected"]]
        self.assertEqual(frozen["volunteer_ids"][rejected[0]["ref"]], self.resume.pk)

    def test_one_capability_discovery_per_run_and_exact_snapshot_pin(self):
        from resume_contracts.fixtures import capabilities_fixture
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.capabilities", side_effect=capabilities_fixture) as discover:
            run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        self.assertEqual(discover.call_count, 1)
        pin = run.scope_items.get().kernel_snapshot["pin"]
        self.assertEqual(pin["toolset_version"], run.toolset_version)
        self.assertEqual(pin["instruction_version"], run.prompt_version)

    def test_removed_task_snapshot_fails_before_deterministic_business_writes(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        run.scope_items.update(kernel_snapshot={})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute") as execute:
            runner.execute_run(run.pk)
        run.refresh_from_db()
        self.assertEqual(run.status, "failed")
        self.assertFalse(execute.called)
        self.assertFalse(m.AssignmentAttempt.objects.exists())

    def test_changed_runtime_result_pin_is_rejected(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        def invalid(envelope, **kwargs):
            result = self.response(envelope)
            result.pin.instruction_version = "unexpected-future-version"
            return result
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=invalid):
            runner.execute_run(run.pk)
        self.assertFalse(m.AssignmentAttempt.objects.exists())
        self.assertEqual(m.AgentDispatchDecision.objects.get().error_code, "agent_invalid_output")

    def test_nul_in_model_conclusion_is_controlled_failure(self):
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        def invalid(envelope, **kwargs):
            result = self.response(envelope)
            result.profile.claims[0].summary += chr(0)
            return result
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=invalid):
            runner.execute_run(run.pk)
        self.assertFalse(m.ResumeProfile.objects.exists())
        self.assertEqual(m.AgentDispatchDecision.objects.get().error_code, "agent_invalid_output")

    def test_feedback_capability_failure_keeps_feedback_and_pending_work(self):
        from apps.pipeline.tasks import process_next_volunteer_task
        second = m.Resume.objects.create(candidate=self.candidate, apply_id="A2", entity="GW", position_name="软件", volunteer_rank=2)
        run = runner.create_run("all", {"candidate_ids": [self.candidate.pk]})
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=self.response):
            runner.execute_run(run.pk)
        attempt = allocate.dispatch_attempt(m.AssignmentAttempt.objects.get())
        with self.captureOnCommitCallbacks(execute=False):
            allocate.submit_feedback(attempt, "rejected", note="岗位不适合", reason_code="other")
        self.workflow.refresh_from_db()
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.capabilities", side_effect=AIServiceError("agent_kernel_unavailable", "不可用")):
            result = process_next_volunteer_task(self.candidate.pk, second.pk, self.workflow.revision)
        attempt.refresh_from_db()
        self.workflow.refresh_from_db()
        self.assertEqual(attempt.feedback_result, "rejected")
        self.assertEqual(result["status"], "needs_attention")
        self.assertEqual(self.workflow.block_reason, "agent_kernel_unavailable")
