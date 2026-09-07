"""合成协议样例，不包含真实候选人数据。"""
import hashlib
import json

from .models import AnalysisRequestV1, AnalysisResponseV1, PROTOCOL, SCORE_WEIGHTS

TEXT = "负责后端服务开发与测试工作，完成接口设计和自动化测试。\n" * 20


def request_fixture():
    return AnalysisRequestV1.model_validate(dict(task_id="fixture-task", idempotency_key="fixture-key", workflow_revision=1,
        pin=dict(pin_id="fixture-pin", kernel_build="dev", model_config_revision="fixture-model"),
        model=dict(api_style="chat_json", base_url="http://127.0.0.1:9999/v1", model_name="fixture-model"),
        scope=dict(candidate=dict(ref="candidate-fixture", highest_major="软件工程", highest_education="本科"),
            volunteer_ref="volunteer-fixture", artifact=dict(path="resumes/fixture.pdf", checksum="a"*64, size_bytes=100,
                expires_at=2100000000, signature="b"*64),
            jobs=[dict(ref=ref, content_hash="c"*64, position_name="软件开发", responsibilities="服务开发与测试",
                       department_ref="department-fixture", department_name="示例部门") for ref in ("job-a", "job-b")])) )


def response_fixture(request=None, scenario="success"):
    request = request or request_fixture()
    evidence = [dict(quote="负责后端服务开发与测试工作", page=1, start_line=1, end_line=1)]
    score = .3 if scenario == "low_match" else .8
    matches = [dict(job_ref=job.ref, dimensions={k:score for k in SCORE_WEIGHTS}, confidence=score, evidence=evidence,
                    risks=[], reason="合成测试结果，仅用于开发验收", score=score, rank=index)
               for index, job in enumerate(sorted(request.scope.jobs,key=lambda j:j.ref),1)]
    data = dict(protocol_version=PROTOCOL, task_id=request.task_id, idempotency_key=request.idempotency_key,
        pin=request.pin.model_dump(), workflow_revision=request.workflow_revision,
        profile=dict(source_text=TEXT,claims=[dict(kind="project",summary="示例后端项目",evidence=evidence)],risks=[]), matches=matches,
        manifest=dict(input_hash=hashlib.sha256(json.dumps(request.model_dump(mode="json"),sort_keys=True).encode()).hexdigest(),
            resume_checksum=request.scope.artifact.checksum,covered_jobs=[m["job_ref"] for m in matches],
            tool_versions={},warnings=["MOCK_ONLY"],terminal_state="DONE",ocr_pages=0),safe_trace=dict(turns=0))
    if scenario in {"failed", "budget_exhausted"}:
        data.update(profile=None,matches=[])
        data["manifest"].update(terminal_state="FAILED",covered_jobs=[],failure_code=scenario)
    result=AnalysisResponseV1.model_validate(data).model_dump(mode="json")
    if scenario == "invalid_reference": result["matches"][0]["job_ref"]="outside-allowed-pool"
    if scenario == "incomplete": result["matches"]=result["matches"][:-1]
    if scenario == "invalid_schema": result["recommendation"]="dispatch"
    return result
