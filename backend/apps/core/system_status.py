"""Derived system resume status for candidate list and scoped reprocessing."""

from datetime import date
import re

from django.db.models import Q

from apps.core import candidate_summary
from apps.core import models as m
from apps.core.departments import secondary_department
from apps.core.name_pinyin import name_to_pinyin


RAW = "raw"
ARCHIVED = "archived"
PENDING_REALLOCATION = "pending_reallocation"
PENDING_REVIEW = "pending_review"
PENDING_DISPATCH = "pending_dispatch"
PENDING_SCREENING = "pending_screening"
SCREENING_PASSED = "screening_passed"
SCREENING_REJECTED = "screening_rejected"

LABELS = {
    RAW: "待处理",
    ARCHIVED: "已归档",
    PENDING_REALLOCATION: "待重新分配",
    PENDING_REVIEW: "待复核",
    PENDING_DISPATCH: "待下发",
    PENDING_SCREENING: "待业务反馈",
    SCREENING_PASSED: "通过",
    SCREENING_REJECTED: "不通过",
}

ACTIVE_ATTEMPT_STATUSES = {
    m.AssignmentAttempt.STATUS_PENDING_REVIEW,
    m.AssignmentAttempt.STATUS_PENDING_DISPATCH,
    m.AssignmentAttempt.STATUS_DISPATCHED,
    m.AssignmentAttempt.STATUS_PASSED,
    m.AssignmentAttempt.STATUS_REJECTED,
}


class InvalidSystemStatus(ValueError):
    pass


def normalize_statuses(value):
    if not value:
        return []
    if isinstance(value, str):
        values = value.split(",")
    else:
        values = value
    normalized = [str(item).strip() for item in values if str(item).strip()]
    unknown = sorted(set(normalized) - set(LABELS))
    if unknown:
        raise InvalidSystemStatus(
            f"不支持的简历状态：{','.join(unknown)}；可选值为 {','.join(LABELS)}"
        )
    return list(dict.fromkeys(normalized))


def normalize_workflow_statuses(value):
    if not value:
        return []
    if isinstance(value, str):
        values = value.split(",")
    else:
        values = value
    allowed = {choice[0] for choice in m.CandidateWorkflow.STATUS_CHOICES}
    return [item for item in [str(v).strip() for v in values] if item in allowed]


def current_resume(candidate, workflow=None):
    workflow = workflow or _workflow(candidate)
    if workflow and workflow.current_resume_id:
        return workflow.current_resume
    resumes = list(candidate.resumes.all())
    if not resumes:
        return None
    return sorted(
        resumes,
        key=lambda resume: (
            resume.volunteer_rank if resume.volunteer_rank is not None else 999,
            resume.apply_date.toordinal() if resume.apply_date else 0,
            resume.id,
        ),
    )[0]


def candidate_system_status(candidate):
    workflow = _workflow(candidate)
    resume = current_resume(candidate, workflow)
    attempt = candidate_summary.latest_effective_attempt(
        workflow, resume_id=resume.id if resume else None
    )
    if attempt and attempt.status == m.AssignmentAttempt.STATUS_PASSED:
        return SCREENING_PASSED
    if attempt and attempt.status == m.AssignmentAttempt.STATUS_REJECTED:
        return SCREENING_REJECTED
    if attempt and attempt.status == m.AssignmentAttempt.STATUS_DISPATCHED:
        return PENDING_SCREENING
    if attempt and attempt.status == m.AssignmentAttempt.STATUS_PENDING_REVIEW:
        return PENDING_REVIEW
    if attempt and attempt.status == m.AssignmentAttempt.STATUS_PENDING_DISPATCH:
        return PENDING_DISPATCH
    if workflow and workflow.block_reason == m.CandidateWorkflow.BLOCK_JOB_HC_EXHAUSTED:
        return PENDING_REALLOCATION
    return ARCHIVED if _has_processing_evidence(candidate, workflow) else RAW


def system_status_label(status):
    return LABELS.get(status, status or "")


