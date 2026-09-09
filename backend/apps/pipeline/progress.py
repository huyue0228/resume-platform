"""任务节点状态；提交时登记完整计划，worker 逐节点推进。"""
from django.utils import timezone

from apps.core.models import ProcessingRunStage


STAGE_LABELS = {
    "queued": "等待后台处理",
    "initialize": "检查处理服务",
    "preparing": "准备候选人材料",
    "step1": "整理简历与志愿",
    "step2": "核验学历与院校",
    "step3": "匹配可选岗位",
    "step4": "AI 分析与保存结果",
    "finalize": "汇总处理结果",
}
STAGE_DESCRIPTIONS = {
    "queued": "任务已提交，等待后台开始处理。",
    "initialize": "检查模型连接和分析服务，准备本次处理配置。",
    "preparing": "读取所选简历，准备候选人材料和岗位信息。",
    "step1": "检查重复投递，整理候选人的志愿顺序。",
    "step2": "依据院校和学历要求核验候选人资格。",
    "step3": "确定当前志愿对应的可选岗位。",
    "step4": "逐份分析简历与岗位匹配情况，并保存结果。",
    "finalize": "核对完成、需处理和失败数量，生成任务汇总。",
}


def begin_stage(run, step, *, total=1, message=""):
    now = timezone.now()
    run.current_stage = step
    run.last_heartbeat_at = now
    run.message = message or STAGE_DESCRIPTIONS.get(step, "")
    run.save(update_fields=["current_stage", "last_heartbeat_at", "message"])
    run.stages.filter(step=step).update(
        status="running", started_at=now, total_count=total,
        processed_count=0, message=run.message, error="",
    )


def finish_stage(run, step, *, message=""):
    for stage in run.stages.filter(step=step):
        stage.status = "success"
        stage.started_at = stage.started_at or timezone.now()
        stage.finished_at = timezone.now()
        stage.processed_count = stage.total_count
        stage.success_count = stage.total_count
        stage.message = message or stage.message
        stage.save(update_fields=["status", "started_at", "finished_at", "processed_count", "success_count", "message"])


def stop_stages(run_id, *, status, message):
    """保留已完成节点；中止当前节点，后续节点明确标为未执行。"""
    now = timezone.now()
    stages = ProcessingRunStage.objects.filter(run_id=run_id)
    stages.filter(status__in=["running", "waiting_conflict"]).update(
        status=status, message=message, error=message if status == "failed" else "", finished_at=now,
    )
    stages.filter(status="pending").update(
        status="skipped", message="任务已停止，此节点未执行", finished_at=now,
    )
