from rest_framework import serializers
from django.contrib.auth.models import Group
from django.db import transaction
from django.db.models import Count, Q
from django.utils import timezone

from apps.accounts.contact_users import sync_contact_user
from apps.accounts.models import User
from apps.accounts.protected_users import is_protected_admin
from apps.accounts.permissions import (
    PERMISSION_TREE,
    all_permission_codes,
    ensure_permission_definitions,
    permission_code,
    user_permission_codes,
    user_role_names,
)
from apps.core import candidate_summary
from apps.core import models as m
from apps.core import system_status
from apps.core.departments import resolve_department_hierarchy


_PUBLIC_REASON_CODE_ALIASES = {
    "ai_special_route": "ai_dispatched",
    "ai_special_route_unavailable": "ai_connection_error",
}


def _public_reason_code(value):
    return _PUBLIC_REASON_CODE_ALIASES.get(value, value)


def _public_ai_message(value):
    text = str(value or "")
    return (
        text.replace("AI 专项强制分配", "AI 自动分配")
        .replace("AI 专项分流", "AI 后台分配")
    )


def _public_match_reason(attempt):
    if attempt and attempt.route_code == "ai_special_route":
        return "AI 自动分配"
    return _public_ai_message(attempt.match_reason if attempt else "")


VISIBLE_DEPARTMENT_ATTEMPT_STATUSES = {
    m.AssignmentAttempt.STATUS_DISPATCHED,
    m.AssignmentAttempt.STATUS_PASSED,
    m.AssignmentAttempt.STATUS_REJECTED,
}


def department_attempt_scope_q(user, *, prefix=""):
    """返回接口人部门收件箱的数据范围，不包含待复核、待下发或已取消记录。"""
    contact = getattr(user, "contact", None)
    department = getattr(contact, "department", None) if contact else None
    if not contact or not contact.is_active or not department:
        return Q(pk__in=[])

    status_field = f"{prefix}status__in"
    department_field = f"{prefix}current_department_id"
    parent_field = f"{prefix}current_department__parent_id"
    visible_status = Q(**{status_field: VISIBLE_DEPARTMENT_ATTEMPT_STATUSES})
    if (
        contact.contact_level == m.Contact.LEVEL_SECONDARY
        and department.level == 2
    ):
        return visible_status & (
            Q(**{department_field: department.id})
            | (
                Q(**{f"{prefix}current_department__level": 3})
                & Q(**{parent_field: department.id})
            )
        )
    if (
        contact.contact_level == m.Contact.LEVEL_TERTIARY
        and department.level == 3
    ):
        return visible_status & Q(**{department_field: department.id})
    return Q(pk__in=[])


def department_attempt_is_visible(attempt, user):
    contact = getattr(user, "contact", None)
    department = getattr(contact, "department", None) if contact else None
    current = getattr(attempt, "current_department", None)
    if (
        not contact
        or not contact.is_active
        or not department
        or not current
        or attempt.status not in VISIBLE_DEPARTMENT_ATTEMPT_STATUSES
    ):
        return False
    if contact.contact_level == m.Contact.LEVEL_SECONDARY and department.level == 2:
        return current.id == department.id or (
            current.level == 3 and current.parent_id == department.id
        )
    if contact.contact_level == m.Contact.LEVEL_TERTIARY and department.level == 3:
        return current.id == department.id
    return False


def visible_candidate_attempt(candidate, user, *, permissions=None):
    """返回当前用户在统一简历库中可操作的最新尝试。"""
    workflow = candidate_summary.workflow_or_none(candidate)
    if not workflow:
        return None
    if permissions is None:
        permissions = user_permission_codes(user)
    if "resume.view" in permissions:
        resume = candidate_summary.current_resume(candidate)
        return candidate_summary.latest_effective_attempt(
            workflow, resume_id=resume.id if resume else None
        )
    if "attempt.view_department" not in permissions:
        return None
    visible = [
        attempt
        for attempt in workflow.attempts.all()
        if department_attempt_is_visible(attempt, user)
    ]
    if not visible:
        return None
    return sorted(set(visible), key=lambda item: (item.attempt_no, item.id))[-1]


class CurrentUserSerializer(serializers.ModelSerializer):
    roles = serializers.SerializerMethodField()
    permissions = serializers.SerializerMethodField()
    contact = serializers.SerializerMethodField()
    data_scope = serializers.SerializerMethodField()

    class Meta:
        model = User
        fields = [
            "id",
            "username",
            "first_name",
            "last_name",
            "email",
            "role",
            "roles",
            "permissions",
            "contact",
            "data_scope",
            "is_superuser",
            "is_staff",
        ]

    def get_roles(self, obj):
        return user_role_names(obj)

    def get_permissions(self, obj):
        return sorted(user_permission_codes(obj))

    def get_contact(self, obj):
        if not obj.contact:
            return None
        return ContactSerializer(obj.contact).data

    def get_data_scope(self, obj):
        permissions = user_permission_codes(obj)
        if "attempt.view_all" in permissions:
            return {"type": "all"}
        contact = obj.contact
        department = contact.department if contact else None
        if (
            contact
            and contact.is_active
            and department
            and "attempt.view_department" in permissions
        ):
            include_descendants = (
                contact.contact_level == m.Contact.LEVEL_SECONDARY
                and department.level == 2
            )
            department_ids = [department.id]
            if include_descendants:
                department_ids.extend(
                    department.children.filter(level=3).values_list("id", flat=True)
                )
            return {
                "type": "department",
                "department_id": department.id,
                "department_level": department.level,
                "department_ids": department_ids,
                "include_descendants": include_descendants,
            }
        return {"type": "none"}


class UserSerializer(serializers.ModelSerializer):
    role_ids = serializers.PrimaryKeyRelatedField(
        source="groups",
        queryset=Group.objects.all(),
        many=True,
        required=False,
    )
    roles = serializers.SerializerMethodField()
    permissions = serializers.SerializerMethodField()
    contact_name = serializers.CharField(source="contact.name", read_only=True, default="")
    is_protected = serializers.SerializerMethodField()

    class Meta:
        model = User
        fields = [
            "id",
            "username",
            "email",
            "role",
            "contact",
            "contact_name",
            "is_active",
            "is_staff",
            "is_superuser",
            "is_protected",
            "role_ids",
            "roles",
            "permissions",
        ]
        read_only_fields = ["is_superuser"]
        extra_kwargs = {
            "email": {"required": True, "allow_blank": False},
        }

    def get_roles(self, obj):
        return user_role_names(obj)

    def get_permissions(self, obj):
        return sorted(user_permission_codes(obj))

    def get_is_protected(self, obj):
        return is_protected_admin(obj)

    def validate_email(self, value):
        email = str(value or "").strip().casefold()
        duplicate = User.objects.filter(email__iexact=email)
        if self.instance is not None:
            duplicate = duplicate.exclude(pk=self.instance.pk)
        if duplicate.exists():
            raise serializers.ValidationError("该邮箱已被其他账号使用")
        return email

    def to_internal_value(self, data):
        if "password" in data:
            raise serializers.ValidationError(
                {"password": "系统账号不接受密码，请使用 W3 登录"}
            )
        return super().to_internal_value(data)

    def create(self, validated_data):
        groups = validated_data.pop("groups", [])
        user = User(**validated_data)
        user.set_unusable_password()
        user.save()
        if groups:
            user.groups.set(groups)
        return user

    def update(self, instance, validated_data):
        groups = validated_data.pop("groups", None)
        for attr, value in validated_data.items():
            setattr(instance, attr, value)
        instance.set_unusable_password()
        instance.save()
        if groups is not None:
            instance.groups.set(groups)
        return instance