def filter_queryset_by_system_status(qs, statuses):
    statuses = normalize_statuses(statuses)
    if not statuses:
        return qs
    candidates = (
        qs.select_related("workflow__current_resume")
        .prefetch_related("resumes", "workflow__attempts")
        .distinct()
    )
    ids = [
        candidate.id
        for candidate in candidates
        if candidate_system_status(candidate) in statuses
    ]
    return qs.filter(id__in=ids)


PROCESSING_RESULT_VALUES = {
    "success",
    "completed",
    "needs_attention",
    "failed",
    "review",
    "dispatch",
    "archive",
    "skipped",
    "cancelled",
}


def filter_queryset_by_processing_result(qs, params):
    """按处理中心某次 AI 运行的候选人级结果筛选。

    ``completed`` / ``needs_attention`` / ``failed`` / ``cancelled`` 与
    ScopeItem 的统一结果类型一一对应；``success`` 仅作为旧链接的 completed
    别名。待复核、待下发和建议归档是已完成业务结果的 AI 细分。
    """
    raw_run_id = _value(params, "processing_run_id")
    if not raw_run_id:
        return qs
    try:
        run_id = int(raw_run_id)
    except (TypeError, ValueError):
        return qs.none()

    result = _value(params, "processing_result")
    if not result:
        return qs.filter(processing_scope_items__run_id=run_id).distinct()
    if result not in PROCESSING_RESULT_VALUES:
        raise ValueError(f"不支持的处理结果：{result}")

    result_types = {
        "success": m.ProcessingRunScopeItem.RESULT_COMPLETED,
        "completed": m.ProcessingRunScopeItem.RESULT_COMPLETED,
        "needs_attention": m.ProcessingRunScopeItem.RESULT_NEEDS_ATTENTION,
        "failed": m.ProcessingRunScopeItem.RESULT_FAILED,
        "cancelled": m.ProcessingRunScopeItem.RESULT_CANCELLED,
    }
    scope_statuses = {
        "skipped": "skipped_manual_change",
    }
    if result in result_types:
        return qs.filter(
            processing_scope_items__run_id=run_id,
            processing_scope_items__result_type=result_types[result],
        ).distinct()
    if result in scope_statuses:
        return qs.filter(
            processing_scope_items__run_id=run_id,
            processing_scope_items__status=scope_statuses[result],
        ).distinct()

    recommendation = {
        "review": m.AgentDispatchDecision.RECOMMEND_REVIEW,
        "dispatch": m.AgentDispatchDecision.RECOMMEND_DISPATCH,
        "archive": m.AgentDispatchDecision.RECOMMEND_ARCHIVE,
    }[result]
    return qs.filter(
        workflow__agent_decisions__processing_run_id=run_id,
        workflow__agent_decisions__recommendation=recommendation,
    ).distinct()


