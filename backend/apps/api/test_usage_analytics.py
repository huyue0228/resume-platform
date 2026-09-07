import uuid
from datetime import datetime, time, timedelta
from unittest.mock import patch
from zoneinfo import ZoneInfo

from django.test import TestCase, override_settings
from django.utils import timezone
from rest_framework.authtoken.models import Token
from rest_framework.test import APIClient

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults
from apps.api import usage_analytics
from apps.core import models as m
from apps.core import tasks


SHANGHAI_TZ = ZoneInfo("Asia/Shanghai")
PAGE_VIEW_URL = "/api/analytics/usage/page-view/"
OVERVIEW_URL = "/api/analytics/usage/overview/"


class UsagePageViewApiTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.user = User.objects.create_user(username="000001")
        self.client = APIClient()

    def payload(self, **overrides):
        payload = {
            "event_id": str(uuid.uuid4()),
            "session_id": str(uuid.uuid4()),
            "page_key": "/analytics",
        }
        payload.update(overrides)
        return payload

    def test_requires_login(self):
        response = self.client.post(PAGE_VIEW_URL, self.payload(), format="json")

        self.assertEqual(response.status_code, 401)
        self.assertFalse(m.UsagePageView.objects.exists())

    def test_accepts_token_and_session_auth_and_uses_server_snapshot(self):
        token = Token.objects.create(user=self.user)
        self.client.credentials(HTTP_AUTHORIZATION=f"Token {token.key}")
        before = timezone.now()
        token_response = self.client.post(
            PAGE_VIEW_URL,
            self.payload(occurred_at="2000-01-01T00:00:00Z"),
            format="json",
        )
        after = timezone.now()

        self.assertEqual(token_response.status_code, 201)
        event = m.UsagePageView.objects.get()
        self.assertEqual(event.user, self.user)
        self.assertEqual(event.employee_no_snapshot, "000001")
        self.assertGreaterEqual(event.occurred_at, before)
        self.assertLessEqual(event.occurred_at, after)

        session_client = APIClient()
        session_client.force_login(self.user)
        session_response = session_client.post(
            PAGE_VIEW_URL,
            self.payload(page_key="/resumes"),
            format="json",
        )
        self.assertEqual(session_response.status_code, 201)
        self.assertEqual(m.UsagePageView.objects.count(), 2)

    def test_duplicate_event_is_idempotent_and_does_not_replace_payload(self):
        self.client.force_authenticate(self.user)
        event_id = str(uuid.uuid4())
        first = self.client.post(
            PAGE_VIEW_URL,
            self.payload(event_id=event_id),
            format="json",
        )
        second = self.client.post(
            PAGE_VIEW_URL,
            self.payload(event_id=event_id, page_key="/resumes"),
            format="json",
        )

        self.assertEqual(first.status_code, 201)
        self.assertEqual(first.json(), {"accepted": True, "duplicate": False})
        self.assertEqual(second.status_code, 200)
        self.assertEqual(second.json(), {"accepted": True, "duplicate": True})
        self.assertEqual(m.UsagePageView.objects.count(), 1)
        self.assertEqual(m.UsagePageView.objects.get().page_key, "/analytics")

    def test_rejects_invalid_uuid_and_unstable_page_key(self):
        self.client.force_authenticate(self.user)
        cases = [
            self.payload(event_id="not-a-uuid"),
            self.payload(session_id="not-a-uuid"),
            self.payload(page_key="/resumes/123?tab=detail"),
        ]
        for payload in cases:
            with self.subTest(payload=payload):
                response = self.client.post(PAGE_VIEW_URL, payload, format="json")
                self.assertEqual(response.status_code, 400)
        self.assertFalse(m.UsagePageView.objects.exists())

    def test_first_new_event_each_shanghai_day_enqueues_cleanup_once(self):
        self.client.force_authenticate(self.user)
        today = timezone.localdate(timezone=SHANGHAI_TZ)
        with patch(
            "apps.api.usage_analytics.cleanup_usage_page_views.delay"
        ) as enqueue:
            with self.captureOnCommitCallbacks(execute=True):
                first = self.client.post(PAGE_VIEW_URL, self.payload(), format="json")
                second = self.client.post(PAGE_VIEW_URL, self.payload(), format="json")
            self.assertEqual(first.status_code, 201)
            self.assertEqual(second.status_code, 201)
            enqueue.assert_called_once_with()

            with patch(
                "apps.api.usage_analytics.timezone.localdate",
                return_value=today + timedelta(days=1),
            ), self.captureOnCommitCallbacks(execute=True):
                third = self.client.post(PAGE_VIEW_URL, self.payload(), format="json")
            self.assertEqual(third.status_code, 201)
            self.assertEqual(enqueue.call_count, 2)

        marker = m.Config.objects.get(
            key=usage_analytics.CLEANUP_SCHEDULE_CONFIG_KEY
        )
        self.assertEqual(
            marker.value["last_scheduled_date"],
            (today + timedelta(days=1)).isoformat(),
        )


