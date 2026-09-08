"""流水线编排：单步、上传后主流程或一键全流程，记录共享 ProcessingRun。"""
from copy import deepcopy

from django.conf import settings
from django.db import transaction
from django.utils import timezone

from apps.core.models import (
    Candidate,
    CandidateWorkflow,
    Config,
    Job,
    ProcessingRun,
    ProcessingRunJobCapacity,
    ProcessingRunScopeItem,
    ProcessingRunStage,
)
from apps.pipeline import ai_config
from apps.pipeline.agent_kernel.task_contracts import runtime_pin

from .cancellation import RunCancelled, raise_if_cancel_requested
from .services import allocate, dedup

STEP_FUNCS = {
    "step1": lambda mode, scope, run: dedup.run(scope),
    "step2": lambda mode, scope, run: allocate.run_school_gate(
        scope, mode, processing_run=run
    ),
    "step3": lambda mode, scope, run: allocate.run_allocation_precheck(
        scope, mode, processing_run=run
    ),
    "step4": lambda mode, scope, run: "Agent Kernel 筛选由候选人任务执行",
}

RESUME_PROCESS_STEP = "resume_process"
STAGE_LABELS = {
    "step1": "查重与志愿排序",
    "step2": "院校分类与学历/院校准入",
    "step3": "Policy Gate 与固定业务引用",
    "step4": "Agent Kernel 证据化筛选与分配",
}

STEP_ORDER = ["step1", "step2", "step3", "step4"]


def _candidate_ids_for_run(step, scope):
    """在提交时冻结候选人范围，后台执行不再重新解释页面筛选。"""
    candidate_ids = scope.get("candidate_ids") or []
    if candidate_ids:
        return sorted({int(candidate_id) for candidate_id in candidate_ids})
    if step in {"step2", "step3", "step4", RESUME_PROCESS_STEP, "all"}:
        return list(allocate.candidate_ids_for_scope(scope))
    if step == "step1":
        return list(dedup.candidate_ids_for_scope(scope))
    return []


def _scope_summary(scope, candidate_ids):
    summary = {"candidate_count": len(candidate_ids)}
    statuses = scope.get("system_statuses") or []
    if statuses:
        summary["system_statuses"] = statuses
    source = scope.get("source")
    if source:
        summary["source"] = source
    return summary


def _stage_steps(step):
    if step == RESUME_PROCESS_STEP:
        return ["step1", "step2", "step3", "step4"]
    if step == "all":
        return STEP_ORDER
    if step == "step2":
        return ["step2", "step3", "step4"]
    if step == "step3":
        return ["step3", "step4"]
    return [step]


def _job_hc_coefficient():
    config = Config.objects.filter(key="job_hc_coefficient").first()
    value = config.value if config else 1
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= 100:
        return 1
    return value


def create_run(step, scope=None, created_by=None):
    if step not in {"all", RESUME_PROCESS_STEP} and step not in STEP_FUNCS:
        raise ValueError(f"未知步骤: {step}")
    # 网络探测在事务外完成，每个批次只探测一次；后续候选人共享冻结版本。
    config = ai_config.get_ai_model_config() if "step4" in _stage_steps(step) else None
    pin = runtime_pin(model_config=config) if config else None
    return _create_run(step, scope, created_by, config, pin)


