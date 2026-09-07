"""Celery 应用。Demo 默认 eager（同步），生产用 Redis broker 异步。"""
import os

from celery import Celery

os.environ.setdefault("DJANGO_SETTINGS_MODULE", "config.settings")

app = Celery("smart_resume_filter")
app.config_from_object("django.conf:settings", namespace="CELERY")
app.autodiscover_tasks()
