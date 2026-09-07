import hashlib
import json
import math
import statistics
from collections import Counter, defaultdict
from datetime import date, timedelta

from django.core.cache import cache
from django.db.models import (
    Avg,
    Count,
    DurationField,
    ExpressionWrapper,
    F,
    Min,
    Q,
)
from django.db.models.functions import TruncDate
from django.utils import timezone
from rest_framework import status
from rest_framework.response import Response
from rest_framework.views import APIView

from apps.accounts.permissions import HasPermissionCode
from apps.core.analytics_scope import (
    AnalyticsQueryError,
    effective_resume_ids,
    normalize_filters,
    rejection_reason_key,
    scoped_resumes,
)
from apps.core import models as m


CACHE_SECONDS = 300
TOP_N = 10


def _cache_key(filters):
    serializable = {
        key: value.isoformat() if isinstance(value, date) else value
        for key, value in filters.items()
    }
    digest = hashlib.sha256(
        json.dumps(serializable, sort_keys=True, ensure_ascii=True).encode("utf-8")
    ).hexdigest()
    return f"recruitment-analytics:v5:{digest}"


def _percentage(value, total):
    return round(value * 100 / total, 2) if total else 0


def _average_hours(attempts, end_field):
    queryset = attempts.exclude(**{f"{end_field}__isnull": True})
    durations = (
        queryset.values("workflow__candidate_id")
        .annotate(
            imported_at=Min("resume__imported_at"),
            event_at=Min(end_field),
        )
        .annotate(
            duration=ExpressionWrapper(
                F("event_at") - F("imported_at"), output_field=DurationField()
            )
        )
        .aggregate(value=Avg("duration"))["value"]
    )
    return round(durations.total_seconds() / 3600, 2) if durations else None


def _average_hours_to_event(attempts, event_types):
    first_by_candidate = {}
    rows = (
        m.AssignmentHandlingEvent.objects.filter(
            attempt__in=attempts,
            event_type__in=event_types,
        )
        .values(
            "attempt__workflow__candidate_id",
            "attempt__resume__imported_at",
            "occurred_at",
        )
        .order_by("occurred_at", "id")
    )
    for row in rows:
        first_by_candidate.setdefault(
            row["attempt__workflow__candidate_id"],
            _hours_between(row["attempt__resume__imported_at"], row["occurred_at"]),
        )
    values = [value for value in first_by_candidate.values() if value is not None]
    return round(sum(values) / len(values), 2) if values else None


def _choice_distribution(queryset, field, candidate_field, labels):
    rows = (
        queryset.exclude(
            Q(**{field: ""}) | Q(**{f"{field}__isnull": True})
        )
        .values(field)
        .annotate(count=Count(candidate_field, distinct=True))
        .order_by("-count", field)
    )
    return [
        {
            "key": row[field],
            "label": labels.get(row[field], row[field] or "未填写"),
            "count": row["count"],
        }
        for row in rows
    ]


def _alias_distribution(items, aliases):
    """把仅供内部审计的分类折叠为稳定的公开统计口径。"""
    merged = {}
    for item in items:
        key, label = aliases.get(item["key"], (item["key"], item["label"]))
        current = merged.setdefault(key, {"key": key, "label": label, "count": 0})
        current["count"] += item["count"]
    return sorted(merged.values(), key=lambda item: (-item["count"], item["key"]))


def _named_ranking(queryset, *, value_field, label_field, candidate_field):
    rows = (
        queryset.values(value_field, label_field)
        .annotate(count=Count(candidate_field, distinct=True))
        .order_by("-count", label_field)[:TOP_N]
    )
    return [
        {
            "key": row[value_field] or row[label_field] or "unknown",
            "label": row[label_field] or "未填写",
            "count": row["count"],
        }
        for row in rows
    ]


