"""Celery 任务包装。Demo eager 同步执行；生产用 Redis broker 异步。"""
import logging
import random
import uuid
from concurrent.futures import ThreadPoolExecutor
from datetime import timedelta

from celery import shared_task
from celery.exceptions import MaxRetriesExceededError, Retry
from django.conf import settings
from django.db import close_old_connections, transaction
from django.utils import timezone

from apps.core import models as m

from . import ai_config, runner
from .ai import prompt_harness, school_province
from .services import allocate


logger = logging.getLogger(__name__)
_LOCAL_ENRICHMENT_EXECUTOR = ThreadPoolExecutor(
    max_workers=1,
    thread_name_prefix="school-province-ai",
)


@shared_task
def execute_runs_sequence_task(run_ids):
    """依次领取已创建的运行；每条运行在提交时已固化用户选择的单一模式。"""
    return [runner.execute_run(run_id).id for run_id in run_ids]


@shared_task(
    queue="ai",
    acks_late=True,
    reject_on_worker_lost=True,
)
def enrich_school_provinces_task(school_ids, prompt_version=None):
    """分批补全仍为空的院校省份；失败只写日志，不影响已经完成的导入。"""
    school_ids = sorted(
        {int(item) for item in school_ids if str(item).isdigit() and int(item) > 0}
    )
    if not school_ids or not ai_config.is_ai_available():
        return {"status": "skipped", "requested": len(school_ids), "updated": 0}

    schools = list(
        m.School.objects.filter(id__in=school_ids, province="").order_by("id")
    )
    updated = 0
    failed_batches = 0
    for offset in range(0, len(schools), school_province.MAX_SCHOOLS_PER_REQUEST):
        batch = schools[offset : offset + school_province.MAX_SCHOOLS_PER_REQUEST]
        try:
            prompt_kwargs = (
                {"prompt_version": prompt_version} if prompt_version else {}
            )
            provinces = school_province.infer_school_provinces(
                [school.name for school in batch],
                **prompt_kwargs,
            )
        except Exception as exc:  # noqa: BLE001 - 不记录模型原文或连接详情
            failed_batches += 1
            logger.warning(
                "School province enrichment failed batch_size=%s error_type=%s",
                len(batch),
                type(exc).__name__,
            )
            continue
        for school in batch:
            province = provinces.get(school.name, "")
            if province:
                # 排队后用户可能已手工维护；只更新此刻仍为空的记录。
                updated += m.School.objects.filter(
                    pk=school.id,
                    province="",
                ).update(province=province)
    return {
        "status": "completed" if not failed_batches else "partial",
        "requested": len(schools),
        "updated": updated,
        "failed_batches": failed_batches,
    }


def _run_local_school_province_enrichment(school_ids, prompt_version):
    close_old_connections()
    try:
        return enrich_school_provinces_task.run(school_ids, prompt_version)
    finally:
        close_old_connections()


def submit_school_province_enrichment(school_ids):
    """生产投递 Celery；本地 eager 仍放入后台线程，避免阻塞导入响应。"""
    prompt_version = prompt_harness.get_active_prompt_version()
    if settings.CELERY_TASK_ALWAYS_EAGER:
        _LOCAL_ENRICHMENT_EXECUTOR.submit(
            _run_local_school_province_enrichment,
            list(school_ids),
            prompt_version,
        )
        return {"backend": "local_thread", "task_id": ""}
    task = enrich_school_provinces_task.apply_async(
        args=[list(school_ids), prompt_version],
        queue="ai",
    )
    return {"backend": "celery", "task_id": getattr(task, "id", "") or ""}


