"""不启动 Docker 的交付回归，守住私有简历的唯一 HTTP 入口。"""
import re
import shlex
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class FrontendDeliveryTests(unittest.TestCase):
    def test_resume_fetches_use_authenticated_api_client(self):
        client = (ROOT / "frontend/src/api/client.js").read_text()
        self.assertIn("baseURL: '/api'", client)
        self.assertIn("client.interceptors.request.use", client)
        self.assertIn("config.headers.Authorization = `Token ${token}`", client)
        services = (ROOT / "frontend/src/api/services.js").read_text()
        for function, endpoint in (
            ("previewResume", "/resumes/${id}/preview/"),
            ("previewAllocationResume", "/workflow-attempts/${id}/resume-preview/"),
            ("exportCandidates", "/candidates/export/"),
            ("exportAllocations", "/workflow-attempts/export/"),
        ):
            match = re.search(
                r"export function " + function + r"\([^\n]*\)\s*\{(.*?)\n\}",
                services, re.S,
            )
            self.assertIsNotNone(match, function)
            self.assertIn("return client.get(", match.group(1))
            self.assertIn(endpoint, match.group(1))
            self.assertIn("responseType: 'blob'", match.group(1))

    def test_preview_downloads_the_authenticated_blob_not_a_media_url(self):
        preview = (ROOT / "frontend/src/components/ResumePreview.jsx").read_text()
        self.assertIn("attemptId ? previewAllocationResume(attemptId) : previewResume(resume.id)", preview)
        self.assertIn("URL.createObjectURL(blob)", preview)
        self.assertIn("downloadBlob(state.blob, filename)", preview)
        self.assertNotIn("/media/", preview)

    def test_offline_go_check_does_not_depend_on_database_or_model(self):
        script = (ROOT / "skills/smart-resume-offline-release/scripts/release.sh").read_text()
        commands = [shlex.split(line) for line in script.splitlines()
                    if line.startswith("docker run ") and '"$APP_IMAGE" check' in line]
        self.assertEqual(len(commands), 1)
        self.assertNotIn("--add-host", commands[0])
        self.assertEqual(commands[0][-1], "check")


if __name__ == "__main__":
    unittest.main()
