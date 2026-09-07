"""招聘分析与简历库下钻共用的候选人范围口径。"""

import json
from datetime import date, datetime, time, timedelta

from django.db.models import Case, F, IntegerField, OuterRef, Q, Subquery, When
from django.db.models.functions import Coalesce
from django.utils import timezone

from apps.core import candidate_summary
from apps.core import models as m


MAX_RANGE_DAYS = 366
MAX_DRILLDOWN_VALUES = 20


class AnalyticsQueryError(ValueError):
    pass


def _parse_date(value, field_name):
    if not value:
        return None
    try:
        return date.fromisoformat(value)
    except (TypeError, ValueError) as exc:
        raise AnalyticsQueryError(f"{field_name} 必须是 YYYY-MM-DD") from exc


def _parse_int(value, field_name):
    if value in (None, ""):
        return None
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise AnalyticsQueryError(f"{field_name} 必须是正整数") from exc
    if parsed <= 0:
        raise AnalyticsQueryError(f"{field_name} 必须是正整数")
    return parsed


def normalize_filters(params):
    today = timezone.localdate()
    date_to = _parse_date(params.get("date_to"), "date_to") or today
    date_from = _parse_date(params.get("date_from"), "date_from") or (
        date_to - timedelta(days=29)
    )
    if date_from > date_to:
        raise AnalyticsQueryError("date_from 不能晚于 date_to")
    if (date_to - date_from).days + 1 > MAX_RANGE_DAYS:
        raise AnalyticsQueryError(f"日期范围不能超过 {MAX_RANGE_DAYS} 天")

    education = str(params.get("education") or "").strip()
    allowed_educations = {item[0] for item in m.Candidate.HIGHEST_EDUCATION_CHOICES}
    if education and education not in allowed_educations:
        raise AnalyticsQueryError("education 不是有效学历代码")
    source = str(params.get("source") or "").strip()
    allowed_sources = {item[0] for item in m.AssignmentAttempt.SOURCE_CHOICES}
    if source and source not in allowed_sources:
        raise AnalyticsQueryError("source 不是有效分配来源")

    return {
        "date_from": date_from,
        "date_to": date_to,
        "entity": str(params.get("entity") or "").strip(),
        "job_id": _parse_int(params.get("job_id"), "job_id"),
        "primary_department_id": _parse_int(
            params.get("primary_department_id"), "primary_department_id"
        ),
        "department_id": _parse_int(params.get("department_id"), "department_id"),
        "school_tag_id": _parse_int(params.get("school_tag_id"), "school_tag_id"),
        "education": education,
        "source": source,
    }