class RoleSerializer(serializers.ModelSerializer):
    permission_codes = serializers.ListField(
        child=serializers.CharField(), required=False, write_only=True
    )
    permissions = serializers.SerializerMethodField()

    class Meta:
        model = Group
        fields = ["id", "name", "permissions", "permission_codes"]

    def get_permissions(self, obj):
        known_codes = set(all_permission_codes())
        return sorted(
            code
            for permission in obj.permissions.all()
            if (code := permission_code(permission.codename)) in known_codes
        )

    def validate_permission_codes(self, codes):
        normalized_codes = list(dict.fromkeys(codes))
        unknown_codes = sorted(set(normalized_codes) - set(all_permission_codes()))
        if unknown_codes:
            raise serializers.ValidationError(
                f"包含未知权限码：{', '.join(unknown_codes)}"
            )
        return normalized_codes

    def _set_permissions(self, group, codes):
        permissions = ensure_permission_definitions()
        group.permissions.set([permissions[code] for code in codes])

    def create(self, validated_data):
        codes = validated_data.pop("permission_codes", None)
        group = super().create(validated_data)
        if codes is not None:
            self._set_permissions(group, codes)
        return group

    def update(self, instance, validated_data):
        codes = validated_data.pop("permission_codes", None)
        group = super().update(instance, validated_data)
        if codes is not None:
            self._set_permissions(group, codes)
        return group


class ConfigSerializer(serializers.Serializer):
    key = serializers.CharField(read_only=True)
    label = serializers.CharField(read_only=True)
    description = serializers.CharField(read_only=True)
    value_type = serializers.CharField(read_only=True)
    value = serializers.JSONField()


class PermissionTreeSerializer(serializers.Serializer):
    @staticmethod
    def tree():
        return PERMISSION_TREE


class ResumeListSerializer(serializers.ModelSerializer):
    candidate_name = serializers.CharField(source="candidate.name", read_only=True)
    phone = serializers.CharField(source="candidate.phone", read_only=True)
    school_tag = serializers.SerializerMethodField()
    school_tags = serializers.SerializerMethodField()

    class Meta:
        model = m.Resume
        fields = [
            "id",
            "candidate_name",
            "phone",
            "entity",
            "position_name",
            "volunteer_rank",
            "job_category",
            "school_tag",
            "school_tags",
            "status",
        ]

    def get_school_tag(self, obj):
        c = obj.candidate
        return (
            getattr(c.highest_degree_tag, "name", "")
            or getattr(c.first_degree_tag, "name", "")
            or c.highest_degree_platform
            or c.first_degree_platform
            or ""
        )

    def get_school_tags(self, obj):
        return [tag.name for tag in obj.candidate.school_tags.all()]


class ResumeBriefSerializer(serializers.ModelSerializer):
    class Meta:
        model = m.Resume
        fields = [
            "id",
            "apply_id",
            "entity",
            "org",
            "position_name",
            "status",
            "apply_date",
            "volunteer_rank",
            "assigned_entity",
            "job_category",
            "category_mode",
            "category_reason",
            "resume_file",
        ]


