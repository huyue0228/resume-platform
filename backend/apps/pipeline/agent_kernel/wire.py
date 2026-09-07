"""唯一跨仓适配器：业务快照不越过 HTTP 边界。"""
from resume_contracts.models import AnalysisRequestV1, AnalysisResponseV1, JobRequirementV1

from apps.pipeline.services.admission_snapshot import prepare_snapshot
from .task_contracts import TaskResultV1


def analysis_request(envelope):
    d = prepare_snapshot(envelope.snapshot)
    if not d["admission_passed"] or d["status"] != "ready":
        raise ValueError("candidate has no admitted analysis scope")
    snapshot = envelope.snapshot
    volunteer = next(v for v in snapshot["volunteers"] if v["ref"] == d["current_volunteer_ref"])
    jobs = [{key: value for key, value in job.items() if key in JobRequirementV1.model_fields}
            for job in snapshot["jobs"] if job["ref"] in d["job_refs"]]
    return AnalysisRequestV1(task_id=envelope.task_id, idempotency_key=envelope.idempotency_key,
        trigger=envelope.trigger, pin=envelope.pin, workflow_revision=snapshot["workflow"]["revision"],
        model=envelope.model.model_dump(), budget=envelope.budget,
        scope=dict(candidate={k: snapshot["candidate"][k] for k in ("ref", "highest_major", "highest_education")},
                   volunteer_ref=volunteer["ref"], artifact=volunteer["artifact"], jobs=jobs, taxonomy=snapshot.get("taxonomy", [])))


def platform_result(payload, envelope):
    parsed = AnalysisResponseV1.model_validate(payload)
    # 确定性结论来自本地冻结输入，内核没有修改它的字段或权限。
    return TaskResultV1.model_validate(dict(**parsed.model_dump(mode="json"), deterministic=prepare_snapshot(envelope.snapshot)))