@transaction.atomic
def _create_run(step, scope, created_by, config, pin):
    if step not in {"all", RESUME_PROCESS_STEP} and step not in STEP_FUNCS:
        raise ValueError(f"未知步骤: {step}")
    mode = "ai"  # 业务审计字段；新任务不存在可选运行模式。
    scope = deepcopy(scope or {})
    candidate_ids = _candidate_ids_for_run(step, scope)
    expected_revision = scope.get("expected_workflow_revision")
    if expected_revision is not None:
        workflows = list(CandidateWorkflow.objects.select_for_update().filter(candidate_id__in=candidate_ids))
        if len(workflows) != 1 or workflows[0].revision != expected_revision:
            raise ValueError("续办前流程已被修改")
        existing = ProcessingRun.objects.filter(scope__trigger="feedback_rejected",
            scope__retry_resume_id=scope.get("retry_resume_id"),
            scope__expected_workflow_revision=expected_revision,
            scope_items__candidate_id=candidate_ids[0]).first()
        if existing:
            return existing
    # candidate_ids 保存在范围明细表；scope 只保留触发时的可审计筛选快照。
    scope.pop("candidate_ids", None)
    versions = {
        "model_name": config.model_name,
        # 保留存量列名，新任务在此字段记录 Kernel 内置指令版本。
        "prompt_version": pin.instruction_version,
        "decision_version": pin.policy_version,
        "kernel_build": pin.kernel_build,
        "protocol_version": pin.protocol_version,
        "toolset_version": pin.toolset_version,
        "result_schema_version": pin.result_schema_version,
        "policy_version": pin.policy_version,
        "model_config_revision": pin.model_config_revision,
        "pin_id": pin.pin_id,
    } if pin else {}
    coefficient = _job_hc_coefficient()
    run = ProcessingRun.objects.create(
        step=step,
        mode=mode,
        scope=scope,
        scope_summary=_scope_summary(scope, candidate_ids),
        status="pending",
        created_by=created_by if getattr(created_by, "is_authenticated", False) else None,
        created_by_username_snapshot=(
            created_by.username if getattr(created_by, "is_authenticated", False) else ""
        ),
        job_hc_coefficient_snapshot=coefficient,
        **versions,
    )
    ProcessingRunJobCapacity.objects.bulk_create(
        [
            ProcessingRunJobCapacity(
                run=run,
                job=job,
                headcount_snapshot=job.headcount,
                coefficient_snapshot=coefficient,
                capacity=job.headcount * coefficient,
            )
            for job in Job.objects.filter(is_active=True).order_by("id")
        ],
        batch_size=1000,
    )
    workflow_revisions = dict(
        CandidateWorkflow.objects.filter(candidate_id__in=candidate_ids).values_list(
            "candidate_id", "revision"
        )
    )
    ProcessingRunScopeItem.objects.bulk_create(
        [
            ProcessingRunScopeItem(
                run=run,
                candidate_id=candidate_id,
                workflow_revision_at_submit=workflow_revisions.get(candidate_id),
            )
            for candidate_id in candidate_ids
        ],
        batch_size=1000,
    )
    ProcessingRunStage.objects.bulk_create(
        [
            ProcessingRunStage(
                run=run,
                sequence=index,
                step=stage_step,
                label=STAGE_LABELS[stage_step],
            )
            for index, stage_step in enumerate(_stage_steps(step), start=1)
        ]
    )
    if pin:
        from apps.pipeline.agent_kernel.task_contracts import freeze_case
        for item in run.scope_items.select_related("candidate"):
            item.kernel_snapshot = freeze_case(item.candidate, run)
            item.save(update_fields=["kernel_snapshot"])
    return run


def _run_scope(run):
    scope = deepcopy(run.scope or {})
    candidate_ids = list(run.scope_items.order_by("candidate_id").values_list("candidate_id", flat=True))
    if candidate_ids:
        scope["candidate_ids"] = candidate_ids
    return scope


def _heartbeat(run, *, stage=""):
    run.current_stage = stage
    run.last_heartbeat_at = timezone.now()
    run.save(update_fields=["current_stage", "last_heartbeat_at"])


def _run_one_stage(run, stage, mode, scope):
    raise_if_cancel_requested(run)
    stage_record = run.stages.get(step=stage)
    stage_record.status = "running"
    stage_record.started_at = timezone.now()
    stage_record.save(update_fields=["status", "started_at"])
    _heartbeat(run, stage=stage)
    if stage == "step1":
        message = dedup.run(scope, processing_run=run, processing_stage=stage_record)
    elif stage == "step2":
        message = allocate.run_school_gate(
            scope, mode, processing_run=run, processing_stage=stage_record
        )
    elif stage == "step3":
        message = allocate.run_allocation_precheck(
            scope, mode, processing_run=run, processing_stage=stage_record
        )
    else:
        message = STEP_FUNCS[stage](mode, scope, run)
    run.refresh_from_db()
    stage_record.refresh_from_db()
    stage_record.status = (
        "partial_failed"
        if stage_record.failed_count
        else "needs_attention"
        if stage_record.needs_attention_count
        else "success"
    )
    stage_record.message = message
    stage_record.finished_at = timezone.now()
    stage_record.save(update_fields=["status", "message", "finished_at"])
    return message