class CandidateSerializer(serializers.ModelSerializer):
    phone = serializers.SerializerMethodField()
    school_tag = serializers.SerializerMethodField()
    school_tags = serializers.SerializerMethodField()
    system_status = serializers.SerializerMethodField()
    system_status_label = serializers.SerializerMethodField()
    workflow_id = serializers.SerializerMethodField()
    workflow_status = serializers.SerializerMethodField()
    current_resume = serializers.SerializerMethodField()
    preview_resume = serializers.SerializerMethodField()
    current_rank = serializers.SerializerMethodField()
    current_apply_id = serializers.SerializerMethodField()
    current_apply_date = serializers.SerializerMethodField()
    job_department_name = serializers.SerializerMethodField()
    current_department_id = serializers.SerializerMethodField()
    current_department_name = serializers.SerializerMethodField()
    current_primary_department_id = serializers.SerializerMethodField()
    current_primary_department_name = serializers.SerializerMethodField()
    reason_type = serializers.SerializerMethodField()
    reason_text = serializers.SerializerMethodField()
    archive_reason = serializers.SerializerMethodField()
    archive_detail = serializers.SerializerMethodField()
    allocation_source = serializers.SerializerMethodField()
    processing_result = serializers.SerializerMethodField()
    reason_code = serializers.SerializerMethodField()
    resumes = serializers.SerializerMethodField()
    attempts = serializers.SerializerMethodField()
    current_attempt = serializers.SerializerMethodField()
    highest_education_label = serializers.CharField(
        source="get_highest_education_display", read_only=True
    )

    class Meta:
        model = m.Candidate
        fields = [
            "id",
            "identity_hash",
            "name",
            "phone",
            "gender",
            "household_province",
            "first_degree_school",
            "highest_degree_school",
            "highest_major",
            "highest_education",
            "highest_education_label",
            "first_degree_tag",
            "highest_degree_tag",
            "first_degree_platform",
            "highest_degree_platform",
            "school_tag",
            "school_tags",
            "system_status",
            "system_status_label",
            "workflow_id",
            "workflow_status",
            "current_resume",
            "preview_resume",
            "current_rank",
            "current_apply_id",
            "current_apply_date",
            "job_department_name",
            "current_department_id",
            "current_department_name",
            "current_primary_department_id",
            "current_primary_department_name",
            "reason_type",
            "reason_text",
            "archive_reason",
            "archive_detail",
            "allocation_source",
            "processing_result",
            "reason_code",
            "resumes",
            "attempts",
            "current_attempt",
            "imported_at",
            "updated_at",
        ]

    def _cached_candidate_value(self, cache_name, obj, factory):
        cache = getattr(self, cache_name, None)
        if cache is None:
            cache = {}
            setattr(self, cache_name, cache)
        key = obj.pk if obj.pk is not None else id(obj)
        if key not in cache:
            cache[key] = factory()
        return cache[key]

    def _permissions(self):
        if not hasattr(self, "_permission_codes"):
            request = self.context.get("request")
            self._permission_codes = (
                user_permission_codes(request.user) if request else set()
            )
        return self._permission_codes

    def _workflow(self, obj):
        return self._cached_candidate_value(
            "_workflow_cache", obj, lambda: candidate_summary.workflow_or_none(obj)
        )

    def _visible_attempt(self, obj):
        request = self.context.get("request")
        user = request.user if request else None

        def resolve():
            if not request:
                resume = candidate_summary.current_resume(obj)
                return candidate_summary.latest_effective_attempt(
                    self._workflow(obj), resume_id=resume.id if resume else None
                )
            return visible_candidate_attempt(
                obj, user, permissions=self._permissions()
            )

        return self._cached_candidate_value(
            "_visible_attempt_cache",
            obj,
            resolve,
        )

    def _is_full_view(self):
        request = self.context.get("request")
        return not request or "resume.view" in self._permissions()

    def _current_resume(self, obj):
        def resolve():
            if not self._is_full_view():
                attempt = self._visible_attempt(obj)
                return attempt.resume if attempt else None
            return candidate_summary.current_resume(obj)

        return self._cached_candidate_value("_current_resume_cache", obj, resolve)

    def get_phone(self, obj):
        if not self._is_full_view():
            return ""
        return obj.phone

    def get_school_tag(self, obj):
        names = [item["name"] for item in self.get_school_tags(obj)]
        return "、".join(names)

    def get_school_tags(self, obj):
        tags = list(obj.school_tags.all())
        if not tags:
            fallback = [obj.first_degree_tag, obj.highest_degree_tag]
            tags = list({tag.id: tag for tag in fallback if tag}.values())
        return [
            {"id": tag.id, "code": tag.code, "name": tag.name}
            for tag in sorted(tags, key=lambda tag: (tag.code, tag.id))
        ]

    def get_system_status(self, obj):
        if not self._is_full_view():
            attempt = self._visible_attempt(obj)
            if not attempt:
                return system_status.RAW
            if attempt.status == m.AssignmentAttempt.STATUS_PASSED:
                return system_status.SCREENING_PASSED
            if attempt.status == m.AssignmentAttempt.STATUS_REJECTED:
                return system_status.SCREENING_REJECTED
            return system_status.PENDING_SCREENING
        return system_status.candidate_system_status(obj)

    def get_system_status_label(self, obj):
        return system_status.system_status_label(self.get_system_status(obj))

    def get_workflow_id(self, obj):
        workflow = self._workflow(obj)
        return workflow.id if workflow else None

    def get_workflow_status(self, obj):
        if not self._is_full_view():
            attempt = self._visible_attempt(obj)
            if attempt and attempt.status == m.AssignmentAttempt.STATUS_PASSED:
                return m.CandidateWorkflow.STATUS_PASSED
            return m.CandidateWorkflow.STATUS_IN_PROGRESS
        workflow = self._workflow(obj)
        return workflow.status if workflow else m.CandidateWorkflow.STATUS_PENDING

    def get_current_resume(self, obj):
        resume = self._current_resume(obj)
        return ResumeBriefSerializer(resume).data if resume else None

    def get_preview_resume(self, obj):
        if not self._is_full_view():
            attempt = self._visible_attempt(obj)
            return (
                ResumeBriefSerializer(attempt.resume).data
                if attempt and attempt.resume.resume_file
                else None
            )
        resume = candidate_summary.preview_resume(obj)
        return ResumeBriefSerializer(resume).data if resume else None

    def get_current_rank(self, obj):
        resume = self._current_resume(obj)
        return resume.volunteer_rank if resume else None

    def get_current_apply_id(self, obj):
        resume = self._current_resume(obj)
        return resume.apply_id if resume else ""

    def get_current_apply_date(self, obj):
        resume = self._current_resume(obj)
        return resume.apply_date.isoformat() if resume and resume.apply_date else None

    def get_job_department_name(self, obj):
        if not self._is_full_view():
            attempt = self._visible_attempt(obj)
            if attempt:
                job_department = getattr(getattr(attempt.resume, "job", None), "department", None)
                secondary = resolve_department_hierarchy(job_department).secondary
                return secondary.name if secondary else ""
        return candidate_summary.job_department_name(obj)

    def _current_department_hierarchy(self, obj):
        attempt = self._visible_attempt(obj)
        department = attempt.current_department if attempt else None
        return resolve_department_hierarchy(department)

    def get_current_department_id(self, obj):
        attempt = self._visible_attempt(obj)
        return attempt.current_department_id if attempt else None

    def get_current_department_name(self, obj):
        attempt = self._visible_attempt(obj)
        return attempt.current_department.name if attempt and attempt.current_department else ""

    def get_current_primary_department_id(self, obj):
        primary = self._current_department_hierarchy(obj).primary
        return primary.id if primary else None

    def get_current_primary_department_name(self, obj):
        primary = self._current_department_hierarchy(obj).primary
        return primary.name if primary else ""

    def get_reason_type(self, obj):
        if not self._is_full_view():
            return (
                candidate_summary.REASON_ASSIGNMENT
                if self._visible_attempt(obj)
                else candidate_summary.REASON_NONE
            )
        reason_type = candidate_summary.reason(obj)[0]
        if reason_type:
            return reason_type
        if self.get_system_status(obj) == system_status.ARCHIVED and self._processing_item(obj):
            return candidate_summary.REASON_ARCHIVE
        return reason_type

    def get_reason_text(self, obj):
        if not self._is_full_view():
            attempt = self._visible_attempt(obj)
            if attempt:
                return (
                    attempt.feedback_reason_label_snapshot
                    or attempt.feedback_note
                    or attempt.manual_reason
                    or _public_match_reason(attempt)
                )
            return ""
        attempt = self._visible_attempt(obj)
        if attempt and attempt.feedback_reason_label_snapshot:
            return attempt.feedback_reason_label_snapshot
        status_code = self.get_system_status(obj)
        item = self._processing_item(obj)
        if (
            status_code in {system_status.ARCHIVED, system_status.PENDING_REALLOCATION}
            and item
            and item.result_message
        ):
            return _public_ai_message(item.result_message)
        return _public_ai_message(candidate_summary.reason(obj)[1])

    def get_archive_reason(self, obj):
        if not self._is_full_view():
            return ""
        workflow = self._workflow(obj)
        return workflow.archive_reason if workflow else ""

    def get_archive_detail(self, obj):
        if not self._is_full_view():
            return ""
        workflow = self._workflow(obj)
        return workflow.archive_detail if workflow else ""

    def get_allocation_source(self, obj):
        if not self._is_full_view():
            attempt = self._visible_attempt(obj)
            return attempt.source if attempt else ""
        return candidate_summary.allocation_source(obj)

    def _processing_item(self, obj):
        def resolve():
            request = self.context.get("request")
            query_params = getattr(request, "query_params", {}) if request else {}
            return candidate_summary.latest_processing_scope_item(
                obj, query_params.get("processing_run_id")
            )

        return self._cached_candidate_value("_processing_item_cache", obj, resolve)

    def get_processing_result(self, obj):
        if not self._is_full_view():
            return ""
        item = self._processing_item(obj)
        return item.result_type if item else ""

    def get_reason_code(self, obj):
        attempt = self._visible_attempt(obj)
        if attempt and attempt.feedback_reason_code:
            return attempt.feedback_reason_code
        if not self._is_full_view():
            return ""
        item = self._processing_item(obj)
        return _public_reason_code(item.reason_code) if item else ""

    def get_resumes(self, obj):
        if not self._is_full_view():
            attempt = self._visible_attempt(obj)
            return [ResumeBriefSerializer(attempt.resume).data] if attempt else []
        resumes = sorted(
            obj.resumes.all(),
            key=lambda resume: (
                resume.volunteer_rank if resume.volunteer_rank is not None else 999,
                resume.apply_date.toordinal() if resume.apply_date else 0,
                resume.id,
            ),
        )
        return ResumeBriefSerializer(resumes, many=True).data

    def get_attempts(self, obj):
        workflow = self._workflow(obj)
        if not workflow:
            return []
        request = self.context.get("request")
        if request and not self._is_full_view():
            attempt = self._visible_attempt(obj)
            return [self._attempt_data(attempt, include_ai=False)] if attempt else []
        attempts = sorted(
            workflow.attempts.all(), key=lambda attempt: (attempt.attempt_no, attempt.id)
        )
        return [self._attempt_data(attempt, include_ai=True) for attempt in attempts]

    def get_current_attempt(self, obj):
        request = self.context.get("request")
        user = request.user if request else None
        attempt = self._visible_attempt(obj)
        if not attempt:
            return None
        include_ai = bool(user and self._is_full_view())
        return self._attempt_data(attempt, include_ai=include_ai)

    def _attempt_data(self, attempt, *, include_ai):
        context = {**self.context, "permission_codes": self._permissions()}
        data = AssignmentAttemptSerializer(attempt, context=context).data
        if not include_ai:
            data.pop("agent_decision", None)
            data.pop("agent_decision_summary", None)
        return data