def scoped_resumes(filters):
    """返回投递 cohort，并按候选人的当前有效志愿附加唯一分配尝试。"""
    current_timezone = timezone.get_current_timezone()
    start_at = timezone.make_aware(
        datetime.combine(filters["date_from"], time.min), current_timezone
    )
    end_at = timezone.make_aware(
        datetime.combine(filters["date_to"] + timedelta(days=1), time.min),
        current_timezone,
    )
    resumes = m.Resume.objects.filter(imported_at__gte=start_at, imported_at__lt=end_at)
    if filters["entity"]:
        resumes = resumes.filter(entity=filters["entity"])
    if filters["school_tag_id"]:
        resumes = resumes.filter(
            Q(candidate__school_tags__id=filters["school_tag_id"])
            | Q(
                candidate__school_tags__isnull=True,
                candidate__highest_degree_tag_id=filters["school_tag_id"],
            )
            | Q(
                candidate__school_tags__isnull=True,
                candidate__highest_degree_tag__isnull=True,
                candidate__first_degree_tag_id=filters["school_tag_id"],
            )
        ).distinct()
    if filters["education"]:
        resumes = resumes.filter(candidate__highest_education=filters["education"])

    workflow_current_resume = m.CandidateWorkflow.objects.filter(
        candidate_id=OuterRef("candidate_id"),
        current_resume_id__isnull=False,
    )
    cohort_fallback_resume = m.Resume.objects.filter(
        candidate_id=OuterRef("candidate_id"),
        imported_at__gte=start_at,
        imported_at__lt=end_at,
    )
    if filters["entity"]:
        cohort_fallback_resume = cohort_fallback_resume.filter(entity=filters["entity"])
    cohort_fallback_resume = cohort_fallback_resume.order_by(
        "volunteer_rank", "apply_date", "id"
    )
    resumes = resumes.annotate(
        workflow_current_resume_id=Subquery(
            workflow_current_resume.values("current_resume_id")[:1],
            output_field=IntegerField(),
        ),
        cohort_fallback_resume_id=Subquery(
            cohort_fallback_resume.values("id")[:1],
            output_field=IntegerField(),
        ),
    ).annotate(
        latest_effective_resume_id=Coalesce(
            "workflow_current_resume_id",
            "cohort_fallback_resume_id",
            output_field=IntegerField(),
        )
    )

    effective_resume = m.Resume.objects.filter(pk=OuterRef("latest_effective_resume_id"))
    resumes = resumes.annotate(
        latest_effective_job_id=Subquery(effective_resume.values("job_id")[:1])
    )
    if filters["job_id"]:
        resumes = resumes.filter(latest_effective_job_id=filters["job_id"])

    latest_attempt = (
        m.AssignmentAttempt.objects.filter(
            resume_id=OuterRef("latest_effective_resume_id")
        )
        .exclude(status=m.AssignmentAttempt.STATUS_CANCELLED)
        .annotate(
            current_primary_department_id=Case(
                When(
                    current_department__level=1,
                    then=F("current_department_id"),
                ),
                When(
                    current_department__level=2,
                    then=F("current_department__parent_id"),
                ),
                When(
                    current_department__level=3,
                    then=F("current_department__parent__parent_id"),
                ),
                default=None,
                output_field=IntegerField(),
            ),
            current_secondary_department_id=Case(
                When(
                    current_department__level=2,
                    then=F("current_department_id"),
                ),
                When(
                    current_department__level=3,
                    then=F("current_department__parent_id"),
                ),
                default=None,
                output_field=IntegerField(),
            )
        )
        .order_by("-attempt_no", "-id")
    )
    resumes = resumes.annotate(
        latest_effective_attempt_id=Subquery(latest_attempt.values("id")[:1]),
        latest_effective_secondary_department_id=Subquery(
            latest_attempt.values("current_secondary_department_id")[:1]
        ),
        latest_effective_primary_department_id=Subquery(
            latest_attempt.values("current_primary_department_id")[:1]
        ),
        latest_effective_source=Subquery(latest_attempt.values("source")[:1]),
    )
    if filters["primary_department_id"]:
        resumes = resumes.filter(
            latest_effective_primary_department_id=filters["primary_department_id"]
        )
    if filters["department_id"]:
        resumes = resumes.filter(
            latest_effective_secondary_department_id=filters["department_id"]
        )
    if filters["source"]:
        resumes = resumes.filter(latest_effective_source=filters["source"])
    return resumes.distinct()


def effective_resume_ids(candidate_ids, base_resumes):
    current = dict(
        m.CandidateWorkflow.objects.filter(
            candidate_id__in=candidate_ids, current_resume_id__isnull=False
        ).values_list("candidate_id", "current_resume_id")
    )
    fallback = {}
    for candidate_id, resume_id in base_resumes.order_by(
        "candidate_id", "volunteer_rank", "apply_date", "id"
    ).values_list("candidate_id", "id"):
        fallback.setdefault(candidate_id, resume_id)
    return [
        current.get(candidate_id) or fallback.get(candidate_id)
        for candidate_id in candidate_ids
    ]


