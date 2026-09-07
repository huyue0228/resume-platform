"""Step1 查重与志愿排序：按 Candidate 聚合投递，组内按南北规则 + 投递时间排序。"""
from datetime import date

from django.db import transaction
from django.utils import timezone

from apps.core import models as m

from ..cancellation import raise_if_cancel_requested
from ..regions import candidate_region


def candidate_ids_for_scope(scope=None):
    scope = scope or {}
    candidate_ids = scope.get("candidate_ids") or []
    qs = m.Candidate.objects.all()
    if candidate_ids:
        qs = qs.filter(id__in=candidate_ids)
    return qs.order_by("id").values_list("id", flat=True)


def _sync_progress(processing_run, processing_stage, count):
    if not processing_run:
        return
    processing_run.last_heartbeat_at = timezone.now()
    processing_run.save(update_fields=["last_heartbeat_at"])
    if processing_stage:
        processing_stage.processed_count = count
        processing_stage.success_count = count
        processing_stage.save(update_fields=["processed_count", "success_count"])


def run(scope=None, processing_run=None, processing_stage=None):
    count = 0
    candidate_ids = list(candidate_ids_for_scope(scope))
    if processing_run:
        processing_run.total_count = len(candidate_ids)
        processing_run.save(update_fields=["total_count"])
    if processing_stage:
        processing_stage.total_count = len(candidate_ids)
        processing_stage.save(update_fields=["total_count"])

    for index, candidate_id in enumerate(candidate_ids, start=1):
        raise_if_cancel_requested(processing_run)
        # 候选人级行锁使并发上传/重跑不会同时改写同一人的志愿顺序。
        with transaction.atomic():
            cand = m.Candidate.objects.select_for_update().prefetch_related("resumes").get(pk=candidate_id)
            resumes = list(cand.resumes.all())
            if resumes:
                region = candidate_region(cand)
                preferred = "GW" if region == "北" else ("YLS" if region == "南" else None)

                def sort_key(r):
                    pref = 0 if (preferred and r.entity == preferred) else 1
                    return (pref, r.apply_date or date.max)

                resumes.sort(key=sort_key)
                item = processing_run.scope_items.filter(candidate_id=candidate_id).first() if processing_run else None
                frozen = item.kernel_snapshot if item else {}
                if item and item.result_type:
                    continue
                if frozen.get("lane") in {"enforced", "review_only"} and frozen.get("preflight"):
                    ids = frozen["volunteer_ids"]
                    order = {ids[ref]: rank for rank, ref in enumerate(frozen["preflight"]["volunteer_order"])}
                    resumes.sort(key=lambda resume: order.get(resume.pk, len(order)))
                for rank, resume in enumerate(resumes, start=1):
                    resume.volunteer_rank = rank
                    resume.assigned_entity = resume.entity
                    resume.save(update_fields=["volunteer_rank", "assigned_entity"])
                    count += 1
        _sync_progress(processing_run, processing_stage, index)
    return f"已为 {count} 条投递完成查重与志愿排序"
