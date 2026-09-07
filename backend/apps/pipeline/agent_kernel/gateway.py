"""模型就绪检查与唯一的远端候选人分析入口。"""
from django.conf import settings
from apps.pipeline import ai_config
from apps.pipeline.errors import AIServiceError
from .client import AgentKernelClient

def validate_runtime():
    if settings.AGENT_KERNEL_ROLLOUT not in {"review_only", "enforced"}:
        raise AIServiceError("agent_configuration_invalid", "仅支持 review_only 或 enforced，不再支持旧 AI 基线")

def is_agent_ready():
    try:
        validate_runtime()
    except AIServiceError:
        return False
    return ai_config.is_ai_available() and AgentKernelClient().is_ready()

def evaluate_resume(resume, job, *, department=None, force=False,
                    processing_run_id=None, cancelled=None):
    validate_runtime()
    from .matching import evaluate
    return evaluate(resume, job, department=department, force=force,
                    processing_run_id=processing_run_id, cancelled=cancelled)
