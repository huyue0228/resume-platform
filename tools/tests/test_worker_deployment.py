"""队列合并后的命名、独立消费、进程退出与信号回收验证。"""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("worker", ROOT / "backend/worker.py")
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class WorkerProcessTests(unittest.TestCase):
    def test_queues_keep_independent_pools_and_configured_concurrency(self):
        with patch.dict(os.environ, {"WORKER_CONCURRENCY": "3", "AI_WORKER_CONCURRENCY": "7"}):
            default, ai = worker.worker_commands()
        self.assertIn("default", default)
        self.assertIn("--pool=prefork", default)
        self.assertIn("--concurrency=3", default)
        self.assertIn("ai", ai)
        self.assertIn("--pool=threads", ai)
        self.assertIn("--concurrency=7", ai)
        with patch.dict(os.environ, {"WORKER_CONCURRENCY": "0"}):
            with self.assertRaises(ValueError):
                worker.worker_commands()

    def exercise_shutdown(self, signal_parent):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            # 真实子进程记录 PID、接收 SIGTERM；不需要 Docker、Redis 或业务数据库。
            child = '''import os, pathlib, signal, sys, time
folder, name = pathlib.Path(sys.argv[1]), sys.argv[2]
def stop(*_):
    (folder / (name + ".stopped")).touch()
    sys.exit(0)
signal.signal(signal.SIGTERM, stop)
(folder / (name + ".pid")).write_text(str(os.getpid()))
while not (folder / "exit").exists() or name != "default":
    time.sleep(.02)
'''
            commands = [[sys.executable, "-c", child, temporary, name] for name in ("default", "ai")]
            launcher = f"import sys; sys.path.insert(0, {str(ROOT / 'backend')!r}); import worker; sys.exit(worker.supervise({commands!r}, shutdown_timeout=2))"
            process = subprocess.Popen([sys.executable, "-c", launcher], stderr=subprocess.DEVNULL)
            try:
                deadline = time.monotonic() + 5
                while not all((folder / (name + ".pid")).exists() for name in ("default", "ai")):
                    if time.monotonic() >= deadline:
                        self.fail("worker children failed to start")
                    time.sleep(.02)
                if signal_parent:
                    process.send_signal(signal.SIGTERM)
                else:
                    (folder / "exit").touch()
                self.assertEqual(process.wait(timeout=5), 0 if signal_parent else 1)
                self.assertTrue((folder / "ai.stopped").exists())
                if signal_parent:
                    self.assertTrue((folder / "default.stopped").exists())
                for path in folder.glob("*.pid"):
                    with self.assertRaises(ProcessLookupError):
                        os.kill(int(path.read_text()), 0)
            finally:
                if process.poll() is None:
                    process.terminate()
                    process.wait(timeout=5)

    def test_one_queue_exiting_stops_the_other_and_returns_failure(self):
        self.exercise_shutdown(False)

    def test_container_stop_reaches_both_queues_and_reaps_children(self):
        self.exercise_shutdown(True)

    def test_application_runs_web_api_and_both_queues_as_independent_processes(self):
        script = "import application, json; print(json.dumps(application.application_commands()))"
        commands = json.loads(subprocess.check_output([sys.executable, "-c", script], cwd=ROOT / "backend", text=True))
        self.assertEqual(len(commands), 4)
        self.assertEqual(commands[0], ["nginx", "-g", "daemon off;"])
        self.assertIn("gunicorn", commands[1])
        self.assertIn("127.0.0.1:8000", commands[1])
        self.assertIn("default", commands[2])
        self.assertIn("ai", commands[3])

    @unittest.skipUnless(importlib.util.find_spec("django"), "Django dependencies unavailable")
    def test_healthcheck_initializes_django_and_uses_the_configured_host(self):
        script = '''import application
from unittest.mock import patch, MagicMock
reply = MagicMock()
reply.__enter__.return_value.status = 200
with patch.object(application, "urlopen", return_value=reply) as request, patch.object(application, "queues_healthy", return_value=0) as queues:
    assert application.healthcheck() == 0
    assert request.call_count == 2
    for call in request.call_args_list:
        assert call.args[0].get_header("Host") == "resume-verify.internal"
    queues.assert_called_once()
'''
        environment = {key: value for key, value in os.environ.items() if key != "DJANGO_SETTINGS_MODULE"}
        environment["DJANGO_ALLOWED_HOSTS"] = "resume-verify.internal"
        subprocess.run([sys.executable, "-c", script], cwd=ROOT / "backend", env=environment, check=True)


@unittest.skipUnless(shutil.which("docker"), "Docker Compose CLI unavailable")
class ComposeDeploymentTests(unittest.TestCase):
    def compose_config(self, relative, project=None):
        # --env-file 使用仓库占位模板；从不读取现场 .env 或输出环境密钥。
        command = ["docker", "compose", "--env-file", str(ROOT / ".env.example"), "-f", str(ROOT / relative)]
        if project:
            command.extend(["--project-name", project])
        command.extend(["config", "--format", "json"])
        return json.loads(subprocess.check_output(command, cwd=ROOT, text=True))

    def test_source_and_offline_default_to_four_named_services(self):
        expected = {"agent-kernel": "agent-kernel", "db": "postgres", "redis": "redis", "app": "app"}
        for relative in ("docker-compose.yml", "skills/smart-resume-offline-release/assets/docker-compose.yml"):
            with self.subTest(compose=relative):
                config = self.compose_config(relative)
                services = config["services"]
                self.assertEqual(set(services), set(expected))
                self.assertEqual({key: value["container_name"] for key, value in services.items()},
                                 {key: f"smart-resume-filter-{suffix}" for key, suffix in expected.items()})
                self.assertEqual(services["app"]["command"], ["python", "application.py"])
                self.assertIn("--healthcheck", services["app"]["healthcheck"]["test"])
                self.assertEqual(services["app"]["environment"]["AI_WORKER_CONCURRENCY"], "20")
                self.assertEqual(services["app"]["environment"]["CELERY_TASK_ALWAYS_EAGER"], "False")
                if relative.startswith("skills/"):
                    self.assertTrue(all("build" not in service for service in services.values()))

    def test_explicit_project_name_scopes_names_and_existing_volumes(self):
        config = self.compose_config("docker-compose.yml", "resume-acceptance")
        self.assertEqual(config["services"]["app"]["container_name"], "resume-acceptance-app")
        self.assertEqual(config["volumes"]["pgdata"]["name"], "resume-acceptance_pgdata")
        self.assertEqual(config["volumes"]["media_data"]["name"], "resume-acceptance_media_data")
