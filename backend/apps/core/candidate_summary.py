"""候选人主列表合并展示字段。

简历库现在承担候选人工作流与归档候选人的主入口职责。本模块把“当前
志愿、岗位部门、原因”这些跨 Resume / Workflow / AssignmentAttempt 的
展示规则集中起来，供 API 序列化和候选人筛选复用，避免两边各解释一套口径。
"""

from apps.core import models as m
from apps.core.departments import secondary_department


REASON_ASSIGNMENT = "assignment"
REASON_ARCHIVE = "archive"
REASON_BLOCK = "block"
REASON_CLASSIFICATION = "classification"
REASON_NONE = ""


def workflow_or_none(candidate):
    try:
        return candidate.workflow
    except (m.CandidateWorkflow.DoesNotExist, AttributeError):
        return None


def current_resume(candidate):
    workflow = workflow_or_none(candidate)
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


def preview_resume(candidate):
    """返回详情页默认预览用的投递。

    当前有效志愿仍由 `current_resume` 表示；但当前志愿可能还没有关联简历文件。
    预览场景优先使用当前志愿，有文件才直接返回；否则回退到同候选人第一条
    有 `resume_file` 的投递，避免详情页明明存在其他简历文件却显示不可预览。
    """
    current = current_resume(candidate)
    if current and current.resume_file:
        return current
    resumes = sorted(
        list(candidate.resumes.all()),
        key=lambda resume: (
            resume.volunteer_rank if resume.volunteer_rank is not None else 999,
            resume.apply_date.toordinal() if resume.apply_date else 0,
            resume.id,
        ),
    )
    for resume in resumes:
        if resume.resume_file:
            return resume
    return None


def latest_effective_attempt(workflow, resume_id=None):
    if not workflow:
        return None
    attempts = [
        attempt
        for attempt in workflow.attempts.all()
        if attempt.status != m.AssignmentAttempt.STATUS_CANCELLED
        and (resume_id is None or attempt.resume_id == resume_id)
    ]
    if not attempts:
        return None
    return sorted(attempts, key=lambda attempt: (attempt.attempt_no, attempt.id))[-1]


def latest_processing_scope_item(candidate, run_id=None):
    """返回简历库当前展示的候选人处理结果。

    未指定运行时取候选人全部处理记录中的最新一条；从处理任务跳转进入
    简历库时，只在该运行范围内取最新一条。筛选和序列化必须共用这个口径，
    否则历史原因会命中筛选，但列表展示的是另一条更新记录。
    """
    items = list(candidate.processing_scope_items.all())
    if run_id not in (None, ""):
        items = [item for item in items if str(item.run_id) == str(run_id)]
    return max(items, key=lambda item: (item.created_at, item.id)) if items else None


def allocation_source(candidate):
    """返回简历库主列表应展示的分配来源。

    有当前有效分配尝试时，尝试本身是最准确的来源（也保留手动强制分配）。
    Agent 建议归档、Policy Gate 归档或阻塞时不会生成尝试，但流程已由自动策略
    实际进入处理；这类记录回显已固化的工作流策略。历史 ``rule`` 值只用于
    兼容既有审计记录，不再代表可提交的处理模式。
    """
    workflow = workflow_or_none(candidate)
    if not workflow:
        return ""

    resume = current_resume(candidate)
    attempt = latest_effective_attempt(
        workflow, resume_id=resume.id if resume else None
    )
    if attempt:
        return attempt.source

    if (
        workflow.started_at
        and workflow.dispatch_strategy
        in {
            m.AssignmentAttempt.SOURCE_RULE,
            m.AssignmentAttempt.SOURCE_AI,
        }
    ):
        return workflow.dispatch_strategy
    return ""


def current_apply_id(candidate):
    resume = current_resume(candidate)
    return resume.apply_id if resume else ""


def current_apply_date(candidate):
    resume = current_resume(candidate)
    return resume.apply_date if resume else None


def current_rank(candidate):
    workflow = workflow_or_none(candidate)
    if workflow and workflow.current_rank:
        return workflow.current_rank
    resume = current_resume(candidate)
    return resume.volunteer_rank if resume else None


def job_department_name(candidate):
    resume = current_resume(candidate)
    department = resume.job.department if resume and resume.job_id else None
    department = secondary_department(department)
    return department.name if department else ""


def reason(candidate):
    workflow = workflow_or_none(candidate)
    if workflow and workflow.status == m.CandidateWorkflow.STATUS_ARCHIVED:
        return REASON_ARCHIVE, workflow.archive_detail or workflow.archive_reason
    if workflow and workflow.block_reason:
        return REASON_BLOCK, workflow.block_detail or workflow.block_reason

    resume = current_resume(candidate)
    attempt = latest_effective_attempt(
        workflow, resume_id=resume.id if resume else None
    )
    if attempt:
        return (
            REASON_ASSIGNMENT,
            attempt.feedback_reason_label_snapshot
            or attempt.feedback_note
            or attempt.manual_reason
            or attempt.match_reason,
        )

    if resume and resume.category_reason:
        return REASON_CLASSIFICATION, resume.category_reason
    return REASON_NONE, ""