def apply_candidate_filters(
    qs,
    params,
    *,
    current_resume_resolver=None,
    current_attempt_resolver=None,
):
    restricted_scope = current_resume_resolver is not None
    resume_for = current_resume_resolver or candidate_summary.current_resume
    attempt_for = current_attempt_resolver or _current_assignment_attempt

    def resume_text(candidate, attr):
        resume = resume_for(candidate)
        return str(getattr(resume, attr, "") or "") if resume else ""

    def job_department_name(candidate):
        resume = resume_for(candidate)
        department = resume.job.department if resume and resume.job_id else None
        department = secondary_department(department)
        return department.name if department else ""

    search = _value(params, "search")
    if search:
        normalized_search = search.strip().lower()
        if restricted_scope:
            qs = _filter_by_candidate_summary(
                qs,
                lambda candidate: (
                    normalized_search in candidate.name.casefold()
                    or normalized_search in candidate.name_pinyin.casefold()
                    or normalized_search in candidate.name_pinyin_initials.casefold()
                    or normalized_search in resume_text(candidate, "apply_id").casefold()
                    or normalized_search
                    in resume_text(candidate, "position_name").casefold()
                ),
            )
        else:
            qs = qs.filter(
                Q(name__icontains=search)
                | Q(name_pinyin__icontains=normalized_search)
                | Q(name_pinyin_initials__icontains=normalized_search)
                | Q(phone__icontains=search)
                | Q(resumes__apply_id__icontains=search)
                | Q(resumes__position_name__icontains=search)
            )
    name = _value(params, "name")
    if name:
        name_search = name.strip().lower()
        qs = qs.filter(
            Q(name__icontains=name)
            | Q(name_pinyin__icontains=name_search)
            | Q(name_pinyin_initials__icontains=name_search)
        )
    if _value(params, "phone"):
        qs = (
            qs.none()
            if restricted_scope
            else qs.filter(phone__icontains=_value(params, "phone"))
        )
    if _value(params, "first_degree_school"):
        qs = qs.filter(
            first_degree_school__icontains=_value(params, "first_degree_school")
        )
    if _value(params, "highest_degree_school"):
        qs = qs.filter(
            highest_degree_school__icontains=_value(params, "highest_degree_school")
        )
    highest_major_values = _list_value(params, "highest_major_in")
    if highest_major_values:
        qs = qs.filter(highest_major__in=highest_major_values)
    elif _value(params, "highest_major"):
        qs = qs.filter(highest_major__icontains=_value(params, "highest_major"))
    current_rank_values = _list_value(params, "current_rank_in")
    if current_rank_values:
        qs = _filter_by_candidate_summary_values(
            qs,
            current_rank_values,
            lambda candidate: getattr(resume_for(candidate), "volunteer_rank", None),
        )
    elif _value(params, "current_rank"):
        rank = _value(params, "current_rank")
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: str(
                getattr(resume_for(candidate), "volunteer_rank", None) or ""
            )
            == rank,
        )
    if _value(params, "current_apply_id"):
        apply_id = _value(params, "current_apply_id")
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: apply_id.lower()
            in resume_text(candidate, "apply_id").lower(),
        )
    qs = filter_queryset_by_current_apply_date(
        qs,
        params,
        resume_resolver=current_resume_resolver,
    )
    current_entity_values = _list_value(params, "current_entity_in")
    if current_entity_values:
        qs = _filter_by_candidate_summary_values(
            qs,
            current_entity_values,
            lambda candidate: resume_text(candidate, "entity"),
        )
    elif _value(params, "current_entity"):
        entity = _value(params, "current_entity")
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: entity.lower() in resume_text(candidate, "entity").lower(),
        )
    position_name_values = _list_value(params, "current_position_name_in")
    if position_name_values:
        qs = _filter_by_candidate_summary_values(
            qs,
            position_name_values,
            lambda candidate: resume_text(candidate, "position_name"),
        )
    elif _value(params, "current_position_name"):
        position_name = _value(params, "current_position_name")
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: position_name.lower()
            in resume_text(candidate, "position_name").lower(),
        )
    job_category_values = _list_value(params, "current_job_category_in")
    if job_category_values:
        qs = _filter_by_candidate_summary_values(
            qs,
            job_category_values,
            lambda candidate: resume_text(candidate, "job_category"),
        )
    elif _value(params, "current_job_category"):
        job_category = _value(params, "current_job_category")
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: job_category.lower()
            in resume_text(candidate, "job_category").lower(),
        )
    department_name_values = _list_value(params, "job_department_name_in")
    if department_name_values:
        qs = _filter_by_candidate_summary_values(
            qs,
            department_name_values,
            job_department_name,
        )
    elif _value(params, "job_department_name"):
        department_name = _value(params, "job_department_name")
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: department_name.lower()
            in job_department_name(candidate).lower(),
        )
    current_department_values = _list_value(params, "current_department_id")
    current_primary_department_values = _list_value(
        params, "current_primary_department_id"
    )

    def positive_ids(values, parameter):
        try:
            ids = {int(value) for value in values}
        except (TypeError, ValueError) as exc:
            raise ValueError(f"{parameter} 必须是正整数") from exc
        if any(value <= 0 for value in ids):
            raise ValueError(f"{parameter} 必须是正整数")
        return ids

    if current_department_values:
        current_department_ids = positive_ids(
            current_department_values, "current_department_id"
        )
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: getattr(
                attempt_for(candidate), "current_department_id", None
            )
            in current_department_ids,
        )
    if current_primary_department_values:
        current_primary_department_ids = positive_ids(
            current_primary_department_values, "current_primary_department_id"
        )

        def matches_primary_department(candidate):
            attempt = attempt_for(candidate)
            department = attempt.current_department if attempt else None
            while department and department.level > 1:
                department = department.parent
            return bool(
                department
                and department.level == 1
                and department.id in current_primary_department_ids
            )

        qs = _filter_by_candidate_summary(qs, matches_primary_department)
    school_tag_values = _list_value(params, "school_tag_in")
    if school_tag_values:
        normalized_school_tags = {
            str(value).strip().casefold()
            for value in school_tag_values
            if str(value).strip()
        }
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: bool(
                normalized_school_tags
                & {name.casefold() for name in candidate_school_tags(candidate)}
            ),
        )
    elif _value(params, "school_tag"):
        school_tag = _value(params, "school_tag")
        qs = qs.filter(
            Q(highest_degree_tag__name__icontains=school_tag)
            | Q(highest_degree_tag__code__icontains=school_tag)
            | Q(first_degree_tag__name__icontains=school_tag)
            | Q(first_degree_tag__code__icontains=school_tag)
            | Q(highest_degree_platform__icontains=school_tag)
            | Q(first_degree_platform__icontains=school_tag)
            | Q(school_tags__name__icontains=school_tag)
            | Q(school_tags__code__icontains=school_tag)
        ).distinct()
    workflow_statuses = (
        normalize_workflow_statuses(_list_value(params, "workflow_status"))
        or normalize_workflow_statuses(_list_value(params, "status"))
    )
    if workflow_statuses:
        if restricted_scope:
            qs = _filter_by_candidate_summary(
                qs,
                lambda candidate: _visible_workflow_status(attempt_for(candidate))
                in workflow_statuses,
            )
        else:
            status_filter = Q()
            if m.CandidateWorkflow.STATUS_PENDING in workflow_statuses:
                status_filter |= Q(
                    workflow__status=m.CandidateWorkflow.STATUS_PENDING
                ) | Q(workflow__isnull=True)
            explicit_statuses = [
                status
                for status in workflow_statuses
                if status != m.CandidateWorkflow.STATUS_PENDING
            ]
            if explicit_statuses:
                status_filter |= Q(workflow__status__in=explicit_statuses)
            qs = qs.filter(status_filter)
    reason_type = _value(params, "reason_type")
    if reason_type:
        if restricted_scope:
            if reason_type != candidate_summary.REASON_ASSIGNMENT:
                qs = qs.none()
        else:
            expected_reason = "" if reason_type == "none" else reason_type
            qs = _filter_by_candidate_summary(
                qs,
                lambda candidate: candidate_summary.reason(candidate)[0]
                == expected_reason,
            )
    reason_codes = _list_value(params, "reason_code")
    feedback_reason_codes = _list_value(params, "feedback_reason_code")
    if feedback_reason_codes:
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: getattr(
                attempt_for(candidate), "feedback_reason_code", ""
            )
            in feedback_reason_codes,
        )
    run_id = _value(params, "processing_run_id")
    if reason_codes:
        if restricted_scope:
            qs = _filter_by_candidate_summary(
                qs,
                lambda candidate: getattr(
                    attempt_for(candidate), "feedback_reason_code", ""
                )
                in reason_codes,
            )
        else:
            # 原因筛选必须命中列表当前 ScopeItem；主表不再支持处理结果表头筛选。
            def matches_current_processing_item(candidate):
                item = candidate_summary.latest_processing_scope_item(candidate, run_id)
                return bool(item and item.reason_code in reason_codes)

            qs = _filter_by_candidate_summary(qs, matches_current_processing_item)
    if _value(params, "imported_after"):
        qs = qs.filter(imported_at__date__gte=_value(params, "imported_after"))
    if _value(params, "imported_before"):
        qs = qs.filter(imported_at__date__lte=_value(params, "imported_before"))
    system_statuses = (
        _list_value(params, "system_statuses") or _list_value(params, "system_status")
    )
    if restricted_scope and system_statuses:
        statuses = normalize_statuses(system_statuses)
        qs = _filter_by_candidate_summary(
            qs,
            lambda candidate: _visible_system_status(attempt_for(candidate)) in statuses,
        )
    else:
        qs = filter_queryset_by_system_status(qs, system_statuses)
    if restricted_scope and (
        run_id or _value(params, "processing_result")
    ):
        return qs.none()
    return filter_queryset_by_processing_result(qs, params).distinct()


