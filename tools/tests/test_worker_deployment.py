"""Go 平台四服务部署和既有数据卷命名验证。"""
import json
from pathlib import Path
import shutil
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]
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
                self.assertEqual(services["app"]["command"], ["serve"])
                self.assertIn("healthcheck", services["app"]["healthcheck"]["test"])
                self.assertNotIn("AI_WORKER_CONCURRENCY", services["app"]["environment"])
                self.assertNotIn("CELERY_TASK_ALWAYS_EAGER", services["app"]["environment"])
                if relative.startswith("skills/"):
                    self.assertTrue(all("build" not in service for service in services.values()))

    def test_explicit_project_name_scopes_names_and_existing_volumes(self):
        config = self.compose_config("docker-compose.yml", "resume-acceptance")
        self.assertEqual(config["services"]["app"]["container_name"], "resume-acceptance-app")
        self.assertEqual(config["volumes"]["pgdata"]["name"], "resume-acceptance_pgdata")
        self.assertEqual(config["volumes"]["media_data"]["name"], "resume-acceptance_media_data")
