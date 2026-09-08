import hashlib
import importlib.util
import json
import tarfile
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("release", Path(__file__).resolve().parents[1] / "release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)

class ReleaseTests(unittest.TestCase):
    def images(self, root):
        images = root / "images"
        images.mkdir()
        for component in release.COMPONENTS:
            (images / (component + ".json")).write_text(json.dumps(dict(component=component,
                version="test-v1", platform="linux/amd64", commit="a"*40, published=True,
                image=f"gitlab.internal:5000/resume/platform-{component}:test-v1", digest="sha256:"+"1"*64)))
        return images

    def test_package_preserves_registry_port_and_checks_every_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            images = self.images(root)
            archive = release.package("test-v1", images, root / "output")
            with tarfile.open(archive) as tar:
                content = {x.name: tar.extractfile(x).read() for x in tar.getmembers()}
            self.assertIn(b"gitlab.internal:5000/resume/platform-backend@sha256:", content["images.env"])
            self.assertNotIn(b"build:", content["compose.yml"])
            self.assertNotIn(b"AGENT_KERNEL_MODE:", content["compose.yml"])
            self.assertIn(b"AGENT_KERNEL_ROLLOUT=review_only", content["env.example"])
            self.assertIn(b"AGENT_KERNEL_CA_BUNDLE=\n", content["env.example"])
            self.assertIn(b"SSL_CERT_FILE: /etc/agent-kernel/model-ca.pem", content["compose.model-ca.yml"])
            self.assertIn(b"create_host_path: false", content["compose.model-ca.yml"])
            self.assertEqual(set(release.COMPONENTS), {"backend", "frontend", "postgres", "redis"})
            for name in ("images.env", "compose.yml", "env.example"):
                for removed in (b"BACKUP_", b"RESTIC_", b"backup-scheduler", b"profiles: [\"restore\"]"):
                    self.assertNotIn(removed, content[name])
            for line in content["SHA256SUMS"].decode().splitlines():
                digest, name = line.split("  ", 1)
                self.assertEqual(hashlib.sha256(content[name]).hexdigest(), digest)
            with self.assertRaises(ValueError):
                release.package("test-v1", images, root / "output")

    def test_rejects_mixed_revisions_and_invalid_digest(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            images = self.images(root)
            backend = images / "backend.json"
            data = json.loads(backend.read_text())
            data["commit"] = "b"*40
            backend.write_text(json.dumps(data))
            with self.assertRaises(ValueError):
                release.package("test-v1", images, root / "output")
        with self.assertRaises(ValueError):
            release.image_reference(dict(image="registry:5000/team/app:v1", published=True, digest=""))

    def test_rejects_path_injection(self):
        with self.assertRaises(ValueError):
            release.version("../../unrelated")

if __name__ == "__main__":
    unittest.main()