class JobSerializer(serializers.ModelSerializer):
    department_name = serializers.SerializerMethodField()
    primary_department_id = serializers.SerializerMethodField()
    primary_department_name = serializers.SerializerMethodField()
    secondary_department_id = serializers.SerializerMethodField()
    secondary_department_name = serializers.SerializerMethodField()
    responsibilities = serializers.CharField(
        required=True,
        allow_blank=False,
        trim_whitespace=True,
    )
    majors = serializers.SerializerMethodField()
    major_names = serializers.ListField(
        child=serializers.CharField(allow_blank=False),
        write_only=True,
        required=False,
    )

    class Meta:
        model = m.Job
        fields = [
            "id",
            "entity",
            "department",
            "department_name",
            "primary_department_id",
            "primary_department_name",
            "secondary_department_id",
            "secondary_department_name",
            "category",
            "public_name",
            "is_public",
            "position_name",
            "job_family",
            "location",
            "education",
            "responsibilities",
            "headcount",
            "is_active",
            "majors",
            "major_names",
        ]

    def get_majors(self, obj):
        return list(obj.majors.order_by("id").values_list("major", flat=True))

    def _department_hierarchy(self, obj):
        cache = getattr(self, "_department_hierarchy_cache", None)
        if cache is None:
            cache = {}
            self._department_hierarchy_cache = cache
        key = (obj.pk, obj.department_id)
        if key not in cache:
            cache[key] = resolve_department_hierarchy(obj.department)
        return cache[key]

    def get_department_name(self, obj):
        department = self._department_hierarchy(obj).secondary
        return department.name if department else ""

    def get_primary_department_id(self, obj):
        department = self._department_hierarchy(obj).primary
        return department.id if department else None

    def get_primary_department_name(self, obj):
        department = self._department_hierarchy(obj).primary
        return department.name if department else ""

    def get_secondary_department_id(self, obj):
        department = self._department_hierarchy(obj).secondary
        return department.id if department else None

    def get_secondary_department_name(self, obj):
        return self.get_department_name(obj)

    def validate_department(self, department):
        if not department or department.level != 2:
            raise serializers.ValidationError("岗位必须绑定二级部门")
        return department

    def validate(self, attrs):
        department = attrs.get("department", getattr(self.instance, "department", None))
        if not department or department.level != 2:
            raise serializers.ValidationError(
                {"department": "岗位必须绑定二级部门"}
            )
        responsibilities = attrs.get(
            "responsibilities",
            getattr(self.instance, "responsibilities", ""),
        )
        responsibilities = str(responsibilities or "").strip()
        if not responsibilities:
            raise serializers.ValidationError(
                {"responsibilities": "工作职责不能为空"}
            )
        if "responsibilities" in attrs:
            attrs["responsibilities"] = responsibilities
        return attrs

    def _clean_major_names(self, values):
        seen = set()
        result = []
        for value in values:
            text = str(value).strip()
            if text and text not in seen:
                seen.add(text)
                result.append(text)
        return result

    def _replace_majors(self, job, major_names):
        m.JobMajor.objects.filter(job=job).delete()
        m.JobMajor.objects.bulk_create(
            [m.JobMajor(job=job, major=major) for major in major_names]
        )

    def create(self, validated_data):
        major_names = self._clean_major_names(validated_data.pop("major_names", []))
        job = super().create(validated_data)
        self._replace_majors(job, major_names)
        return job

    def update(self, instance, validated_data):
        has_major_names = "major_names" in validated_data
        major_names = self._clean_major_names(validated_data.pop("major_names", []))
        job = super().update(instance, validated_data)
        if has_major_names:
            self._replace_majors(job, major_names)
        return job


class SchoolSerializer(serializers.ModelSerializer):
    school_tag_name = serializers.CharField(
        source="school_tag.name", read_only=True, default=""
    )

    class Meta:
        model = m.School
        fields = [
            "id",
            "name",
            "platform",
            "province",
            "school_tag",
            "school_tag_name",
        ]
        read_only_fields = ["platform", "province"]

    def to_internal_value(self, data):
        if "province" in data:
            raise serializers.ValidationError(
                {"province": "所在省份由 AI 自动补全，不接受用户填写"}
            )
        return super().to_internal_value(data)

    def create(self, validated_data):
        if "school_tag" in validated_data:
            school_tag = validated_data.get("school_tag")
            validated_data["platform"] = school_tag.name if school_tag else ""
        return super().create(validated_data)

    def update(self, instance, validated_data):
        if "school_tag" in validated_data:
            school_tag = validated_data.get("school_tag")
            validated_data["platform"] = school_tag.name if school_tag else ""
        return super().update(instance, validated_data)


