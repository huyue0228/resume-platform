"""Django Policy Gate：Kernel 建议在进入业务事务前必须再次通过这里。"""

from apps.pipeline.errors import AIServiceError

from .contracts import PROPOSAL_VERSION, AgentActionProposalV1, CaseEnvelopeV2


def _normalize(value):
    return "".join(str(value or "").casefold().split())


def _evidence_exists(text, quote):
    normalized_quote = _normalize(quote)
    return len(normalized_quote) >= 4 and normalized_quote in _normalize(text)


def validate_proposal(
    envelope: CaseEnvelopeV2,
    proposal: AgentActionProposalV1,
):
    """验证协议、冻结版本、动作一致性和逐字证据。"""

    if proposal.proposal_version != PROPOSAL_VERSION:
        raise AIServiceError("agent_invalid_output", "Agent 建议协议版本不兼容")
    if proposal.task_id != envelope.task_id or proposal.pin_id != envelope.pin.pin_id:
        raise AIServiceError("ai_reference_invalidated", "Agent 建议引用的冻结任务已失效")
    if proposal.safe_trace.kernel_build != envelope.pin.kernel_build:
        raise AIServiceError("ai_reference_invalidated", "Agent Kernel 构建版本与任务冻结值不一致")
    if proposal.action != proposal.evaluation.decision.recommendation:
        raise AIServiceError("agent_invalid_output", "Agent 动作与结构化建议不一致")
    quotes = [
        *proposal.evaluation.decision.evidence,
        *proposal.evaluation.decision.ai_specialist_evidence,
    ]
    if proposal.action != "archive" and not quotes:
        raise AIServiceError("agent_evidence_invalid", "Agent 建议缺少简历原文证据")
    if any(not _evidence_exists(envelope.resume.text, quote) for quote in quotes):
        raise AIServiceError("agent_evidence_invalid", "Agent 返回的简历证据无法校验")
    return proposal.evaluation
