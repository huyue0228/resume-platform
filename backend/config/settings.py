"""
海纳智聘 — Django 配置。

Demo 默认：SQLite + Celery eager（同步执行，免装 Redis）。
生产可通过环境变量切换 PostgreSQL，并启用 Celery + Redis。
"""
import os
from pathlib import Path

BASE_DIR = Path(__file__).resolve().parent.parent


def env_bool(key, default=False):
    return os.environ.get(key, str(default)).lower() in ("1", "true", "yes", "on")


SECRET_KEY = os.environ.get("DJANGO_SECRET_KEY", "demo-insecure-secret-key-change-me")
DEBUG = env_bool("DJANGO_DEBUG", True)
ALLOWED_HOSTS = os.environ.get("DJANGO_ALLOWED_HOSTS", "*").split(",")

INSTALLED_APPS = [
    "django.contrib.admin",
    "django.contrib.auth",
    "django.contrib.contenttypes",
    "django.contrib.sessions",
    "django.contrib.messages",
    "django.contrib.staticfiles",
    # 第三方
    "rest_framework",
    "rest_framework.authtoken",
    "corsheaders",
    "django_filters",
    # 业务 app
    "apps.accounts",
    "apps.core",
    "apps.ingestion",
    "apps.pipeline",
    "apps.api",
]

MIDDLEWARE = [
    "corsheaders.middleware.CorsMiddleware",
    "django.middleware.security.SecurityMiddleware",
    "django.contrib.sessions.middleware.SessionMiddleware",
    "django.middleware.common.CommonMiddleware",
    "django.middleware.csrf.CsrfViewMiddleware",
    "django.contrib.auth.middleware.AuthenticationMiddleware",
    "django.contrib.messages.middleware.MessageMiddleware",
    "django.middleware.clickjacking.XFrameOptionsMiddleware",
]

ROOT_URLCONF = "config.urls"

TEMPLATES = [
    {
        "BACKEND": "django.template.backends.django.DjangoTemplates",
        "DIRS": [],
        "APP_DIRS": True,
        "OPTIONS": {
            "context_processors": [
                "django.template.context_processors.debug",
                "django.template.context_processors.request",
                "django.contrib.auth.context_processors.auth",
                "django.contrib.messages.context_processors.messages",
            ],
        },
    },
]

WSGI_APPLICATION = "config.wsgi.application"

# 数据库：默认 SQLite，设置 POSTGRES_DB 时切换 PostgreSQL
if os.environ.get("POSTGRES_DB"):
    DATABASES = {
        "default": {
            "ENGINE": "django.db.backends.postgresql",
            "NAME": os.environ["POSTGRES_DB"],
            "USER": os.environ.get("POSTGRES_USER", "postgres"),
            "PASSWORD": os.environ.get("POSTGRES_PASSWORD", "postgres"),
            "HOST": os.environ.get("POSTGRES_HOST", "localhost"),
            "PORT": os.environ.get("POSTGRES_PORT", "5432"),
        }
    }
else:
    DATABASES = {
        "default": {
            "ENGINE": "django.db.backends.sqlite3",
            "NAME": BASE_DIR / "db.sqlite3",
        }
    }

AUTH_USER_MODEL = "accounts.User"

AUTH_PASSWORD_VALIDATORS = []

LANGUAGE_CODE = "zh-hans"
TIME_ZONE = "Asia/Shanghai"
USE_I18N = True
USE_TZ = True

STATIC_URL = "static/"
MEDIA_URL = "media/"
MEDIA_ROOT = BASE_DIR / "media"
if os.environ.get("FILE_UPLOAD_TEMP_DIR"):
    FILE_UPLOAD_TEMP_DIR = os.environ["FILE_UPLOAD_TEMP_DIR"]
DATA_UPLOAD_MAX_MEMORY_SIZE = None

# 仅保留独立 Kernel，不再提供 embedded 或旧 Python AI 运行模式。
if "AGENT_KERNEL_MODE" in os.environ:
    from django.core.exceptions import ImproperlyConfigured
    raise ImproperlyConfigured("AGENT_KERNEL_MODE 已删除，请移除此配置并使用独立 Kernel")
AGENT_KERNEL_URL = os.environ.get("AGENT_KERNEL_URL", "http://127.0.0.1:8090")
AGENT_KERNEL_TOKEN = os.environ.get("AGENT_KERNEL_TOKEN", "")
AGENT_KERNEL_BUILD = os.environ.get("AGENT_KERNEL_BUILD", "")
AGENT_KERNEL_ROLLOUT = os.environ.get("AGENT_KERNEL_ROLLOUT", "review_only")
AGENT_KERNEL_ALLOW_MOCK = DEBUG and env_bool("AGENT_KERNEL_ALLOW_MOCK", False)
AGENT_KERNEL_DOCUMENT_SIGNING_KEY = os.environ.get("AGENT_KERNEL_DOCUMENT_SIGNING_KEY", AGENT_KERNEL_TOKEN)
AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY = env_bool(
    "AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY", False
)