class DepartmentSerializer(serializers.ModelSerializer):
    parent_id = serializers.IntegerField(source="parent.id", read_only=True, default=None)
    parent_name = serializers.CharField(source="parent.name", read_only=True, default="")
    primary_department_id = serializers.SerializerMethodField()
    primary_department_name = serializers.SerializerMethodField()

    class Meta:
        model = m.Department
        fields = [
            "id",
            "name",
            "level",
            "parent",
            "parent_id",
            "parent_name",
            "primary_department_id",
            "primary_department_name",
            "entity",
        ]

    def get_primary_department_id(self, obj):
        primary = resolve_department_hierarchy(obj).primary
        return primary.id if primary else None

    def get_primary_department_name(self, obj):
        primary = resolve_department_hierarchy(obj).primary
        return primary.name if primary else ""

    def validate(self, attrs):
        level = attrs.get(
            "level", getattr(self.instance, "level", 2)
        )
        parent = attrs.get(
            "parent", getattr(self.instance, "parent", None)
        )
        if level not in {1, 2, 3}:
            raise serializers.ValidationError({"level": "部门层级只能是 1、2 或 3"})
        if self.instance and parent and parent.pk == self.instance.pk:
            raise serializers.ValidationError({"parent": "部门不能以自身作为父部门"})

        expected_parent_level = {1: None, 2: 1, 3: 2}[level]
        if expected_parent_level is None and parent is not None:
            raise serializers.ValidationError({"parent": "一级部门不能设置父部门"})
        if expected_parent_level is not None and (
            parent is None or parent.level != expected_parent_level
        ):
            raise serializers.ValidationError(
                {"parent": f"{level}级部门必须归属于{expected_parent_level}级部门"}
            )

        seen = set()
        ancestor = parent
        while ancestor:
            if ancestor.pk in seen or (
                self.instance and ancestor.pk == self.instance.pk
            ):
                raise serializers.ValidationError({"parent": "部门层级不能形成循环"})
            seen.add(ancestor.pk)
            ancestor = ancestor.parent

        if self.instance:
            structure_changed = (
                ("level" in attrs and level != self.instance.level)
                or (
                    "parent" in attrs
                    and getattr(parent, "pk", None) != self.instance.parent_id
                )
            )
            if structure_changed:
                related_managers = (
                    "children",
                    "contacts",
                    "jobs",
                    "initial_assignment_attempts",
                    "current_assignment_attempts",
                    "assignment_events_from",
                    "assignment_events_to",
                    "agent_decisions",
                    "prepared_processing_items",
                )
                if any(
                    getattr(self.instance, relation).exists()
                    for relation in related_managers
                ):
                    raise serializers.ValidationError(
                        {"detail": "部门存在下级部门或业务引用，不可修改层级或父部门"}
                    )
        return attrs


class ContactSerializer(serializers.ModelSerializer):
    department_name = serializers.CharField(
        source="department.name", read_only=True, default=""
    )
    entity = serializers.CharField(source="department.entity", read_only=True, default="")
    department_level = serializers.IntegerField(source="department.level", read_only=True)
    parent_department = serializers.IntegerField(source="department.parent_id", read_only=True)

    class Meta:
        model = m.Contact
        fields = [
            "id",
            "name",
            "employee_no",
            "email",
            "department",
            "department_name",
            "department_level",
            "parent_department",
            "entity",
            "contact_level",
            "can_delegate",
            "is_active",
        ]
        extra_kwargs = {
            "email": {"required": True, "allow_blank": False},
        }

    def validate(self, attrs):
        email = str(attrs.get("email", getattr(self.instance, "email", "")) or "")
        email = email.strip().casefold()
        if not email:
            raise serializers.ValidationError({"email": "请输入邮箱"})
        duplicate_email = m.Contact.objects.filter(email__iexact=email)
        if self.instance is not None:
            duplicate_email = duplicate_email.exclude(pk=self.instance.pk)
        if duplicate_email.exists():
            raise serializers.ValidationError({"email": "该邮箱已被其他接口人使用"})
        attrs["email"] = email

        department = attrs.get("department")
        if department is None and self.instance is not None:
            department = self.instance.department
        if department is None:
            raise serializers.ValidationError({"department": "请选择所属部门"})
        if department.level not in (2, 3):
            raise serializers.ValidationError(
                {"department": "接口人只能绑定二级或三级部门"}
            )

        contact_level = (
            m.Contact.LEVEL_TERTIARY
            if department.level == 3
            else m.Contact.LEVEL_SECONDARY
        )
        attrs["contact_level"] = contact_level
        if contact_level == m.Contact.LEVEL_TERTIARY:
            attrs["can_delegate"] = False
        return attrs

    @transaction.atomic
    def create(self, validated_data):
        contact = super().create(validated_data)
        try:
            sync_contact_user(contact)
        except ValueError as exc:
            field = "email" if "邮箱" in str(exc) else "employee_no"
            raise serializers.ValidationError({field: str(exc)}) from exc
        return contact

    @transaction.atomic
    def update(self, instance, validated_data):
        contact = super().update(instance, validated_data)
        try:
            sync_contact_user(contact)
        except ValueError as exc:
            field = "email" if "邮箱" in str(exc) else "employee_no"
            raise serializers.ValidationError({field: str(exc)}) from exc
        return contact


class SchoolTagSerializer(serializers.ModelSerializer):
    class Meta:
        model = m.SchoolTag
        fields = ["id", "code", "name", "is_default", "is_active"]


def normalize_major_name(value):
    """专业词表统一规范化口径：忽略大小写和空白。"""
    return "".join((value or "").lower().split())


class MajorCategorySerializer(serializers.ModelSerializer):
    alias_count = serializers.SerializerMethodField()

    class Meta:
        model = m.MajorCategory
        fields = [
            "id",
            "code",
            "name",
            "description",
            "is_active",
            "sort_order",
            "alias_count",
            "created_at",
            "updated_at",
        ]
        read_only_fields = ["created_at", "updated_at"]

    def get_alias_count(self, obj):
        if hasattr(obj, "alias_count"):
            return obj.alias_count
        return obj.aliases.count()


class MajorAliasSerializer(serializers.ModelSerializer):
    category_name = serializers.CharField(source="category.name", read_only=True)
    normalized_name = serializers.CharField(read_only=True)

    class Meta:
        model = m.MajorAlias
        fields = [
            "id",
            "category",
            "category_name",
            "name",
            "normalized_name",
            "match_type",
            "source",
            "note",
            "is_active",
            "created_at",
            "updated_at",
        ]
        read_only_fields = ["created_at", "updated_at"]

    def validate_name(self, value):
        if not value or not str(value).strip():
            raise serializers.ValidationError("请输入专业名称或关键词")
        return str(value).strip()

    def validate(self, attrs):
        category = attrs.get("category") or (
            self.instance.category if self.instance else None
        )
        name = attrs.get("name") or (self.instance.name if self.instance else "")
        match_type = attrs.get("match_type") or (
            self.instance.match_type if self.instance else m.MajorAlias.MATCH_CONTAINS
        )
        normalized_name = normalize_major_name(name)
        qs = m.MajorAlias.objects.filter(
            category=category,
            normalized_name=normalized_name,
            match_type=match_type,
        )
        if self.instance:
            qs = qs.exclude(pk=self.instance.pk)
        if category and normalized_name and qs.exists():
            raise serializers.ValidationError(
                {"name": "同一专业大类下已存在相同专业名称和匹配方式的别名"}
            )
        return attrs

    def create(self, validated_data):
        validated_data["normalized_name"] = normalize_major_name(
            validated_data.get("name")
        )
        return super().create(validated_data)

    def update(self, instance, validated_data):
        if "name" in validated_data:
            validated_data["normalized_name"] = normalize_major_name(
                validated_data.get("name")
            )
        return super().update(instance, validated_data)


