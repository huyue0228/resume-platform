"""独立 Agent Kernel HTTP 客户端；异常只暴露受控错误。"""

import logging

import httpx
from django.conf import settings
from pydantic import ValidationError

from apps.pipeline.errors import AIServiceError

from .contracts import (
    AgentActionProposalV1,
    CaseEnvelopeV2,
)


logger = logging.getLogger(__name__)


ERROR_CODES = {
    "llm_timeout": "模型请求超时",
    "agent_cancelled": "Agent 任务已取消",
    "agent_budget_exhausted": "Agent 已达到本次工具或轮次预算",
    "agent_evidence_invalid": "Agent 返回的简历证据无法校验",
    "ai_connection_error": "模型服务连接失败",
    "agent_invalid_output": "Agent 未返回符合协议的结果",
    "agent_kernel_unavailable": "Agent Kernel 服务不可用",
}


class AgentKernelClient:
    def execute(self, envelope, *, model_api_key=""):
        from .wire import analysis_request, platform_result
        if not self.token:
            raise AIServiceError("agent_kernel_unavailable", "Agent Kernel 服务令牌尚未配置")
        try:
            request = analysis_request(envelope)
            response = httpx.post(f"{self.base_url}/v2/tasks/execute",
                json=request.model_dump(mode="json"),
                headers={"X-Agent-Kernel-Token": self.token, "X-Model-API-Key": model_api_key},
                timeout=httpx.Timeout(envelope.budget.max_duration_seconds + 15, connect=10))
            if response.status_code == 504:
                raise AIServiceError("llm_timeout", "候选人分析超时")
            response.raise_for_status()
            result = platform_result(response.json(), envelope)
            if "MOCK_ONLY" in result.manifest.warnings and not (settings.DEBUG and settings.AGENT_KERNEL_ALLOW_MOCK):
                raise AIServiceError("agent_invalid_output", "当前环境不接受模拟分析结果")
            return result
        except httpx.TimeoutException as exc:
            raise AIServiceError("llm_timeout", "候选人分析超时") from exc
        except httpx.HTTPError as exc:
            raise AIServiceError("agent_kernel_unavailable", "候选人分析服务不可用") from exc
        except ValueError as exc:
            raise AIServiceError("agent_invalid_output", "候选人分析返回内容不符合协议") from exc

    def __init__(self, *, base_url=None, token=None):
        self.base_url = (
            base_url or getattr(settings, "AGENT_KERNEL_URL", "http://127.0.0.1:8090")
        ).rstrip("/")
        self.token = token or getattr(settings, "AGENT_KERNEL_TOKEN", "")

    def is_ready(self):
        """只接受与控制面冻结版本完全一致的健康实例。"""

        if not self.token:
            return False
        try:
            response = httpx.get(f"{self.base_url}/healthz", timeout=3.0)
            response.raise_for_status()
            payload = response.json()
        except (httpx.HTTPError, ValueError):
            return False
        from .task_contracts import PROTOCOL, TOOLSET, RESULT
        expected = {"ok": True, "build": getattr(settings, "AGENT_KERNEL_BUILD", "dev"),
                    "task_protocol_version": PROTOCOL, "task_toolset_version": TOOLSET,
                    "task_result_version": RESULT}
        if isinstance(payload, dict) and bool(payload.get("mock")) != bool(settings.DEBUG and settings.AGENT_KERNEL_ALLOW_MOCK):
            return False
        return isinstance(payload, dict) and all(
            payload.get(key) == value for key, value in expected.items()
        )

    def evaluate(self, envelope: CaseEnvelopeV2, *, model_api_key=""):
        if not self.token:
            raise AIServiceError(
                "agent_kernel_unavailable", "Agent Kernel 服务令牌尚未配置"
            )
        timeout = httpx.Timeout(
            envelope.model.timeout_seconds + 15,
            connect=min(10, envelope.model.timeout_seconds),
        )
        try:
            response = httpx.post(
                f"{self.base_url}/v1/evaluate",
                json=envelope.model_dump(mode="json"),
                headers={
                    "X-Agent-Kernel-Token": self.token,
                    "X-Model-API-Key": model_api_key,
                },
                timeout=timeout,
            )
        except httpx.TimeoutException as exc:
            raise AIServiceError("llm_timeout", ERROR_CODES["llm_timeout"]) from exc
        except httpx.HTTPError as exc:
            raise AIServiceError(
                "agent_kernel_unavailable", "Agent Kernel 服务不可用"
            ) from exc
        if response.status_code != 200:
            try:
                payload = response.json()
            except ValueError:
                payload = {}
            code = payload.get("code")
            if code not in ERROR_CODES:
                code = "agent_kernel_unavailable"
            message = ERROR_CODES.get(code, "Agent Kernel 服务不可用")
            logger.warning(
                "Agent Kernel rejected request status=%s code=%s",
                response.status_code,
                code,
            )
            safe_trace = payload.get("safe_trace")
            raise AIServiceError(
                code,
                message,
                safe_trace=safe_trace if isinstance(safe_trace, dict) else {},
            )
        try:
            return AgentActionProposalV1.model_validate(response.json())
        except (ValueError, ValidationError) as exc:
            raise AIServiceError(
                "agent_invalid_output", "Agent Kernel 返回内容不符合协议"
            ) from exc
