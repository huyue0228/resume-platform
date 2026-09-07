import logging
import hmac
import mimetypes
import os
import re
import time
import uuid
from datetime import date, datetime, time as datetime_time, timedelta
from io import BytesIO
from urllib.parse import quote

from django.conf import settings
from django.contrib.auth.models import Group
from django.db import transaction
from django.db.models import Count, Prefetch
from django.db.models.deletion import ProtectedError
from django.http import HttpResponse, HttpResponseRedirect
from django.db.models import Q
from django.utils import timezone
from rest_framework.authtoken.models import Token
from rest_framework import status, viewsets
from rest_framework.decorators import action
from rest_framework.permissions import AllowAny
from rest_framework.parsers import FormParser, MultiPartParser
from rest_framework.response import Response
from rest_framework.views import APIView

from apps.accounts import oauth2
from apps.accounts.models import User
from apps.accounts.protected_users import (
    PROTECTED_ADMIN_USERNAME,
    is_protected_admin,
)
from apps.accounts.permissions import (
    HasPermissionCode,
    PERMISSION_TREE,
    has_permission_code,
    user_permission_codes,
)
from apps.core import models as m
from apps.core import analytics_scope, candidate_summary, system_status
from apps.core.departments import resolve_department_hierarchy
from apps.core.name_pinyin import name_to_pinyin
from apps.ingestion.sources import (
    RESUME_SUBDIR,
    import_files,
    validate_import_file_headers,
)
from apps.ingestion.tabular_imports import (
    build_import_template_workbook,
    get_import_table_schema,
)
from apps.pipeline import ai_config, cancellation, runner
from apps.pipeline.agent_kernel import gateway as agent_gateway
from apps.pipeline.tasks import (
    execute_runs_sequence_task,
    submit_school_province_enrichment,
)
from apps.pipeline.services import allocate as allocate_service
from apps.pipeline.ai import model_connection as ai_service

from . import serializers
from .job_export import build_job_export_workbook
from .pagination import StandardResultsSetPagination
from .resume_export import (
    CandidateExportRecord,
    ExportFieldError,
    ExportOptionError,
    build_resume_export_excel,
    build_resume_export_zip,
    export_fields_payload,
    parse_export_fields,
    parse_include_resume_files,
)
from .result_report import build_result_report, current_effective_attempt


logger = logging.getLogger(__name__)


CONFIG_REGISTRY = {
    "welink_enabled": {
        "label": "WeLink 下发开关",
        "description": "关闭时仅记录下发状态，不调用真实 WeLink。",
        "value_type": "boolean",
        "default": False,
    },
    "job_hc_coefficient": {
        "label": "岗位 HC 系数",
        "description": "每个处理任务按岗位 HC × 系数冻结自动分配容量。",
        "value_type": "integer",
        "default": 1,
        "min": 1,
        "max": 100,
    },
}


BULK_DISPATCH_FILTER_FIELDS = {
    "system_status",
    "system_statuses",
    "current_entity_in",
    "current_position_name_in",
    "job_department_name_in",
    "current_job_category_in",
    "school_tag_in",
    "allocation_source",
    "reason_code",
}

DEPARTMENT_SCOPE_FORBIDDEN_FILTER_FIELDS = {
    "processing_run_id",
    "processing_result",
    "workflow_status",
    "reason_code",
}


def bool_query_value(value):
    if value in ["true", "false"]:
        return value == "true"
    return None


def _query_list_values(params, key):
    values = params.getlist(key) if hasattr(params, "getlist") else []
    if not values:
        value = params.get(key)
        values = [value] if value is not None else []
    return [
        item.strip()
        for value in values
        for item in str(value).split(",")
        if item.strip()
    ]


def _pinyin_text_matches(value, query):
    query = str(query or "").strip().lower()
    value = str(value or "")
    if not query:
        return True
    full_pinyin, initials = name_to_pinyin(value)
    return query in value.lower() or query in full_pinyin or query in initials


def _filter_queryset_text_with_pinyin(qs, field, query):
    if not query:
        return qs
    matching_ids = [
        obj.id
        for obj in qs.select_related(None).only("id", field)
        if _pinyin_text_matches(getattr(obj, field), query)
    ]
    return qs.filter(id__in=matching_ids)


def _filter_indexed_name(qs, field_prefix, query):
    """Use persisted pinyin columns for person/school name filters."""
    if not query:
        return qs
    normalized = str(query).strip().lower()
    prefix = f"{field_prefix}__" if field_prefix else ""
    return qs.filter(
        Q(**{f"{prefix}name__icontains": query})
        | Q(**{f"{prefix}name_pinyin__icontains": normalized})
        | Q(**{f"{prefix}name_pinyin_initials__icontains": normalized})
    )


def _filter_option(value, *, option_value=None):
    full_pinyin, initials = name_to_pinyin(value)
    return {
        "label": value,
        "value": value if option_value is None else option_value,
        "search_text": " ".join(
            item for item in [value.lower(), full_pinyin, initials] if item
        ),
    }


def _attempt_mutation_error(exc):
    detail = str(exc)
    if isinstance(exc, allocate_service.AttemptStateChanged) or any(
        marker in detail
        for marker in ("反馈已提交", "仅待", "仅已下发", "状态已变化")
    ):
        return Response(
            {"detail": f"状态已变化：{detail}"},
            status=status.HTTP_409_CONFLICT,
        )
    return Response({"detail": detail}, status=status.HTTP_400_BAD_REQUEST)


def _can_transfer_departments(user):
    permissions = user_permission_codes(user)
    if "attempt.view_all" in permissions:
        return True
    contact = getattr(user, "contact", None)
    department = getattr(contact, "department", None) if contact else None
    return bool(
        contact
        and contact.is_active
        and contact.contact_level == m.Contact.LEVEL_SECONDARY
        and contact.can_delegate
        and department
        and department.level == 2
    )


def _reject_unknown_body_fields(request, allowed_fields):
    unknown = sorted(set(request.data.keys()) - set(allowed_fields))
    if not unknown:
        return None
    return Response(
        {"detail": f"不支持的请求字段：{','.join(unknown)}"},
        status=status.HTTP_400_BAD_REQUEST,
    )


def submit_processing_runs(runs):
    """提交一组已创建运行，并为上传与手动处理统一回填 Celery 审计标识。"""
    if not runs:
        return runs
    task = execute_runs_sequence_task.delay([run.id for run in runs])
    task_id = getattr(task, "id", "") or ""
    for run in runs:
        run.refresh_from_db()
        if not run.celery_task_id:
            run.celery_task_id = task_id
            run.save(update_fields=["celery_task_id"])
    return runs


def _clear_user_references(users):
    user_ids = [user.id for user in users if user and user.id]
    if not user_ids:
        return
    m.AssignmentAttempt.objects.filter(created_by_id__in=user_ids).update(
        created_by=None
    )
    m.AssignmentHandlingEvent.objects.filter(actor_id__in=user_ids).update(
        actor=None
    )


def _delete_users(users):
    users = [
        user
        for user in users
        if user and user.id and not is_protected_admin(user)
    ]
    if not users:
        return
    _clear_user_references(users)
    user_ids = [user.id for user in users]
    Token.objects.filter(user_id__in=user_ids).delete()
    for user in users:
        user.groups.clear()
        user.user_permissions.clear()
        user.delete()


def _clear_contact_references(contact):
    if not contact or not contact.id:
        return


def delete_contact_and_bound_users(contact):
    with transaction.atomic():
        locked_contact = m.Contact.objects.select_for_update().get(pk=contact.pk)
        users = list(User.objects.select_for_update().filter(contact=locked_contact))
        _clear_contact_references(locked_contact)
        User.objects.filter(contact=locked_contact).exclude(
            username=PROTECTED_ADMIN_USERNAME
        ).update(contact=None)
        _delete_users(users)
        locked_contact.delete()


def delete_user_and_bound_contact(user):
    with transaction.atomic():
        locked_user = User.objects.select_for_update().get(pk=user.pk)
        contact = locked_user.contact
        if contact:
            delete_contact_and_bound_users(contact)
            return
        _delete_users([locked_user])