def _department_ranking(attempts):
    grouped_candidates = {}
    for row in attempts.values(
        "workflow__candidate_id",
        "current_department_id",
        "current_department__name",
        "current_department__level",
        "current_department__parent_id",
        "current_department__parent__name",
    ):
        level = row["current_department__level"]
        if level == 2:
            key = row["current_department_id"]
            label = row["current_department__name"]
        elif level == 3:
            key = row["current_department__parent_id"]
            label = row["current_department__parent__name"]
        else:
            key = None
            label = None
        label = label or "未分配"
        key = key or f"text:{label}"
        grouped_candidates.setdefault((key, label), set()).add(
            row["workflow__candidate_id"]
        )
    return [
        {"key": key, "label": label, "count": len(candidate_ids)}
        for (key, label), candidate_ids in sorted(
            grouped_candidates.items(), key=lambda item: (-len(item[1]), item[0][1])
        )[:TOP_N]
    ]


def _primary_department_ranking(attempts):
    grouped_candidates = {}
    for row in attempts.values(
        "workflow__candidate_id",
        "current_department_id",
        "current_department__name",
        "current_department__level",
        "current_department__parent_id",
        "current_department__parent__name",
        "current_department__parent__parent_id",
        "current_department__parent__parent__name",
    ):
        level = row["current_department__level"]
        if level == 1:
            key = row["current_department_id"]
            label = row["current_department__name"]
        elif level == 2:
            key = row["current_department__parent_id"]
            label = row["current_department__parent__name"]
        elif level == 3:
            key = row["current_department__parent__parent_id"]
            label = row["current_department__parent__parent__name"]
        else:
            key = None
            label = None
        label = label or "未归属一级部门"
        key = key or f"text:{label}"
        grouped_candidates.setdefault((key, label), set()).add(
            row["workflow__candidate_id"]
        )
    return [
        {"key": key, "label": label, "count": len(candidate_ids)}
        for (key, label), candidate_ids in sorted(
            grouped_candidates.items(), key=lambda item: (-len(item[1]), item[0][1])
        )[:TOP_N]
    ]


def _job_ranking(candidate_ids, base_resumes):
    resume_ids = [item for item in effective_resume_ids(candidate_ids, base_resumes) if item]
    rows = m.Resume.objects.filter(id__in=resume_ids).values(
        "candidate_id",
        "job_id",
        "job__public_name",
        "job__position_name",
        "position_name",
    )
    counter = Counter()
    for row in rows:
        label = (
            row["job__public_name"]
            or row["job__position_name"]
            or row["position_name"]
            or "未分类"
        )
        counter[(row["job_id"] or f"text:{label}", label)] += 1
    return [
        {"key": key, "label": label, "count": count}
        for (key, label), count in sorted(
            counter.items(), key=lambda item: (-item[1], item[0][1])
        )[:TOP_N]
    ]


def _school_tag_ranking(candidate_ids):
    counter = Counter()
    candidates = (
        m.Candidate.objects.filter(id__in=candidate_ids)
        .select_related("highest_degree_tag", "first_degree_tag")
        .prefetch_related("school_tags")
    )
    for candidate in candidates:
        tags = list(candidate.school_tags.all())
        if not tags:
            tags = list(
                {
                    tag.id: tag
                    for tag in (
                        candidate.highest_degree_tag,
                        candidate.first_degree_tag,
                    )
                    if tag
                }.values()
            )
        if not tags:
            counter[("text:未填写", "未填写")] += 1
        for tag in tags:
            counter[(tag.id, tag.name)] += 1
    return [
        {"key": key, "label": label, "count": count}
        for (key, label), count in sorted(
            counter.items(), key=lambda item: (-item[1], item[0][1])
        )[:TOP_N]
    ]