def filter_queryset_by_current_apply_date(qs, params, *, resume_resolver=None):
    start_date, end_date = parse_current_apply_date_range(params)
    if not start_date and not end_date:
        return qs

    def matches(candidate):
        if resume_resolver:
            resume = resume_resolver(candidate)
            apply_date = resume.apply_date if resume else None
        else:
            apply_date = candidate_summary.current_apply_date(candidate)
        if not apply_date:
            return False
        if start_date and apply_date < start_date:
            return False
        if end_date and apply_date > end_date:
            return False
        return True

    return _filter_by_candidate_summary(qs, matches)


def parse_current_apply_date_range(params):
    raw_start = _value(params, "current_apply_date_from")
    raw_end = _value(params, "current_apply_date_to")

    def parse(raw_value, parameter):
        if raw_value in (None, ""):
            return None
        value = str(raw_value).strip()
        if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", value):
            raise ValueError(f"{parameter} 日期格式必须为 YYYY-MM-DD")
        try:
            return date.fromisoformat(value)
        except ValueError as exc:
            raise ValueError(f"{parameter} 日期格式必须为 YYYY-MM-DD") from exc

    start_date = parse(raw_start, "current_apply_date_from")
    end_date = parse(raw_end, "current_apply_date_to")
    if start_date and end_date and start_date > end_date:
        raise ValueError("投递时间开始日期不能晚于结束日期")
    return start_date, end_date


