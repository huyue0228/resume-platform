"""新内核适配与 Django Policy；模型结果不包含业务动作。"""
import uuid
from copy import deepcopy
from dataclasses import replace

from django.conf import settings

from apps.core import models as m
from apps.pipeline import ai_config
from apps.pipeline.screening_types import ResumeScreeningOutput, ScreeningResult
from apps.pipeline.errors import AIServiceError
from resume_contracts.models import SCORE_WEIGHTS
from . import legacy_baseline
from .client import AgentKernelClient
from .task_contracts import build_task, freeze_case, job_hash


def reusable_analysis(item, frozen, envelope):
    """仅容量/流程变化时复用已验证分析；内容、模型、画像上下文变化均失效。"""
    from .task_contracts import TaskResultV1
    if not item or frozen["lane"] == "shadow" or not frozen.get("preflight"):
        return None
    d = frozen["preflight"]
    if d["status"] != "ready":
        return None
    def content_key(case):
        snapshot = case["snapshot"]
        preflight = case.get("preflight", {})
        volunteer = next((v for v in snapshot["volunteers"] if v["ref"] == preflight.get("current_volunteer_ref")), {})
        refs = set(preflight.get("job_refs", []))
        return (case["pin"], snapshot["candidate"]["highest_major"], snapshot["candidate"]["highest_education"],
                volunteer.get("ref"), volunteer.get("artifact", {}).get("checksum"),
                sorted((j["ref"], j["content_hash"]) for j in snapshot["jobs"] if j["ref"] in refs), snapshot.get("taxonomy", []))
    key = content_key(frozen)
    prior = m.ProcessingRunScopeItem.objects.filter(candidate=item.candidate, kernel_result__manifest__terminal_state="DONE").exclude(pk=item.pk).order_by("-pk")[:20]
    for previous in prior:
        if not previous.kernel_snapshot or content_key(previous.kernel_snapshot) != key:
            continue
        # MCP 目录尚未纳入任务版本钉定，不能假定外部知识仍未变化。
        if any(name.startswith("mcp.") for name in previous.kernel_result.get("manifest", {}).get("tool_versions", {})):
            continue
        payload = deepcopy(previous.kernel_result)
        payload["manifest"]["reused_from_task_id"] = payload["task_id"]
        payload.update(task_id=envelope.task_id, idempotency_key=envelope.idempotency_key,
                       workflow_revision=envelope.snapshot["workflow"]["revision"], deterministic=deepcopy(d))
        payload["safe_trace"].update(turns=0, tool_call_count=0, tool_calls=[], input_tokens=0, output_tokens=0)
        return TaskResultV1.model_validate(payload)
    return None


def prepare_run(run):
    """平台本地执行志愿、准入和岗位池构造，零外部调用。"""
    from apps.pipeline.cancellation import raise_if_cancel_requested
    from apps.pipeline.services.admission_snapshot import prepare_snapshot
    for item in run.scope_items.select_related("candidate").exclude(kernel_snapshot={}):
        raise_if_cancel_requested(run)
        frozen = item.kernel_snapshot
        snapshot = deepcopy(frozen["snapshot"])
        retry = (run.scope or {}).get("retry_resume_id")
        if retry:
            snapshot["workflow"]["retry_volunteer_ref"] = next(
                (ref for ref, pk in frozen["volunteer_ids"].items() if pk == retry), "invalid-retry")
        frozen["preflight"] = prepare_snapshot(snapshot)
        item.kernel_snapshot = frozen
        item.save(update_fields=["kernel_snapshot"])