def _trend_rows(base_resumes, attempts, start_date, end_date):
    dates = []
    cursor = start_date
    while cursor <= end_date:
        dates.append(cursor)
        cursor += timedelta(days=1)
    trend = {
        item: {
            "date": item.isoformat(),
            "resumes": 0,
            "allocated": 0,
            "dispatched": 0,
            "feedback": 0,
            "passed": 0,
        }
        for item in dates
    }

    imports = (
        base_resumes.annotate(day=TruncDate("imported_at"))
        .values("day")
        .annotate(count=Count("id"))
    )
    for row in imports:
        if row["day"] in trend:
            trend[row["day"]]["resumes"] = row["count"]

    allocated_rows = (
        attempts.annotate(day=TruncDate("created_at"))
        .filter(day__gte=start_date, day__lte=end_date)
        .values("day")
        .annotate(count=Count("workflow__candidate_id", distinct=True))
    )
    for row in allocated_rows:
        if row["day"] in trend:
            trend[row["day"]]["allocated"] = row["count"]

    event_types = {
        "dispatched": [m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED],
        "feedback": [
            m.AssignmentHandlingEvent.EVENT_FEEDBACK_PASSED,
            m.AssignmentHandlingEvent.EVENT_FEEDBACK_REJECTED,
        ],
        "passed": [m.AssignmentHandlingEvent.EVENT_FEEDBACK_PASSED],
    }
    events = m.AssignmentHandlingEvent.objects.filter(attempt__in=attempts)
    for key, types in event_types.items():
        rows = (
            events.filter(event_type__in=types)
            .annotate(day=TruncDate("occurred_at"))
            .filter(day__gte=start_date, day__lte=end_date)
            .values("day")
            .annotate(count=Count("attempt__workflow__candidate_id", distinct=True))
        )
        for row in rows:
            if row["day"] in trend:
                trend[row["day"]][key] = row["count"]
    return list(trend.values())


def _hours_between(start_at, end_at):
    if not start_at or not end_at or end_at < start_at:
        return None
    return (end_at - start_at).total_seconds() / 3600


def _duration_metric(values):
    values = sorted(value for value in values if value is not None and value >= 0)
    if not values:
        return {"avg": None, "median": None, "p90": None, "sample_count": 0}
    p90_index = max(0, math.ceil(len(values) * 0.9) - 1)
    return {
        "avg": round(sum(values) / len(values), 2),
        "median": round(statistics.median(values), 2),
        "p90": round(values[p90_index], 2),
        "sample_count": len(values),
    }


def _department_identity(department):
    if department is None:
        return None
    if department.level == 1:
        primary = department
    elif department.level == 2:
        primary = department.parent
    elif department.level == 3:
        primary = department.parent.parent if department.parent else None
    else:
        primary = None
    return {
        "department_id": department.id,
        "department_name": department.name,
        "primary_department_id": primary.id if primary else None,
        "primary_department_name": primary.name if primary else "未归属一级部门",
    }


