import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SKILL = ROOT / "skills/smart-resume-offline-deploy"


class ModelCADeliveryTests(unittest.TestCase):
    def compose_args(self, ca_line, override=None):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            env_file = root / ".env"
            env_file.write_text(ca_line)
            docker = root / "docker"
            docker.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
            docker.chmod(0o755)
            env = os.environ.copy()
            env.pop("AGENT_KERNEL_CA_BUNDLE", None)
            env.update(PATH=str(root) + os.pathsep + env["PATH"],
                       ENV_FILE=str(env_file), SKILL_DIR=str(SKILL),
                       COMPOSE_FILE="docker-compose.yml", PROJECT_NAME="test-model-ca")
            if override is not None:
                env["AGENT_KERNEL_CA_BUNDLE"] = override
            return subprocess.check_output(
                ["bash", "-c", 'source "$SKILL_DIR/scripts/compose.sh"; compose config --images'],
                env=env, text=True,
            ).splitlines()

    def test_public_ca_deployment_uses_main_compose(self):
        for line in ("", "AGENT_KERNEL_CA_BUNDLE=\n", 'AGENT_KERNEL_CA_BUNDLE=""\n'):
            with self.subTest(line=line):
                args = self.compose_args(line)
                self.assertEqual(args.count("-f"), 1)
                self.assertEqual(args[-2:], ["config", "--images"])

    def test_private_ca_deployment_adds_shipped_overlay(self):
        for line in ("AGENT_KERNEL_CA_BUNDLE=/etc/company/ca.pem\n",
                     'AGENT_KERNEL_CA_BUNDLE="/etc/company ca/ca.pem"\n'):
            with self.subTest(line=line):
                args = self.compose_args(line)
                self.assertEqual(args.count("-f"), 2)
                self.assertIn(str(SKILL / "assets/compose.model-ca.yml"), args)

    def test_environment_override_matches_compose_precedence(self):
        self.assertEqual(self.compose_args("", override="/etc/company/ca.pem").count("-f"), 2)
        self.assertEqual(self.compose_args("AGENT_KERNEL_CA_BUNDLE=/etc/company/ca.pem\n", override="").count("-f"), 1)
