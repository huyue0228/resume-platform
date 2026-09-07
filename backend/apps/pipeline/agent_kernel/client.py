"""独立 Kernel 客户端：只约束公开协议，执行版本由能力发现后冻结。"""
import httpx
from django.conf import settings
from resume_contracts.models import KernelCapabilitiesV1
from apps.pipeline.errors import AIServiceError

ERROR_MESSAGES = {
    "kernel_version_unavailable": "冻结的 Kernel 版本已不可用，请重新提交任务",
    "idempotency_conflict": "任务幂等引用冲突，请重新提交任务",
    "llm_timeout": "候选人分析超时",
    "agent_cancelled": "候选人分析已取消",
    "agent_budget_exhausted": "候选人分析已达到预算限制",
    "agent_evidence_invalid": "候选人分析证据未通过校验",
    "ai_connection_error": "模型服务连接失败",
    "agent_invalid_output": "候选人分析返回内容不符合协议",
}


class AgentKernelClient:
    def __init__(self, *, base_url=None, token=None):
        self.base_url = (base_url or settings.AGENT_KERNEL_URL).rstrip("/")
        self.token = settings.AGENT_KERNEL_TOKEN if token is None else token

    def _headers(self):
        if not self.token:
            raise AIServiceError("agent_kernel_unavailable", "Agent Kernel 服务令牌尚未配置")
        return {"X-Agent-Kernel-Token": self.token}

    def capabilities(self):
        """认证并验证公开接口；工具和指令版本是 Kernel 管理的不透明标识。"""
        try:
            response = httpx.get(f"{self.base_url}/v2/capabilities", headers=self._headers(), timeout=3.0)
            response.raise_for_status()
            caps = KernelCapabilitiesV1.model_validate(response.json())
        except httpx.HTTPError as exc:
            raise AIServiceError("agent_kernel_unavailable", "Agent Kernel 能力发现失败") from exc
        except ValueError as exc:
            raise AIServiceError("agent_protocol_incompatible", "Agent Kernel 公开协议不兼容") from exc
        if caps.mock != bool(settings.DEBUG and settings.AGENT_KERNEL_ALLOW_MOCK):
            raise AIServiceError("agent_protocol_incompatible", "模拟与真实 Kernel 环境不匹配")
        expected_build = settings.AGENT_KERNEL_BUILD
        if expected_build and caps.kernel_build != expected_build:
            raise AIServiceError("kernel_version_unavailable", "Agent Kernel 与部署锁定版本不一致")
        return caps

    def is_ready(self):
        try:
            self.capabilities()
            return True
        except AIServiceError:
            return False

    def execute(self, envelope, *, model_api_key=""):
        from .wire import analysis_request, platform_result
        try:
            request = analysis_request(envelope)
            response = httpx.post(f"{self.base_url}/v2/tasks/execute",
                json=request.model_dump(mode="json"),
                headers={**self._headers(), "X-Model-API-Key": model_api_key},
                timeout=httpx.Timeout(envelope.budget.max_duration_seconds + 15, connect=10))
            if response.status_code >= 400:
                try:
                    payload = response.json()
                except ValueError:
                    payload = {}
                code = payload.get("code") if isinstance(payload, dict) else None
                if isinstance(code, str) and code in ERROR_MESSAGES:
                    raise AIServiceError(code, ERROR_MESSAGES[code])
                if response.status_code == 504:
                    raise AIServiceError("llm_timeout", ERROR_MESSAGES["llm_timeout"])
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
