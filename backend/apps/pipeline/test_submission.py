"""批量提交不能同步读取材料；后台准备沿用固定范围和取消语义。"""
import hashlib
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
from unittest.mock import patch

from django.contrib.auth.models import Group
from django.db import connection
from django.test import override_settings
from django.test.utils import CaptureQueriesContext
from django.utils import timezone
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults
from apps.core import models as m
from apps.ingestion.sources import RESUME_SUBDIR
from apps.pipeline import ai_config, runner
from apps.pipeline.agent_kernel import matching, task_contracts
from apps.pipeline.test_support import KernelTestCase, analysis_response


class SubmissionPreparationTests(KernelTestCase):
    def setUp(self):
        ai_config.save_ai_connection_config(dict(
            api_style="responses", model_name="test", base_url="https://model.invalid/v1", api_key="test-key",
        ))
        ai_config.mark_ai_connection_tested()
        self.department = m.Department.objects.create(name="测试部门", level=2)
        self.job = m.Job.objects.create(entity="GW", department=self.department, public_name="软件", position_name="开发", responsibilities="后端服务开发与测试", headcount=10)

    def candidates(self, count):
        offset = m.Candidate.objects.count()
        candidates = m.Candidate.objects.bulk_create([
            m.Candidate(identity_hash=f"submission-{offset+i}", name=f"合成候选人{offset+i}", household_province="北京")
            for i in range(count)
        ])
        m.Resume.objects.bulk_create([
            m.Resume(candidate=c, apply_id=f"submission-{c.pk}", entity="GW", position_name="软件", resume_file=f"sample-{c.pk}.pdf")
            for c in candidates
        ])
        return candidates

    @override_settings(CELERY_TASK_ALWAYS_EAGER=True)
    def test_bulk_api_enqueues_before_any_material_preparation(self):
        candidates = self.candidates(250)
        ensure_rbac_defaults()
        user = User.objects.create_user(username="submission-hr", role=User.ROLE_HR)
        user.groups.add(Group.objects.get(name="HR"))
        client = APIClient()
        client.force_authenticate(user)
        with patch.object(task_contracts, "_resume_artifact", side_effect=AssertionError("HTTP must not read PDFs")), patch.object(
            matching, "prepare_run", side_effect=AssertionError("HTTP must not prepare inputs"),
        ), patch.object(ai_config, "get_ai_model_config", side_effect=AssertionError("HTTP must not inspect models")), patch.object(
            task_contracts, "runtime_pin", side_effect=AssertionError("HTTP must not probe Kernel"),
        ), patch("apps.api.views.enqueue_runs", return_value=SimpleNamespace(id="queued-task")) as enqueue, CaptureQueriesContext(connection) as queries:
            response = client.post("/api/pipeline/run/", {
                "step": "resume_process", "scope": {"candidate_ids": [c.pk for c in candidates]},
            }, format="json")
        self.assertEqual(response.status_code, 202, response.data)
        run = m.ProcessingRun.objects.get()
        self.assertEqual(run.status, "pending")
        self.assertEqual(run.total_count, 250)
        self.assertEqual(run.celery_task_id, enqueue.call_args.kwargs["task_id"])
        self.assertEqual(run.scope_items.filter(kernel_snapshot={}).count(), 250)
        self.assertEqual(run.pin_id, "")
        self.assertFalse(run.job_capacities.exists())
        self.assertEqual([stage["step"] for stage in response.data["processing_runs"][0]["stages"]],
                         ["queued", "initialize", "preparing", "step1", "step2", "step3", "step4", "finalize"])
        enqueue.assert_called_once_with([run.pk], task_id=run.celery_task_id)
        self.assertFalse(any('FROM "core_school"' in q["sql"] for q in queries))
        # 批量插入可随 SQLite 参数上限分块；不能恢复逐候选人查询的提交路径。
        self.assertLess(len(queries), 100)

    def test_worker_reads_real_files_and_loads_shared_data_once(self):
        candidates = self.candidates(3)
        with TemporaryDirectory() as media, override_settings(MEDIA_ROOT=media):
            folder = Path(media) / RESUME_SUBDIR
            folder.mkdir(parents=True)
            for c in candidates:
                (folder / f"sample-{c.pk}.pdf").write_bytes(f"synthetic PDF {c.pk}".encode())
            run = runner.create_run("all", {"candidate_ids": [c.pk for c in candidates]})
            with patch.object(matching, "case_context", wraps=task_contracts.case_context) as shared, CaptureQueriesContext(connection) as queries:
                runner.initialize_run(run)
                matching.prepare_run(run)
            shared.assert_called_once_with(run)
            self.assertEqual(sum('FROM "core_school"' in q["sql"] for q in queries), 1)
        for item in run.scope_items.all():
            frozen = item.kernel_snapshot
            artifact = frozen["snapshot"]["volunteers"][0]["artifact"]
            self.assertEqual(artifact["checksum"], hashlib.sha256(f"synthetic PDF {item.candidate_id}".encode()).hexdigest())
            self.assertEqual(frozen["pin"]["pin_id"], run.pin_id)
            self.assertIn("preflight", frozen)
        self.assertEqual(run.params["snapshot_preparation"], "complete")
        original = list(run.scope_items.values_list("kernel_snapshot", flat=True))
        with patch.object(task_contracts, "_resume_artifact", side_effect=AssertionError("prepared input must not be re-read")):
            runner.initialize_run(run)
            matching.prepare_run(run)
        self.assertEqual(list(run.scope_items.values_list("kernel_snapshot", flat=True)), original)

    def test_queued_selection_does_not_reapply_changed_filters(self):
        selected = self.candidates(1)[0]
        run = runner.create_run("all", {"candidate_filters": {"name": selected.name}})
        other = self.candidates(1)[0]
        m.Candidate.objects.filter(pk=selected.pk).update(name="已更名")
        scope = runner._run_scope(run)
        from apps.pipeline.services import allocate, dedup
        self.assertEqual(list(allocate.candidate_ids_for_scope(scope)), [selected.pk])
        self.assertEqual(list(dedup.candidate_ids_for_scope(scope)), [selected.pk])
        runner.initialize_run(run)
        matching.prepare_run(run)
        self.assertFalse(run.scope_items.filter(candidate=other).exists())

    def test_empty_submission_does_not_expand_to_later_imports(self):
        run = runner.create_run("all")
        self.candidates(1)
        from apps.pipeline.services import allocate, dedup
        self.assertEqual(list(allocate.candidate_ids_for_scope(runner._run_scope(run))), [])
        self.assertEqual(list(dedup.candidate_ids_for_scope(runner._run_scope(run))), [])
        with patch.object(task_contracts, "_resume_artifact") as read:
            runner.execute_run(run.pk)
        read.assert_not_called()
        self.assertFalse(m.CandidateWorkflow.objects.exists())

    def test_cancel_during_preparation_stops_before_business_stages(self):
        candidates = self.candidates(3)
        run = runner.create_run("all", {"candidate_ids": [c.pk for c in candidates]})
        def cancel_after_first(resume):
            m.ProcessingRun.objects.filter(pk=run.pk).update(cancel_requested_at=timezone.now(), status="cancelling")
            return dict(path="", checksum="", media_type="application/pdf", size_bytes=0)
        with patch.object(task_contracts, "_resume_artifact", side_effect=cancel_after_first) as read, patch.object(runner, "_run_one_stage") as stage:
            runner.execute_run(run.pk)
        run.refresh_from_db()
        self.assertEqual(run.status, "cancelled")
        self.assertEqual(read.call_count, 1)
        stage.assert_not_called()
        self.assertFalse(m.AssignmentAttempt.objects.exists())

    @override_settings(CELERY_TASK_ALWAYS_EAGER=False)
    def test_all_nodes_follow_real_background_work_and_finish(self):
        candidate = self.candidates(1)[0]
        media = TemporaryDirectory()
        self.addCleanup(media.cleanup)
        media_settings = override_settings(MEDIA_ROOT=media.name)
        media_settings.enable()
        self.addCleanup(media_settings.disable)
        folder = Path(media.name) / RESUME_SUBDIR
        folder.mkdir(parents=True)
        (folder / f"sample-{candidate.pk}.pdf").write_bytes(b"synthetic document")
        run = runner.create_run("all", {"candidate_ids": [candidate.pk]})
        original = task_contracts._resume_artifact
        def read_after_stage_started(resume):
            current = m.ProcessingRun.objects.get(pk=run.pk)
            self.assertEqual(current.current_stage, "preparing")
            self.assertEqual(current.stages.get(step="initialize").status, "success")
            self.assertEqual(current.stages.get(step="preparing").status, "running")
            self.assertEqual(current.stages.get(step="step1").status, "pending")
            return original(resume)
        with patch.object(task_contracts, "_resume_artifact", side_effect=read_after_stage_started), patch(
            "apps.pipeline.tasks.dispatch_ai_run_task.delay",
        ):
            runner.execute_run(run.pk)
        run.refresh_from_db()
        self.assertEqual(run.current_stage, "step4")
        self.assertEqual(run.stages.get(step="finalize").status, "pending")
        with patch("apps.pipeline.agent_kernel.client.AgentKernelClient.execute", side_effect=analysis_response):
            runner._execute_ai_eager(run)
        run.refresh_from_db()
        self.assertEqual(run.status, "success", run.error)
        self.assertEqual(list(run.stages.values_list("status", flat=True)), ["success"] * 8)
        self.assertTrue(all(stage.started_at and stage.finished_at for stage in run.stages.all()))

    def test_initialization_failure_stops_the_correct_node(self):
        candidate = self.candidates(1)[0]
        run = runner.create_run("all", {"candidate_ids": [candidate.pk]})
        ai_config.invalidate_ai_connection_test()
        with patch.object(task_contracts, "_resume_artifact") as read:
            runner.execute_run(run.pk)
        read.assert_not_called()
        run.refresh_from_db()
        self.assertEqual(run.status, "failed")
        self.assertEqual(run.stages.get(step="queued").status, "success")
        failed = run.stages.get(step="initialize")
        self.assertEqual(failed.status, "failed")
        self.assertIn("检查处理服务失败", failed.error)
        self.assertTrue(all(s.status == "skipped" for s in run.stages.filter(sequence__gt=2)))

    def test_dispatch_cannot_skip_preparation_even_when_cancelled(self):
        from apps.pipeline.tasks import dispatch_ai_run_task
        run = runner.create_run("all")
        run.status = "cancelling"
        run.current_stage = "initialize"
        run.cancel_requested_at = timezone.now()
        run.save()
        with patch("apps.pipeline.tasks.process_ai_scope_item_task.apply_async") as dispatch:
            self.assertEqual(dispatch_ai_run_task(run.pk), "not_ready")
        dispatch.assert_not_called()
        self.assertFalse(runner.finalize_ai_run_if_complete(run.pk))

    @override_settings(CELERY_TASK_ALWAYS_EAGER=True)
    def test_submission_publisher_ignores_eager_execution(self):
        from apps.pipeline.tasks import enqueue_runs, execute_runs_sequence_task
        with patch.object(execute_runs_sequence_task.app, "send_task") as send, patch.object(execute_runs_sequence_task, "apply") as inline:
            enqueue_runs([42], task_id="submitted-task")
        inline.assert_not_called()
        self.assertEqual(send.call_args.args, (execute_runs_sequence_task.name,))
        self.assertEqual(send.call_args.kwargs["args"], [[42]])
        self.assertTrue(send.call_args.kwargs["ignore_result"])
        self.assertFalse(send.call_args.kwargs["retry"])

    def test_broker_failure_is_visible_and_does_not_leave_a_pending_run(self):
        from apps.api.views import submit_processing_runs
        from apps.pipeline.errors import AIServiceError
        run = runner.create_run("all")
        with patch("apps.api.views.enqueue_runs", side_effect=RuntimeError("private broker credentials")):
            with self.assertRaises(AIServiceError) as raised:
                submit_processing_runs([run])
        self.assertNotIn("private", str(raised.exception))
        run.refresh_from_db()
        self.assertEqual(run.status, "failed")
        self.assertEqual(run.stages.get(step="queued").status, "failed")
        self.assertTrue(all(s.status == "skipped" for s in run.stages.filter(sequence__gt=1)))

    def test_preparation_error_is_a_visible_failed_run(self):
        candidate = self.candidates(1)[0]
        run = runner.create_run("all", {"candidate_ids": [candidate.pk]})
        with patch.object(task_contracts, "_resume_artifact", side_effect=RuntimeError("material preparation failed")), patch.object(runner, "_run_one_stage") as stage:
            runner.execute_run(run.pk)
        run.refresh_from_db()
        self.assertEqual(run.status, "failed")
        self.assertIn("准备候选人材料失败", run.error)
        stage.assert_not_called()
        self.assertFalse(m.AssignmentAttempt.objects.exists())
