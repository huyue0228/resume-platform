"""Agent Gateway：允许内嵌实现与独立 Go Kernel 在同一业务接口后切换。"""

from django.conf import settings

from apps.pipeline import ai_config
from . import legacy_baseline as ai_service
from apps.pipeline.errors import AIServiceError

from .client import AgentKernelClient


def is_agent_ready():
    """模型连接与执行内核都可用时，新的 Agent 任务才可提交。"""

    if not ai_config.is_ai_available():
        return False
    mode = getattr(settings, "AGENT_KERNEL_MODE", "embedded")
    if mode == "embedded":
        return getattr(settings, "AGENT_KERNEL_ROLLOUT", "shadow") == "shadow"
    if mode == "remote":
        return AgentKernelClient().is_ready()
    return False


def evaluate_resume(
    resume,
    job,
    *,
    department=None,
    force=False,
    processing_run_id=None,
    cancelled=None,
    prompt_version=None,
):
    """统一评估入口；远端模式才跨进程，内嵌模式用于迁移与回归。"""

    mode = getattr(settings, "AGENT_KERNEL_MODE", "embedded")
    if mode == "embedded":
        if getattr(settings, "AGENT_KERNEL_ROLLOUT", "shadow") != "shadow":
            raise AIServiceError("agent_kernel_unavailable", "review_only 和 enforced 必须使用远端 Go Kernel")
        return ai_service.screen_resume(
            resume,
            job,
            department=department,
            force=force,
            processing_run_id=processing_run_id,
            cancelled=cancelled,
            prompt_version=prompt_version,
        )
    if mode != "remote":
        raise AIServiceError("agent_kernel_unavailable", "Agent Kernel 运行模式配置无效")

    from .matching import evaluate
    return evaluate(resume, job, department=department, force=force,
                    processing_run_id=processing_run_id, cancelled=cancelled,
                    prompt_version=prompt_version)