def candidate_export_response(records, field_keys, *, include_resume_files=True):
    if include_resume_files:
        content, exported_count, missing_count, candidate_count = (
            build_resume_export_zip(records, field_keys)
        )
        response = HttpResponse(content, content_type="application/zip")
        response["Content-Disposition"] = 'attachment; filename="resumes_export.zip"'
        response["X-Export-Mode"] = "zip"
    else:
        content, candidate_count = build_resume_export_excel(records, field_keys)
        exported_count = 0
        missing_count = 0
        response = HttpResponse(
            content,
            content_type=(
                "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            ),
        )
        response["Content-Disposition"] = (
            f"attachment; filename*=UTF-8''{quote('简历库清单.xlsx')}"
        )
        response["X-Export-Mode"] = "excel"
    response["X-Export-Count"] = str(exported_count)
    response["X-Export-Missing"] = str(missing_count)
    response["X-Export-Candidate-Count"] = str(candidate_count)
    return response


def resume_preview_response(resume):
    if not resume.resume_file:
        return Response({"detail": "该投递暂无简历文件"}, status=status.HTTP_404_NOT_FOUND)
    fname = os.path.basename(resume.resume_file)
    path = os.path.join(settings.MEDIA_ROOT, RESUME_SUBDIR, fname)
    if not os.path.exists(path):
        return Response({"detail": "简历文件不存在"}, status=status.HTTP_404_NOT_FOUND)
    content_type = mimetypes.guess_type(fname)[0] or "application/octet-stream"
    with open(path, "rb") as file_obj:
        response = HttpResponse(file_obj.read(), content_type=content_type)
    response["Content-Disposition"] = f"inline; filename*=UTF-8''{quote(fname)}"
    response["X-Resume-Filename"] = quote(fname)
    return response


def assignment_department_options():
    """返回可分配部门选项，不暴露接口人身份信息。"""

    departments = (
        m.Department.objects.filter(level__in=[2, 3])
        .select_related("parent__parent")
        .order_by("level", "parent_id", "name", "id")
    )
    return serializers.DepartmentSerializer(departments, many=True).data


class PermissionedModelViewSet(viewsets.ModelViewSet):
    permission_classes = [HasPermissionCode]


class PermissionedReadOnlyModelViewSet(viewsets.ReadOnlyModelViewSet):
    permission_classes = [HasPermissionCode]


def _oauth2_redirect(url):
    response = HttpResponseRedirect(url)
    response["Cache-Control"] = "no-store"
    response["Referrer-Policy"] = "no-referrer"
    return response


class W3OAuth2StatusView(APIView):
    permission_classes = [AllowAny]

    def get(self, request):
        config = oauth2.get_config()
        ready = False
        if config.enabled:
            try:
                config.require_ready()
                ready = True
            except oauth2.OAuth2ConfigurationError:
                pass
        response = Response(
            {
                "enabled": config.enabled,
                "ready": ready,
                "debug_token_login_enabled": settings.DEBUG and not ready,
                "start_url": "/api/auth/w3/start/" if ready else None,
            }
        )
        response["Cache-Control"] = "no-store"
        return response


class W3OAuth2StartView(APIView):
    permission_classes = [AllowAny]

    def get(self, request):
        config = oauth2.get_config()
        try:
            authorization_url, state_value, verifier = (
                oauth2.create_authorization_request(config)
            )
        except oauth2.OAuth2ConfigurationError:
            return Response(
                {"detail": "W3 OAuth2 尚未正确配置"},
                status=status.HTTP_503_SERVICE_UNAVAILABLE,
            )
        request.session["w3_oauth2_transaction"] = {
            "state": state_value,
            "verifier": verifier,
            "created_at": time.time(),
        }
        request.session.modified = True
        return _oauth2_redirect(authorization_url)


class W3OAuth2CallbackView(APIView):
    permission_classes = [AllowAny]

    def get(self, request):
        config = oauth2.get_config()
        try:
            config.require_ready()
        except oauth2.OAuth2ConfigurationError:
            return Response(
                {"detail": "W3 OAuth2 尚未正确配置"},
                status=status.HTTP_503_SERVICE_UNAVAILABLE,
            )

        transaction_data = request.session.pop("w3_oauth2_transaction", None)
        request.session.modified = True
        state_value = request.query_params.get("state", "")
        if not self._valid_transaction(config, transaction_data, state_value):
            return self._error_redirect(config, "state_invalid")
        if request.query_params.get("error"):
            return self._error_redirect(config, "provider_denied")

        code = request.query_params.get("code", "")
        if not code:
            return self._error_redirect(config, "authorization_code_missing")

        try:
            access_token = oauth2.exchange_code(
                config, code, transaction_data.get("verifier", "")
            )
            userinfo = oauth2.fetch_userinfo(config, access_token)
            employee_no = oauth2.extract_employee_no(
                userinfo, config.employee_no_field
            )
            email = oauth2.extract_email(userinfo, config.email_field)
        except oauth2.OAuth2ProtocolError as exc:
            logger.warning("W3 OAuth2 登录失败：%s", exc.code)
            return self._error_redirect(config, exc.code)

        user = User.objects.filter(
            username=employee_no,
            email__iexact=email,
        ).first()
        if not user:
            logger.warning("W3 OAuth2 工号和邮箱未映射到同一本地账号")
            return self._error_redirect(config, "account_not_found")
        if not user.is_active:
            return self._error_redirect(config, "account_inactive")

        token, _ = Token.objects.get_or_create(user=user)
        request.session.cycle_key()
        request.session["w3_oauth2_pending_login"] = {
            "token": token.key,
            "user_id": user.pk,
            "created_at": time.time(),
        }
        request.session.modified = True
        return _oauth2_redirect(
            oauth2.add_query_params(
                config.frontend_callback_url, {"oauth2": "success"}
            )
        )

    @staticmethod
    def _valid_transaction(config, transaction_data, state_value):
        if not isinstance(transaction_data, dict) or not state_value:
            return False
        stored_state = transaction_data.get("state")
        created_at = transaction_data.get("created_at")
        if not isinstance(stored_state, str) or not isinstance(
            created_at, (int, float)
        ):
            return False
        if time.time() - created_at > config.transaction_ttl_seconds:
            return False
        return hmac.compare_digest(stored_state, state_value)

    @staticmethod
    def _error_redirect(config, error_code):
        return _oauth2_redirect(
            oauth2.add_query_params(
                config.frontend_callback_url, {"oauth2_error": error_code}
            )
        )


class W3OAuth2CompleteView(APIView):
    permission_classes = [AllowAny]

    def post(self, request):
        config = oauth2.get_config()
        pending = request.session.pop("w3_oauth2_pending_login", None)
        request.session.modified = True
        if not self._valid_pending(config, pending):
            return Response(
                {"detail": "W3 登录凭据无效或已过期，请重新登录"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        user = User.objects.filter(pk=pending["user_id"], is_active=True).first()
        if not user or not Token.objects.filter(
            user=user, key=pending["token"]
        ).exists():
            return Response(
                {"detail": "W3 登录账号不可用，请联系管理员"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        response = Response(
            {
                "token": pending["token"],
                "user": serializers.CurrentUserSerializer(user).data,
            }
        )
        response["Cache-Control"] = "no-store"
        return response

    @staticmethod
    def _valid_pending(config, pending):
        if not isinstance(pending, dict):
            return False
        token = pending.get("token")
        user_id = pending.get("user_id")
        created_at = pending.get("created_at")
        if (
            not isinstance(token, str)
            or not token
            or not isinstance(user_id, int)
            or user_id <= 0
            or not isinstance(created_at, (int, float))
        ):
            return False
        return time.time() - created_at <= config.transaction_ttl_seconds


class AuthLogoutView(APIView):
    def post(self, request):
        Token.objects.filter(user=request.user).delete()
        return Response({"detail": "已退出登录"})


class MeView(APIView):
    def get(self, request):
        return Response(serializers.CurrentUserSerializer(request.user).data)


class ImportView(APIView):
    """数据导入：multipart 上传 4 张表 + 简历包。"""

    permission_classes = [HasPermissionCode]
    permission_code = "resume.import"
    parser_classes = [MultiPartParser, FormParser]

    FIELD_KEYS = ["resume_list", "jobs", "schools", "contacts", "resume_package"]

    def post(self, request):
        files = {key: request.FILES.get(key) for key in self.FIELD_KEYS}
        if not any(files.values()):
            return Response(
                {"detail": "未上传任何文件"}, status=status.HTTP_400_BAD_REQUEST
            )
        try:
            validate_import_file_headers(files)
        except Exception as exc:  # noqa: BLE001
            return Response(
                {"detail": f"导入失败: {exc}"}, status=status.HTTP_400_BAD_REQUEST
            )
        if "processing_mode" in request.data:
            return Response(
                {"detail": "简历处理内核由系统固定，不接受 processing_mode"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        mode = request.data.get("mode", "incremental")
        takes_resume = bool(files.get("resume_list") or files.get("resume_package"))
        try:
            counts = import_files(files, mode=mode)
        except Exception as exc:  # noqa: BLE001
            return Response(
                {"detail": f"导入失败: {exc}"}, status=status.HTTP_400_BAD_REQUEST
            )
        candidate_ids = counts.pop("_candidate_ids", [])
        school_ids_missing_province = counts.pop(
            "_school_ids_missing_province", []
        )
        warnings = counts.pop("_warnings", [])
        school_province_enrichment = {
            "status": (
                "ai_unavailable"
                if school_ids_missing_province
                else "not_requested"
            ),
            "school_count": len(school_ids_missing_province),
        }
        if school_ids_missing_province and ai_config.is_ai_available():
            try:
                submitted = submit_school_province_enrichment(
                    school_ids_missing_province
                )
                school_province_enrichment = {
                    "status": "queued",
                    "school_count": len(school_ids_missing_province),
                    **submitted,
                }
            except Exception as exc:  # noqa: BLE001 - 导入成功不因增强任务失败回滚
                logger.warning(
                    "School province enrichment dispatch failed school_count=%s error_type=%s",
                    len(school_ids_missing_province),
                    type(exc).__name__,
                )
                school_province_enrichment["status"] = "queue_failed"
        processing_runs = []
        if takes_resume and candidate_ids and agent_gateway.is_agent_ready():
            try:
                run = runner.create_run(
                    "resume_process",
                    scope={"candidate_ids": candidate_ids, "source": "resume_import"},
                    created_by=request.user,
                )
                processing_runs = [run]
                submit_processing_runs(processing_runs)
            except ai_service.AIServiceError:
                warnings.append("导入已完成，Kernel 版本发现失败，请稍后手动提交处理任务")
        skipped_jobs = counts.get("jobs_skipped", 0)
        detail = (
            f"导入完成，已跳过 {skipped_jobs} 条缺少工作职责的岗位"
            if skipped_jobs
            else "导入完成"
        )
        if school_province_enrichment["status"] == "queued":
            detail += (
                f"，已提交 {school_province_enrichment['school_count']} 所院校"
                "省份后台补全"
            )
        if takes_resume and candidate_ids and not processing_runs:
            detail += "，Agent 尚未就绪，简历已保留为待处理"
        return Response(
            {
                "detail": detail,
                "counts": counts,
                "warnings": warnings,
                "school_province_enrichment": school_province_enrichment,
                "agent_processing": (
                    "submitted"
                    if processing_runs
                    else "pending"
                    if takes_resume and candidate_ids
                    else "not_requested"
                ),
                "processing_runs": serializers.ProcessingRunSerializer(
                    processing_runs, many=True
                ).data,
            },
            status=(
                status.HTTP_202_ACCEPTED
                if any(run.status in ["pending", "running"] for run in processing_runs)
                else status.HTTP_200_OK
            ),
        )


class ImportTemplateView(APIView):
    """下载四类表格导入共用的标准模板。"""

    permission_classes = [HasPermissionCode]
    permission_code = "resume.import"

    def get(self, request, template_type):
        try:
            schema = get_import_table_schema(template_type)
        except ValueError as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_404_NOT_FOUND)

        workbook = build_import_template_workbook(template_type)
        output = BytesIO()
        workbook.save(output)
        response = HttpResponse(
            output.getvalue(),
            content_type=(
                "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            ),
        )
        encoded_name = quote(schema.filename)
        response["Content-Disposition"] = (
            f'attachment; filename="import-template.xlsx"; '
            f"filename*=UTF-8''{encoded_name}"
        )
        return response


class ResumeViewSet(PermissionedReadOnlyModelViewSet):
    serializer_class = serializers.ResumeListSerializer
    permission_code = "resume.view"
    permission_codes_by_action = {
        "manual_assignment_options": "resume.manual_assign",
    }

    def get_queryset(self):
        qs = m.Resume.objects.select_related("candidate", "job").order_by("-imported_at")
        p = self.request.query_params
        search = p.get("search")
        if search:
            qs = qs.filter(candidate__name__icontains=search) | qs.filter(
                candidate__phone__icontains=search
            ) | qs.filter(position_name__icontains=search)
        if p.get("status"):
            qs = qs.filter(status=p["status"])
        if p.get("imported_after"):
            qs = qs.filter(imported_at__date__gte=p["imported_after"])
        if p.get("imported_before"):
            qs = qs.filter(imported_at__date__lte=p["imported_before"])
        return qs.distinct()

    @action(detail=False, methods=["get"], url_path="result-report")
    def result_report(self, request):
        imported_after = request.query_params.get("imported_after")
        imported_before = request.query_params.get("imported_before")
        primary_department_id = request.query_params.get("primary_department_id")
        department_id = request.query_params.get("department_id")
        if not imported_after or not imported_before:
            return Response(
                {"detail": "imported_after 和 imported_before 均为必填日期"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        try:
            if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", imported_after) or not re.fullmatch(
                r"\d{4}-\d{2}-\d{2}", imported_before
            ):
                raise ValueError
            start_date = date.fromisoformat(imported_after)
            end_date = date.fromisoformat(imported_before)
        except (TypeError, ValueError):
            return Response(
                {"detail": "日期格式必须为 YYYY-MM-DD"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        if start_date > end_date:
            return Response(
                {"detail": "开始日期不能晚于结束日期"},
                status=status.HTTP_400_BAD_REQUEST,
            )

        department_filters = {
            "primary_department_id": primary_department_id,
            "department_id": department_id,
        }
        for field_name, raw_value in department_filters.items():
            if raw_value in (None, ""):
                department_filters[field_name] = None
                continue
            try:
                parsed_value = int(raw_value)
                if parsed_value <= 0:
                    raise ValueError
            except (TypeError, ValueError):
                return Response(
                    {"detail": f"{field_name} 必须是正整数"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
            department_filters[field_name] = parsed_value
        primary_department_id = department_filters["primary_department_id"]
        department_id = department_filters["department_id"]

        current_timezone = timezone.get_current_timezone()
        start_at = timezone.make_aware(
            datetime.combine(start_date, datetime_time.min), current_timezone
        )
        end_at = timezone.make_aware(
            datetime.combine(end_date + timedelta(days=1), datetime_time.min),
            current_timezone,
        )
        attempts = (
            m.AssignmentAttempt.objects.exclude(
                status=m.AssignmentAttempt.STATUS_CANCELLED
            )
            .select_related(
                "initial_department__parent",
                "current_department__parent__parent",
            )
            .prefetch_related("handling_events")
            .order_by("attempt_no", "id")
        )
        resume_queryset = (
            m.Resume.objects.filter(
                imported_at__gte=start_at,
                imported_at__lt=end_at,
            )
            .select_related(
                "candidate",
                "candidate__first_degree_tag",
                "candidate__highest_degree_tag",
                "candidate__workflow__current_resume",
            )
            .prefetch_related(
                Prefetch(
                    "candidate__workflow__attempts",
                    queryset=attempts,
                )
            )
            .order_by("imported_at", "id")
        )
        resumes = list(resume_queryset)
        if primary_department_id or department_id:
            def matches_department_filters(resume):
                attempt = current_effective_attempt(resume)
                if not attempt:
                    return False
                hierarchy = resolve_department_hierarchy(attempt.current_department)
                if primary_department_id and (
                    not hierarchy.primary
                    or hierarchy.primary.id != primary_department_id
                ):
                    return False
                if department_id and (
                    not hierarchy.secondary
                    or hierarchy.secondary.id != department_id
                ):
                    return False
                return True

            resumes = [resume for resume in resumes if matches_department_filters(resume)]
        content = build_result_report(resumes)
        filename = f"简历结果报表_{start_date:%Y%m%d}_{end_date:%Y%m%d}.xlsx"
        response = HttpResponse(
            content,
            content_type=(
                "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            ),
        )
        response["Content-Disposition"] = (
            "attachment; filename*=UTF-8''" + quote(filename)
        )
        return response

    @action(detail=True, methods=["get"], url_path="preview")
    def preview(self, request, pk=None):
        resume = self.get_object()
        return resume_preview_response(resume)

    @action(detail=False, methods=["get"], url_path="manual-assignment-options")
    def manual_assignment_options(self, request):
        return Response({"results": assignment_department_options()})

    @action(detail=True, methods=["post"], url_path="manual-assign")
    def manual_assign(self, request, pk=None):
        if not has_permission_code(request.user, "resume.manual_assign"):
            return Response({"detail": "无手动分配权限"}, status=status.HTTP_403_FORBIDDEN)
        invalid_fields = _reject_unknown_body_fields(
            request, {"target_department_id", "manual_reason"}
        )
        if invalid_fields:
            return invalid_fields
        resume = self.get_object()
        target_department_id = request.data.get("target_department_id")
        if not target_department_id:
            return Response(
                {"detail": "target_department_id 为必填项"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        target_department = m.Department.objects.select_related("parent__parent").filter(
            pk=target_department_id, level__in=[2, 3]
        ).first()
        if not target_department:
            return Response(
                {"detail": "目标部门不存在或层级无效"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        try:
            attempt = allocate_service.manual_assign(
                resume,
                target_department,
                user=request.user,
                manual_reason=request.data.get("manual_reason", ""),
            )
        except ValueError as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
        return Response(
            serializers.AssignmentAttemptSerializer(
                attempt, context={"request": request}
            ).data
        )


class CandidateViewSet(PermissionedModelViewSet):
    serializer_class = serializers.CandidateSerializer
    permission_codes_by_action = {
        "list": ["resume.view", "attempt.view_department"],
        "retrieve": ["resume.view", "attempt.view_department"],
        "create": "resume.import",
        "update": "resume.import",
        "partial_update": "resume.import",
        "destroy": "resume.import",
        "bulk_delete": "resume.import",
        "export_resumes": ["resume.view", "attempt.export"],
        "export_fields": ["resume.view", "attempt.export"],
        "filter_options": ["resume.view", "attempt.view_department"],
        "bulk_dispatch": "attempt.dispatch",
        "bulk_transfer": "attempt.transfer_department",
    }

    def _base_queryset(self):
        attempts = m.AssignmentAttempt.objects.select_related(
            "workflow__candidate",
            "resume__candidate",
            "initial_department__parent",
            "current_department__parent__parent",
            "matched_rule",
            "agent_decision",
        ).prefetch_related(
            "handling_events__from_department",
            "handling_events__to_department",
        ).order_by("attempt_no")
        return (
            m.Candidate.objects.prefetch_related(
                "resumes",
                "school_tags",
                "resumes__job__department__parent",
                "resumes__job__majors",
                Prefetch("workflow__attempts", queryset=attempts),
                Prefetch(
                    "processing_scope_items",
                    queryset=m.ProcessingRunScopeItem.objects.order_by("created_at", "id"),
                ),
            )
            .select_related(
                "first_degree_tag",
                "highest_degree_tag",
                "workflow__current_resume",
                "workflow__current_resume__job",
                "workflow__current_resume__job__department",
                "workflow__current_resume__job__department__parent",
            )
            .order_by("-updated_at")
        )

    def get_queryset(self):
        qs = self._scope_queryset(self._base_queryset())
        permissions = user_permission_codes(self.request.user)
        current_resume_resolver = None
        current_attempt_resolver = None
        if "resume.view" not in permissions:
            forbidden = sorted(
                key
                for key in self.request.query_params.keys()
                if key in DEPARTMENT_SCOPE_FORBIDDEN_FILTER_FIELDS
                or key.startswith("analytics_")
            )
            if forbidden:
                from rest_framework.exceptions import ValidationError

                raise ValidationError(
                    {"detail": f"部门接口人不可使用筛选字段：{','.join(forbidden)}"}
                )
            current_attempt_resolver, current_resume_resolver = (
                self._department_scope_resolvers(permissions)
            )

        try:
            if "resume.view" in permissions:
                qs = analytics_scope.apply_candidate_drilldown(
                    qs,
                    self.request.query_params,
                )
            qs = system_status.apply_candidate_filters(
                qs,
                self.request.query_params,
                current_resume_resolver=current_resume_resolver,
                current_attempt_resolver=current_attempt_resolver,
            )
        except ValueError as exc:
            from rest_framework.exceptions import ValidationError

            raise ValidationError({"detail": str(exc)}) from exc
        return self._apply_attempt_filters(qs, self.request.query_params)

    def _department_scope_resolvers(self, permissions):
        cache = {}

        def current_attempt_resolver(candidate):
            if candidate.id not in cache:
                cache[candidate.id] = serializers.visible_candidate_attempt(
                    candidate,
                    self.request.user,
                    permissions=permissions,
                )
            return cache[candidate.id]

        def current_resume_resolver(candidate):
            attempt = current_attempt_resolver(candidate)
            return attempt.resume if attempt else None

        return current_attempt_resolver, current_resume_resolver

    def _scope_queryset(self, qs):
        permissions = user_permission_codes(self.request.user)
        if "resume.view" in permissions:
            return qs
        if "attempt.view_department" not in permissions:
            return qs.none()
        return qs.filter(
            serializers.department_attempt_scope_q(
                self.request.user, prefix="workflow__attempts__"
            )
        ).distinct()

    def _apply_attempt_filters(self, qs, params):
        def values(key):
            if hasattr(params, "getlist"):
                raw_values = params.getlist(key)
            else:
                raw = params.get(key)
                raw_values = raw if isinstance(raw, list) else [raw]
            return {
                item.strip()
                for raw_value in raw_values
                if raw_value is not None
                for item in str(raw_value).split(",")
                if item.strip()
            }

        source_values = values("allocation_source")
        status_values = values("attempt_status")
        if not any([source_values, status_values]):
            return qs
        ids = []
        can_view_resume = "resume.view" in user_permission_codes(self.request.user)
        for candidate in qs:
            attempt = serializers.visible_candidate_attempt(candidate, self.request.user)
            allocation_source = (
                attempt.source
                if attempt
                else candidate_summary.allocation_source(candidate)
                if can_view_resume
                else ""
            )
            if source_values and allocation_source not in source_values:
                continue
            if status_values and (not attempt or attempt.status not in status_values):
                continue
            ids.append(candidate.id)
        return qs.filter(id__in=ids)

    @action(detail=False, methods=["get"], url_path="filter-options")
    def filter_options(self, request):
        """返回简历库表头选择器的当前可选值。"""
        permissions = user_permission_codes(request.user)
        kwargs = {}
        if "resume.view" not in permissions:
            attempt_resolver, resume_resolver = self._department_scope_resolvers(
                permissions
            )
            kwargs = {
                "current_resume_resolver": resume_resolver,
                "current_attempt_resolver": attempt_resolver,
            }
        return Response(
            system_status.candidate_filter_options(
                self._scope_queryset(self._base_queryset()),
                **kwargs,
            )
        )

    @action(detail=False, methods=["get"], url_path="export")
    def export_resumes(self, request):
        try:
            field_keys = parse_export_fields(request.query_params)
            include_resume_files = parse_include_resume_files(request.query_params)
        except (ExportFieldError, ExportOptionError) as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)

        permissions = user_permission_codes(request.user)
        ids = request.query_params.get("ids")
        qs = self.get_queryset()
        if ids:
            id_list = [int(item) for item in ids.split(",") if item.strip().isdigit()]
            qs = qs.filter(id__in=id_list)
        records = []
        for candidate in qs:
            if "resume.view" not in permissions:
                attempt = serializers.visible_candidate_attempt(
                    candidate, request.user, permissions=permissions
                )
                if not attempt:
                    continue
                records.append(
                    CandidateExportRecord(
                        candidate=candidate,
                        current_resume=attempt.resume,
                        attempt=attempt,
                        file_resumes=[attempt.resume],
                    )
                )
                continue

            current_resume = candidate_summary.current_resume(candidate)
            workflow = candidate_summary.workflow_or_none(candidate)
            attempt = candidate_summary.latest_effective_attempt(
                workflow,
                resume_id=current_resume.id if current_resume else None,
            )
            records.append(
                CandidateExportRecord(
                    candidate=candidate,
                    current_resume=current_resume,
                    attempt=attempt,
                    file_resumes=list(candidate.resumes.all()),
                )
            )
        return candidate_export_response(
            records,
            field_keys,
            include_resume_files=include_resume_files,
        )

    @action(detail=False, methods=["get"], url_path="export-fields")
    def export_fields(self, request):
        return Response(export_fields_payload())

    @staticmethod
    def _delete_candidate(candidate):
        """删除一个无受保护历史的候选人；返回失败原因或空字符串。"""

        with transaction.atomic():
            candidate = m.Candidate.objects.select_for_update().get(pk=candidate.pk)
            workflow = (
                m.CandidateWorkflow.objects.select_for_update()
                .filter(candidate=candidate)
                .first()
            )

            protected_history = []
            if m.ProcessingRunScopeItem.objects.filter(
                candidate=candidate,
                status__in=["pending", "queued", "processing", "waiting_conflict"],
            ).exists():
                protected_history.append("正在执行的 AI 处理任务")
            if workflow and m.AssignmentAttempt.objects.filter(workflow=workflow).exists():
                protected_history.append("分配尝试或反馈")
            if m.AgentDispatchDecision.objects.filter(
                Q(workflow__candidate=candidate) | Q(resume__candidate=candidate)
            ).exists():
                protected_history.append("AI 决策")
            if m.AssignmentHandlingEvent.objects.filter(
                attempt__workflow__candidate=candidate
            ).exists():
                protected_history.append("处理日志")

            if protected_history:
                return (
                    "无法删除候选人：已存在受保护历史（"
                    f"{'、'.join(protected_history)}）"
                )

            try:
                candidate.delete()
            except ProtectedError:
                return "无法删除候选人：已存在受保护的关联记录"
        return ""

    def destroy(self, request, *args, **kwargs):
        """只允许清理尚未产生流程历史的候选人。"""

        detail = self._delete_candidate(self.get_object())
        if detail:
            return Response(
                {"detail": detail},
                status=status.HTTP_409_CONFLICT,
            )
        return Response(status=status.HTTP_204_NO_CONTENT)

    @action(detail=False, methods=["post"], url_path="bulk-delete")
    def bulk_delete(self, request):
        """按冻结候选人 ID 批量删除，逐条返回成功和失败结果。"""

        invalid_fields = _reject_unknown_body_fields(request, {"candidate_ids"})
        if invalid_fields:
            return invalid_fields
        candidate_ids = request.data.get("candidate_ids")
        valid = (
            isinstance(candidate_ids, list)
            and bool(candidate_ids)
            and len(candidate_ids) <= 500
            and all(
                isinstance(item, int) and not isinstance(item, bool) and item > 0
                for item in candidate_ids
            )
        )
        if not valid:
            return Response(
                {"detail": "candidate_ids 必须是最多 500 项的非空正整数数组"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        frozen_ids = list(dict.fromkeys(candidate_ids))
        visible = {
            candidate.id: candidate
            for candidate in self._scope_queryset(self._base_queryset()).filter(
                id__in=frozen_ids
            )
        }
        results = []
        deleted = failed = 0
        for candidate_id in frozen_ids:
            candidate = visible.get(candidate_id)
            if not candidate:
                failed += 1
                results.append(
                    {
                        "candidate_id": candidate_id,
                        "status": "failed",
                        "detail": "候选人不存在或无权操作",
                    }
                )
                continue
            detail = self._delete_candidate(candidate)
            if detail:
                failed += 1
                results.append(
                    {
                        "candidate_id": candidate_id,
                        "status": "failed",
                        "detail": detail,
                    }
                )
                continue
            deleted += 1
            results.append({"candidate_id": candidate_id, "status": "deleted"})
        return Response(
            {
                "total": len(frozen_ids),
                "deleted": deleted,
                "failed": failed,
                "results": results,
            }
        )

    @action(detail=False, methods=["post"], url_path="bulk-dispatch")
    def bulk_dispatch(self, request):
        has_candidate_ids = "candidate_ids" in request.data
        has_candidate_filters = "candidate_filters" in request.data
        candidate_ids = request.data.get("candidate_ids")
        candidate_filters = request.data.get("candidate_filters")
        if has_candidate_ids and has_candidate_filters:
            return Response(
                {"detail": "candidate_ids 与 candidate_filters 只能提供一个"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        if not has_candidate_ids and not has_candidate_filters:
            return Response(
                {"detail": "必须提供 candidate_ids 或 candidate_filters"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        qs = self._scope_queryset(self._base_queryset())
        if has_candidate_ids:
            valid_candidate_ids = (
                isinstance(candidate_ids, list)
                and bool(candidate_ids)
                and all(
                    isinstance(item, int)
                    and not isinstance(item, bool)
                    and item > 0
                    for item in candidate_ids
                )
            )
            if not valid_candidate_ids:
                return Response(
                    {"detail": "candidate_ids 必须是非空正整数数组"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
            candidate_ids = list(dict.fromkeys(candidate_ids))
            qs = qs.filter(id__in=candidate_ids)
        else:
            if not isinstance(candidate_filters, dict) or not candidate_filters:
                return Response(
                    {"detail": "candidate_filters 必须是非空对象"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
            unknown_fields = sorted(set(candidate_filters) - BULK_DISPATCH_FILTER_FIELDS)
            if unknown_fields:
                return Response(
                    {"detail": f"批量下发不支持筛选字段：{','.join(unknown_fields)}"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
            if not any(value not in (None, "", [], {}) for value in candidate_filters.values()):
                return Response(
                    {"detail": "candidate_filters 至少包含一个非空筛选值"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
            try:
                qs = system_status.apply_candidate_filters(qs, candidate_filters)
            except ValueError as exc:
                return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
            qs = self._apply_attempt_filters(qs, candidate_filters)

        total = qs.count()
        eligible = 0
        dispatched = 0
        errors = []
        for candidate in qs:
            workflow = candidate_summary.workflow_or_none(candidate)
            resume = candidate_summary.current_resume(candidate)
            attempt = candidate_summary.latest_effective_attempt(
                workflow, resume_id=resume.id if resume else None
            )
            if not attempt or attempt.status != m.AssignmentAttempt.STATUS_PENDING_DISPATCH:
                continue
            eligible += 1
            try:
                allocate_service.dispatch_attempt(attempt, user=request.user)
                dispatched += 1
            except ValueError as exc:
                errors.append({"candidate_id": candidate.id, "detail": str(exc)})
        failed = len(errors)
        skipped = total - eligible
        return Response(
            {
                "detail": f"已下发 {dispatched} 条，跳过 {skipped} 条，失败 {failed} 条",
                "total": total,
                "eligible": eligible,
                "dispatched": dispatched,
                "skipped": skipped,
                "failed": failed,
                "errors": errors,
            }
        )

    @action(detail=False, methods=["post"], url_path="bulk-transfer")
    def bulk_transfer(self, request):
        if not _can_transfer_departments(request.user):
            return Response(
                {"detail": "当前接口人没有部门转派权限"},
                status=status.HTTP_403_FORBIDDEN,
            )
        invalid_fields = _reject_unknown_body_fields(
            request, {"candidate_ids", "target_department_id", "note"}
        )
        if invalid_fields:
            return invalid_fields
        candidate_ids = request.data.get("candidate_ids")
        if not (
            isinstance(candidate_ids, list)
            and candidate_ids
            and all(
                isinstance(item, int) and not isinstance(item, bool) and item > 0
                for item in candidate_ids
            )
        ):
            return Response(
                {"detail": "candidate_ids 必须是非空正整数数组"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        frozen_candidate_ids = list(candidate_ids)
        target_department_id = request.data.get("target_department_id")
        target_department = m.Department.objects.select_related("parent__parent").filter(
            pk=target_department_id, level=2
        ).first()
        if not target_department:
            return Response(
                {"detail": "批量转派目标必须是有效二级部门"},
                status=status.HTTP_400_BAD_REQUEST,
            )

        batch_operation_id = uuid.uuid4()
        candidates = {
            item.id: item
            for item in self._scope_queryset(self._base_queryset()).filter(
                id__in=candidate_ids
            )
        }
        results = []
        errors = []
        transferred = skipped = failed = 0
        permissions = user_permission_codes(request.user)
        seen_candidate_ids = set()
        for candidate_id in frozen_candidate_ids:
            if candidate_id in seen_candidate_ids:
                skipped += 1
                results.append(
                    {
                        "candidate_id": candidate_id,
                        "attempt_id": None,
                        "status": "skipped",
                        "detail": "同一批次候选人 ID 重复",
                    }
                )
                continue
            seen_candidate_ids.add(candidate_id)
            candidate = candidates.get(candidate_id)
            if not candidate:
                failed += 1
                errors.append(
                    {"candidate_id": candidate_id, "detail": "候选人不存在或无权操作"}
                )
                continue
            attempt = serializers.visible_candidate_attempt(
                candidate, request.user, permissions=permissions
            )
            if not attempt or attempt.status != m.AssignmentAttempt.STATUS_DISPATCHED:
                skipped += 1
                results.append(
                    {
                        "candidate_id": candidate_id,
                        "attempt_id": attempt.id if attempt else None,
                        "status": "skipped",
                        "detail": "当前简历不可转派",
                    }
                )
                continue
            try:
                transferred_attempt = allocate_service.transfer_attempt(
                    attempt,
                    target_department,
                    user=request.user,
                    note=request.data.get("note", ""),
                    batch_operation_id=batch_operation_id,
                )
            except ValueError as exc:
                failed += 1
                errors.append({"candidate_id": candidate_id, "detail": str(exc)})
                continue
            transferred += 1
            results.append(
                {
                    "candidate_id": candidate_id,
                    "attempt_id": transferred_attempt.id,
                    "status": "transferred",
                }
            )
        return Response(
            {
                "batch_operation_id": str(batch_operation_id),
                "total": len(frozen_candidate_ids),
                "transferred": transferred,
                "skipped": skipped,
                "failed": failed,
                "results": results,
                "errors": errors,
            }
        )


class JobViewSet(PermissionedModelViewSet):
    serializer_class = serializers.JobSerializer
    permission_codes_by_action = {
        "list": "job.view",
        "retrieve": "job.view",
        "create": "job.manage",
        "update": "job.manage",
        "partial_update": "job.manage",
        "destroy": "job.manage",
        "filter_options": "job.view",
        "export_jobs": "job.view",
    }

    def get_queryset(self):
        qs = (
            m.Job.objects.select_related(
                "department",
                "department__parent",
                "department__parent__parent",
            )
            .prefetch_related("majors")
            .all()
            .order_by("id")
        )
        p = self.request.query_params
        is_active = bool_query_value(p.get("is_active"))
        if is_active is None:
            qs = qs.filter(is_active=True)
        else:
            qs = qs.filter(is_active=is_active)
        entity_values = _query_list_values(p, "entity_in")
        if entity_values:
            qs = qs.filter(entity__in=entity_values)
        elif p.get("entity"):
            qs = qs.filter(entity__icontains=p["entity"])
        public_name_values = _query_list_values(p, "public_name_in")
        if public_name_values:
            qs = qs.filter(public_name__in=public_name_values)
        elif p.get("public_name"):
            qs = _filter_queryset_text_with_pinyin(qs, "public_name", p["public_name"])
        position_name_values = _query_list_values(p, "position_name_in")
        if position_name_values:
            qs = qs.filter(position_name__in=position_name_values)
        elif p.get("position_name"):
            qs = _filter_queryset_text_with_pinyin(qs, "position_name", p["position_name"])
        category_values = _query_list_values(p, "category_in")
        if category_values:
            qs = qs.filter(category__in=category_values)
        elif p.get("category"):
            qs = qs.filter(category__icontains=p["category"])
        job_family_values = _query_list_values(p, "job_family_in")
        if job_family_values:
            qs = qs.filter(job_family__in=job_family_values)
        elif p.get("job_family"):
            qs = qs.filter(job_family__icontains=p["job_family"])
        primary_department_values = _query_list_values(
            p, "primary_department_name_in"
        )
        if primary_department_values:
            qs = qs.filter(
                department__level=2,
                department__parent__level=1,
                department__parent__name__in=primary_department_values,
            )
        elif p.get("primary_department_name"):
            value = p["primary_department_name"]
            qs = qs.filter(
                department__level=2,
                department__parent__level=1,
                department__parent__name__icontains=value,
            )
        secondary_department_values = _query_list_values(
            p, "secondary_department_name_in"
        ) or _query_list_values(p, "department_name_in")
        if secondary_department_values:
            qs = qs.filter(
                department__level=2,
                department__name__in=secondary_department_values,
            )
        elif p.get("secondary_department_name") or p.get("department_name"):
            value = p.get("secondary_department_name") or p["department_name"]
            qs = qs.filter(
                department__level=2,
                department__name__icontains=value,
            )
        location_values = _query_list_values(p, "location_in")
        if location_values:
            qs = qs.filter(location__in=location_values)
        elif p.get("location"):
            qs = qs.filter(location__icontains=p["location"])
        education_values = _query_list_values(p, "education_in")
        if education_values:
            qs = qs.filter(education__in=education_values)
        elif p.get("education"):
            qs = qs.filter(education__icontains=p["education"])
        if p.get("responsibilities"):
            qs = qs.filter(responsibilities__icontains=p["responsibilities"])
        if p.get("headcount"):
            qs = qs.filter(headcount=p["headcount"])
        is_public = bool_query_value(p.get("is_public"))
        if is_public is not None:
            qs = qs.filter(is_public=is_public)
        return qs

    @action(detail=False, methods=["get"], url_path="filter-options")
    def filter_options(self, request):
        """返回岗位要求表头选择器候选值及拼音搜索文本。"""
        jobs = list(
            m.Job.objects.filter(is_active=True).select_related(
                "department",
                "department__parent",
                "department__parent__parent",
            )
        )

        def options(values):
            return [
                _filter_option(value)
                for value in sorted(
                    {
                        str(item).strip()
                        for item in values
                        if item is not None and str(item).strip()
                    }
                )
            ]

        def department_names(level):
            for job in jobs:
                hierarchy = resolve_department_hierarchy(job.department)
                department = getattr(hierarchy, level)
                yield department.name if department else ""

        return Response(
            {
                "entity": options(job.entity for job in jobs),
                "public_name": options(job.public_name for job in jobs),
                "position_name": options(job.position_name for job in jobs),
                "category": options(job.category for job in jobs),
                "job_family": options(job.job_family for job in jobs),
                "primary_department_name": options(department_names("primary")),
                "secondary_department_name": options(
                    department_names("secondary")
                ),
                "department_name": options(department_names("secondary")),
                "location": options(job.location for job in jobs),
                "education": options(job.education for job in jobs),
            }
        )

    @action(detail=False, methods=["get"], url_path="export")
    def export_jobs(self, request):
        jobs = list(self.filter_queryset(self.get_queryset()))
        if not jobs:
            return Response(
                {"detail": "当前筛选条件下没有可下载的启用岗位"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        content = build_job_export_workbook(jobs)
        response = HttpResponse(
            content,
            content_type=(
                "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            ),
        )
        response["Content-Disposition"] = (
            "attachment; filename=\"jobs.xlsx\"; filename*=UTF-8''"
            + quote("职位清单.xlsx")
        )
        response["X-Export-Count"] = str(len(jobs))
        return response

    def destroy(self, request, *args, **kwargs):
        job = self.get_object()
        job.is_active = False
        job.save(update_fields=["is_active"])
        return Response(status=status.HTTP_204_NO_CONTENT)


class SchoolViewSet(PermissionedModelViewSet):
    serializer_class = serializers.SchoolSerializer
    permission_codes_by_action = {
        "list": "school.view",
        "retrieve": "school.view",
        "create": "school.manage",
        "update": "school.manage",
        "partial_update": "school.manage",
        "destroy": "school.manage",
        "filter_options": "school.view",
    }

    @staticmethod
    def _queue_province_enrichment(school):
        if school.province.strip() or not ai_config.is_ai_available():
            return

        def submit():
            try:
                submit_school_province_enrichment([school.id])
            except Exception as exc:  # noqa: BLE001 - 主数据保存不受增强任务影响
                logger.warning(
                    "School province enrichment dispatch failed school_id=%s error_type=%s",
                    school.id,
                    type(exc).__name__,
                )

        transaction.on_commit(submit)

    def perform_create(self, serializer):
        school = serializer.save()
        self._queue_province_enrichment(school)

    def perform_update(self, serializer):
        school = serializer.save()
        self._queue_province_enrichment(school)

    def get_queryset(self):
        qs = m.School.objects.select_related("school_tag").order_by("name")
        p = self.request.query_params
        if p.get("name"):
            qs = _filter_indexed_name(qs, "", p["name"])
        platform_values = _query_list_values(p, "platform_in")
        if platform_values:
            qs = qs.filter(platform__in=platform_values)
        elif p.get("platform"):
            qs = qs.filter(
                Q(platform__icontains=p["platform"])
                | Q(school_tag__name__icontains=p["platform"])
                | Q(school_tag__code__icontains=p["platform"])
            )
        if p.get("province"):
            qs = qs.filter(province__icontains=p["province"])
        return qs

    @action(detail=False, methods=["get"], url_path="filter-options")
    def filter_options(self, request):
        values = (
            m.School.objects.exclude(platform="")
            .values_list("platform", flat=True)
            .distinct()
        )
        return Response(
            {
                "platform": [
                    _filter_option(value)
                    for value in sorted({str(value).strip() for value in values})
                ],
                "school_tag": [
                    _filter_option(tag.name, option_value=tag.id)
                    for tag in m.SchoolTag.objects.filter(is_active=True).order_by(
                        "code", "id"
                    )
                ],
            }
        )


class SchoolTagViewSet(PermissionedModelViewSet):
    serializer_class = serializers.SchoolTagSerializer
    permission_code = "settings.manage_config"

    def get_queryset(self):
        qs = m.SchoolTag.objects.all().order_by("code", "id")
        p = self.request.query_params
        if p.get("code"):
            qs = qs.filter(code__icontains=p["code"])
        if p.get("name"):
            qs = qs.filter(name__icontains=p["name"])
        is_default = bool_query_value(p.get("is_default"))
        if is_default is not None:
            qs = qs.filter(is_default=is_default)
        is_active = bool_query_value(p.get("is_active"))
        if is_active is not None:
            qs = qs.filter(is_active=is_active)
        return qs


class MajorCategoryViewSet(PermissionedModelViewSet):
    serializer_class = serializers.MajorCategorySerializer
    permission_code = "settings.manage_config"

    def get_queryset(self):
        qs = m.MajorCategory.objects.annotate(alias_count=Count("aliases")).order_by(
            "sort_order", "code", "id"
        )
        p = self.request.query_params
        if p.get("code"):
            qs = qs.filter(code__icontains=p["code"])
        if p.get("name"):
            qs = qs.filter(name__icontains=p["name"])
        if p.get("description"):
            qs = qs.filter(description__icontains=p["description"])
        is_active = bool_query_value(p.get("is_active"))
        if is_active is not None:
            qs = qs.filter(is_active=is_active)
        return qs

    def destroy(self, request, *args, **kwargs):
        category = self.get_object()
        try:
            self.perform_destroy(category)
        except ProtectedError:
            return Response(
                {"detail": "该专业大类仍有关联别名，需先删除或迁移别名后再删除。"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        return Response(status=status.HTTP_204_NO_CONTENT)


class MajorAliasViewSet(PermissionedModelViewSet):
    serializer_class = serializers.MajorAliasSerializer
    permission_code = "settings.manage_config"

    def get_queryset(self):
        qs = m.MajorAlias.objects.select_related("category").order_by(
            "category__sort_order", "category__code", "name", "id"
        )
        p = self.request.query_params
        if p.get("category"):
            qs = qs.filter(category_id=p["category"])
        if p.get("name"):
            qs = qs.filter(name__icontains=p["name"])
        if p.get("normalized_name"):
            qs = qs.filter(normalized_name__icontains=p["normalized_name"])
        if p.get("source"):
            qs = qs.filter(source=p["source"])
        if p.get("match_type"):
            qs = qs.filter(match_type=p["match_type"])
        if p.get("note"):
            qs = qs.filter(note__icontains=p["note"])
        is_active = bool_query_value(p.get("is_active"))
        if is_active is not None:
            qs = qs.filter(is_active=is_active)
        return qs


class DepartmentViewSet(PermissionedModelViewSet):
    serializer_class = serializers.DepartmentSerializer
    permission_codes_by_action = {
        "list": "department.view",
        "retrieve": "department.view",
        "create": "department.manage",
        "update": "department.manage",
        "partial_update": "department.manage",
        "destroy": "department.manage",
    }

    def get_queryset(self):
        qs = m.Department.objects.all().order_by("id")
        p = self.request.query_params
        if p.get("name"):
            qs = qs.filter(name__icontains=p["name"])
        return qs

    def destroy(self, request, *args, **kwargs):
        department = self.get_object()
        if department.children.exists():
            return Response(
                {"detail": "存在下级部门不可删除"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        try:
            department.delete()
        except ProtectedError:
            return Response(
                {"detail": "部门已有业务引用不可删除"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        return Response(status=status.HTTP_204_NO_CONTENT)


class ContactViewSet(PermissionedModelViewSet):
    serializer_class = serializers.ContactSerializer
    permission_codes_by_action = {
        "list": "department.view",
        "retrieve": "department.view",
        "create": "department.manage",
        "update": "department.manage",
        "partial_update": "department.manage",
        "destroy": "department.manage",
        "filter_options": "department.view",
    }

    def get_queryset(self):
        qs = m.Contact.objects.select_related("department").order_by("id")
        p = self.request.query_params
        is_active = bool_query_value(p.get("is_active"))
        if is_active is None and self.action == "list":
            qs = qs.filter(is_active=True)
        elif is_active is not None:
            qs = qs.filter(is_active=is_active)
        if p.get("name"):
            qs = _filter_indexed_name(qs, "", p["name"])
        if p.get("employee_no"):
            qs = qs.filter(employee_no__icontains=p["employee_no"])
        if p.get("email"):
            qs = qs.filter(email__icontains=p["email"])
        department_values = _query_list_values(p, "department_in")
        if department_values:
            qs = qs.filter(department_id__in=department_values)
        elif p.get("department_name"):
            qs = qs.filter(department__name__icontains=p["department_name"])
        if p.get("department_level"):
            qs = qs.filter(department__level=p["department_level"])
        if p.get("contact_level"):
            qs = qs.filter(contact_level=p["contact_level"])
        if p.get("department"):
            qs = qs.filter(department_id=p["department"])
        if p.get("parent_department"):
            qs = qs.filter(department__parent_id=p["parent_department"])
        can_delegate = bool_query_value(p.get("can_delegate"))
        if can_delegate is not None:
            qs = qs.filter(can_delegate=can_delegate)
        if p.get("entity"):
            qs = qs.filter(department__entity__icontains=p["entity"])
        return qs

    @action(detail=False, methods=["get"], url_path="filter-options")
    def filter_options(self, request):
        departments = (
            m.Department.objects.filter(contacts__isnull=False)
            .order_by("name", "id")
            .distinct()
        )
        return Response(
            {
                "department": [
                    _filter_option(department.name, option_value=department.id)
                    for department in departments
                ]
            }
        )

    def destroy(self, request, *args, **kwargs):
        contact = self.get_object()
        delete_contact_and_bound_users(contact)
        return Response(status=status.HTTP_204_NO_CONTENT)


class SchoolTagRuleViewSet(PermissionedModelViewSet):
    serializer_class = serializers.SchoolTagRuleSerializer
    permission_code = "settings.manage_config"

    def get_queryset(self):
        qs = m.SchoolTagRule.objects.prefetch_related(
            "tag_links__school_tag", "education_links"
        ).order_by("priority", "id")
        p = self.request.query_params
        if p.get("name"):
            qs = qs.filter(name__icontains=p["name"])
        if p.get("priority"):
            qs = qs.filter(priority=p["priority"])
        if p.get("is_active") in ["true", "false"]:
            qs = qs.filter(is_active=p["is_active"] == "true")
        return qs


class CandidateWorkflowViewSet(PermissionedReadOnlyModelViewSet):
    serializer_class = serializers.CandidateWorkflowSerializer
    permission_code = "attempt.view_all"

    def get_queryset(self):
        qs = m.CandidateWorkflow.objects.select_related(
            "candidate", "current_resume", "passed_attempt__resume"
        ).order_by("-updated_at")
        p = self.request.query_params
        if p.get("status"):
            qs = qs.filter(status=p["status"])
        if p.get("candidate_name"):
            qs = qs.filter(candidate__name__icontains=p["candidate_name"])
        if p.get("phone"):
            qs = qs.filter(candidate__phone__icontains=p["phone"])
        if p.get("current_rank"):
            qs = qs.filter(current_rank=p["current_rank"])
        if p.get("current_apply_id"):
            qs = qs.filter(current_resume__apply_id__icontains=p["current_apply_id"])
        if p.get("dispatch_strategy"):
            qs = qs.filter(dispatch_strategy=p["dispatch_strategy"])
        if p.get("search"):
            qs = qs.filter(
                Q(candidate__name__icontains=p["search"])
                | Q(candidate__phone__icontains=p["search"])
            )
        if p.get("current_position_name"):
            qs = qs.filter(current_resume__position_name__icontains=p["current_position_name"])
        if p.get("archive_reason"):
            qs = qs.filter(
                Q(archive_reason__icontains=p["archive_reason"])
                | Q(archive_detail__icontains=p["archive_reason"])
            )
        return qs.distinct()


class AssignmentAttemptViewSet(PermissionedReadOnlyModelViewSet):
    serializer_class = serializers.AssignmentAttemptSerializer
    permission_codes_by_action = {
        "list": ["attempt.view_all", "attempt.view_department"],
        "retrieve": ["attempt.view_all", "attempt.view_department"],
        "dispatch_welink": "attempt.dispatch",
        "bulk_dispatch": "attempt.dispatch",
        "transfer": "attempt.transfer_department",
        "transfer_options": "attempt.transfer_department",
        "confirm_review": "attempt.dispatch",
        "cancel_attempt": "attempt.dispatch",
        "cancel_review": "attempt.dispatch",
        "transfer_to_manual": "resume.manual_assign",
        "feedback": "attempt.feedback",
        "feedback_reasons": "attempt.feedback",
        "handling_events": ["attempt.view_all", "attempt.view_department"],
        "export_resumes": "attempt.export",
        "resume_preview": "attempt.export",
    }

    def get_queryset(self):
        qs = m.AssignmentAttempt.objects.select_related(
            "workflow__candidate",
            "workflow__candidate__first_degree_tag",
            "workflow__candidate__highest_degree_tag",
            "resume__candidate",
            "resume__job__department__parent",
            "initial_department__parent",
            "current_department__parent__parent",
            "matched_rule",
            "agent_decision",
        ).prefetch_related(
            "handling_events__from_department",
            "handling_events__to_department",
            "resume__job__majors",
            "workflow__candidate__resumes__job__department__parent",
            "workflow__candidate__resumes__job__majors",
            "workflow__candidate__processing_scope_items",
        ).order_by("-created_at")
        permissions = user_permission_codes(self.request.user)
        if "attempt.view_all" not in permissions:
            if "attempt.view_department" not in permissions:
                return qs.none()
            qs = qs.filter(serializers.department_attempt_scope_q(self.request.user))
        p = self.request.query_params
        legacy_filters = sorted(
            field
            for field in [
                "department",
                "primary_department_id",
                "contact",
                "sub_contact",
                "contact_name",
                "sub_contact_name",
            ]
            if field in p
        )
        if legacy_filters:
            from rest_framework.exceptions import ValidationError

            raise ValidationError(
                {"detail": f"不支持的旧筛选字段：{','.join(legacy_filters)}"}
            )
        if p.get("status"):
            qs = qs.filter(status=p["status"])
        if p.get("source"):
            qs = qs.filter(source=p["source"])
        current_department_id = p.get("current_department_id")
        if current_department_id:
            qs = qs.filter(current_department_id=current_department_id)
        primary_department_id = p.get("current_primary_department_id")
        if primary_department_id:
            qs = qs.filter(
                Q(current_department_id=primary_department_id)
                | Q(current_department__parent_id=primary_department_id)
                | Q(current_department__parent__parent_id=primary_department_id)
            )
        if p.get("candidate_name"):
            qs = qs.filter(resume__candidate__name__icontains=p["candidate_name"])
        if p.get("volunteer_rank"):
            qs = qs.filter(resume__volunteer_rank=p["volunteer_rank"])
        if p.get("apply_id"):
            qs = qs.filter(
                Q(resume__apply_id__icontains=p["apply_id"])
                | Q(resume_apply_id_snapshot__icontains=p["apply_id"])
            )
        if p.get("position_name"):
            qs = qs.filter(
                Q(resume__position_name__icontains=p["position_name"])
                | Q(position_name_snapshot__icontains=p["position_name"])
            )
        if p.get("department_name"):
            qs = qs.filter(
                Q(current_department__name__icontains=p["department_name"])
                | Q(current_department_name_snapshot__icontains=p["department_name"])
            )
        if p.get("feedback_reason_code"):
            qs = qs.filter(feedback_reason_code=p["feedback_reason_code"])
        if p.get("match_reason"):
            qs = qs.filter(match_reason__icontains=p["match_reason"])
        return qs

    def _transfer_departments(self, request):
        permissions = user_permission_codes(request.user)
        departments = m.Department.objects.select_related("parent__parent").filter(
            level__in=[2, 3]
        )
        if "attempt.view_all" in permissions:
            return departments
        contact = getattr(request.user, "contact", None)
        own_department = getattr(contact, "department", None) if contact else None
        if (
            not contact
            or not contact.is_active
            or contact.contact_level != m.Contact.LEVEL_SECONDARY
            or not contact.can_delegate
            or not own_department
            or own_department.level != 2
        ):
            return departments.none()
        return departments.filter(
            Q(level=2) | Q(level=3, parent_id=own_department.id)
        )

    @action(detail=True, methods=["post"], url_path="dispatch")
    def dispatch_welink(self, request, pk=None):
        attempt = self.get_object()
        try:
            attempt = allocate_service.dispatch_attempt(attempt, user=request.user)
        except ValueError as exc:
            return _attempt_mutation_error(exc)
        return Response(
            {
                "detail": "已下发至部门",
                "attempt": serializers.AssignmentAttemptSerializer(
                    attempt, context={"request": request}
                ).data,
            }
        )

    @action(detail=False, methods=["post"], url_path="bulk-dispatch")
    def bulk_dispatch(self, request):
        """批量下发待下发尝试。

        body.ids 有值时只处理指定 ID；否则按当前筛选条件处理全部。
        只会下发 pending_dispatch，其他状态跳过。
        """
        ids = request.data.get("ids") or []
        if isinstance(ids, str):
            ids = [int(x) for x in ids.split(",") if x.strip().isdigit()]
        qs = self.get_queryset()
        if ids:
            qs = qs.filter(id__in=ids)
        total = qs.count()
        pending_qs = qs.filter(status=m.AssignmentAttempt.STATUS_PENDING_DISPATCH)
        dispatched = 0
        errors = []
        for attempt in pending_qs:
            try:
                allocate_service.dispatch_attempt(attempt, user=request.user)
                dispatched += 1
            except ValueError as exc:
                errors.append({"id": attempt.id, "detail": str(exc)})
        skipped = total - dispatched
        return Response(
            {
                "detail": f"已下发 {dispatched} 条，跳过 {skipped} 条",
                "total": total,
                "dispatched": dispatched,
                "skipped": skipped,
                "errors": errors,
            }
        )

    @action(detail=True, methods=["post"], url_path="transfer")
    def transfer(self, request, pk=None):
        attempt = self.get_object()
        if not _can_transfer_departments(request.user):
            return Response(
                {"detail": "当前接口人没有部门转派权限"},
                status=status.HTTP_403_FORBIDDEN,
            )
        invalid_fields = _reject_unknown_body_fields(
            request, {"target_department_id", "note"}
        )
        if invalid_fields:
            return invalid_fields
        target_department_id = request.data.get("target_department_id")
        if not target_department_id:
            return Response(
                {"detail": "target_department_id 为必填项"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        target_department = self._transfer_departments(request).filter(
            pk=target_department_id
        ).first()
        if not target_department:
            return Response(
                {"detail": "目标部门不存在或不在可转派范围内"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        try:
            attempt = allocate_service.transfer_attempt(
                attempt,
                target_department,
                user=request.user,
                note=request.data.get("note", ""),
            )
        except ValueError as exc:
            return _attempt_mutation_error(exc)
        return Response(
            serializers.AssignmentAttemptSerializer(
                attempt, context={"request": request}
            ).data
        )

    @action(detail=True, methods=["get"], url_path="transfer-options")
    def transfer_options(self, request, pk=None):
        attempt = self.get_object()
        if not _can_transfer_departments(request.user):
            return Response(
                {"detail": "当前接口人没有部门转派权限"},
                status=status.HTTP_403_FORBIDDEN,
            )
        if attempt.status != m.AssignmentAttempt.STATUS_DISPATCHED:
            return Response(
                {"detail": "当前分配状态不可转派"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        departments = self._transfer_departments(request).order_by("level", "name", "id")
        return Response(
            {"results": serializers.DepartmentSerializer(departments, many=True).data}
        )

    @action(detail=False, methods=["get"], url_path="feedback-reasons")
    def feedback_reasons(self, request):
        return Response(
            {
                "results": [
                    {"value": value, "label": label}
                    for value, label in m.AssignmentAttempt.REJECTION_REASON_CHOICES
                ]
            }
        )

    @action(detail=True, methods=["get"], url_path="handling-events")
    def handling_events(self, request, pk=None):
        attempt = self.get_object()
        payload = serializers.AssignmentAttemptSerializer(
            attempt, context={"request": request}
        ).data["handling_events"]
        return Response({"results": payload})

    @action(detail=True, methods=["post"], url_path="confirm-review")
    def confirm_review(self, request, pk=None):
        attempt = self.get_object()
        try:
            attempt = allocate_service.confirm_review(attempt, user=request.user)
        except ValueError as exc:
            return _attempt_mutation_error(exc)
        return Response(
            serializers.AssignmentAttemptSerializer(
                attempt, context={"request": request}
            ).data
        )

    @action(detail=True, methods=["post"], url_path="cancel")
    def cancel_attempt(self, request, pk=None):
        attempt = self.get_object()
        if attempt.status != m.AssignmentAttempt.STATUS_PENDING_DISPATCH:
            return Response(
                {"detail": "仅待下发尝试可以通过该接口取消"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        try:
            attempt = allocate_service.cancel_attempt(
                attempt,
                request.data.get("reason") or "hr_cancelled",
                user=request.user,
            )
        except ValueError as exc:
            return _attempt_mutation_error(exc)
        return Response(
            serializers.AssignmentAttemptSerializer(
                attempt, context={"request": request}
            ).data
        )

    @action(detail=True, methods=["post"], url_path="cancel-review")
    def cancel_review(self, request, pk=None):
        attempt = self.get_object()
        if attempt.status != m.AssignmentAttempt.STATUS_PENDING_REVIEW:
            return Response(
                {"detail": "仅待复核 AI 尝试可以取消复核"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        try:
            attempt = allocate_service.cancel_attempt(
                attempt,
                request.data.get("reason") or "hr_cancelled_review",
                user=request.user,
            )
        except ValueError as exc:
            return _attempt_mutation_error(exc)
        return Response(
            serializers.AssignmentAttemptSerializer(
                attempt, context={"request": request}
            ).data
        )

    @action(detail=True, methods=["post"], url_path="transfer-to-manual")
    def transfer_to_manual(self, request, pk=None):
        attempt = self.get_object()
        invalid_fields = _reject_unknown_body_fields(
            request, {"target_department_id", "manual_reason"}
        )
        if invalid_fields:
            return invalid_fields
        target_department = m.Department.objects.select_related("parent__parent").filter(
            pk=request.data.get("target_department_id"), level__in=[2, 3]
        ).first()
        if not target_department:
            return Response(
                {"detail": "目标部门不存在或层级无效"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        try:
            manual_attempt = allocate_service.manual_assign(
                attempt.resume,
                target_department,
                user=request.user,
                manual_reason=request.data.get("manual_reason") or "AI 复核转人工分配",
            )
        except ValueError as exc:
            return _attempt_mutation_error(exc)
        return Response(
            serializers.AssignmentAttemptSerializer(
                manual_attempt, context={"request": request}
            ).data
        )

    @action(detail=True, methods=["post"], url_path="feedback")
    def feedback(self, request, pk=None):
        attempt = self.get_object()
        invalid_fields = _reject_unknown_body_fields(
            request, {"result", "reason_code", "note"}
        )
        if invalid_fields:
            return invalid_fields
        result = request.data.get("result")
        note = request.data.get("note") or ""
        reason_code = request.data.get("reason_code") or ""
        try:
            attempt = allocate_service.submit_feedback(
                attempt,
                result,
                note,
                reason_code=reason_code,
                user=request.user,
            )
        except ValueError as exc:
            return _attempt_mutation_error(exc)
        return Response(
            serializers.AssignmentAttemptSerializer(
                attempt, context={"request": request}
            ).data
        )

    @action(detail=False, methods=["get"], url_path="export")
    def export_resumes(self, request):
        """按候选人一行导出 Excel，可选附上所选尝试对应的简历文件。

        ?ids=1,2,3 导出指定分配尝试；不传则导出当前筛选（含 status）下全部。
        """
        try:
            field_keys = parse_export_fields(request.query_params)
            include_resume_files = parse_include_resume_files(request.query_params)
        except (ExportFieldError, ExportOptionError) as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
        qs = self.get_queryset()
        ids = request.query_params.get("ids")
        if ids:
            id_list = [int(x) for x in ids.split(",") if x.strip().isdigit()]
            qs = qs.filter(id__in=id_list)

        attempts_by_candidate = {}
        for attempt in qs:
            attempts_by_candidate.setdefault(attempt.workflow.candidate_id, []).append(
                attempt
            )
        records = []
        for attempts in attempts_by_candidate.values():
            selected_attempt = max(
                attempts, key=lambda item: (item.attempt_no, item.id)
            )
            records.append(
                CandidateExportRecord(
                    candidate=selected_attempt.workflow.candidate,
                    current_resume=selected_attempt.resume,
                    attempt=selected_attempt,
                    file_resumes=[item.resume for item in attempts],
                )
            )
        return candidate_export_response(
            records,
            field_keys,
            include_resume_files=include_resume_files,
        )

    @action(detail=True, methods=["get"], url_path="resume-preview")
    def resume_preview(self, request, pk=None):
        attempt = self.get_object()
        return resume_preview_response(attempt.resume)


class AgentDispatchDecisionViewSet(PermissionedReadOnlyModelViewSet):
    serializer_class = serializers.AgentDispatchDecisionSerializer
    permission_codes_by_action = {
        "list": "attempt.view_all",
        "retrieve": "attempt.view_all",
        "retry": "attempt.dispatch",
    }

    def get_queryset(self):
        qs = m.AgentDispatchDecision.objects.select_related(
            "workflow__candidate",
            "resume",
            "processing_run",
            "evaluated_job",
            "recommended_job",
            "recommended_department",
        ).order_by("-created_at")
        p = self.request.query_params
        if p.get("recommendation"):
            qs = qs.filter(recommendation=p["recommendation"])
        if p.get("workflow"):
            qs = qs.filter(workflow_id=p["workflow"])
        return qs

    @action(detail=True, methods=["post"], url_path="retry")
    def retry(self, request, pk=None):
        if not agent_gateway.is_agent_ready():
            return Response(
                {"detail": "Agent Kernel 或模型连接尚未就绪，不能重试"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        decision = self.get_object()
        try:
            allocate_service.validate_agent_decision_retry(decision)
            run = runner.create_run(
                "step2",
                scope={
                    "candidate_ids": [decision.workflow.candidate_id],
                    "source": "ai_retry",
                    "retry_decision_id": decision.id,
                    "retry_resume_id": decision.resume_id,
                },
                created_by=request.user,
            )
            submit_processing_runs([run])
        except (ValueError, ai_service.AIServiceError) as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
        return Response(
            {
                "detail": "已创建 AI 重试任务，可在处理任务中心查看进度",
                "run": serializers.ProcessingRunSerializer(run).data,
            },
            status=status.HTTP_202_ACCEPTED,
        )


class ProcessingRunViewSet(PermissionedReadOnlyModelViewSet):
    serializer_class = serializers.ProcessingRunSerializer
    permission_code = "pipeline.view"

    def get_queryset(self):
        qs = m.ProcessingRun.objects.select_related("created_by").prefetch_related("stages")
        if self.request.query_params.get("active") == "true":
            qs = qs.filter(status__in=["pending", "running", "waiting_conflict", "cancelling"])
        return qs

    @action(detail=True, methods=["post"], url_path="cancel")
    def cancel(self, request, pk=None):
        if not has_permission_code(request.user, "pipeline.run"):
            return Response({"detail": "无取消处理任务权限"}, status=status.HTTP_403_FORBIDDEN)
        try:
            run = cancellation.request_cancellation(pk, request.user)
        except ValueError as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_409_CONFLICT)
        return Response(serializers.ProcessingRunSerializer(run).data)


class PipelineRunView(APIView):
    """创建 ProcessingRun 并交给 Celery；eager 本地模式仍会立即完成。"""

    permission_classes = [HasPermissionCode]
    permission_code = "pipeline.run"

    def post(self, request):
        step = request.data.get("step", "all")
        if "mode" in request.data or "modes" in request.data:
            return Response(
                {"detail": "处理内核由系统固定，不接受 mode 或 modes"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        scope = request.data.get("scope") or {}
        if not isinstance(scope, dict):
            return Response(
                {"detail": "处理范围 scope 必须是对象"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        retry_scope_fields = {"source", "retry_decision_id", "retry_resume_id"}
        if retry_scope_fields.intersection(scope):
            return Response(
                {"detail": "AI 重试只能从简历详情中的重试入口发起"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        force_reprocess_requested = "force_reprocess" in scope
        force_reprocess = scope.get("force_reprocess")
        if force_reprocess_requested and force_reprocess is not True:
            return Response(
                {"detail": "force_reprocess 仅支持布尔值 true"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        if force_reprocess_requested:
            candidate_ids = scope.get("candidate_ids")
            valid_candidate_ids = (
                isinstance(candidate_ids, list)
                and bool(candidate_ids)
                and all(
                    isinstance(candidate_id, int)
                    and not isinstance(candidate_id, bool)
                    and candidate_id > 0
                    for candidate_id in candidate_ids
                )
            )
            if step != "step2":
                return Response(
                    {"detail": "force_reprocess 只允许用于 step2"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
            if not valid_candidate_ids:
                return Response(
                    {"detail": "force_reprocess 必须搭配非空的 candidate_ids 正整数数组"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
            if "system_statuses" in scope or "candidate_filters" in scope:
                return Response(
                    {"detail": "force_reprocess 不得与状态或筛选范围同时提交"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
        system_statuses = scope.get("system_statuses")
        if system_statuses is not None:
            if not isinstance(system_statuses, list) or not system_statuses:
                return Response(
                    {"detail": "system_statuses 必须是非空数组"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
            try:
                system_status.normalize_statuses(system_statuses)
            except ValueError as exc:
                return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
        if "candidate_filters" in scope and not isinstance(
            scope.get("candidate_filters"), dict
        ):
            return Response(
                {"detail": "candidate_filters 必须是对象"},
                status=status.HTTP_400_BAD_REQUEST,
            )
        if not agent_gateway.is_agent_ready():
            return Response(
                {"detail": "Agent Kernel 或模型连接尚未就绪"},
                status=status.HTTP_409_CONFLICT,
            )
        try:
            run = runner.create_run(step, scope=scope, created_by=request.user)
        except (ValueError, ai_service.AIServiceError) as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
        runs = [run]
        submit_processing_runs(runs)
        return Response(
            {"processing_runs": serializers.ProcessingRunSerializer(runs, many=True).data},
            status=(
                status.HTTP_202_ACCEPTED
                if any(run.status in ["pending", "running"] for run in runs)
                else status.HTTP_200_OK
            ),
        )


class UserViewSet(PermissionedModelViewSet):
    serializer_class = serializers.UserSerializer
    permission_code = "settings.manage_permissions"

    def get_queryset(self):
        qs = User.objects.select_related("contact").prefetch_related("groups").order_by("id")
        p = self.request.query_params
        if p.get("username"):
            qs = qs.filter(username__icontains=p["username"])
        if p.get("email"):
            qs = qs.filter(email__icontains=p["email"])
        if p.get("role"):
            qs = qs.filter(role=p["role"])
        role_values = _query_list_values(p, "roles_in")
        if role_values:
            qs = qs.filter(groups__name__in=role_values)
        elif p.get("roles"):
            qs = qs.filter(groups__name=p["roles"])
        if p.get("contact_name"):
            qs = _filter_indexed_name(qs, "contact", p["contact_name"])
        if p.get("is_active") in ["true", "false"]:
            qs = qs.filter(is_active=p["is_active"] == "true")
        return qs.distinct()

    def update(self, request, *args, **kwargs):
        if is_protected_admin(self.get_object()):
            return Response(
                {"detail": "内置管理员不允许编辑、停用或修改角色"},
                status=status.HTTP_403_FORBIDDEN,
            )
        return super().update(request, *args, **kwargs)

    def destroy(self, request, *args, **kwargs):
        user = self.get_object()
        if is_protected_admin(user):
            return Response(
                {"detail": "内置管理员不允许删除"},
                status=status.HTTP_403_FORBIDDEN,
            )
        delete_user_and_bound_contact(user)
        return Response(status=status.HTTP_204_NO_CONTENT)


class RoleViewSet(PermissionedModelViewSet):
    queryset = Group.objects.prefetch_related("permissions").order_by("id")
    serializer_class = serializers.RoleSerializer
    permission_code = "settings.manage_permissions"

    def get_queryset(self):
        qs = super().get_queryset()
        if self.request.query_params.get("name"):
            qs = qs.filter(name__icontains=self.request.query_params["name"])
        return qs


class PermissionTreeView(APIView):
    permission_classes = [HasPermissionCode]
    permission_code = "settings.manage_permissions"

    def get(self, request):
        return Response(PERMISSION_TREE)


class ConfigViewSet(viewsets.ViewSet):
    permission_classes = [HasPermissionCode]
    permission_code = ["settings.manage_config", "department.manage"]

    def _item_data(self, key):
        meta = CONFIG_REGISTRY[key]
        config = m.Config.objects.filter(key=key).first()
        value = config.value if config else meta["default"]
        return {"key": key, "value": value, **meta}

    def list(self, request):
        return Response([self._item_data(key) for key in CONFIG_REGISTRY])

    def retrieve(self, request, pk=None):
        if pk not in CONFIG_REGISTRY:
            return Response({"detail": "未知配置项"}, status=status.HTTP_404_NOT_FOUND)
        return Response(self._item_data(pk))

    def partial_update(self, request, pk=None):
        return self._save(request, pk)

    def update(self, request, pk=None):
        return self._save(request, pk)

    def _save(self, request, pk):
        if pk not in CONFIG_REGISTRY:
            return Response({"detail": "未知配置项"}, status=status.HTTP_404_NOT_FOUND)
        if pk == "job_hc_coefficient" and not has_permission_code(
            request.user, "settings.manage_config"
        ):
            return Response(
                {"detail": "无分配参数维护权限"},
                status=status.HTTP_403_FORBIDDEN,
            )
        serializer = serializers.ConfigSerializer(
            data={**self._item_data(pk), "value": request.data.get("value")}
        )
        serializer.is_valid(raise_exception=True)
        value = serializer.validated_data["value"]
        meta = CONFIG_REGISTRY[pk]
        if meta.get("value_type") in ["number", "integer"]:
            if isinstance(value, bool) or not isinstance(value, (int, float)):
                return Response({"detail": "配置值类型不正确"}, status=status.HTTP_400_BAD_REQUEST)
            if meta.get("value_type") == "integer" and not isinstance(value, int):
                return Response({"detail": "配置值必须是整数"}, status=status.HTTP_400_BAD_REQUEST)
            if (
                "min" in meta
                and value < meta["min"]
                or "max" in meta
                and value > meta["max"]
            ):
                return Response(
                    {"detail": f"配置值必须在 {meta.get('min')} 到 {meta.get('max')} 之间"},
                    status=status.HTTP_400_BAD_REQUEST,
                )
        m.Config.objects.update_or_create(
            key=pk, defaults={"value": value}
        )
        return Response(self._item_data(pk))


class AIConnectionSettingsView(APIView):
    """AI 运行参数与专项分流配置，和模型连接使用同一权限边界。"""

    permission_classes = [HasPermissionCode]
    permission_code = "settings.manage_ai_connection"

    def get(self, request):
        return Response(
            {
                "settings": ai_config.list_public_ai_config_items(),
                "departments": assignment_department_options(),
            }
        )


class AIConnectionSettingDetailView(APIView):
    permission_classes = [HasPermissionCode]
    permission_code = "settings.manage_ai_connection"

    def patch(self, request, key):
        if key not in ai_config.PUBLIC_AI_CONFIG_REGISTRY:
            return Response(
                {"detail": "未知 AI 配置项"}, status=status.HTTP_404_NOT_FOUND
            )
        try:
            item = ai_config.save_public_ai_config(key, request.data.get("value"))
        except (TypeError, ValueError) as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)
        return Response(item)


class AIConnectionConfigView(APIView):
    """由 AI 模型连接权限维护的连接配置；API Key 永不回传。"""

    permission_classes = [HasPermissionCode]
    permission_code = "settings.manage_ai_connection"

    def get(self, request):
        try:
            return Response(ai_config.get_ai_connection_status())
        except (RuntimeError, ValueError) as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)

    def patch(self, request):
        try:
            current = ai_config.get_ai_connection_status()
            payload = {
                "api_style": request.data.get("api_style", current["api_style"]),
                "model_name": request.data.get("model_name", current["model_name"]),
                "base_url": request.data.get("base_url", current["base_url"]),
                "api_key": request.data.get("api_key", ""),
                "clear_api_key": bool(request.data.get("clear_api_key", False)),
            }
            ai_config.save_ai_connection_config(payload)
            return Response(ai_config.get_ai_connection_status())
        except (RuntimeError, ValueError) as exc:
            return Response({"detail": str(exc)}, status=status.HTTP_400_BAD_REQUEST)


class AIConnectionTestView(APIView):
    permission_classes = [HasPermissionCode]
    permission_code = "settings.manage_ai_connection"

    def post(self, request):
        try:
            result = ai_service.test_model_connection()
            tested_at = ai_config.mark_ai_connection_tested(
                structured_output_mode=result["structured_output_mode"]
            )
        except ai_service.AIServiceError as exc:
            ai_config.invalidate_ai_connection_test()
            return Response({"ok": False, "code": exc.code, "detail": exc.message})
        except (RuntimeError, ValueError) as exc:
            ai_config.invalidate_ai_connection_test()
            return Response({"ok": False, "code": "ai_not_configured", "detail": str(exc)})
        return Response(
            {
                "ok": True,
                "detail": "模型连接测试成功",
                "tested_at": tested_at,
                **result,
            }
        )


class AIConnectionModelsView(APIView):
    """通过管理员填写的 Base URL 查询 OpenAI 兼容模型列表。"""

    permission_classes = [HasPermissionCode]
    permission_code = "settings.manage_ai_connection"

    def post(self, request):
        try:
            models = ai_service.list_available_models(
                base_url=request.data.get("base_url", ""),
                api_key=request.data.get("api_key", ""),
            )
        except ai_service.AIServiceError as exc:
            return Response({"models": [], "code": exc.code, "detail": exc.message})
        except (RuntimeError, ValueError) as exc:
            return Response({"models": [], "code": "ai_not_configured", "detail": str(exc)})
        return Response({"models": models})