def candidate_filter_options(
    qs, *, current_resume_resolver=None, current_attempt_resolver=None
):
    """汇总简历库主表当前展示字段的可选值，供前端下拉筛选复用。"""
    resume_for = current_resume_resolver or candidate_summary.current_resume
    attempt_for = current_attempt_resolver or _current_assignment_attempt
    values = {
        "highest_major": set(),
        "current_rank": set(),
        "current_entity": set(),
        "current_position_name": set(),
        "job_department_name": set(),
        "current_job_category": set(),
        "school_tag": set(),
    }
    current_departments = {}
    current_primary_departments = {}
    for candidate in qs:
        resume = resume_for(candidate)
        attempt = attempt_for(candidate)
        current_department = attempt.current_department if attempt else None
        primary_department = current_department
        while primary_department and primary_department.level > 1:
            primary_department = primary_department.parent
        _add_option(values["highest_major"], candidate.highest_major)
        _add_option(values["current_rank"], getattr(resume, "volunteer_rank", None))
        _add_option(values["current_entity"], getattr(resume, "entity", ""))
        _add_option(
            values["current_position_name"], getattr(resume, "position_name", "")
        )
        job_department = resume.job.department if resume and resume.job_id else None
        job_department = secondary_department(job_department)
        _add_option(
            values["job_department_name"],
            job_department.name if job_department else "",
        )
        _add_option(values["current_job_category"], getattr(resume, "job_category", ""))
        for tag_name in candidate_school_tags(candidate):
            _add_option(values["school_tag"], tag_name)
        if current_department:
            current_departments[current_department.id] = current_department.name
        if primary_department and primary_department.level == 1:
            current_primary_departments[primary_department.id] = primary_department.name

    raw_options = {
        **{
            key: sorted(items, key=str.casefold)
            for key, items in values.items()
            if key != "current_rank"
        },
        "current_rank": sorted(values["current_rank"], key=lambda value: int(value)),
    }
    result = {
        key: [_filter_option(value) for value in options]
        for key, options in raw_options.items()
    }
    result["current_department"] = [
        _department_filter_option(department_id, name)
        for department_id, name in sorted(
            current_departments.items(), key=lambda item: item[1].casefold()
        )
    ]
    result["current_primary_department"] = [
        _department_filter_option(department_id, name)
        for department_id, name in sorted(
            current_primary_departments.items(), key=lambda item: item[1].casefold()
        )
    ]
    return result


def _filter_option(value):
    label = str(value)
    full_pinyin, initials = name_to_pinyin(label)
    return {
        "label": label,
        "value": label,
        "search_text": " ".join(
            item for item in [label.casefold(), full_pinyin, initials] if item
        ),
    }


