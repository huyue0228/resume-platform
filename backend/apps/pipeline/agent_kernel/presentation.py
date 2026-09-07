"""面向招聘界面的只读投影，展示名称来自冻结业务快照。"""
from copy import deepcopy

from apps.core import models as m


def decision_analysis(decision, *, detailed=False):
    stored = decision.kernel_result or {}
    if not stored:
        return {}
    if not detailed:
        return {"match_count": len(stored.get("matches", [])),
                "terminal_state": stored.get("manifest", {}).get("terminal_state"),
                "reused": bool(stored.get("manifest", {}).get("reused_from_task_id"))}
    result = deepcopy(stored)
    if result.get("profile"):
        result["profile"].pop("source_text", None)
    frozen = m.ProcessingRunScopeItem.objects.filter(
        run_id=decision.processing_run_id, candidate_id=decision.workflow.candidate_id,
    ).values_list("kernel_snapshot", flat=True).first() or {}
    jobs = {job["ref"]: job for job in frozen.get("snapshot", {}).get("jobs", [])}
    for match in result.get("matches", []):
        job = jobs.get(match["job_ref"], {})
        match["job_title"] = job.get("position_name") or job.get("public_name") or "岗位名称不可用"
        match["department_name"] = job.get("department_name", "")
        match["is_selected"] = frozen.get("job_ids", {}).get(match["job_ref"]) == decision.recommended_job_id
    return result