AI_SCOPE_TERMINAL_STATUSES = [
    "success",
    "needs_attention",
    "failed",
    "skipped_manual_change",
    "cancelled",
]


def prepare_ai_stage(run, scope):
    """初始化 Step4；Step2/Step3 已完成 Policy Gate 并冻结业务引用。"""
    raise_if_cancel_requested(run)
    stage = run.stages.get(step="step4")
    now = timezone.now()
    candidate_ids = list(
        run.scope_items.filter(status="pending")
        .order_by("candidate_id")
        .values_list("candidate_id", flat=True)
    )
    concurrency_limit = ai_config.get_ai_runtime_config().concurrency
    run.current_stage = "step4"
    run.total_count = run.scope_items.count()
    run.chunk_size = 1
    run.chunk_total = run.total_count
    run.chunk_done = run.processed_count
    run.chunk_failed = 0
    run.chunk_errors = []
    run.ai_concurrency_limit = concurrency_limit
    run.ai_effective_concurrency = (
        1 if settings.CELERY_TASK_ALWAYS_EAGER else min(2, concurrency_limit)
    )
    run.last_heartbeat_at = now
    run.save()
    stage.status = "running"
    stage.started_at = stage.started_at or now
    stage.total_count = len(candidate_ids)
    stage.processed_count = 0
    stage.success_count = 0
    stage.completed_count = 0
    stage.needs_attention_count = 0
    stage.failed_count = 0
    stage.review_count = 0
    stage.dispatch_count = 0
    stage.archive_count = 0
    stage.skipped_count = 0
    stage.cancelled_count = 0
    stage.save()
    return stage


def record_ai_scope_outcome(run_id, result, *, infrastructure_error=""):
    """只为首次进入终态的 ScopeItem 累计一次进度。"""
    if result.get("already_terminal"):
        return
    status = result.get("status")
    if status not in AI_SCOPE_TERMINAL_STATUSES:
        return
    from django.db.models import F

    run_updates = {
        "processed_count": F("processed_count") + 1,
        "chunk_done": F("chunk_done") + 1,
        "last_heartbeat_at": timezone.now(),
    }
    stage_updates = {"processed_count": F("processed_count") + 1}
    outcome_field = {
        "success": "success_count",
        "needs_attention": "needs_attention_count",
        "failed": "failed_count",
        "skipped_manual_change": "skipped_count",
        "cancelled": "cancelled_count",
    }[status]
    run_updates[outcome_field] = F(outcome_field) + 1
    stage_updates[outcome_field] = F(outcome_field) + 1
    if status == "success":
        run_updates["completed_count"] = F("completed_count") + 1
        stage_updates["completed_count"] = F("completed_count") + 1
    recommendation = result.get("recommendation")
    recommendation_field = {
        "review": "review_count",
        "dispatch": "dispatch_count",
        "archive": "archive_count",
    }.get(recommendation)
    if recommendation_field:
        run_updates[recommendation_field] = F(recommendation_field) + 1
        stage_updates[recommendation_field] = F(recommendation_field) + 1
    if infrastructure_error:
        run_updates["chunk_failed"] = F("chunk_failed") + 1
    updated = ProcessingRun.objects.filter(
        pk=run_id,
        status__in=["running", "waiting_conflict", "cancelling"],
    ).update(**run_updates)
    if updated:
        ProcessingRunStage.objects.filter(run_id=run_id, step="step4").update(
            **stage_updates
        )
    if infrastructure_error and updated:
        with transaction.atomic():
            run = ProcessingRun.objects.select_for_update().get(pk=run_id)
            errors = list(run.chunk_errors or [])
            errors.append(infrastructure_error[:300])
            run.chunk_errors = errors[-50:]
            run.save(update_fields=["chunk_errors"])