def _build_handling_speed(attempts):
    """按事件重建人工处理区段；自动转入三级前的瞬时区段不计时。"""
    attempt_rows = list(
        attempts.select_related(
            "current_department__parent__parent",
            "initial_department__parent__parent",
        )
    )
    attempt_by_id = {item.id: item for item in attempt_rows}
    events_by_attempt = defaultdict(list)
    events = (
        m.AssignmentHandlingEvent.objects.filter(attempt_id__in=attempt_by_id)
        .select_related(
            "from_department__parent__parent",
            "to_department__parent__parent",
        )
        .order_by("attempt_id", "occurred_at", "id")
    )
    for event in events:
        events_by_attempt[event.attempt_id].append(event)

    hr_dispatch_values = []
    department_values = []
    total_feedback_values = []
    department_completed = defaultdict(list)
    department_pending = defaultdict(list)
    department_objects = {}
    now = timezone.now()

    feedback_types = {
        m.AssignmentHandlingEvent.EVENT_FEEDBACK_PASSED,
        m.AssignmentHandlingEvent.EVENT_FEEDBACK_REJECTED,
    }
    for attempt in attempt_rows:
        attempt_events = events_by_attempt.get(attempt.id, [])
        first_dispatch_at = None
        active_department = None
        entered_at = None

        for event in attempt_events:
            if event.event_type == m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED:
                if first_dispatch_at is None:
                    first_dispatch_at = event.occurred_at
                    duration = _hours_between(attempt.created_at, first_dispatch_at)
                    if duration is not None:
                        hr_dispatch_values.append(duration)
                active_department = event.to_department
                entered_at = event.occurred_at
                if active_department:
                    department_objects[active_department.id] = active_department
                continue

            if event.event_type == m.AssignmentHandlingEvent.EVENT_DEPARTMENT_TRANSFERRED:
                if active_department and entered_at and not event.is_system_auto:
                    duration = _hours_between(entered_at, event.occurred_at)
                    if duration is not None:
                        department_values.append(duration)
                        department_completed[active_department.id].append(duration)
                active_department = event.to_department
                entered_at = event.occurred_at
                if active_department:
                    department_objects[active_department.id] = active_department
                continue

            if event.event_type in feedback_types:
                if first_dispatch_at is not None:
                    duration = _hours_between(first_dispatch_at, event.occurred_at)
                    if duration is not None:
                        total_feedback_values.append(duration)
                if active_department and entered_at:
                    duration = _hours_between(entered_at, event.occurred_at)
                    if duration is not None:
                        department_values.append(duration)
                        department_completed[active_department.id].append(duration)
                active_department = None
                entered_at = None
                break

            if event.event_type == m.AssignmentHandlingEvent.EVENT_CANCELLED:
                active_department = None
                entered_at = None
                break

        if (
            attempt.status == m.AssignmentAttempt.STATUS_DISPATCHED
            and active_department
            and entered_at
        ):
            age = _hours_between(entered_at, now)
            if age is not None:
                department_pending[active_department.id].append(age)
                department_objects[active_department.id] = active_department

    pending_values = [
        value for values in department_pending.values() for value in values
    ]
    departments = []
    for department_id in sorted(
        set(department_completed) | set(department_pending),
        key=lambda item: department_objects[item].name,
    ):
        identity = _department_identity(department_objects[department_id])
        pending = department_pending[department_id]
        departments.append(
            {
                **identity,
                "processing_hours": _duration_metric(
                    department_completed[department_id]
                ),
                "pending_count": len(pending),
                "max_pending_age_hours": round(max(pending), 2) if pending else None,
            }
        )

    return {
        "overall": {
            "hr_dispatch_hours": _duration_metric(hr_dispatch_values),
            "department_processing_hours": _duration_metric(department_values),
            "total_feedback_hours": _duration_metric(total_feedback_values),
            "pending_count": len(pending_values),
            "max_pending_age_hours": (
                round(max(pending_values), 2) if pending_values else None
            ),
        },
        "departments": departments,
    }


def _filter_options():
    return {
        "entities": list(
            m.Resume.objects.exclude(entity="")
            .order_by("entity")
            .values_list("entity", flat=True)
            .distinct()
        ),
        "jobs": [
            {"value": item.id, "label": str(item)}
            for item in m.Job.objects.filter(is_active=True).order_by(
                "public_name", "position_name", "id"
            )
        ],
        "primary_departments": [
            {"value": item.id, "label": item.name}
            for item in m.Department.objects.filter(level=1).order_by("name", "id")
        ],
        "departments": [
            {
                "value": item.id,
                "label": item.name,
                "parent_id": item.parent_id,
            }
            for item in m.Department.objects.filter(level=2).order_by("name", "id")
        ],
        "school_tags": [
            {"value": item.id, "label": item.name}
            for item in m.SchoolTag.objects.filter(is_active=True).order_by("name", "id")
        ],
        "educations": [
            {"value": value, "label": label}
            for value, label in m.Candidate.HIGHEST_EDUCATION_CHOICES
        ],
        "sources": [
            {"value": value, "label": label}
            for value, label in m.AssignmentAttempt.SOURCE_CHOICES
        ],
    }


