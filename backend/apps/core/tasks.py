"""Core maintenance tasks."""
from datetime import timedelta

from celery import shared_task
from django.utils import timezone

from .models import UsagePageView


USAGE_PAGE_VIEW_RETENTION_DAYS = 90


@shared_task
def cleanup_usage_page_views():
    """删除超过保留期的页面访问明细。"""
    cutoff = timezone.now() - timedelta(days=USAGE_PAGE_VIEW_RETENTION_DAYS)
    deleted, _ = UsagePageView.objects.filter(occurred_at__lt=cutoff).delete()
    return {"deleted": deleted, "cutoff": cutoff.isoformat()}
