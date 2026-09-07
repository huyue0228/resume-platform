"""服务页面使用频率上报与 Grafana 聚合查询。"""
import logging
import secrets
from datetime import date, datetime, time, timedelta
from zoneinfo import ZoneInfo

from django.conf import settings
from django.db import transaction
from django.db.models import Count, Q
from django.db.models.functions import TruncDay, TruncHour, TruncWeek
from django.utils import timezone
from rest_framework import serializers, status
from rest_framework.permissions import BasePermission, IsAuthenticated
from rest_framework.response import Response
from rest_framework.views import APIView

from apps.accounts.permissions import has_permission_code
from apps.core import models as m
from apps.core.tasks import cleanup_usage_page_views


logger = logging.getLogger(__name__)
SHANGHAI_TZ = ZoneInfo("Asia/Shanghai")
MAX_RANGE_DAYS = 90
DEFAULT_RANGE_DAYS = 30
CLEANUP_SCHEDULE_CONFIG_KEY = "usage_page_view_cleanup_schedule"

# 只接受实际存在的稳定路由，避免查询参数或动态片段制造高基数指标。
PAGE_KEYS = (
    "/analytics",
    "/processing-tasks",
    "/resumes",
    "/jobs",
    "/schools",
    "/departments",
    "/config",
    "/ai-connection",
    "/users",
)


class UsagePageViewSerializer(serializers.Serializer):
    event_id = serializers.UUIDField()
    session_id = serializers.UUIDField()
    page_key = serializers.ChoiceField(choices=PAGE_KEYS)


class UsageOverviewPermission(BasePermission):
    """允许监控密钥或拥有招聘分析权限的登录用户查询。"""

    def has_permission(self, request, view):
        configured = str(getattr(settings, "USAGE_METRICS_TOKEN", "") or "")
        provided = request.headers.get("X-Usage-Metrics-Key", "")
        metrics_key_valid = bool(configured and provided) and secrets.compare_digest(
            configured.encode("utf-8"), provided.encode("utf-8")
        )
        return metrics_key_valid or has_permission_code(request.user, "analytics.view")


class UsageQueryError(ValueError):
    pass


def _enqueue_cleanup():
    try:
        cleanup_usage_page_views.delay()
    except Exception:  # noqa: BLE001 - 统计清理投递失败不能影响页面业务
        logger.exception("Failed to enqueue usage page-view cleanup")


def schedule_usage_cleanup_once_per_day():
    """用数据库门闩保证每个上海自然日最多投递一次清理任务。"""
    today = timezone.localdate(timezone=SHANGHAI_TZ).isoformat()
    should_schedule = False
    with transaction.atomic():
        marker, created = m.Config.objects.select_for_update().get_or_create(
            key=CLEANUP_SCHEDULE_CONFIG_KEY,
            defaults={"value": {"last_scheduled_date": today}},
        )
        if created:
            should_schedule = True
        elif not isinstance(marker.value, dict) or marker.value.get(
            "last_scheduled_date"
        ) != today:
            marker.value = {"last_scheduled_date": today}
            marker.save(update_fields=["value"])
            should_schedule = True
        if should_schedule:
            transaction.on_commit(_enqueue_cleanup)
    return should_schedule


def _parse_date(value, name):
    try:
        return date.fromisoformat(value)
    except (TypeError, ValueError) as exc:
        raise UsageQueryError(f"{name} 必须是 YYYY-MM-DD 日期") from exc


def normalize_usage_filters(query_params):
    today = timezone.localdate(timezone=SHANGHAI_TZ)
    date_to = _parse_date(query_params.get("date_to"), "date_to") if query_params.get(
        "date_to"
    ) else today
    date_from = (
        _parse_date(query_params.get("date_from"), "date_from")
        if query_params.get("date_from")
        else date_to - timedelta(days=DEFAULT_RANGE_DAYS - 1)
    )
    if date_from > date_to:
        raise UsageQueryError("date_from 不能晚于 date_to")
    if (date_to - date_from).days + 1 > MAX_RANGE_DAYS:
        raise UsageQueryError("查询日期范围最长为 90 个自然日")

    granularity = query_params.get("granularity", "day")
    if granularity not in {"hour", "day", "week"}:
        raise UsageQueryError("granularity 必须是 hour、day 或 week")

    page = query_params.get("page") or None
    if page is not None and page not in PAGE_KEYS:
        raise UsageQueryError("page 不是允许统计的页面键")
    return {
        "date_from": date_from,
        "date_to": date_to,
        "granularity": granularity,
        "page": page,
    }


