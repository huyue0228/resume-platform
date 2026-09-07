"""业务单元测试隔离远端能力发现；执行结果由各测试显式提供。"""
from unittest.mock import patch
from django.test import TestCase, override_settings
from resume_contracts.fixtures import capabilities_fixture


class KernelTestCase(TestCase):
    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        settings_patch = override_settings(AGENT_KERNEL_ROLLOUT="enforced", AGENT_KERNEL_BUILD="", AGENT_KERNEL_TOKEN="test", AGENT_KERNEL_DOCUMENT_SIGNING_KEY="test")
        settings_patch.enable()
        cls.addClassCleanup(settings_patch.disable)
        caps_patch = patch("apps.pipeline.agent_kernel.client.AgentKernelClient.capabilities", side_effect=capabilities_fixture)
        caps_patch.start()
        cls.addClassCleanup(caps_patch.stop)


def analysis_response(envelope, **_kwargs):
    """合成完整排名，不模拟数据库写入；可用于控制面 HC 与反馈验收。"""
    from apps.pipeline.agent_kernel.task_contracts import TaskResultV1
    from apps.pipeline.services.admission_snapshot import prepare_snapshot
    d = prepare_snapshot(envelope.snapshot)
    refs = sorted(d["job_refs"])
    evidence = [dict(quote="负责后端服务开发与测试工作", page=1, start_line=1, end_line=1)]
    matches = []
    for rank, ref in enumerate(refs, 1):
        # 按稳定输入岗位顺序设置不同分数，以检验排名而非数据库主键输出。
        index = next(i for i, job in enumerate(envelope.snapshot["jobs"]) if job["ref"] == ref)
        score = .9 - index * .01
        matches.append(dict(job_ref=ref, dimensions={key: score for key in ("major_match", "skills_match", "experience_evidence", "job_requirement", "resume_quality")},
            confidence=.9, evidence=evidence, risks=[], reason="经历支持岗位职责", score=score, rank=rank))
    matches.sort(key=lambda x: (-x["score"], x["job_ref"]))
    for rank, match in enumerate(matches, 1):
        match["rank"] = rank
    artifact = next(v["artifact"] for v in envelope.snapshot["volunteers"] if v["ref"] == d["current_volunteer_ref"])
    return TaskResultV1.model_validate(dict(protocol_version=envelope.protocol_version, task_id=envelope.task_id,
        idempotency_key=envelope.idempotency_key, pin=envelope.pin.model_dump(), workflow_revision=envelope.snapshot["workflow"]["revision"],
        deterministic=d, profile=dict(source_text="负责后端服务开发与测试工作",
            claims=[dict(kind="project", summary="后端服务开发", evidence=evidence)], risks=[]), matches=matches,
        manifest=dict(input_hash="a"*64, resume_checksum=artifact["checksum"], covered_jobs=refs,
            tool_versions={}, warnings=[], terminal_state="DONE", ocr_pages=0), safe_trace=dict(turns=3)))
