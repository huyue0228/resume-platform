"""单机部署入口：同一容器内分别运行 Web、API、调度和 AI 进程。"""
import os
import sys
from urllib.request import Request, urlopen

from worker import healthcheck as queues_healthy, supervise, worker_commands


def application_commands():
    workers = int(os.environ.get("GUNICORN_WORKERS", "3"))
    timeout = int(os.environ.get("GUNICORN_TIMEOUT", "1800"))
    if workers < 1 or timeout < 1:
        raise ValueError("GUNICORN_WORKERS 和 GUNICORN_TIMEOUT 必须为正整数")
    return [
        ["nginx", "-g", "daemon off;"],
        [sys.executable, "-m", "gunicorn", "config.wsgi:application", "--bind", "127.0.0.1:8000",
         "--workers", str(workers), "--timeout", str(timeout), "--access-logfile", "-"],
        *worker_commands(),
    ]


def healthcheck():
    # 校验前端文件、Nginx 到 API 的转发，以及两个后台队列，不能只检查监听端口。
    os.environ.setdefault("DJANGO_SETTINGS_MODULE", "config.settings")
    from django.conf import settings
    host = next((item.lstrip(".") for item in settings.ALLOWED_HOSTS if item and item != "*"), "localhost")
    for path in ("/", "/api/auth/w3/status/"):
        request = Request(f"http://127.0.0.1{path}", headers={"Host": host})
        with urlopen(request, timeout=2) as response:
            if response.status != 200:
                return 1
    return queues_healthy()


if __name__ == "__main__":
    if sys.argv[1:] == ["--healthcheck"]:
        try:
            sys.exit(healthcheck())
        except Exception:
            sys.exit(1)
    sys.exit(supervise(application_commands()))