class SchoolTagRuleSerializer(serializers.ModelSerializer):
    first_degree_tags = serializers.SerializerMethodField()
    highest_degree_tags = serializers.SerializerMethodField()
    first_degree_tag_ids = serializers.ListField(
        child=serializers.IntegerField(), write_only=True, required=False
    )
    highest_degree_tag_ids = serializers.ListField(
        child=serializers.IntegerField(), write_only=True, required=False
    )
    allowed_highest_educations = serializers.ListField(
        child=serializers.ChoiceField(
            choices=m.Candidate.HIGHEST_EDUCATION_CHOICES
        ),
        write_only=True,
        required=False,
    )

    class Meta:
        model = m.SchoolTagRule
        fields = [
            "id",
            "name",
            "first_degree_tags",
            "highest_degree_tags",
            "first_degree_tag_ids",
            "highest_degree_tag_ids",
            "allowed_highest_educations",
            "is_active",
            "priority",
            "created_at",
            "updated_at",
        ]
        read_only_fields = ["created_at", "updated_at"]

    def _tag_payload(self, obj, degree_type):
        links = sorted(
            [
                link
                for link in obj.tag_links.all()
                if link.degree_type == degree_type
            ],
            key=lambda link: (link.school_tag.code, link.school_tag_id),
        )
        return [
            {
                "id": link.school_tag_id,
                "code": link.school_tag.code,
                "name": link.school_tag.name,
            }
            for link in links
        ]

    def get_first_degree_tags(self, obj):
        return self._tag_payload(obj, m.SchoolTagRuleTag.DEGREE_FIRST)

    def get_highest_degree_tags(self, obj):
        return self._tag_payload(obj, m.SchoolTagRuleTag.DEGREE_HIGHEST)

    def to_representation(self, instance):
        data = super().to_representation(instance)
        selected = {link.education for link in instance.education_links.all()}
        data["allowed_highest_educations"] = [
            code
            for code, _label in m.Candidate.HIGHEST_EDUCATION_CHOICES
            if code in selected
        ]
        return data

    def _tags_for_ids(self, tag_ids, field_name):
        unique_ids = list(dict.fromkeys(tag_ids or []))
        tags_by_id = {
            tag.id: tag for tag in m.SchoolTag.objects.filter(id__in=unique_ids)
        }
        tags = [tags_by_id[tag_id] for tag_id in unique_ids if tag_id in tags_by_id]
        found_ids = {tag.id for tag in tags}
        missing = set(unique_ids) - found_ids
        if missing:
            raise serializers.ValidationError({field_name: "存在无效院校标签"})
        return tags

    def validate(self, attrs):
        attrs = super().validate(attrs)
        is_active = attrs.get(
            "is_active",
            self.instance.is_active if self.instance else True,
        )
        if not is_active:
            return attrs

        first_ids = attrs.get("first_degree_tag_ids")
        highest_ids = attrs.get("highest_degree_tag_ids")
        if self.instance:
            if first_ids is None:
                first_ids = list(
                    self.instance.tag_links.filter(
                        degree_type=m.SchoolTagRuleTag.DEGREE_FIRST
                    ).values_list("school_tag_id", flat=True)
                )
            if highest_ids is None:
                highest_ids = list(
                    self.instance.tag_links.filter(
                        degree_type=m.SchoolTagRuleTag.DEGREE_HIGHEST
                    ).values_list("school_tag_id", flat=True)
                )

        errors = {}
        if not first_ids:
            errors["first_degree_tag_ids"] = "启用规则至少需要一个第一学历标签"
        if not highest_ids:
            errors["highest_degree_tag_ids"] = "启用规则至少需要一个最高学历标签"
        if errors:
            raise serializers.ValidationError(errors)
        return attrs

    def _sync_tag_links(self, rule, degree_type, tags):
        if tags is None:
            return
        rule.tag_links.filter(degree_type=degree_type).delete()
        m.SchoolTagRuleTag.objects.bulk_create(
            [
                m.SchoolTagRuleTag(
                    rule=rule,
                    school_tag=tag,
                    degree_type=degree_type,
                )
                for tag in tags
            ]
        )

    def _sync_education_links(self, rule, educations):
        if educations is None:
            return
        rule.education_links.all().delete()
        m.SchoolTagRuleEducation.objects.bulk_create(
            [
                m.SchoolTagRuleEducation(rule=rule, education=education)
                for education in dict.fromkeys(educations)
            ]
        )

    def create(self, validated_data):
        first_ids = validated_data.pop("first_degree_tag_ids", [])
        highest_ids = validated_data.pop("highest_degree_tag_ids", [])
        educations = validated_data.pop("allowed_highest_educations", [])
        first_tags = self._tags_for_ids(first_ids, "first_degree_tag_ids")
        highest_tags = self._tags_for_ids(highest_ids, "highest_degree_tag_ids")
        rule = super().create(validated_data)
        self._sync_tag_links(rule, m.SchoolTagRuleTag.DEGREE_FIRST, first_tags)
        self._sync_tag_links(rule, m.SchoolTagRuleTag.DEGREE_HIGHEST, highest_tags)
        self._sync_education_links(rule, educations)
        return rule

    def update(self, instance, validated_data):
        first_ids = validated_data.pop("first_degree_tag_ids", None)
        highest_ids = validated_data.pop("highest_degree_tag_ids", None)
        educations = validated_data.pop("allowed_highest_educations", None)
        first_tags = (
            self._tags_for_ids(first_ids, "first_degree_tag_ids")
            if first_ids is not None
            else None
        )
        highest_tags = (
            self._tags_for_ids(highest_ids, "highest_degree_tag_ids")
            if highest_ids is not None
            else None
        )
        rule = super().update(instance, validated_data)
        self._sync_tag_links(rule, m.SchoolTagRuleTag.DEGREE_FIRST, first_tags)
        self._sync_tag_links(rule, m.SchoolTagRuleTag.DEGREE_HIGHEST, highest_tags)
        self._sync_education_links(rule, educations)
        return rule


