"""外包开发专用：模拟内核 + 独立 SQLite/文件目录；禁止用于生产。"""
from .settings import *  # noqa: F403

DEBUG = True
AGENT_KERNEL_ALLOW_MOCK = True
AGENT_KERNEL_MODE = "remote"
AGENT_KERNEL_ROLLOUT = "review_only"
AGENT_KERNEL_BUILD = "dev"
AGENT_KERNEL_URL = "http://127.0.0.1:8091"
AGENT_KERNEL_TOKEN = "local-contract-mock"
AGENT_KERNEL_DOCUMENT_SIGNING_KEY = "local-contract-mock"
CELERY_TASK_ALWAYS_EAGER = True
DATABASES = {"default": {"ENGINE": "django.db.backends.sqlite3", "NAME": BASE_DIR / "db.mock.sqlite3"}}
MEDIA_ROOT = BASE_DIR / "media.mock"
W3_OAUTH2_ENABLED = False