@shared_task(
    bind=True,
    acks_late=True,
    reject_on_worker_lost=True,
    max_retries=20,
)
def dispatch_ai_run_task(self, run_id):
    """按有界窗口持续向专用 AI 队列补充候选人任务。"""
    now = timezone.now()
    runtime = ai_config.get_ai_runtime_config()
    stale_before = now - timedelta(seconds=max(300, runtime.timeout_seconds * 2))
    try:
        with transaction.atomic():
            run = m.ProcessingRun.objects.select_for_update().get(pk=run_id)
            if run.status not in {"running", "cancelling", "waiting_conflict"}:
                return run.status
            if run.cancel_requested_at:
                runner.cancel_unstarted_ai_items(run_id)
                runner.finalize_ai_run_if_complete(run_id)
                return "cancelling"

            # 发布后 worker 异常退出可能遗留 queued；令牌保证重复投递仍幂等。
            run.scope_items.filter(status="queued", queued_at__lt=stale_before).update(
                status="pending"
            )
            window = max(1, runtime.concurrency * 4)
            active = run.scope_items.filter(
                status__in=["queued", "processing", "waiting_conflict"]
            ).count()
            free = max(0, window - active)
            items = list(
                run.scope_items.select_for_update(skip_locked=True)
                .filter(status="pending")
                .order_by("candidate_id")[:free]
            )
            queued = []
            for item in items:
                token = uuid.uuid4()
                item.status = "queued"
                item.dispatch_token = token
                item.queued_at = now
                item.save(update_fields=["status", "dispatch_token", "queued_at"])
                # 与状态更新处于同一事务：发布失败会回滚 queued；若进程在发布后
                # 提交前退出，候选人任务会从 pending 状态接管同一令牌。
                process_ai_scope_item_task.apply_async(
                    args=[run_id, item.id, str(token)],
                    queue="ai",
                )
                queued.append((item.id, str(token)))
            run.last_heartbeat_at = now
            run.save(update_fields=["last_heartbeat_at"])
    except Retry:
        raise
    except Exception as exc:
        logger.warning(
            "AI task dispatch failed run_id=%s error_type=%s",
            run_id,
            type(exc).__name__,
        )
        if self.request.retries >= 20:
            now = timezone.now()
            m.ProcessingRun.objects.filter(pk=run_id).update(
                status="failed",
                message="AI 候选人任务无法投递，请检查 Celery broker 和 AI worker",
                error="ai_task_dispatch_failed",
                current_stage="",
                finished_at=now,
                last_heartbeat_at=now,
            )
            m.ProcessingRunStage.objects.filter(
                run_id=run_id, step="step4", status="running"
            ).update(
                status="failed",
                error="ai_task_dispatch_failed",
                finished_at=now,
            )
            return "failed"
        raise self.retry(countdown=2)

    if runner.finalize_ai_run_if_complete(run_id):
        return "finished"
    return f"queued:{len(queued)}"


def _mark_infrastructure_failure(run_id, scope_item_id):
    with transaction.atomic():
        item = m.ProcessingRunScopeItem.objects.select_for_update().get(
            pk=scope_item_id, run_id=run_id
        )
        if item.status in allocate.AI_SCOPE_TERMINAL_STATUSES:
            return {"status": item.status, "already_terminal": True}
        allocate.release_ai_scope_claim(scope_item_id)
        item.status = "needs_attention"
        item.result_type = m.ProcessingRunScopeItem.RESULT_NEEDS_ATTENTION
        item.reason_code = "ai_connection_error"
        item.result_message = "AI 候选人任务多次异常，请检查 worker 日志后重试"
        item.error_code = "ai_connection_error"
        item.error_message = "AI 候选人任务多次异常，请检查 worker 日志后重试"
        item.finished_at = timezone.now()
        item.save(
            update_fields=[
                "status",
                "result_type",
                "reason_code",
                "result_message",
                "error_code",
                "error_message",
                "finished_at",
            ]
        )
        return {
            "status": "needs_attention",
            "result_type": m.ProcessingRunScopeItem.RESULT_NEEDS_ATTENTION,
            "reason_code": "ai_connection_error",
        }


@shared_task(
    bind=True,
    queue="ai",
    acks_late=True,
    reject_on_worker_lost=True,
    max_retries=10000,
)
def process_ai_scope_item_task(
    self,
    run_id,
    scope_item_id,
    dispatch_token,
    failure_retries=0,
):
    """处理一名候选人；同候选人冲突时释放 worker 并延迟重试。"""
    item = m.ProcessingRunScopeItem.objects.filter(
        pk=scope_item_id,
        run_id=run_id,
    ).first()
    if item and item.status == "pending":
        m.ProcessingRunScopeItem.objects.filter(
            pk=item.id,
            status="pending",
        ).update(
            status="queued",
            dispatch_token=dispatch_token,
            queued_at=timezone.now(),
        )
        item.refresh_from_db()
    if not item or str(item.dispatch_token) != str(dispatch_token):
        return "stale"
    try:
        result = allocate.process_ai_scope_item(run_id, scope_item_id)
        if result.get("status") == "waiting_conflict":
            raise self.retry(countdown=2 + random.uniform(0, 1))
    except MaxRetriesExceededError:
        result = _mark_infrastructure_failure(run_id, scope_item_id)
        runner.record_ai_scope_outcome(
            run_id,
            result,
            infrastructure_error=f"scope_item={scope_item_id} conflict retries exhausted",
        )
    except Retry:
        raise
    except Exception as exc:  # noqa: BLE001 - 只记录类型，不记录可能含密钥的原文
        logger.warning(
            "AI candidate task failed run_id=%s scope_item_id=%s error_type=%s",
            run_id,
            scope_item_id,
            type(exc).__name__,
        )
        allocate.release_ai_scope_claim(scope_item_id)
        if failure_retries < 5:
            raise self.retry(
                countdown=min(30, 2 ** failure_retries),
                kwargs={"failure_retries": failure_retries + 1},
            )
        result = _mark_infrastructure_failure(run_id, scope_item_id)
        runner.record_ai_scope_outcome(
            run_id,
            result,
            infrastructure_error=f"scope_item={scope_item_id} task_execution_error",
        )
    else:
        runner.record_ai_scope_outcome(run_id, result)

    if not runner.finalize_ai_run_if_complete(run_id):
        dispatch_ai_run_task.delay(run_id)
    return result.get("status")