def cancel_unstarted_ai_items(run_id):
    now = timezone.now()
    qs = ProcessingRunScopeItem.objects.filter(
        run_id=run_id,
        status__in=["pending", "queued", "waiting_conflict"],
    )
    count = qs.update(status="cancelled", finished_at=now)
    if count:
        qs.model.objects.filter(
            run_id=run_id,
            status="cancelled",
            result_type="",
        ).update(
            result_type=ProcessingRunScopeItem.RESULT_CANCELLED,
            reason_code="cancelled",
            result_message="任务取消时尚未开始处理",
        )
    if count:
        from django.db.models import F

        ProcessingRun.objects.filter(pk=run_id).update(
            processed_count=F("processed_count") + count,
            cancelled_count=F("cancelled_count") + count,
            chunk_done=F("chunk_done") + count,
            last_heartbeat_at=now,
        )
        ProcessingRunStage.objects.filter(run_id=run_id, step="step4").update(
            processed_count=F("processed_count") + count,
            cancelled_count=F("cancelled_count") + count,
        )
    return count


def finalize_ai_run_if_complete(run_id):
    """以 ScopeItem 为权威来源收口运行；未全部终态时保持运行中。"""
    with transaction.atomic():
        run = ProcessingRun.objects.select_for_update().get(pk=run_id)
        total = run.scope_items.count()
        terminal = run.scope_items.filter(status__in=AI_SCOPE_TERMINAL_STATUSES).count()
        if terminal < total:
            return False
        success = run.scope_items.filter(status="success").count()
        completed = run.scope_items.filter(
            result_type=ProcessingRunScopeItem.RESULT_COMPLETED
        ).count()
        attention = run.scope_items.filter(
            result_type=ProcessingRunScopeItem.RESULT_NEEDS_ATTENTION
        ).count()
        failed = run.scope_items.filter(status="failed").count()
        skipped = run.scope_items.filter(status="skipped_manual_change").count()
        cancelled = run.scope_items.filter(status="cancelled").count()
        review = run.agent_decisions.filter(recommendation="review").count()
        dispatch = run.agent_decisions.filter(recommendation="dispatch").count()
        archive = run.agent_decisions.filter(recommendation="archive").count()
        run.total_count = total
        run.processed_count = terminal
        run.success_count = completed
        run.completed_count = completed
        run.needs_attention_count = attention
        run.failed_count = failed
        run.skipped_count = skipped
        run.cancelled_count = cancelled
        run.review_count = review
        run.dispatch_count = dispatch
        run.archive_count = archive
        run.chunk_done = terminal
        now = timezone.now()
        if run.cancel_requested_at:
            run.status = "cancelled"
            run.cancelled_at = run.cancelled_at or now
            run.message = "任务已取消；已完成的候选人处理结果已保留"
        else:
            run.status = (
                "partial_failed"
                if failed
                else "needs_attention"
                if attention
                else "success"
            )
            run.message = (
                f"AI 并发处理完成：处理完成 {completed}，需处理 {attention}，失败 {failed}，"
                f"跳过 {skipped}，取消 {cancelled}"
            )
        run.current_stage = ""
        run.finished_at = now
        run.last_heartbeat_at = now
        run.save()
        stage = ProcessingRunStage.objects.select_for_update().get(
            run=run, step="step4"
        )
        step4_total = run.scope_items.exclude(
            reason_code__in=[
                "education_not_eligible",
                "school_not_eligible",
                "job_not_found",
                "secondary_department_missing",
                "job_mapping_ambiguous",
                "internal_position_name_missing",
                "job_hc_exhausted",
                "rule_assigned",
                "terminal_workflow",
                "no_resume_available",
            ]
        ).count()
        stage.total_count = step4_total
        stage.processed_count = step4_total
        stage.success_count = run.scope_items.filter(
            result_type=ProcessingRunScopeItem.RESULT_COMPLETED,
        ).exclude(reason_code__in=[
            "education_not_eligible", "school_not_eligible", "job_not_found",
            "secondary_department_missing",
            "rule_assigned", "terminal_workflow", "no_resume_available",
            "job_mapping_ambiguous", "internal_position_name_missing", "job_hc_exhausted",
        ]).count()
        stage.completed_count = stage.success_count
        stage.needs_attention_count = attention
        stage.failed_count = failed
        stage.skipped_count = skipped
        stage.cancelled_count = cancelled
        stage.review_count = review
        stage.dispatch_count = dispatch
        stage.archive_count = archive
        stage.status = (
            "cancelled"
            if run.status == "cancelled"
            else "partial_failed"
            if failed
            else "needs_attention"
            if attention
            else "success"
        )
        stage.message = run.message
        stage.finished_at = now
        stage.save()
        return True


