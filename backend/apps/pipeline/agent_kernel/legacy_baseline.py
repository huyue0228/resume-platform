"""仅迁移 shadow 基线使用；禁止成为远端内核失败的隐式降级。"""
from django.conf import settings
from apps.pipeline.errors import AIServiceError


def screen_resume(*args, **kwargs):
    if settings.AGENT_KERNEL_ROLLOUT != "shadow":
        raise AIServiceError("agent_kernel_unavailable", "旧基线仅允许用于 shadow 对比")
    from apps.pipeline.ai.service import screen_resume as legacy
    return legacy(*args, **kwargs)