class CandidateWorkflowSerializer(serializers.ModelSerializer):
    candidate_name = serializers.CharField(source="candidate.name", read_only=True)
    phone = serializers.CharField(source="candidate.phone", read_only=True)
    current_resume = serializers.SerializerMethodField()
    current_apply_id = serializers.SerializerMethodField()
    current_position_name = serializers.SerializerMethodField()
    current_rank = serializers.SerializerMethodField()

    def _display_resume(self, obj):
        if hasattr(obj, "_display_resume_cache"):
            return obj._display_resume_cache
        if obj.current_resume_id:
            resume = obj.current_resume
        elif obj.passed_attempt_id and obj.passed_attempt:
            resume = obj.passed_attempt.resume
        else:
            latest_attempt = (
                obj.attempts.select_related("resume")
                .order_by("-attempt_no", "-created_at", "-id")
                .first()
            )
            resume = latest_attempt.resume if latest_attempt else None
        obj._display_resume_cache = resume
        return resume

    def get_current_resume(self, obj):
        resume = self._display_resume(obj)
        return resume.id if resume else None

    def get_current_apply_id(self, obj):
        resume = self._display_resume(obj)
        return resume.apply_id if resume else ""

    def get_current_position_name(self, obj):
        resume = self._display_resume(obj)
        return resume.position_name if resume else ""

    def get_current_rank(self, obj):
        if obj.current_rank:
            return obj.current_rank
        resume = self._display_resume(obj)
        return resume.volunteer_rank if resume else None

    class Meta:
        model = m.CandidateWorkflow
        fields = [
            "id",
            "candidate",
            "candidate_name",
            "phone",
            "status",
            "current_resume",
            "current_apply_id",
            "current_position_name",
            "current_rank",
            "dispatch_strategy",
            "archive_reason",
            "archive_detail",
            "passed_attempt",
            "started_at",
            "completed_at",
            "created_at",
            "updated_at",
        ]


class AssignmentHandlingEventSerializer(serializers.ModelSerializer):
    event_type_label = serializers.CharField(source="get_event_type_display", read_only=True)
    from_department_name = serializers.SerializerMethodField()
    to_department_name = serializers.SerializerMethodField()
    metadata = serializers.SerializerMethodField()

    class Meta:
        model = m.AssignmentHandlingEvent
        fields = [
            "id",
            "event_type",
            "event_type_label",
            "from_department",
            "from_department_name",
            "to_department",
            "to_department_name",
            "actor",
            "actor_username_snapshot",
            "note",
            "batch_operation_id",
            "is_system_auto",
            "metadata",
            "occurred_at",
        ]
        read_only_fields = fields

    def get_from_department_name(self, obj):
        return obj.from_department_name_snapshot or (
            obj.from_department.name if obj.from_department else ""
        )

    def get_to_department_name(self, obj):
        return obj.to_department_name_snapshot or (
            obj.to_department.name if obj.to_department else ""
        )

    def get_metadata(self, obj):
        permissions = self.context.get("permission_codes")
        if permissions is None:
            request = self.context.get("request")
            permissions = user_permission_codes(request.user) if request else set()
        if "attempt.view_all" in permissions:
            return obj.metadata
        metadata = obj.metadata if isinstance(obj.metadata, dict) else {}
        allowed = {
            "enabled",
            "delivery_status",
            "recipient_count",
            "skipped_reason",
            "error",
        }
        if isinstance(metadata.get("welink"), dict):
            return {
                "welink": {
                    key: value
                    for key, value in metadata["welink"].items()
                    if key in allowed
                }
            }
        return {key: value for key, value in metadata.items() if key in allowed}


class AssignmentAttemptSerializer(serializers.ModelSerializer):
    candidate_name = serializers.CharField(
        source="resume.candidate.name", read_only=True
    )
    apply_id = serializers.CharField(source="resume.apply_id", read_only=True)
    position_name = serializers.CharField(source="resume.position_name", read_only=True)
    volunteer_rank = serializers.IntegerField(
        source="resume.volunteer_rank", read_only=True
    )
    initial_department_name = serializers.SerializerMethodField()
    current_department_name = serializers.SerializerMethodField()
    primary_department_id = serializers.SerializerMethodField()
    primary_department_name = serializers.SerializerMethodField()
    handling_events = serializers.SerializerMethodField()
    matched_rule_name = serializers.CharField(
        source="matched_rule.name", read_only=True, default=""
    )
    match_reason = serializers.SerializerMethodField()
    agent_decision_summary = serializers.SerializerMethodField()
    agent_decision = serializers.SerializerMethodField()

    def _permission_codes(self):
        permissions = self.context.get("permission_codes")
        if permissions is not None:
            return permissions
        if not hasattr(self, "_resolved_permission_codes"):
            request = self.context.get("request")
            self._resolved_permission_codes = (
                user_permission_codes(request.user) if request else set()
            )
        return self._resolved_permission_codes

    def _can_view_all_attempts(self):
        return "attempt.view_all" in self._permission_codes()

    def get_match_reason(self, obj):
        return _public_match_reason(obj)

    def get_initial_department_name(self, obj):
        return obj.initial_department_name_snapshot or obj.initial_department.name

    def get_current_department_name(self, obj):
        return obj.current_department_name_snapshot or obj.current_department.name

    def get_primary_department_id(self, obj):
        primary = resolve_department_hierarchy(obj.current_department).primary
        return primary.id if primary else None

    def get_primary_department_name(self, obj):
        primary = resolve_department_hierarchy(obj.current_department).primary
        return primary.name if primary else ""

    def get_handling_events(self, obj):
        events = list(obj.handling_events.all())
        context = {**self.context, "permission_codes": self._permission_codes()}
        payload = AssignmentHandlingEventSerializer(
            events, many=True, context=context
        ).data
        previous_at = None
        for item, event in zip(payload, events):
            item["duration_since_previous_seconds"] = (
                max(0, int((event.occurred_at - previous_at).total_seconds()))
                if previous_at
                else None
            )
            previous_at = event.occurred_at
        return payload

    def get_agent_decision_summary(self, obj):
        if not self._can_view_all_attempts():
            return None
        decision = obj.agent_decision
        if not decision:
            return None
        return {
            "id": decision.id,
            "recommendation": decision.recommendation,
            "recommended_job": decision.recommended_job_id,
            "matched_job_category": decision.matched_job_category,
            "confidence_score": decision.confidence_score,
            "score_breakdown": decision.score_breakdown,
            "summary": decision.summary,
            "reason": decision.reason,
            "evidence": decision.evidence,
            "risks": decision.risks,
            "risk_flags": decision.risk_flags,
            "error_code": _public_reason_code(decision.error_code),
            "error_message": _public_ai_message(decision.error_message),
        }

    def get_agent_decision(self, obj):
        return obj.agent_decision_id if self._can_view_all_attempts() else None

    class Meta:
        model = m.AssignmentAttempt
        fields = [
            "id",
            "workflow",
            "resume",
            "candidate_name",
            "apply_id",
            "position_name",
            "volunteer_rank",
            "attempt_no",
            "source",
            "status",
            "initial_department",
            "initial_department_name",
            "current_department",
            "current_department_name",
            "primary_department_id",
            "primary_department_name",
            "matched_rule",
            "matched_rule_name",
            "agent_decision",
            "agent_decision_summary",
            "confidence_score",
            "review_required",
            "match_mode",
            "match_reason",
            "dispatched_at",
            "feedback_result",
            "feedback_reason_code",
            "feedback_reason_label_snapshot",
            "feedback_note",
            "feedback_at",
            "cancelled_at",
            "cancel_reason",
            "manual_reason",
            "initial_department_name_snapshot",
            "current_department_name_snapshot",
            "resume_apply_id_snapshot",
            "position_name_snapshot",
            "created_by_username_snapshot",
            "created_by",
            "capacity_reservation",
            "capacity_released_at",
            "handling_events",
            "created_at",
            "updated_at",
        ]
        read_only_fields = [
            "attempt_no",
            "status",
            "initial_department",
            "current_department",
            "matched_rule",
            "agent_decision",
            "confidence_score",
            "review_required",
            "match_mode",
            "match_reason",
            "dispatched_at",
            "feedback_result",
            "feedback_reason_code",
            "feedback_reason_label_snapshot",
            "feedback_note",
            "feedback_at",
            "cancelled_at",
            "cancel_reason",
            "created_by",
            "created_by_username_snapshot",
            "capacity_reservation",
            "capacity_released_at",
        ]


