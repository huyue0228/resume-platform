"""处理任务的协作式取消：只在候选人/阶段边界停止，不回滚已落库结果。"""
from django.db import models, transaction
from django.utils import timezone

from apps.core.models import ProcessingRun


ACTIVE_STATUSES = {"pending", "running", "waiting_conflict", "cancelling"}


class RunCancelled(Exception):
    """工作线程发现用户已请求取消时中断当前编排。"""


def is_cancel_requested(run_id):
    return ProcessingRun.objects.filter(
        pk=run_id
    ).filter(
        models.Q(status__in=["cancelling", "cancelled"])
        | models.Q(cancel_requested_at__isnull=False)
    ).exists()


def raise_if_cancel_requested(run):
    if run and is_cancel_requested(run.pk):
        raise RunCancelled()


def request_cancellation(run_id, user):
    """取消排队任务，或请求运行中的任务在安全边界停止。"""
    with transaction.atomic():
        run = ProcessingRun.objects.select_for_update().get(pk=run_id)
        if run.status not in ACTIVE_STATUSES:
            raise ValueError("该处理任务已结束，无法取消")
        now = timezone.now()
        run.cancel_requested_at = run.cancel_requested_at or now
        run.cancelled_by = user
        run.cancelled_by_username_snapshot = user.username
        if run.status == "pending":
            run.status = "cancelled"
            run.cancelled_at = now
            run.finished_at = now
            run.current_stage = ""
            run.message = "任务已在执行前取消"
            run.stages.filter(status="pending").update(
                status="cancelled", finished_at=now
            )
        else:
            run.status = "cancelling"
            run.message = "已请求取消，正在等待当前候选人处理结束"
            if run.mode == "ai" and run.step in {"step2", "resume_process", "all"}:
                from .tasks import dispatch_ai_run_task

                transaction.on_commit(lambda: dispatch_ai_run_task.delay(run.id))
        run.save()
        return run