def rejection_reason_key(value):
    """结构化拒绝原因本身就是稳定、可安全下钻的公开代码。"""
    return str(value or "")


DRILLDOWN_DIMENSIONS = {
    "candidate",
    "classified",
    "allocated",
    "dispatched",
    "feedback",
    "passed",
    "archived",
    "source",
    "ai_recommendation",
    "ai_error",
    "job",
    "primary_department",
    "department",
    "school_tag",
    "education",
    "archive_reason",
    "rejection_reason",
}

VALUE_DIMENSIONS = {
    "source",
    "ai_recommendation",
    "ai_error",
    "job",
    "primary_department",
    "department",
    "school_tag",
    "education",
    "archive_reason",
    "rejection_reason",
}


def _json_string_list(params, key):
    raw = params.get(key)
    if raw in (None, ""):
        return []
    try:
        values = json.loads(raw)
    except (TypeError, ValueError) as exc:
        raise AnalyticsQueryError(f"{key} 必须是字符串或整数数组") from exc
    if (
        not isinstance(values, list)
        or len(values) > MAX_DRILLDOWN_VALUES
        or any(not isinstance(item, (str, int)) for item in values)
    ):
        raise AnalyticsQueryError(
            f"{key} 必须是不超过 {MAX_DRILLDOWN_VALUES} 项的字符串或整数数组"
        )
    return [str(item) for item in values]


def _drilldown_filters(params):
    return normalize_filters(
        {
            key: params.get(f"analytics_{key}")
            for key in (
                "date_from",
                "date_to",
                "entity",
                "job_id",
                "primary_department_id",
                "department_id",
                "school_tag_id",
                "education",
                "source",
            )
        }
    )


def _row_matches(pairs, key, label):
    key = str(key)
    return any(
        key == expected_key and (not expected_label or label == expected_label)
        for expected_key, expected_label in pairs
    )