def validate_result(envelope, result, frozen, resume):
    def reject(message):
        raise AIServiceError("agent_invalid_output", message)
    if (result.task_id != envelope.task_id or result.idempotency_key != envelope.idempotency_key
        or result.pin != envelope.pin or result.workflow_revision != envelope.snapshot["workflow"]["revision"]):
        reject("任务、版本或流程引用不一致")
    if result.manifest.terminal_state != "DONE":
        raise AIServiceError("agent_incomplete", "候选人分析未完成，请人工处理或重试", safe_trace=result.safe_trace.model_dump(mode="json"))
    d = result.deterministic
    if frozen["volunteer_ids"].get(d.current_volunteer_ref) != resume.pk:
        reject("Kernel 与控制面当前志愿不一致")
    if not d.admission_passed or d.status != "ready" or not result.profile:
        reject("Kernel 准入或画像校验未通过")
    lines = [(page + 1, line) for page, text in enumerate(result.profile.source_text.split("\f")) for line in text.split("\n")]
    evidence_items = [e for c in result.profile.claims for e in c.evidence] + [e for match in result.matches for e in match.evidence]
    for evidence in evidence_items:
        quote = "".join(evidence.quote.split())
        if len(quote) < 8 or evidence.end_line < evidence.start_line or evidence.end_line > len(lines):
            reject("简历证据定位无效")
        selected = lines[evidence.start_line - 1:evidence.end_line]
        if any(page != evidence.page for page, _text in selected) or quote not in "".join("".join(text.split()) for _page, text in selected):
            reject("简历证据与 Provider 原文不一致")
    from apps.pipeline.services.job_mapping import resolve_job_pool
    # 双重校验范围；内容变化在提交事务中再次校验。
    jobs, _mapping = resolve_job_pool(resume, list(m.Job.objects.filter(is_active=True).select_related("department")))
    expected = {j.pk for j in jobs if j.department_id and j.department.level == 2}
    references = {ref for ref, pk in frozen["job_ids"].items() if pk in expected}
    if set(d.job_refs) != references or len(d.job_refs) != len(references):
        reject("合规岗位池与控制面不一致")
    refs = [match.job_ref for match in result.matches]
    if len(refs) != len(set(refs)) or set(refs) != references or set(result.manifest.covered_jobs) != references:
        reject("岗位覆盖不完整或引用越界")
    if result.manifest.resume_checksum != next(v["artifact"]["checksum"] for v in envelope.snapshot["volunteers"] if v["ref"] == d.current_volunteer_ref):
        reject("简历文件摘要不一致")
    expected_order = sorted(result.matches, key=lambda match: (-match.score, match.job_ref))
    if result.matches != expected_order or [match.rank for match in result.matches] != list(range(1, len(refs) + 1)):
        reject("岗位排名不稳定")
    for match in result.matches:
        score = round(sum(match.dimensions.model_dump()[k] * w for k, w in SCORE_WEIGHTS.items()), 4)
        if abs(score - match.score) > .00011:
            reject("岗位综合分与固定权重不一致")


def policy_output(result, match, frozen):
    claims = result.profile.claims
    def texts(kind):
        return [c.summary for c in claims if c.kind == kind]
    def experiences(kind):
        return [dict(name=c.details.name or c.summary[:128], role=c.details.role, period=c.details.period, description=c.summary,
                     evidence=c.evidence[0].quote) for c in claims if c.kind == kind]
    score = match.score
    thresholds = frozen["thresholds"]
    recommendation = "dispatch" if score >= thresholds["dispatch"] else "review" if score >= thresholds["review"] else "archive"
    if "profile_incomplete" in result.profile.risks and recommendation == "dispatch":
        recommendation = "review"
    if frozen["lane"] == "review_only":
        recommendation = "review"
    specialist = [e.quote for c in claims if c.kind == "agent_experience" for e in c.evidence]
    return ResumeScreeningOutput.model_validate(dict(profile=dict(
        major_direction="；".join(texts("major_direction")),
        educations=[dict(school_name=c.details.school_name, degree=c.details.degree, major=c.details.major, period=c.details.period, evidence=c.evidence[0].quote) for c in claims if c.kind == "education"],
        projects=experiences("project"), internships=experiences("internship"), skills=texts("skill"),
        certificates=texts("certificate"), summary="；".join(c.summary for c in claims), risk_flags=result.profile.risks),
        decision=dict(recommendation=recommendation, score_breakdown=match.dimensions.model_dump(),
            summary=match.reason, reason=match.reason, evidence=[e.quote for e in match.evidence], risks=match.risks,
            ai_specialist_match=bool(specialist), ai_specialist_confidence=max((c.confidence for c in claims if c.kind == "agent_experience"), default=0),
            ai_specialist_evidence=specialist)))