DEFAULT_AUTO_FIELD = "django.db.models.BigAutoField"

# DRF：W3 登录和 DEBUG 开发命令签发的会话都使用 Token。
REST_FRAMEWORK = {
    "DEFAULT_AUTHENTICATION_CLASSES": [
        "rest_framework.authentication.TokenAuthentication",
        "rest_framework.authentication.SessionAuthentication",
    ],
    "DEFAULT_PERMISSION_CLASSES": ["rest_framework.permissions.IsAuthenticated"],
    "DEFAULT_PAGINATION_CLASS": "apps.api.pagination.StandardResultsSetPagination",
    "PAGE_SIZE": 20,
    "DEFAULT_FILTER_BACKENDS": ["django_filters.rest_framework.DjangoFilterBackend"],
}

# CORS：demo 允许全部来源（前端 dev server 调用）
CORS_ALLOW_ALL_ORIGINS = True

# W3 OAuth2：默认关闭，接入时通过服务端环境变量启用。访问令牌不会下发到前端。
W3_OAUTH2_ENABLED = env_bool("W3_OAUTH2_ENABLED", False)
W3_OAUTH2_CLIENT_ID = os.environ.get("W3_OAUTH2_CLIENT_ID", "")
W3_OAUTH2_CLIENT_SECRET = os.environ.get("W3_OAUTH2_CLIENT_SECRET", "")
W3_OAUTH2_AUTHORIZE_URL = os.environ.get("W3_OAUTH2_AUTHORIZE_URL", "")
W3_OAUTH2_TOKEN_URL = os.environ.get("W3_OAUTH2_TOKEN_URL", "")
W3_OAUTH2_USERINFO_URL = os.environ.get("W3_OAUTH2_USERINFO_URL", "")
W3_OAUTH2_REDIRECT_URI = os.environ.get("W3_OAUTH2_REDIRECT_URI", "")
W3_OAUTH2_FRONTEND_CALLBACK_URL = os.environ.get(
    "W3_OAUTH2_FRONTEND_CALLBACK_URL", "/login"
)
W3_OAUTH2_SCOPE = os.environ.get("W3_OAUTH2_SCOPE", "")
W3_OAUTH2_EMPLOYEE_NO_FIELD = os.environ.get(
    "W3_OAUTH2_EMPLOYEE_NO_FIELD", "employeeNumber"
)
W3_OAUTH2_EMAIL_FIELD = os.environ.get("W3_OAUTH2_EMAIL_FIELD", "email")
W3_OAUTH2_CLIENT_AUTH_METHOD = os.environ.get(
    "W3_OAUTH2_CLIENT_AUTH_METHOD", "client_secret_basic"
)
W3_OAUTH2_USE_PKCE = env_bool("W3_OAUTH2_USE_PKCE", True)
W3_OAUTH2_TIMEOUT_SECONDS = float(os.environ.get("W3_OAUTH2_TIMEOUT_SECONDS", "10"))
W3_OAUTH2_TRANSACTION_TTL_SECONDS = int(
    os.environ.get("W3_OAUTH2_TRANSACTION_TTL_SECONDS", "300")
)
# Grafana 等监控方只读查询使用频率指标；空值表示禁用密钥访问。
USAGE_METRICS_TOKEN = os.environ.get("USAGE_METRICS_TOKEN", "")
# OAuth2 state/PKCE 和一次性登录凭据存于服务端 Session；正式启用时 Cookie 仅走 HTTPS。
SESSION_COOKIE_SECURE = W3_OAUTH2_ENABLED and not DEBUG
SESSION_COOKIE_HTTPONLY = True
SESSION_COOKIE_SAMESITE = "Lax"

# Celery：demo 默认 eager（同步执行，不需要 Redis/worker）
CELERY_BROKER_URL = os.environ.get("CELERY_BROKER_URL", "redis://localhost:6379/0")
CELERY_RESULT_BACKEND = os.environ.get("CELERY_RESULT_BACKEND", "redis://localhost:6379/1")
CELERY_TASK_ALWAYS_EAGER = env_bool("CELERY_TASK_ALWAYS_EAGER", True)
CELERY_TASK_EAGER_PROPAGATES = True
CELERY_TASK_DEFAULT_QUEUE = "default"
CELERY_TASK_ROUTES = {
    "apps.pipeline.tasks.process_ai_scope_item_task": {"queue": "ai"},
}
CELERY_WORKER_PREFETCH_MULTIPLIER = 1
# 候选人任务只包含一次模型调用及其有限重试；为 Redis broker 留足可见性窗口，
# 防止长超时请求尚未结束就被重复投递。worker 丢失仍由 acks_late 立即重投。
CELERY_BROKER_TRANSPORT_OPTIONS = {"visibility_timeout": 21600}
