"""一个容器运行两个队列进程，保留调度与长时间 AI 调用之间的独立并发。"""
import os
import signal
import socket
import subprocess
import sys
import time


def worker_commands():
    commands = []
    for queue, pool, variable, default in (
        ("default", "prefork", "WORKER_CONCURRENCY", 2),
        ("ai", "threads", "AI_WORKER_CONCURRENCY", 20),
    ):
        concurrency = int(os.environ.get(variable, default))
        if concurrency < 1:
            raise ValueError(f"{variable} 必须为正整数")
        commands.append([
            sys.executable, "-m", "celery", "-A", "config", "worker", "-l", "info",
            "-Q", queue, f"--hostname={queue}@%h", f"--pool={pool}",
            f"--concurrency={concurrency}", "--prefetch-multiplier=1",
        ])
    return commands


def supervise(commands, shutdown_timeout=90):
    """任一队列进程退出则停止另一进程，由容器 restart 策略整体重启。"""
    children = []
    stopping = False

    def request_stop(_signum, _frame):
        nonlocal stopping
        stopping = True

    previous = {sig: signal.signal(sig, request_stop) for sig in (signal.SIGTERM, signal.SIGINT)}
    try:
        for command in commands:
            if stopping:
                break
            children.append(subprocess.Popen(command, start_new_session=True))
        while not stopping:
            if any(child.poll() is not None for child in children):
                print("后台队列进程已退出，将重启工作容器。", file=sys.stderr, flush=True)
                return 1
            time.sleep(0.2)
        return 0
    finally:
        # SIGTERM 发给 Celery 主进程，让其等待在途任务；超时才清理整个进程组。
        for child in children:
            if child.poll() is None:
                child.terminate()
        deadline = time.monotonic() + shutdown_timeout
        for child in children:
            try:
                child.wait(timeout=max(0, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                pass
        for child in children:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            child.wait()
        for sig, handler in previous.items():
            signal.signal(sig, handler)


def healthcheck():
    from config.celery import app
    names = {f"{queue}@{socket.gethostname()}" for queue in ("default", "ai")}
    replies = app.control.ping(destination=sorted(names), timeout=5)
    healthy = {name for reply in replies for name, result in reply.items() if result.get("ok") == "pong"}
    return 0 if names <= healthy else 1


if __name__ == "__main__":
    if sys.argv[1:] == ["--healthcheck"]:
        try:
            sys.exit(healthcheck())
        except Exception:
            # 不输出含 broker 连接信息的异常。
            sys.exit(1)
    sys.exit(supervise(worker_commands()))