def _execute_ai_eager(run):
    """SQLite/eager 开发模式沿用同步语义，但复用候选人级幂等处理。"""
    for item_id in run.scope_items.filter(status="pending").order_by("candidate_id").values_list("id", flat=True):
        result = allocate.process_ai_scope_item(run.id, item_id)
        record_ai_scope_outcome(run.id, result)
    finalize_ai_run_if_complete(run.id)


def execute_run(run_id):
    # 只由第一个成功领取 pending 任务的 worker 运行，避免取消请求与 worker
    # 启动同时发生时，把已取消任务重新写回 running。
    with transaction.atomic():
        run = ProcessingRun.objects.select_for_update().get(pk=run_id)
        if run.status != "pending" or run.cancel_requested_at:
            if run.status == "cancelling":
                run.status = "cancelled"
                run.cancelled_at = run.cancelled_at or timezone.now()
                run.finished_at = run.cancelled_at
                run.current_stage = ""
                run.message = "任务已取消；未开始处理"
                run.save(
                    update_fields=[
                        "status",
                        "cancelled_at",
                        "finished_at",
                        "current_stage",
                        "message",
                    ]
                )
            return run
        run.status = "running"
        run.started_at = timezone.now()
        run.last_heartbeat_at = run.started_at
        run.save(update_fields=["status", "started_at", "last_heartbeat_at"])
    step, mode, scope = run.step, run.mode, _run_scope(run)
    async_ai_scheduled = False
    try:
        messages = []
        from apps.pipeline.agent_kernel.matching import prepare_run
        prepare_run(run)
        for stage in _stage_steps(step):
            if stage == "step4" and mode == "ai":
                prepare_ai_stage(run, scope)
                if settings.CELERY_TASK_ALWAYS_EAGER:
                    _execute_ai_eager(run)
                else:
                    from .tasks import dispatch_ai_run_task

                    dispatch_ai_run_task.delay(run.id)
                    async_ai_scheduled = True
                break
            messages.append(f"{stage}: {_run_one_stage(run, stage, mode, scope)}")
        if async_ai_scheduled:
            run.refresh_from_db()
            return run
        if mode == "ai" and "step4" in _stage_steps(step):
            run.refresh_from_db()
            return run
        message = " | ".join(messages)
        run.refresh_from_db()
        run.status = (
            "partial_failed"
            if run.failed_count
            else "needs_attention"
            if run.needs_attention_count
            else "success"
        )
        run.message = message
    except RunCancelled:
        run.refresh_from_db()
        run.status = "cancelled"
        run.message = "任务已取消；已完成的候选人处理结果已保留"
        run.cancelled_at = run.cancelled_at or timezone.now()
        run.stages.filter(status="running").update(
            status="cancelled", finished_at=run.cancelled_at
        )
    except Exception as exc:  # noqa: BLE001 - 记录失败信息供前端展示
        run.status = "failed"
        run.message = f"{type(exc).__name__}: {exc}"
        run.error = run.message
    run.finished_at = timezone.now()
    run.last_heartbeat_at = run.finished_at
    run.current_stage = ""
    run.save()
    return run