def evaluate(resume, job, *, processing_run_id=None, cancelled=None, **kwargs):
    item = m.ProcessingRunScopeItem.objects.filter(run_id=processing_run_id, candidate=resume.candidate).first() if processing_run_id else None
    frozen = item.kernel_snapshot if item and item.kernel_snapshot else freeze_case(resume.candidate)
    if frozen["pin"]["model_config_revision"] != ai_config.current_ai_connection_fingerprint():
        raise AIServiceError("agent_model_config_unavailable", "冻结模型版本已不可用，请重新提交任务")
    config = ai_config.get_ai_model_config()
    envelope = build_task(frozen, config, resume.candidate.workflow.revision,
                          task_id=f'{frozen["task_id"]}-{item.attempt_count}' if item else uuid.uuid4().hex,
                          retry_resume_id=(item.run.scope or {}).get("retry_resume_id") if item else resume.pk)
    if cancelled and cancelled():
        raise AIServiceError("agent_cancelled", "任务已取消")
    try:
        result = None if kwargs.get("force") else reusable_analysis(item, frozen, envelope)
        result = result or AgentKernelClient().execute(envelope, model_api_key=config.api_key)
        validate_result(envelope, result, frozen, resume)
    except AIServiceError as exc:
        if item:
            m.ProcessingRunScopeItem.objects.filter(pk=item.pk).update(kernel_result={"terminal_state": "FAILED", "code": exc.code, "safe_trace": exc.safe_trace})
        if frozen["lane"] != "shadow":
            raise
        return legacy_baseline.screen_resume(resume, job, processing_run_id=processing_run_id, cancelled=cancelled, **kwargs)
    if item:
        m.ProcessingRunScopeItem.objects.filter(pk=item.pk).update(kernel_result=result.model_dump(mode="json"))
    if frozen["lane"] == "shadow":
        # 明确选择的迁移基线，shadow 结果本身没有业务写权限。
        baseline = legacy_baseline.screen_resume(resume, job, processing_run_id=processing_run_id, cancelled=cancelled, **kwargs)
        if item:
            matched = next((match for match in result.matches if frozen["job_ids"][match.job_ref] == job.pk), None)
            frozen.setdefault("shadow_comparison", {}).update(
                baseline_score=baseline.confidence, kernel_fixed_job_score=matched.score if matched else None,
                kernel_top_score=result.matches[0].score if result.matches else None)
            m.ProcessingRunScopeItem.objects.filter(pk=item.pk).update(kernel_snapshot=frozen)
        return baseline
    match = result.matches[0]
    selected = m.Job.objects.select_related("department").get(pk=frozen["job_ids"][match.job_ref])
    output = policy_output(result, match, frozen)
    profile = m.ResumeProfile(resume=resume)  # 提交事务前不写画像。
    return ScreeningResult(profile=profile, output=output, job=selected, department=selected.department,
        confidence=match.score, score_breakdown=match.dimensions.model_dump(), model_name=config.model_name,
        prompt_version=envelope.pin.instruction_version, decision_version=config.decision_version,
        kernel_pin_id=envelope.pin.pin_id, kernel_build=envelope.pin.kernel_build,
        protocol_version=envelope.pin.protocol_version, toolset_version=envelope.pin.toolset_version,
        safe_trace=result.safe_trace.model_dump(mode="json"),
        kernel_task=dict(result=result.model_dump(mode="json"), frozen=frozen))


def validate_live_jobs(result):
    task = result.kernel_task
    if not task:
        return
    frozen = task["frozen"]
    snapshots = {j["ref"]: j for j in frozen["snapshot"]["jobs"]}
    ids = [frozen["job_ids"][match["job_ref"]] for match in task["result"]["matches"]]
    jobs = {job.pk: job for job in m.Job.objects.select_for_update(of=("self",)).select_related("department").prefetch_related("majors").filter(pk__in=ids).order_by("pk")}
    for match in task["result"]["matches"]:
        ref = match["job_ref"]
        job = jobs.get(frozen["job_ids"][ref])
        if not job or job_hash(job) != snapshots[ref]["content_hash"]:
            raise AIServiceError("ai_reference_invalidated", "岗位内容版本已变化，需要重新分析")


def select_match(result, job):
    from .task_contracts import TaskResultV1
    task = result.kernel_task; parsed = TaskResultV1.model_validate(task["result"])
    match = next(x for x in parsed.matches if task["frozen"]["job_ids"][x.job_ref] == job.pk)
    output = policy_output(parsed, match, task["frozen"])
    return replace(result, job=job, department=job.department, output=output,
                   confidence=match.score, score_breakdown=match.dimensions.model_dump())


def persist_profile(result, resume):
    if not result.kernel_task:
        return result
    profile, _ = m.ResumeProfile.objects.get_or_create(resume=resume)
    data = result.output.profile
    profile.file_checksum = result.kernel_task["result"]["manifest"]["resume_checksum"]
    profile.raw_text = result.kernel_task["result"]["profile"]["source_text"]
    profile.parse_model = result.kernel_build
    profile.profile_version = result.protocol_version
    profile.education_experiences = [x.model_dump() for x in data.educations]
    profile.project_experiences = [x.model_dump() for x in data.projects]
    profile.internship_experiences = [x.model_dump() for x in data.internships]
    profile.skills, profile.certificates = data.skills, data.certificates
    profile.major_direction, profile.summary = data.major_direction[:128], data.summary
    profile.profile_risk_flags, profile.parse_status, profile.parse_error = data.risk_flags, "parsed", ""
    if result.kernel_task["result"]["manifest"]["ocr_pages"]:
        profile.profile_risk_flags = list(dict.fromkeys([*profile.profile_risk_flags, "ocr_fallback"]))
    from django.utils import timezone
    profile.parsed_at = timezone.now()
    profile.save()
    return replace(result, profile=profile)