def build_recruitment_overview(filters):
    resumes = scoped_resumes(filters)

    resume_ids = resumes.values("latest_effective_resume_id")
    candidate_ids = list(
        resumes.order_by().values_list("candidate_id", flat=True).distinct()
    )
    latest_attempt_ids = resumes.exclude(
        latest_effective_attempt_id__isnull=True
    ).values("latest_effective_attempt_id")
    attempts = m.AssignmentAttempt.objects.filter(id__in=latest_attempt_ids)
    decisions = m.AgentDispatchDecision.objects.filter(resume_id__in=resume_ids)
    workflows = m.CandidateWorkflow.objects.filter(candidate_id__in=candidate_ids)

    resume_count = resumes.count()
    candidate_count = len(candidate_ids)
    classified_count = (
        resumes.exclude(job_category="")
        .filter(
            Q(candidate__school_tags__isnull=False)
            | Q(candidate__first_degree_tag_id__isnull=False)
            | Q(candidate__highest_degree_tag_id__isnull=False)
            | ~Q(candidate__first_degree_platform="")
            | ~Q(candidate__highest_degree_platform="")
        )
        .values("candidate_id")
        .distinct()
        .count()
    )
    allocated_count = attempts.values("workflow__candidate_id").distinct().count()
    dispatched_count = (
        attempts.filter(
            status__in=[
                m.AssignmentAttempt.STATUS_DISPATCHED,
                m.AssignmentAttempt.STATUS_PASSED,
                m.AssignmentAttempt.STATUS_REJECTED,
            ]
        )
        .values("workflow__candidate_id")
        .distinct()
        .count()
    )
    feedback_count = (
        attempts.filter(
            status__in=[
                m.AssignmentAttempt.STATUS_PASSED,
                m.AssignmentAttempt.STATUS_REJECTED,
            ]
        )
        .values("workflow__candidate_id")
        .distinct()
        .count()
    )
    passed_count = (
        attempts.filter(status=m.AssignmentAttempt.STATUS_PASSED)
        .values("workflow__candidate_id")
        .distinct()
        .count()
    )
    archived_count = workflows.filter(
        status=m.CandidateWorkflow.STATUS_ARCHIVED
    ).count()

    summary = {
        "resume_count": resume_count,
        "candidate_count": candidate_count,
        "classified_count": classified_count,
        "allocated_count": allocated_count,
        "dispatched_count": dispatched_count,
        "feedback_count": feedback_count,
        "passed_count": passed_count,
        "archived_count": archived_count,
    }
    conversion = {
        "allocated_rate": _percentage(allocated_count, candidate_count),
        "dispatched_rate": _percentage(dispatched_count, candidate_count),
        "feedback_rate": _percentage(feedback_count, candidate_count),
        "passed_rate": _percentage(passed_count, candidate_count),
    }
    handling_speed = _build_handling_speed(attempts)
    average_hours = {
        "to_allocation": _average_hours(attempts, "created_at"),
        "to_dispatch": _average_hours_to_event(
            attempts,
            [m.AssignmentHandlingEvent.EVENT_DEPARTMENT_DISPATCHED],
        ),
        "to_feedback": _average_hours_to_event(
            attempts,
            [
                m.AssignmentHandlingEvent.EVENT_FEEDBACK_PASSED,
                m.AssignmentHandlingEvent.EVENT_FEEDBACK_REJECTED,
            ],
        ),
    }

    source_distribution = _choice_distribution(
        attempts,
        "source",
        "workflow__candidate_id",
        dict(m.AssignmentAttempt.SOURCE_CHOICES),
    )
    recommendation_distribution = _choice_distribution(
        decisions,
        "recommendation",
        "workflow__candidate_id",
        dict(m.AgentDispatchDecision.RECOMMEND_CHOICES),
    )
    ai_error_distribution = _alias_distribution(
        _choice_distribution(
            decisions,
            "error_code",
            "workflow__candidate_id",
            {},
        ),
        {
            "ai_special_route_unavailable": (
                "ai_connection_error",
                "AI 连接异常",
            )
        },
    )
    primary_department_ranking = _primary_department_ranking(attempts)
    department_ranking = _department_ranking(attempts)
    school_tag_ranking = _school_tag_ranking(candidate_ids)
    education_distribution = _choice_distribution(
        m.Candidate.objects.filter(id__in=candidate_ids),
        "highest_education",
        "id",
        dict(m.Candidate.HIGHEST_EDUCATION_CHOICES),
    )
    archive_reason_distribution = _choice_distribution(
        workflows.filter(status=m.CandidateWorkflow.STATUS_ARCHIVED),
        "archive_reason",
        "candidate_id",
        dict(m.CandidateWorkflow.ARCHIVE_REASON_CHOICES),
    )
    rejection_rows = (
        attempts.filter(
            status=m.AssignmentAttempt.STATUS_REJECTED,
        )
        .exclude(feedback_reason_code="")
        .values("feedback_reason_code")
        .annotate(count=Count("workflow__candidate_id", distinct=True))
        .order_by("-count", "feedback_reason_code")[:TOP_N]
    )
    rejection_labels = dict(m.AssignmentAttempt.REJECTION_REASON_CHOICES)
    rejection_reason_distribution = [
        {
            "key": rejection_reason_key(row["feedback_reason_code"]),
            "label": rejection_labels.get(
                row["feedback_reason_code"], row["feedback_reason_code"]
            ),
            "count": row["count"],
        }
        for row in rejection_rows
    ]

    return {
        "data_as_of": timezone.localtime().isoformat(),
        "filters": {
            key: value.isoformat() if isinstance(value, date) else value
            for key, value in filters.items()
        },
        "methodology": {
            "cohort": "Resume.imported_at 落在所选日期范围内的投递记录",
            "candidate_scope": "候选人数及各阶段按 Candidate 去重",
            "job_scope": "岗位排行优先使用 CandidateWorkflow.current_resume",
            "department_scope": (
                "部门筛选和排行使用候选人当前有效志愿的最新非取消分配尝试；"
                "三级收件归入父级二级部门，一级部门继续按当前部门树回溯"
            ),
            "conversion_denominator": "所选 cohort 去重候选人数",
            "handling_speed": (
                "自然时间小时；P90 使用最近秩；"
                "系统自动完成的部门转派不计转出部门人工处理时长"
            ),
        },
        "summary": summary,
        "conversion": conversion,
        "average_hours": average_hours,
        "handling_speed": handling_speed,
        "trend": _trend_rows(
            resumes, attempts, filters["date_from"], filters["date_to"]
        ),
        "source_distribution": source_distribution,
        "ai_recommendation_distribution": recommendation_distribution,
        "ai_error_distribution": ai_error_distribution,
        "job_ranking": _job_ranking(candidate_ids, resumes),
        "primary_department_ranking": primary_department_ranking,
        "department_ranking": department_ranking,
        "school_tag_ranking": school_tag_ranking,
        "education_distribution": education_distribution,
        "archive_reason_distribution": archive_reason_distribution,
        "rejection_reason_distribution": rejection_reason_distribution,
        "filter_options": _filter_options(),
    }


class RecruitmentOverviewView(APIView):
    permission_classes = [HasPermissionCode]
    permission_code = "analytics.view"

    def get(self, request):
        try:
            filters = normalize_filters(request.query_params)
        except AnalyticsQueryError as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
        key = _cache_key(filters)
        payload = cache.get(key)
        if payload is None:
            payload = build_recruitment_overview(filters)
            cache.set(key, payload, CACHE_SECONDS)
        return Response(payload)