def _metric_aggregates():
    return {
        "page_views": Count("id"),
        "sessions": Count("session_id", distinct=True),
        "active_users": Count(
            "employee_no_snapshot",
            distinct=True,
            filter=~Q(employee_no_snapshot=""),
        ),
    }


def _range_bounds(filters):
    start = datetime.combine(filters["date_from"], time.min, tzinfo=SHANGHAI_TZ)
    end = datetime.combine(
        filters["date_to"] + timedelta(days=1),
        time.min,
        tzinfo=SHANGHAI_TZ,
    )
    return start, end


def _bucket_floor(value, granularity):
    local = value.astimezone(SHANGHAI_TZ)
    local = local.replace(minute=0, second=0, microsecond=0)
    if granularity in {"day", "week"}:
        local = local.replace(hour=0)
    if granularity == "week":
        local -= timedelta(days=local.weekday())
    return local


def _trend_rows(queryset, filters):
    truncator = {
        "hour": TruncHour,
        "day": TruncDay,
        "week": TruncWeek,
    }[filters["granularity"]]
    rows = (
        queryset.annotate(
            bucket=truncator("occurred_at", tzinfo=SHANGHAI_TZ)
        )
        .values("bucket")
        .annotate(**_metric_aggregates())
        .order_by("bucket")
    )
    by_bucket = {
        _bucket_floor(row["bucket"], filters["granularity"]).isoformat(): row
        for row in rows
    }

    start, end = _range_bounds(filters)
    cursor = _bucket_floor(start, filters["granularity"])
    step = (
        timedelta(hours=1)
        if filters["granularity"] == "hour"
        else timedelta(days=1)
    )
    if filters["granularity"] == "week":
        step = timedelta(days=7)
    result = []
    while cursor < end:
        key = cursor.isoformat()
        row = by_bucket.get(key, {})
        result.append(
            {
                "bucket": key,
                "page_views": row.get("page_views", 0),
                "sessions": row.get("sessions", 0),
                "active_users": row.get("active_users", 0),
            }
        )
        cursor += step
    return result


def build_usage_overview(filters):
    start, end = _range_bounds(filters)
    data_as_of = timezone.now()
    queryset = m.UsagePageView.objects.filter(
        occurred_at__gte=start,
        occurred_at__lt=end,
        occurred_at__lte=data_as_of,
    )
    if filters["page"]:
        queryset = queryset.filter(page_key=filters["page"])

    summary = queryset.aggregate(**_metric_aggregates())
    ranking = list(
        queryset.values("page_key")
        .annotate(**_metric_aggregates())
        .order_by("-page_views", "page_key")
    )
    return {
        "data_as_of": timezone.localtime(data_as_of, SHANGHAI_TZ).isoformat(),
        "filters": {
            "date_from": filters["date_from"].isoformat(),
            "date_to": filters["date_to"].isoformat(),
            "granularity": filters["granularity"],
            "page": filters["page"],
        },
        "summary": summary,
        "trend": _trend_rows(queryset, filters),
        "page_ranking": ranking,
    }


class UsagePageViewCreateView(APIView):
    permission_classes = [IsAuthenticated]

    def post(self, request):
        serializer = UsagePageViewSerializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        event, created = m.UsagePageView.objects.get_or_create(
            event_id=serializer.validated_data["event_id"],
            defaults={
                "session_id": serializer.validated_data["session_id"],
                "user": request.user,
                "employee_no_snapshot": request.user.username,
                "page_key": serializer.validated_data["page_key"],
            },
        )
        if created:
            try:
                schedule_usage_cleanup_once_per_day()
            except Exception:  # noqa: BLE001 - 统计维护失败不影响页面访问上报
                logger.exception("Failed to schedule usage page-view cleanup")
        return Response(
            {"accepted": True, "duplicate": not created},
            status=status.HTTP_201_CREATED if created else status.HTTP_200_OK,
        )


class UsageOverviewView(APIView):
    permission_classes = [UsageOverviewPermission]

    def get(self, request):
        try:
            filters = normalize_usage_filters(request.query_params)
        except UsageQueryError as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
        return Response(build_usage_overview(filters))