@override_settings(USAGE_METRICS_TOKEN="grafana-secret")
class UsageOverviewApiTests(TestCase):
    def setUp(self):
        ensure_rbac_defaults()
        self.hr = User.objects.create_user(username="000101")
        self.hr.groups.add(self.hr.groups.model.objects.get(name="HR"))
        self.other_user = User.objects.create_user(username="000102")
        self.no_access = User.objects.create_user(username="000103")
        self.no_access.groups.add(
            self.no_access.groups.model.objects.get(name="二级接口人")
        )
        self.client = APIClient()

    def create_event(self, *, user, session_id, page_key, occurred_at):
        event = m.UsagePageView.objects.create(
            event_id=uuid.uuid4(),
            session_id=session_id,
            user=user,
            employee_no_snapshot=user.username,
            page_key=page_key,
        )
        m.UsagePageView.objects.filter(pk=event.pk).update(occurred_at=occurred_at)
        event.refresh_from_db()
        return event

    def test_allows_grafana_key_or_analytics_permission_only(self):
        missing = self.client.get(OVERVIEW_URL)
        wrong = self.client.get(
            OVERVIEW_URL,
            HTTP_X_USAGE_METRICS_KEY="wrong",
        )
        self.client.force_authenticate(self.no_access)
        forbidden = self.client.get(OVERVIEW_URL)
        self.client.force_authenticate(self.hr)
        permitted = self.client.get(OVERVIEW_URL)
        self.client.force_authenticate(user=None)
        grafana = self.client.get(
            OVERVIEW_URL,
            HTTP_X_USAGE_METRICS_KEY="grafana-secret",
        )

        self.assertIn(missing.status_code, {401, 403})
        self.assertIn(wrong.status_code, {401, 403})
        self.assertEqual(forbidden.status_code, 403)
        self.assertEqual(permitted.status_code, 200)
        self.assertEqual(grafana.status_code, 200)

    @override_settings(USAGE_METRICS_TOKEN="")
    def test_empty_configured_key_never_enables_anonymous_access(self):
        response = self.client.get(
            OVERVIEW_URL,
            HTTP_X_USAGE_METRICS_KEY="anything",
        )
        self.assertIn(response.status_code, {401, 403})

    def test_default_filters_are_latest_30_natural_days(self):
        self.client.force_authenticate(self.hr)
        today = timezone.localdate(timezone=SHANGHAI_TZ)
        response = self.client.get(OVERVIEW_URL)

        self.assertEqual(response.status_code, 200)
        filters = response.json()["filters"]
        self.assertEqual(filters["date_from"], (today - timedelta(days=29)).isoformat())
        self.assertEqual(filters["date_to"], today.isoformat())
        self.assertEqual(filters["granularity"], "day")
        self.assertIsNone(filters["page"])
        self.assertEqual(len(response.json()["trend"]), 30)
        self.assertTrue(
            all(row["page_views"] == 0 for row in response.json()["trend"])
        )

    def test_rejects_invalid_query_parameters_and_ranges_over_90_days(self):
        self.client.force_authenticate(self.hr)
        today = timezone.localdate(timezone=SHANGHAI_TZ)
        cases = [
            {"date_from": "2026-02-30"},
            {"date_to": "not-a-date"},
            {"date_from": today.isoformat(), "date_to": (today - timedelta(days=1)).isoformat()},
            {
                "date_from": (today - timedelta(days=90)).isoformat(),
                "date_to": today.isoformat(),
            },
            {"granularity": "month"},
            {"page": "/resumes/123"},
        ]
        for params in cases:
            with self.subTest(params=params):
                response = self.client.get(OVERVIEW_URL, params)
                self.assertEqual(response.status_code, 400)

        boundary = self.client.get(
            OVERVIEW_URL,
            {
                "date_from": (today - timedelta(days=89)).isoformat(),
                "date_to": today.isoformat(),
            },
        )
        self.assertEqual(boundary.status_code, 200)
        self.assertEqual(len(boundary.json()["trend"]), 90)

    def test_aggregates_totals_zero_filled_trends_and_page_ranking(self):
        self.client.force_authenticate(self.hr)
        first_day = timezone.localdate(timezone=SHANGHAI_TZ) - timedelta(days=2)
        second_day = first_day + timedelta(days=1)
        session_1 = uuid.uuid4()
        session_2 = uuid.uuid4()
        self.create_event(
            user=self.hr,
            session_id=session_1,
            page_key="/analytics",
            occurred_at=datetime.combine(first_day, time(10, 15), SHANGHAI_TZ),
        )
        self.create_event(
            user=self.hr,
            session_id=session_1,
            page_key="/analytics",
            occurred_at=datetime.combine(first_day, time(10, 45), SHANGHAI_TZ),
        )
        self.create_event(
            user=self.other_user,
            session_id=session_2,
            page_key="/resumes",
            occurred_at=datetime.combine(first_day, time(12, 0), SHANGHAI_TZ),
        )
        self.create_event(
            user=self.other_user,
            session_id=session_2,
            page_key="/analytics",
            occurred_at=datetime.combine(second_day, time(9, 5), SHANGHAI_TZ),
        )
        params = {
            "date_from": first_day.isoformat(),
            "date_to": second_day.isoformat(),
            "granularity": "day",
        }

        response = self.client.get(OVERVIEW_URL, params)

        self.assertEqual(response.status_code, 200)
        payload = response.json()
        self.assertIn("data_as_of", payload)
        self.assertEqual(
            payload["summary"],
            {"page_views": 4, "sessions": 2, "active_users": 2},
        )
        self.assertEqual(
            payload["trend"],
            [
                {
                    "bucket": datetime.combine(first_day, time.min, SHANGHAI_TZ).isoformat(),
                    "page_views": 3,
                    "sessions": 2,
                    "active_users": 2,
                },
                {
                    "bucket": datetime.combine(second_day, time.min, SHANGHAI_TZ).isoformat(),
                    "page_views": 1,
                    "sessions": 1,
                    "active_users": 1,
                },
            ],
        )
        self.assertEqual(
            payload["page_ranking"],
            [
                {
                    "page_key": "/analytics",
                    "page_views": 3,
                    "sessions": 2,
                    "active_users": 2,
                },
                {
                    "page_key": "/resumes",
                    "page_views": 1,
                    "sessions": 1,
                    "active_users": 1,
                },
            ],
        )

        hourly = self.client.get(
            OVERVIEW_URL,
            {
                "date_from": first_day.isoformat(),
                "date_to": first_day.isoformat(),
                "granularity": "hour",
            },
        ).json()["trend"]
        self.assertEqual(len(hourly), 24)
        self.assertEqual(hourly[10]["page_views"], 2)
        self.assertEqual(hourly[10]["sessions"], 1)
        self.assertEqual(hourly[11]["page_views"], 0)
        self.assertEqual(hourly[12]["page_views"], 1)

        weekly = self.client.get(
            OVERVIEW_URL,
            {
                "date_from": first_day.isoformat(),
                "date_to": second_day.isoformat(),
                "granularity": "week",
            },
        ).json()["trend"]
        expected_week_start = first_day - timedelta(days=first_day.weekday())
        self.assertEqual(weekly[0]["bucket"][:10], expected_week_start.isoformat())
        self.assertEqual(sum(row["page_views"] for row in weekly), 4)

    def test_page_filter_applies_to_all_metrics(self):
        self.client.force_authenticate(self.hr)
        day = timezone.localdate(timezone=SHANGHAI_TZ) - timedelta(days=1)
        self.create_event(
            user=self.hr,
            session_id=uuid.uuid4(),
            page_key="/analytics",
            occurred_at=datetime.combine(day, time(9), SHANGHAI_TZ),
        )
        self.create_event(
            user=self.hr,
            session_id=uuid.uuid4(),
            page_key="/resumes",
            occurred_at=datetime.combine(day, time(10), SHANGHAI_TZ),
        )
        response = self.client.get(
            OVERVIEW_URL,
            {
                "date_from": day.isoformat(),
                "date_to": day.isoformat(),
                "page": "/resumes",
            },
        )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["filters"]["page"], "/resumes")
        self.assertEqual(response.json()["summary"]["page_views"], 1)
        self.assertEqual(
            [row["page_key"] for row in response.json()["page_ranking"]],
            ["/resumes"],
        )

    def test_user_deletion_keeps_employee_snapshot_in_active_user_metric(self):
        self.client.force_authenticate(self.hr)
        day = timezone.localdate(timezone=SHANGHAI_TZ) - timedelta(days=1)
        event = self.create_event(
            user=self.other_user,
            session_id=uuid.uuid4(),
            page_key="/analytics",
            occurred_at=datetime.combine(day, time(9), SHANGHAI_TZ),
        )
        self.other_user.delete()
        event.refresh_from_db()

        response = self.client.get(
            OVERVIEW_URL,
            {"date_from": day.isoformat(), "date_to": day.isoformat()},
        )

        self.assertIsNone(event.user)
        self.assertEqual(event.employee_no_snapshot, "000102")
        self.assertEqual(response.json()["summary"]["active_users"], 1)


class UsageCleanupTaskTests(TestCase):
    def test_deletes_only_events_older_than_90_days(self):
        user = User.objects.create_user(username="000201")
        now = timezone.now()

        def create_at(occurred_at):
            event = m.UsagePageView.objects.create(
                event_id=uuid.uuid4(),
                session_id=uuid.uuid4(),
                user=user,
                employee_no_snapshot=user.username,
                page_key="/analytics",
            )
            m.UsagePageView.objects.filter(pk=event.pk).update(
                occurred_at=occurred_at
            )
            return event

        expired = create_at(now - timedelta(days=90, seconds=1))
        retained = create_at(now - timedelta(days=90))

        with patch("apps.core.tasks.timezone.now", return_value=now):
            result = tasks.cleanup_usage_page_views.run()

        self.assertEqual(result["deleted"], 1)
        self.assertFalse(m.UsagePageView.objects.filter(pk=expired.pk).exists())
        self.assertTrue(m.UsagePageView.objects.filter(pk=retained.pk).exists())