def _department_filter_option(department_id, name):
    option = _filter_option(name)
    option["value"] = department_id
    return option


def _filter_by_candidate_summary(qs, predicate):
    candidates = (
        qs.select_related(
            "workflow",
            "workflow__current_resume",
            "workflow__current_resume__job",
            "workflow__current_resume__job__department",
            "workflow__current_resume__job__department__parent",
        )
        .prefetch_related(
            "resumes",
            "school_tags",
            "resumes__job__department__parent",
            "workflow__attempts__resume",
            "workflow__attempts__current_department__parent__parent",
            "processing_scope_items",
        )
        .distinct()
    )
    ids = [candidate.id for candidate in candidates if predicate(candidate)]
    return qs.filter(id__in=ids)


def _filter_by_candidate_summary_values(qs, values, accessor):
    normalized_values = {str(value).strip().casefold() for value in values if str(value).strip()}
    return _filter_by_candidate_summary(
        qs,
        lambda candidate: str(accessor(candidate) or "").strip().casefold()
        in normalized_values,
    )


def _current_resume_text(candidate, attr):
    resume = candidate_summary.current_resume(candidate)
    return str(getattr(resume, attr, "") or "") if resume else ""


def _current_assignment_attempt(candidate):
    workflow = _workflow(candidate)
    resume = candidate_summary.current_resume(candidate)
    return candidate_summary.latest_effective_attempt(
        workflow, resume_id=resume.id if resume else None
    )


def _visible_system_status(attempt):
    if not attempt:
        return RAW
    if attempt.status == m.AssignmentAttempt.STATUS_PASSED:
        return SCREENING_PASSED
    if attempt.status == m.AssignmentAttempt.STATUS_REJECTED:
        return SCREENING_REJECTED
    return PENDING_SCREENING


def _visible_workflow_status(attempt):
    if attempt and attempt.status == m.AssignmentAttempt.STATUS_PASSED:
        return m.CandidateWorkflow.STATUS_PASSED
    return m.CandidateWorkflow.STATUS_IN_PROGRESS


def candidate_school_tag(candidate):
    return "、".join(candidate_school_tags(candidate))


def candidate_school_tags(candidate):
    names = [tag.name for tag in candidate.school_tags.all()]
    if names:
        return list(dict.fromkeys(names))
    return list(
        dict.fromkeys(
            value
            for value in (
                getattr(candidate.highest_degree_tag, "name", ""),
                getattr(candidate.first_degree_tag, "name", ""),
                candidate.highest_degree_platform,
                candidate.first_degree_platform,
            )
            if value
        )
    )


def _add_option(values, value):
    text = str(value or "").strip()
    if text:
        values.add(text)


def _workflow(candidate):
    try:
        return candidate.workflow
    except (m.CandidateWorkflow.DoesNotExist, AttributeError):
        return None


def _has_processing_evidence(candidate, workflow):
    """排队/提交不算处理；只认已落地的筛选、决策、尝试或业务阻塞证据。"""
    if workflow:
        if workflow.archive_reason or workflow.block_reason or workflow.started_at:
            return True
        if list(workflow.attempts.all()):
            return True
        if workflow.agent_decisions.exists():
            return True
    if any(
        resume.category_mode in {"rule", "ai"} or bool(resume.job_category)
        for resume in candidate.resumes.all()
    ):
        return True
    terminal_statuses = {
        "success",
        "needs_attention",
        "failed",
        "skipped_manual_change",
        "cancelled",
    }
    return any(
        bool(item.result_type) or item.status in terminal_statuses
        for item in candidate.processing_scope_items.all()
    )


def _value(params, key):
    if hasattr(params, "get"):
        value = params.get(key)
    else:
        value = None
    if isinstance(value, list):
        return value[0] if value else None
    return value


def _list_value(params, key):
    if hasattr(params, "getlist"):
        values = params.getlist(key)
        if values:
            return [
                item.strip()
                for value in values
                for item in str(value).split(",")
                if item.strip()
            ]
    value = params.get(key) if hasattr(params, "get") else None
    if value is None:
        return []
    if isinstance(value, list):
        return [
            item.strip()
            for entry in value
            for item in str(entry).split(",")
            if item.strip()
        ]
    return str(value).split(",")
