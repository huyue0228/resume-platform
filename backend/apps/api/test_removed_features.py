from django.test import TestCase


class RemovedFeatureRouteTests(TestCase):
    def test_prompt_management_api_is_absent(self):
        for path in [
            "/api/ai-prompts/",
            "/api/ai-prompts/draft/",
            "/api/ai-prompts/versions/legacy/",
        ]:
            with self.subTest(path=path):
                self.assertEqual(self.client.get(path).status_code, 404)