def apply_candidate_drilldown(qs, params):
    """按看板生效条件和点击的数据项收窄候选人列表。"""
    dimension = str(params.get("analytics_dimension") or "").strip()
    if not dimension:
        return qs
    if dimension not in DRILLDOWN_DIMENSIONS:
        raise AnalyticsQueryError("analytics_dimension 不是有效的看板下钻类型")

    values = _json_string_list(params, "analytics_values")
    labels = _json_string_list(params, "analytics_value_labels")
    if labels and len(labels) != len(values):
        raise AnalyticsQueryError("analytics_value_labels 与 analytics_values 数量不一致")
    if dimension in VALUE_DIMENSIONS and not values:
        raise AnalyticsQueryError("当前看板下钻类型必须提供 analytics_values")
    labels.extend([""] * (len(values) - len(labels)))
    pairs = list(zip(values, labels))

    resumes = scoped_resumes(_drilldown_filters(params))
    resume_ids = resumes.values("latest_effective_resume_id")
    candidate_ids = list(
        resumes.order_by().values_list("candidate_id", flat=True).distinct()
    )
    if dimension == "candidate":
        return qs.filter(id__in=candidate_ids)
    if dimension == "classified":
        matched_ids = (
            resumes.exclude(job_category="")
            .filter(
                Q(candidate__school_tags__isnull=False)
                | Q(candidate__first_degree_tag_id__isnull=False)
                | Q(candidate__highest_degree_tag_id__isnull=False)
                | ~Q(candidate__first_degree_platform="")
                | ~Q(candidate__highest_degree_platform="")
            )
            .values("candidate_id")
        )
        return qs.filter(id__in=matched_ids)

    latest_attempt_ids = resumes.exclude(
        latest_effective_attempt_id__isnull=True
    ).values("latest_effective_attempt_id")
    attempts = m.AssignmentAttempt.objects.filter(id__in=latest_attempt_ids)
    if dimension == "allocated":
        return qs.filter(id__in=attempts.values("workflow__candidate_id"))
    if dimension == "dispatched":
        return qs.filter(
            id__in=attempts.filter(
                status__in=[
                    m.AssignmentAttempt.STATUS_DISPATCHED,
                    m.AssignmentAttempt.STATUS_PASSED,
                    m.AssignmentAttempt.STATUS_REJECTED,
                ]
            ).values("workflow__candidate_id")
        )
    if dimension == "feedback":
        return qs.filter(
            id__in=attempts.filter(
                status__in=[
                    m.AssignmentAttempt.STATUS_PASSED,
                    m.AssignmentAttempt.STATUS_REJECTED,
                ]
            ).values("workflow__candidate_id")
        )
    if dimension == "passed":
        return qs.filter(
            id__in=attempts.filter(
                status=m.AssignmentAttempt.STATUS_PASSED
            ).values("workflow__candidate_id")
        )
    if dimension == "source":
        return qs.filter(
            id__in=attempts.filter(source__in=values).values("workflow__candidate_id")
        )
    if dimension in {"primary_department", "department"}:
        matched_ids = []
        rows = attempts.select_related(
            "current_department__parent__parent"
        ).values(
            "workflow__candidate_id",
            "current_department_id",
            "current_department__name",
            "current_department__level",
            "current_department__parent_id",
            "current_department__parent__name",
            "current_department__parent__parent_id",
            "current_department__parent__parent__name",
        )
        for row in rows:
            if dimension == "primary_department":
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
            else:
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
            if _row_matches(pairs, key, label):
                matched_ids.append(row["workflow__candidate_id"])
        return qs.filter(id__in=matched_ids)

    workflows = m.CandidateWorkflow.objects.filter(candidate_id__in=candidate_ids)
    if dimension == "archived":
        return qs.filter(
            id__in=workflows.filter(status=m.CandidateWorkflow.STATUS_ARCHIVED).values(
                "candidate_id"
            )
        )
    if dimension == "archive_reason":
        return qs.filter(
            id__in=workflows.filter(
                status=m.CandidateWorkflow.STATUS_ARCHIVED,
                archive_reason__in=values,
            ).values("candidate_id")
        )

    decisions = m.AgentDispatchDecision.objects.filter(resume_id__in=resume_ids)
    if dimension == "ai_recommendation":
        return qs.filter(
            id__in=decisions.filter(recommendation__in=values).values(
                "workflow__candidate_id"
            )
        )
    if dimension == "ai_error":
        error_values = set(values)
        if "ai_connection_error" in error_values:
            error_values.add("ai_special_route_unavailable")
        return qs.filter(
            id__in=decisions.filter(error_code__in=error_values).values(
                "workflow__candidate_id"
            )
        )

    if dimension == "job":
        effective_ids = [
            item for item in effective_resume_ids(candidate_ids, resumes) if item
        ]
        matched_ids = []
        for row in m.Resume.objects.filter(id__in=effective_ids).values(
            "candidate_id",
            "job_id",
            "job__public_name",
            "job__position_name",
            "position_name",
        ):
            label = (
                row["job__public_name"]
                or row["job__position_name"]
                or row["position_name"]
                or "未分类"
            )
            key = row["job_id"] or f"text:{label}"
            if _row_matches(pairs, key, label):
                matched_ids.append(row["candidate_id"])
        return qs.filter(id__in=matched_ids)

    if dimension == "school_tag":
        matched_ids = []
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
            if any(_row_matches(pairs, tag.id, tag.name) for tag in tags):
                matched_ids.append(candidate.id)
        return qs.filter(id__in=matched_ids)
    if dimension == "education":
        return qs.filter(id__in=candidate_ids, highest_education__in=values)
    if dimension == "rejection_reason":
        return qs.filter(
            id__in=attempts.filter(
                status=m.AssignmentAttempt.STATUS_REJECTED,
                feedback_reason_code__in=values,
            ).values("workflow__candidate_id")
        )

    return qs.none()