class ProcessingRunSerializer(serializers.ModelSerializer):
    stages = serializers.SerializerMethodField()
    scope_summary = serializers.JSONField(read_only=True)
    elapsed_seconds = serializers.SerializerMethodField()
    activity = serializers.SerializerMethodField()

    class Meta:
        model = m.ProcessingRun
        fields = [
            "id", "step", "mode", "status", "message", "scope_summary",
            "current_stage", "last_heartbeat_at", "activity",
            "created_by", "created_by_username_snapshot",
            "celery_task_id", "celery_group_id", "params",
            "total_count", "processed_count", "success_count", "failed_count",
            "completed_count", "needs_attention_count",
            "review_count", "dispatch_count", "archive_count", "skipped_count", "cancelled_count",
            "chunk_size", "chunk_total", "chunk_done", "chunk_failed", "chunk_errors",
            "ai_concurrency_limit", "ai_effective_concurrency",
            "ai_retry_count", "ai_rate_limit_count",
            "model_name", "prompt_version", "decision_version",
            "kernel_build", "protocol_version", "toolset_version",
            "result_schema_version", "policy_version",
            "model_config_revision", "pin_id",
            "job_hc_coefficient_snapshot",
            "created_at", "started_at", "finished_at", "elapsed_seconds",
            "error",
            "cancel_requested_at", "cancelled_at", "cancelled_by", "cancelled_by_username_snapshot",
            "stages",
        ]

    def get_elapsed_seconds(self, obj):
        end_at = obj.finished_at or timezone.now()
        return max(0, int((end_at - obj.created_at).total_seconds()))

    def get_stages(self, obj):
        from apps.pipeline.progress import STAGE_DESCRIPTIONS
        now = timezone.now()
        return [
            {
                "sequence": stage.sequence,
                "step": stage.step,
                "label": stage.label,
                "description": STAGE_DESCRIPTIONS.get(stage.step, ""),
                "status": stage.status,
                "total_count": stage.total_count,
                "processed_count": stage.processed_count,
                "success_count": stage.success_count,
                "completed_count": stage.completed_count,
                "needs_attention_count": stage.needs_attention_count,
                "failed_count": stage.failed_count,
                "review_count": stage.review_count,
                "dispatch_count": stage.dispatch_count,
                "archive_count": stage.archive_count,
                "skipped_count": stage.skipped_count,
                "cancelled_count": stage.cancelled_count,
                "message": stage.message,
                "error": stage.error,
                "started_at": stage.started_at,
                "finished_at": stage.finished_at,
                "elapsed_seconds": max(0, int(((stage.finished_at or now) - stage.started_at).total_seconds())) if stage.started_at else None,
            }
            for stage in obj.stages.all()
        ]

    def get_activity(self, obj):
        if obj.current_stage != "step4" or obj.status not in {"running", "cancelling", "waiting_conflict"}:
            return None
        counts = {row["status"]: row["count"] for row in obj.scope_items.values("status").annotate(count=Count("id"))}
        return {
            "processing": counts.get("processing", 0),
            "queued": counts.get("queued", 0) + counts.get("pending", 0),
            "waiting_conflict": counts.get("waiting_conflict", 0),
        }


class AgentDispatchDecisionSerializer(serializers.ModelSerializer):
    kernel_result = serializers.SerializerMethodField()

    def get_kernel_result(self, obj):
        from apps.pipeline.agent_kernel.presentation import decision_analysis
        detailed = getattr(self.context.get("view"), "action", None) == "retrieve"
        return decision_analysis(obj, detailed=detailed)

    candidate_name = serializers.CharField(
        source="resume.candidate.name", read_only=True
    )
    apply_id = serializers.CharField(source="resume.apply_id", read_only=True)
    position_name = serializers.CharField(source="resume.position_name", read_only=True)
    recommended_department_name = serializers.CharField(
        source="recommended_department.name", read_only=True, default=""
    )
    evaluated_job_name = serializers.SerializerMethodField()
    recommended_job_name = serializers.SerializerMethodField()

    def _job_name(self, job):
        if not job:
            return ""
        return job.public_name or job.position_name or f"Job#{job.pk}"

    def get_evaluated_job_name(self, obj):
        return self._job_name(obj.evaluated_job)

    def get_recommended_job_name(self, obj):
        return self._job_name(obj.recommended_job)

    def to_representation(self, instance):
        data = super().to_representation(instance)
        data["error_code"] = _public_reason_code(data.get("error_code"))
        data["error_message"] = _public_ai_message(data.get("error_message"))
        return data

    class Meta:
        model = m.AgentDispatchDecision
        fields = [
            "id",
            "workflow",
            "resume",
            "processing_run",
            "candidate_name",
            "apply_id",
            "position_name",
            "profile",
            "recommendation",
            "evaluated_job",
            "evaluated_job_name",
            "recommended_job",
            "recommended_job_name",
            "matched_job_category",
            "recommended_department",
            "recommended_department_name",
            "confidence_score",
            "score_breakdown",
            "summary",
            "reason",
            "evidence",
            "risks",
            "risk_flags",
            "error_code",
            "error_message",
            "model_name",
            "prompt_version",
            "decision_version",
            "kernel_pin_id",
            "kernel_build",
            "protocol_version",
            "toolset_version",
            "safe_trace",
            "kernel_result",
            "created_at",
        ]
