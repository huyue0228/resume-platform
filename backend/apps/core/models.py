"""领域模型，对应《数据库设计》。所有数据同处一个池，按时间标签筛选，不分批次。"""
from django.conf import settings
from django.db import models


class Department(models.Model):
    """部门（树形：一层 / 二层 / 三级）。"""

    name = models.CharField(max_length=128)
    level = models.PositiveSmallIntegerField(default=2, help_text="1=一层, 2=二层, 3=三级")
    parent = models.ForeignKey(
        "self", null=True, blank=True, on_delete=models.PROTECT, related_name="children"
    )
    entity = models.CharField(max_length=64, blank=True, help_text="招聘主体")

    class Meta:
        unique_together = ("name", "parent")

    def __str__(self):
        return self.name


class Contact(models.Model):
    """部门接口人。"""

    LEVEL_SECONDARY = "secondary"
    LEVEL_TERTIARY = "tertiary"
    LEVEL_CHOICES = [
        (LEVEL_SECONDARY, "二级接口人"),
        (LEVEL_TERTIARY, "三级接口人"),
    ]

    name = models.CharField(max_length=64)
    name_pinyin = models.CharField(max_length=128, blank=True)
    name_pinyin_initials = models.CharField(max_length=32, blank=True)
    employee_no = models.CharField(max_length=32, unique=True, help_text="工号")
    email = models.EmailField(blank=True, default="", help_text="公司邮箱")
    department = models.ForeignKey(
        Department, null=True, blank=True, on_delete=models.SET_NULL, related_name="contacts"
    )
    contact_level = models.CharField(
        max_length=16, choices=LEVEL_CHOICES, default=LEVEL_SECONDARY
    )
    can_delegate = models.BooleanField(default=True, help_text="二级接口人是否可转派")
    is_active = models.BooleanField(default=True)

    def save(self, *args, **kwargs):
        from .name_pinyin import name_to_pinyin

        self.email = str(self.email or "").strip().casefold()
        self.name_pinyin, self.name_pinyin_initials = name_to_pinyin(self.name)
        update_fields = kwargs.get("update_fields")
        if update_fields is not None:
            update_fields = set(update_fields)
            if "name" in update_fields:
                update_fields |= {"name_pinyin", "name_pinyin_initials"}
            kwargs["update_fields"] = update_fields
        super().save(*args, **kwargs)

    class Meta:
        indexes = [
            models.Index(fields=["name"]),
            models.Index(fields=["name_pinyin"]),
            models.Index(fields=["name_pinyin_initials"]),
        ]

    def __str__(self):
        return f"{self.name}({self.employee_no})"


class SchoolTag(models.Model):
    """院校标签字典。"""

    code = models.CharField(max_length=64, unique=True)
    name = models.CharField(max_length=64)
    is_default = models.BooleanField(default=False)
    is_active = models.BooleanField(default=True)

    class Meta:
        ordering = ["code", "id"]
        indexes = [
            models.Index(fields=["code", "is_active"]),
            models.Index(fields=["is_default"]),
        ]

    def __str__(self):
        return self.name


class School(models.Model):
    """院校清单。"""

    name = models.CharField(max_length=128, unique=True, help_text="学校")
    name_pinyin = models.CharField(max_length=256, blank=True)
    name_pinyin_initials = models.CharField(max_length=64, blank=True)
    platform = models.CharField(max_length=64, blank=True, help_text="平台标签")
    province = models.CharField(max_length=32, blank=True)
    school_tag = models.ForeignKey(
        SchoolTag,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="schools",
    )

    def save(self, *args, **kwargs):
        from .name_pinyin import name_to_pinyin

        self.name_pinyin, self.name_pinyin_initials = name_to_pinyin(self.name)
        update_fields = kwargs.get("update_fields")
        if update_fields is not None and "name" in update_fields:
            kwargs["update_fields"] = set(update_fields) | {
                "name_pinyin",
                "name_pinyin_initials",
            }
        super().save(*args, **kwargs)

    class Meta:
        indexes = [
            models.Index(fields=["name"]),
            models.Index(fields=["name_pinyin"]),
            models.Index(fields=["name_pinyin_initials"]),
        ]

    def __str__(self):
        return self.name


class Job(models.Model):
    """岗位需求（即校招岗位分类及专业要求 / 结构化需求表）。"""

    entity = models.CharField(max_length=64, blank=True, help_text="主体")
    department = models.ForeignKey(
        Department, null=True, blank=True, on_delete=models.SET_NULL, related_name="jobs"
    )
    category = models.CharField(max_length=64, blank=True, help_text="岗位类别")
    public_name = models.CharField(max_length=128, blank=True, help_text="对外发布名称")
    is_public = models.BooleanField(default=True, help_text="是否对外发布")
    position_name = models.CharField(max_length=128, blank=True, help_text="职位名称")
    job_family = models.CharField(max_length=64, blank=True, help_text="岗位族")
    location = models.CharField(max_length=64, blank=True, help_text="工作地点")
    education = models.CharField(max_length=32, blank=True, help_text="学历要求")
    responsibilities = models.TextField(blank=True, default="", help_text="工作职责")
    headcount = models.PositiveIntegerField(default=0, help_text="需求数量(HC)")
    is_active = models.BooleanField(default=True)

    class Meta:
        indexes = [
            models.Index(fields=["is_active", "entity"]),
            models.Index(fields=["is_active", "category"]),
        ]

    def __str__(self):
        return self.public_name or self.position_name or f"Job#{self.pk}"

    def save(self, *args, **kwargs):
        if self.department_id and not Department.objects.filter(
            pk=self.department_id,
            level=2,
        ).exists():
            raise ValueError("岗位必须绑定二级部门")
        super().save(*args, **kwargs)


class JobMajor(models.Model):
    """岗位需求专业（多值）。"""

    job = models.ForeignKey(Job, on_delete=models.CASCADE, related_name="majors")
    major = models.CharField(max_length=64)

    def __str__(self):
        return self.major


class MajorCategory(models.Model):
    """专业大类词表的一级分类。

    这个模型只保存可维护主数据，不直接挂到候选人或岗位上。分配时会按
    当前启用词表即时解析专业文本，历史分配结果则通过 match_reason 保留
    当时命中的大类名称，避免后续维护词表时影响历史审计。
    """

    code = models.CharField(max_length=64, unique=True)
    name = models.CharField(max_length=64)
    description = models.TextField(blank=True)
    is_active = models.BooleanField(default=True)
    sort_order = models.IntegerField(default=0)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        ordering = ["sort_order", "code", "id"]
        indexes = [
            models.Index(fields=["is_active", "sort_order"]),
            models.Index(fields=["code", "is_active"]),
        ]

    def __str__(self):
        return self.name


class MajorAlias(models.Model):
    """专业大类的别名、关键词或原始专业名。

    `normalized_name` 由序列化器 / seed 命令按统一规则写入，分配时直接复用；
    内置项也允许用户编辑和停用。`category` 使用 PROTECT，确保一个大类仍有
    别名时不会被误删，符合“先删除或迁移别名，再删大类”的维护口径。
    """

    MATCH_EXACT = "exact"
    MATCH_CONTAINS = "contains"
    MATCH_TYPE_CHOICES = [
        (MATCH_EXACT, "精确匹配"),
        (MATCH_CONTAINS, "包含匹配"),
    ]

    SOURCE_BUILTIN = "builtin"
    SOURCE_USER = "user"
    SOURCE_IMPORT = "import"
    SOURCE_CHOICES = [
        (SOURCE_BUILTIN, "内置"),
        (SOURCE_USER, "人工维护"),
        (SOURCE_IMPORT, "导入"),
    ]

    category = models.ForeignKey(
        MajorCategory,
        on_delete=models.PROTECT,
        related_name="aliases",
    )
    name = models.CharField(max_length=128)
    normalized_name = models.CharField(max_length=128)
    match_type = models.CharField(
        max_length=16, choices=MATCH_TYPE_CHOICES, default=MATCH_CONTAINS
    )
    source = models.CharField(max_length=16, choices=SOURCE_CHOICES, default=SOURCE_USER)
    note = models.TextField(blank=True)
    is_active = models.BooleanField(default=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        ordering = ["category__sort_order", "category__code", "name", "id"]
        indexes = [
            models.Index(fields=["normalized_name", "is_active"]),
            models.Index(fields=["match_type", "is_active"]),
            models.Index(fields=["source"]),
        ]

    def __str__(self):
        return self.name


class Candidate(models.Model):
    """同学 / 人。以 identity_hash(规范化姓名+手机号) 全局唯一标识。"""

    EDUCATION_ASSOCIATE = "associate"
    EDUCATION_BACHELOR = "bachelor"
    EDUCATION_MASTER = "master"
    EDUCATION_DOCTOR = "doctor"
    HIGHEST_EDUCATION_CHOICES = [
        (EDUCATION_ASSOCIATE, "大专"),
        (EDUCATION_BACHELOR, "本科"),
        (EDUCATION_MASTER, "硕士"),
        (EDUCATION_DOCTOR, "博士"),
    ]

    identity_hash = models.CharField(max_length=64, unique=True)
    name = models.CharField(max_length=64)
    phone = models.CharField(max_length=32, blank=True)
    name_pinyin = models.CharField(max_length=128, blank=True)
    name_pinyin_initials = models.CharField(max_length=32, blank=True)
    gender = models.CharField(max_length=8, blank=True)
    household_province = models.CharField(max_length=32, blank=True, help_text="户口所在地")
    first_degree_school = models.CharField(max_length=128, blank=True)
    highest_degree_school = models.CharField(max_length=128, blank=True)
    highest_major = models.CharField(max_length=128, blank=True)
    highest_education = models.CharField(
        max_length=16,
        choices=HIGHEST_EDUCATION_CHOICES,
        blank=True,
        help_text="最高学历",
    )
    # Step2 院校分类与准入结果
    first_degree_platform = models.CharField(max_length=64, blank=True)
    highest_degree_platform = models.CharField(max_length=64, blank=True)
    first_degree_tag = models.ForeignKey(
        SchoolTag,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="first_degree_candidates",
    )
    highest_degree_tag = models.ForeignKey(
        SchoolTag,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="highest_degree_candidates",
    )
    school_tags = models.ManyToManyField(
        SchoolTag,
        blank=True,
        related_name="candidates",
        help_text="当前有效简历全部教育经历映射出的去重院校标签",
    )

    imported_at = models.DateTimeField(auto_now_add=True, help_text="导入时间（时间标签）")
    updated_at = models.DateTimeField(auto_now=True)

    def __str__(self):
        return self.name

    def save(self, *args, **kwargs):
        from .name_pinyin import name_to_pinyin

        self.name_pinyin, self.name_pinyin_initials = name_to_pinyin(self.name)
        update_fields = kwargs.get("update_fields")
        if update_fields is not None and "name" in update_fields:
            kwargs["update_fields"] = set(update_fields) | {
                "name_pinyin",
                "name_pinyin_initials",
            }
        super().save(*args, **kwargs)

    class Meta:
        indexes = [
            models.Index(fields=["name"]),
            models.Index(fields=["name_pinyin"]),
            models.Index(fields=["name_pinyin_initials"]),
        ]


class Resume(models.Model):
    """投递 / 应聘记录。以应聘ID 全局唯一。"""

    candidate = models.ForeignKey(Candidate, on_delete=models.CASCADE, related_name="resumes")
    apply_id = models.CharField(max_length=64, unique=True, help_text="应聘ID")
    resume_file = models.CharField(max_length=256, blank=True, help_text="简历包文件名")
    entity = models.CharField(max_length=64, blank=True, help_text="招聘主体")
    org = models.CharField(max_length=128, blank=True, help_text="所属机构")
    position_name = models.CharField(max_length=128, blank=True, help_text="对外职位名称")
    status = models.CharField(max_length=32, blank=True, default="待处理", help_text="应聘状态")
    apply_date = models.DateField(null=True, blank=True, help_text="应聘日期")
    # Step1 查重与志愿排序
    volunteer_rank = models.PositiveSmallIntegerField(null=True, blank=True, help_text="志愿次序 1-4")
    assigned_entity = models.CharField(max_length=16, blank=True, help_text="分配主体 GW/YLS")
    # Step3 岗位与分配前置检查固定的岗位分类结果
    job = models.ForeignKey(
        Job, null=True, blank=True, on_delete=models.SET_NULL, related_name="resumes"
    )
    job_category = models.CharField(max_length=64, blank=True, help_text="岗位类别标签")
    category_mode = models.CharField(max_length=8, blank=True, help_text="rule/ai")
    category_reason = models.TextField(blank=True, help_text="AI 模式可解释理由")

    imported_at = models.DateTimeField(auto_now_add=True, help_text="导入时间（时间标签）")
    updated_at = models.DateTimeField(auto_now=True)

    def __str__(self):
        return f"{self.candidate.name}-{self.position_name}"

    class Meta:
        indexes = [
            models.Index(fields=["imported_at"]),
        ]


class ResumeProfile(models.Model):
    """AI 策略读取 PDF 后形成的、可按文件和版本复用的结构化画像。"""

    resume = models.OneToOneField(Resume, on_delete=models.CASCADE, related_name="profile")
    file_checksum = models.CharField(max_length=64, blank=True, db_index=True)
    parse_model = models.CharField(max_length=32, blank=True)
    profile_version = models.CharField(max_length=32, blank=True)
    raw_text = models.TextField(blank=True)
    education_experiences = models.JSONField(default=list, blank=True)
    project_experiences = models.JSONField(default=list, blank=True)
    internship_experiences = models.JSONField(default=list, blank=True)
    skills = models.JSONField(default=list, blank=True)
    certificates = models.JSONField(default=list, blank=True)
    major_direction = models.CharField(max_length=128, blank=True)
    summary = models.TextField(blank=True)
    profile_risk_flags = models.JSONField(default=list, blank=True)
    parse_status = models.CharField(max_length=32, default="pending")
    parse_error = models.TextField(blank=True)
    parsed_at = models.DateTimeField(null=True, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)


class SchoolTagRule(models.Model):
    """院校标签准入规则。"""

    name = models.CharField(max_length=128)
    is_active = models.BooleanField(default=True)
    priority = models.IntegerField(default=0)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        ordering = ["priority", "id"]
        indexes = [
            models.Index(fields=["is_active", "priority"]),
        ]

    def __str__(self):
        return self.name


class SchoolTagRuleTag(models.Model):
    """院校准入规则标签明细。"""

    DEGREE_FIRST = "first"
    DEGREE_HIGHEST = "highest"
    DEGREE_CHOICES = [
        (DEGREE_FIRST, "第一学历"),
        (DEGREE_HIGHEST, "最高学历"),
    ]

    rule = models.ForeignKey(
        SchoolTagRule, on_delete=models.CASCADE, related_name="tag_links"
    )
    school_tag = models.ForeignKey(
        SchoolTag, on_delete=models.PROTECT, related_name="rule_links"
    )
    degree_type = models.CharField(max_length=16, choices=DEGREE_CHOICES)

    class Meta:
        unique_together = ("rule", "school_tag", "degree_type")
        indexes = [
            models.Index(fields=["rule", "degree_type"]),
            models.Index(fields=["school_tag", "degree_type"]),
        ]

    def __str__(self):
        return f"{self.rule}-{self.school_tag}-{self.degree_type}"


class SchoolTagRuleEducation(models.Model):
    """院校准入规则允许的候选人最高学历明细。"""

    rule = models.ForeignKey(
        SchoolTagRule, on_delete=models.CASCADE, related_name="education_links"
    )
    education = models.CharField(
        max_length=16, choices=Candidate.HIGHEST_EDUCATION_CHOICES
    )

    class Meta:
        constraints = [
            models.UniqueConstraint(
                fields=["rule", "education"],
                name="core_rule_education_unique",
            )
        ]
        indexes = [models.Index(fields=["rule", "education"])]

    def __str__(self):
        return f"{self.rule}-{self.get_education_display()}"


class CandidateWorkflow(models.Model):
    """候选人级分配流程。"""

    STATUS_PENDING = "pending"
    STATUS_IN_PROGRESS = "in_progress"
    STATUS_PASSED = "passed"
    STATUS_ARCHIVED = "archived"
    STATUS_CHOICES = [
        (STATUS_PENDING, "待分配"),
        (STATUS_IN_PROGRESS, "进行中"),
        (STATUS_PASSED, "已通过"),
        (STATUS_ARCHIVED, "已归档"),
    ]

    ARCHIVE_SCHOOL_RULE_NOT_MATCHED = "school_rule_not_matched"
    ARCHIVE_NO_NEXT_RESUME = "no_next_resume"
    ARCHIVE_JOB_NOT_MATCHED = "job_not_matched"
    ARCHIVE_JOB_MAPPING_AMBIGUOUS = "job_mapping_ambiguous"
    ARCHIVE_INTERNAL_POSITION_NAME_MISSING = "internal_position_name_missing"
    ARCHIVE_DEPARTMENT_NOT_FOUND = "department_not_found"
    ARCHIVE_AGENT_NO_RECOMMENDATION = "agent_no_recommendation"
    ARCHIVE_HR_CANCELLED = "hr_cancelled"
    ARCHIVE_ALL_REJECTED = "all_rejected"
    ARCHIVE_REASON_CHOICES = [
        (ARCHIVE_SCHOOL_RULE_NOT_MATCHED, "院校标签未命中规则"),
        (ARCHIVE_NO_NEXT_RESUME, "没有下一条可尝试志愿"),
        (ARCHIVE_JOB_NOT_MATCHED, "未匹配岗位"),
        (ARCHIVE_JOB_MAPPING_AMBIGUOUS, "对外职位映射到多个内部职位"),
        (ARCHIVE_INTERNAL_POSITION_NAME_MISSING, "内部职位名称缺失"),
        (ARCHIVE_DEPARTMENT_NOT_FOUND, "无有效二层部门"),
        (ARCHIVE_AGENT_NO_RECOMMENDATION, "AI 无有效建议"),
        (ARCHIVE_HR_CANCELLED, "HR 取消当前分配"),
        (ARCHIVE_ALL_REJECTED, "全部志愿未通过"),
    ]

    BLOCK_JOB_HC_EXHAUSTED = "job_hc_exhausted"
    BLOCK_REASON_CHOICES = [
        (BLOCK_JOB_HC_EXHAUSTED, "当前任务岗位 HC 容量已用尽"),
    ]

    candidate = models.OneToOneField(
        Candidate, on_delete=models.CASCADE, related_name="workflow"
    )
    status = models.CharField(
        max_length=32, choices=STATUS_CHOICES, default=STATUS_PENDING
    )
    current_resume = models.ForeignKey(
        Resume,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="current_workflows",
    )
    current_rank = models.PositiveSmallIntegerField(null=True, blank=True)
    dispatch_strategy = models.CharField(max_length=16, default="ai")
    archive_reason = models.CharField(
        max_length=64, choices=ARCHIVE_REASON_CHOICES, blank=True
    )
    archive_detail = models.TextField(blank=True)
    block_reason = models.CharField(
        max_length=64, choices=BLOCK_REASON_CHOICES, blank=True
    )
    block_detail = models.TextField(blank=True)
    passed_attempt = models.ForeignKey(
        "AssignmentAttempt",
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="passed_workflows",
    )
    started_at = models.DateTimeField(null=True, blank=True)
    completed_at = models.DateTimeField(null=True, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)
    revision = models.PositiveIntegerField(default=0)
    active_processing_scope_item = models.OneToOneField(
        "ProcessingRunScopeItem",
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="active_workflow",
    )
    active_processing_token = models.UUIDField(null=True, blank=True, editable=False)
    active_processing_expires_at = models.DateTimeField(null=True, blank=True)

    class Meta:
        indexes = [
            models.Index(fields=["status"]),
            models.Index(fields=["updated_at"]),
        ]

    def save(self, *args, **kwargs):
        """把候选人流程的每次写入变成可供后台任务校验的单调版本。"""
        self.revision = (self.revision or 0) + 1
        update_fields = kwargs.get("update_fields")
        if update_fields is not None:
            kwargs["update_fields"] = set(update_fields) | {"revision"}
        super().save(*args, **kwargs)

    def __str__(self):
        return f"{self.candidate.name}-{self.status}"


class AssignmentAttempt(models.Model):
    """候选人分配流程中的一次分配尝试。"""

    SOURCE_RULE = "rule"
    SOURCE_AI = "ai"
    SOURCE_MANUAL = "manual"
    SOURCE_CHOICES = [
        (SOURCE_RULE, "规则分配"),
        (SOURCE_AI, "AI 分配"),
        (SOURCE_MANUAL, "手动强制分配"),
    ]

    STATUS_PENDING_DISPATCH = "pending_dispatch"
    STATUS_PENDING_REVIEW = "pending_review"
    STATUS_DISPATCHED = "dispatched"
    STATUS_PASSED = "passed"
    STATUS_REJECTED = "rejected"
    STATUS_CANCELLED = "cancelled"
    STATUS_CHOICES = [
        (STATUS_PENDING_DISPATCH, "待下发"),
        (STATUS_PENDING_REVIEW, "待 HR 复核"),
        (STATUS_DISPATCHED, "已下发部门"),
        (STATUS_PASSED, "已通过"),
        (STATUS_REJECTED, "未通过"),
        (STATUS_CANCELLED, "已取消"),
    ]

    FEEDBACK_PASSED = "passed"
    FEEDBACK_REJECTED = "rejected"
    FEEDBACK_CHOICES = [
        (FEEDBACK_PASSED, "通过"),
        (FEEDBACK_REJECTED, "未通过"),
    ]

    REJECTION_REASON_MAJOR_BACKGROUND_MISMATCH = "major_background_mismatch"
    REJECTION_REASON_RESEARCH_EXPERIENCE_MISMATCH = "research_experience_mismatch"
    REJECTION_REASON_KEY_CAPABILITY_MISMATCH = "key_capability_mismatch"
    REJECTION_REASON_PROJECT_INTERNSHIP_MISMATCH = "project_internship_mismatch"
    REJECTION_REASON_POSITION_DIRECTION_MISMATCH = "position_direction_mismatch"
    REJECTION_REASON_OTHER = "other"
    REJECTION_REASON_CHOICES = [
        (REJECTION_REASON_MAJOR_BACKGROUND_MISMATCH, "专业背景不匹配"),
        (REJECTION_REASON_RESEARCH_EXPERIENCE_MISMATCH, "科研经历不符合"),
        (REJECTION_REASON_KEY_CAPABILITY_MISMATCH, "关键能力不匹配"),
        (REJECTION_REASON_PROJECT_INTERNSHIP_MISMATCH, "项目/实习经历不匹配"),
        (REJECTION_REASON_POSITION_DIRECTION_MISMATCH, "岗位方向不匹配"),
        (REJECTION_REASON_OTHER, "其他"),
    ]

    CANCEL_RERUN = "rerun"
    CANCEL_WORKFLOW_PASSED = "workflow_passed"
    CANCEL_MANUAL_REPLACED = "manual_replaced"

    workflow = models.ForeignKey(
        CandidateWorkflow, on_delete=models.CASCADE, related_name="attempts"
    )
    resume = models.ForeignKey(
        Resume, on_delete=models.PROTECT, related_name="assignment_attempts"
    )
    attempt_no = models.PositiveIntegerField()
    source = models.CharField(max_length=16, choices=SOURCE_CHOICES, default=SOURCE_AI)
    status = models.CharField(
        max_length=32, choices=STATUS_CHOICES, default=STATUS_PENDING_DISPATCH
    )
    initial_department = models.ForeignKey(
        Department,
        on_delete=models.PROTECT,
        related_name="initial_assignment_attempts",
        help_text="首次分配的二级部门，创建后不可变更",
    )
    current_department = models.ForeignKey(
        Department,
        on_delete=models.PROTECT,
        related_name="current_assignment_attempts",
        help_text="当前接收部门，可在有效转派时变更",
    )
    matched_rule = models.ForeignKey(
        SchoolTagRule,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="assignment_attempts",
    )
    agent_decision = models.ForeignKey(
        "AgentDispatchDecision",
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="assignment_attempts",
    )
    confidence_score = models.FloatField(null=True, blank=True)
    review_required = models.BooleanField(default=False)
    match_mode = models.CharField(max_length=8, blank=True, help_text="rule/ai")
    match_reason = models.TextField(blank=True)
    dispatched_at = models.DateTimeField(null=True, blank=True)
    feedback_result = models.CharField(
        max_length=16, choices=FEEDBACK_CHOICES, blank=True
    )
    feedback_note = models.TextField(blank=True)
    feedback_reason_code = models.CharField(
        max_length=64, choices=REJECTION_REASON_CHOICES, blank=True, db_index=True
    )
    feedback_reason_label_snapshot = models.CharField(max_length=64, blank=True)
    feedback_at = models.DateTimeField(null=True, blank=True)
    cancelled_at = models.DateTimeField(null=True, blank=True)
    cancel_reason = models.CharField(max_length=64, blank=True)
    manual_reason = models.TextField(blank=True)
    route_code = models.CharField(
        max_length=64,
        blank=True,
        db_index=True,
        help_text="特殊分流代码；普通分配为空",
    )
    special_route_confidence = models.FloatField(null=True, blank=True)
    special_route_evidence = models.JSONField(default=list, blank=True)
    special_route_config_snapshot = models.JSONField(default=dict, blank=True)
    initial_department_name_snapshot = models.CharField(max_length=128, blank=True)
    current_department_name_snapshot = models.CharField(max_length=128, blank=True)
    resume_apply_id_snapshot = models.CharField(max_length=64, blank=True)
    position_name_snapshot = models.CharField(max_length=128, blank=True)
    created_by_username_snapshot = models.CharField(max_length=150, blank=True)
    created_by = models.ForeignKey(
        settings.AUTH_USER_MODEL,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="assignment_attempts_created",
    )
    capacity_reservation = models.ForeignKey(
        "ProcessingRunJobCapacity",
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="attempts",
        help_text="自动分配占用的任务级岗位容量；手工强制分配为空",
    )
    capacity_released_at = models.DateTimeField(null=True, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        unique_together = ("workflow", "attempt_no")
        indexes = [
            models.Index(fields=["status"]),
            models.Index(fields=["current_department", "status"]),
            models.Index(fields=["initial_department", "status"]),
            models.Index(fields=["workflow", "status"]),
            models.Index(fields=["source", "status"]),
            models.Index(fields=["created_at", "source"]),
            models.Index(fields=["dispatched_at"]),
            models.Index(fields=["feedback_at"]),
        ]

    def __str__(self):
        return f"Attempt#{self.pk} {self.status}"

    def save(self, *args, **kwargs):
        if not self.initial_department_id or not Department.objects.filter(
            pk=self.initial_department_id, level=2
        ).exists():
            raise ValueError("首次分配部门必须是有效的二级部门")
        if not self.current_department_id or not Department.objects.filter(
            pk=self.current_department_id, level__in=[2, 3]
        ).exists():
            raise ValueError("当前接收部门必须是有效的二级或三级部门")
        if self.pk:
            original_initial_department_id = (
                type(self)
                .objects.filter(pk=self.pk)
                .values_list("initial_department_id", flat=True)
                .first()
            )
            if (
                original_initial_department_id is not None
                and original_initial_department_id != self.initial_department_id
            ):
                raise ValueError("首次分配部门创建后不可变更")
        super().save(*args, **kwargs)


class AssignmentHandlingEvent(models.Model):
    """分配尝试的追加式处理事件，用于审计和人工时效统计。"""

    EVENT_ATTEMPT_CREATED = "attempt_created"
    EVENT_REVIEW_CONFIRMED = "review_confirmed"
    EVENT_DEPARTMENT_DISPATCHED = "department_dispatched"
    EVENT_DEPARTMENT_TRANSFERRED = "department_transferred"
    EVENT_FEEDBACK_PASSED = "feedback_passed"
    EVENT_FEEDBACK_REJECTED = "feedback_rejected"
    EVENT_CANCELLED = "cancelled"
    EVENT_TYPE_CHOICES = [
        (EVENT_ATTEMPT_CREATED, "尝试创建"),
        (EVENT_REVIEW_CONFIRMED, "HR 确认复核"),
        (EVENT_DEPARTMENT_DISPATCHED, "下发部门"),
        (EVENT_DEPARTMENT_TRANSFERRED, "部门转派"),
        (EVENT_FEEDBACK_PASSED, "反馈通过"),
        (EVENT_FEEDBACK_REJECTED, "反馈不通过"),
        (EVENT_CANCELLED, "取消"),
    ]

    attempt = models.ForeignKey(
        AssignmentAttempt, on_delete=models.CASCADE, related_name="handling_events"
    )
    event_type = models.CharField(max_length=32, choices=EVENT_TYPE_CHOICES)
    from_department = models.ForeignKey(
        Department,
        null=True,
        blank=True,
        on_delete=models.PROTECT,
        related_name="assignment_events_from",
    )
    to_department = models.ForeignKey(
        Department,
        null=True,
        blank=True,
        on_delete=models.PROTECT,
        related_name="assignment_events_to",
    )
    from_department_name_snapshot = models.CharField(max_length=128, blank=True)
    to_department_name_snapshot = models.CharField(max_length=128, blank=True)
    actor_username_snapshot = models.CharField(max_length=150, blank=True)
    actor = models.ForeignKey(
        settings.AUTH_USER_MODEL,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="assignment_handling_events",
    )
    note = models.TextField(blank=True)
    batch_operation_id = models.UUIDField(null=True, blank=True, db_index=True)
    is_system_auto = models.BooleanField(default=False)
    metadata = models.JSONField(default=dict, blank=True)
    occurred_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        ordering = ["occurred_at", "id"]
        default_permissions = ("view",)
        indexes = [
            models.Index(fields=["attempt", "occurred_at"]),
            models.Index(fields=["event_type", "occurred_at"]),
            models.Index(fields=["to_department", "occurred_at"]),
        ]

    def save(self, *args, **kwargs):
        if self.pk:
            raise ValueError("处理事件是不可修改的审计记录")
        super().save(*args, **kwargs)

    def delete(self, *args, **kwargs):
        raise ValueError("处理事件是不可删除的审计记录")


class AgentDispatchDecision(models.Model):
    """AI Agent 对某候选人某个志愿尝试的分流建议。"""

    RECOMMEND_DISPATCH = "dispatch"
    RECOMMEND_REVIEW = "review"
    RECOMMEND_ARCHIVE = "archive"
    RECOMMEND_CHOICES = [
        (RECOMMEND_DISPATCH, "建议下发"),
        (RECOMMEND_REVIEW, "人工复核"),
        (RECOMMEND_ARCHIVE, "建议归档"),
    ]

    workflow = models.ForeignKey(
        CandidateWorkflow, on_delete=models.CASCADE, related_name="agent_decisions"
    )
    resume = models.ForeignKey(
        Resume, on_delete=models.CASCADE, related_name="agent_decisions"
    )
    profile = models.ForeignKey(
        ResumeProfile,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="agent_decisions",
    )
    processing_run = models.ForeignKey(
        "ProcessingRun",
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="agent_decisions",
    )
    recommendation = models.CharField(
        max_length=16, choices=RECOMMEND_CHOICES, null=True, blank=True
    )
    evaluated_job = models.ForeignKey(
        Job,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="agent_evaluations",
    )
    recommended_job = models.ForeignKey(
        Job,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="agent_recommendations",
    )
    matched_job_category = models.CharField(max_length=64, blank=True)
    recommended_department = models.ForeignKey(
        Department,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="agent_decisions",
    )
    confidence_score = models.FloatField(null=True, blank=True)
    score_breakdown = models.JSONField(default=dict, blank=True)
    summary = models.TextField(blank=True)
    reason = models.TextField(blank=True)
    evidence = models.JSONField(default=list, blank=True)
    risks = models.JSONField(default=list, blank=True)
    risk_flags = models.JSONField(default=list, blank=True)
    ai_specialist_match = models.BooleanField(default=False)
    ai_specialist_confidence = models.FloatField(null=True, blank=True)
    ai_specialist_evidence = models.JSONField(default=list, blank=True)
    special_route_applied = models.BooleanField(default=False)
    special_route_config_snapshot = models.JSONField(default=dict, blank=True)
    error_code = models.CharField(max_length=64, blank=True)
    error_message = models.TextField(blank=True)
    model_name = models.CharField(max_length=64, blank=True)
    prompt_version = models.CharField(max_length=128, blank=True)
    decision_version = models.CharField(max_length=128, blank=True)
    kernel_pin_id = models.CharField(max_length=64, blank=True, db_index=True)
    kernel_build = models.CharField(max_length=128, blank=True)
    protocol_version = models.CharField(max_length=32, blank=True)
    toolset_version = models.CharField(max_length=128, blank=True)
    safe_trace = models.JSONField(default=dict, blank=True)
    kernel_result = models.JSONField(default=dict, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        indexes = [
            models.Index(fields=["workflow", "created_at"]),
            models.Index(fields=["recommendation", "confidence_score"]),
            models.Index(fields=["created_at", "error_code"]),
        ]


class ProcessingRun(models.Model):
    """流水线处理任务记录（Celery 跟踪）。"""

    scope = models.JSONField(default=dict, blank=True, help_text="处理范围/筛选条件")
    scope_summary = models.JSONField(default=dict, blank=True, help_text="可对外展示的处理范围摘要")
    step = models.CharField(max_length=16)
    mode = models.CharField(max_length=8, default="ai")
    status = models.CharField(max_length=24, default="pending")
    current_stage = models.CharField(max_length=32, blank=True)
    celery_task_id = models.CharField(max_length=64, blank=True)
    celery_group_id = models.CharField(max_length=64, blank=True)
    params = models.JSONField(default=dict, blank=True)
    message = models.TextField(blank=True)
    total_count = models.PositiveIntegerField(default=0)
    processed_count = models.PositiveIntegerField(default=0)
    success_count = models.PositiveIntegerField(default=0)
    completed_count = models.PositiveIntegerField(default=0)
    needs_attention_count = models.PositiveIntegerField(default=0)
    failed_count = models.PositiveIntegerField(default=0)
    review_count = models.PositiveIntegerField(default=0)
    dispatch_count = models.PositiveIntegerField(default=0)
    archive_count = models.PositiveIntegerField(default=0)
    skipped_count = models.PositiveIntegerField(default=0)
    cancelled_count = models.PositiveIntegerField(default=0)
    chunk_size = models.PositiveIntegerField(null=True, blank=True)
    chunk_total = models.PositiveIntegerField(null=True, blank=True)
    chunk_done = models.PositiveIntegerField(null=True, blank=True)
    chunk_failed = models.PositiveIntegerField(null=True, blank=True)
    chunk_errors = models.JSONField(default=list, blank=True)
    ai_concurrency_limit = models.PositiveSmallIntegerField(null=True, blank=True)
    ai_effective_concurrency = models.PositiveSmallIntegerField(null=True, blank=True)
    ai_retry_count = models.PositiveIntegerField(default=0)
    ai_rate_limit_count = models.PositiveIntegerField(default=0)
    model_name = models.CharField(max_length=64, blank=True)
    prompt_version = models.CharField(max_length=128, blank=True)
    decision_version = models.CharField(max_length=128, blank=True)
    kernel_build = models.CharField(max_length=128, blank=True)
    protocol_version = models.CharField(max_length=32, blank=True)
    toolset_version = models.CharField(max_length=128, blank=True)
    result_schema_version = models.CharField(max_length=32, blank=True)
    policy_version = models.CharField(max_length=128, blank=True)
    model_config_revision = models.CharField(max_length=128, blank=True)
    pin_id = models.CharField(max_length=64, blank=True, db_index=True)
    job_hc_coefficient_snapshot = models.PositiveSmallIntegerField(default=1)
    created_at = models.DateTimeField(auto_now_add=True)
    started_at = models.DateTimeField(null=True, blank=True)
    last_heartbeat_at = models.DateTimeField(null=True, blank=True)
    finished_at = models.DateTimeField(null=True, blank=True)
    created_by = models.ForeignKey(
        settings.AUTH_USER_MODEL,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="processing_runs_created",
    )
    created_by_username_snapshot = models.CharField(max_length=150, blank=True)
    cancel_requested_at = models.DateTimeField(null=True, blank=True)
    cancelled_at = models.DateTimeField(null=True, blank=True)
    cancelled_by = models.ForeignKey(
        settings.AUTH_USER_MODEL,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="processing_runs_cancelled",
    )
    cancelled_by_username_snapshot = models.CharField(max_length=150, blank=True)
    error = models.TextField(blank=True)

    class Meta:
        ordering = ["-created_at"]
        indexes = [
            models.Index(fields=["status", "created_at"]),
            models.Index(fields=["created_by", "status"]),
        ]


class ProcessingRunJobCapacity(models.Model):
    """处理任务创建时冻结的岗位 HC 容量及本任务占用量。"""

    run = models.ForeignKey(
        ProcessingRun, on_delete=models.CASCADE, related_name="job_capacities"
    )
    job = models.ForeignKey(
        Job, on_delete=models.PROTECT, related_name="processing_capacities"
    )
    headcount_snapshot = models.PositiveIntegerField(default=0)
    coefficient_snapshot = models.PositiveSmallIntegerField(default=1)
    capacity = models.PositiveIntegerField(default=0)
    used_count = models.PositiveIntegerField(default=0)

    class Meta:
        constraints = [
            models.UniqueConstraint(
                fields=["run", "job"], name="core_run_job_capacity_unique"
            ),
            models.CheckConstraint(
                check=models.Q(used_count__lte=models.F("capacity")),
                name="core_run_job_capacity_used_lte_capacity",
            ),
        ]
        indexes = [models.Index(fields=["run", "job"])]


class ProcessingRunStage(models.Model):
    """一个处理任务中的可观测阶段，支持 Policy/Agent 多阶段连续展示。"""

    run = models.ForeignKey(
        ProcessingRun, on_delete=models.CASCADE, related_name="stages"
    )
    sequence = models.PositiveSmallIntegerField()
    step = models.CharField(max_length=16)
    label = models.CharField(max_length=64)
    status = models.CharField(max_length=24, default="pending")
    total_count = models.PositiveIntegerField(default=0)
    processed_count = models.PositiveIntegerField(default=0)
    success_count = models.PositiveIntegerField(default=0)
    completed_count = models.PositiveIntegerField(default=0)
    needs_attention_count = models.PositiveIntegerField(default=0)
    failed_count = models.PositiveIntegerField(default=0)
    review_count = models.PositiveIntegerField(default=0)
    dispatch_count = models.PositiveIntegerField(default=0)
    archive_count = models.PositiveIntegerField(default=0)
    skipped_count = models.PositiveIntegerField(default=0)
    cancelled_count = models.PositiveIntegerField(default=0)
    message = models.TextField(blank=True)
    error = models.TextField(blank=True)
    started_at = models.DateTimeField(null=True, blank=True)
    finished_at = models.DateTimeField(null=True, blank=True)

    class Meta:
        ordering = ["sequence", "id"]
        unique_together = ("run", "sequence")
        indexes = [models.Index(fields=["run", "status"])]


class ProcessingRunScopeItem(models.Model):
    """提交任务时冻结的候选人范围和 Kernel 快照。"""
    kernel_snapshot = models.JSONField(default=dict, blank=True)
    kernel_result = models.JSONField(default=dict, blank=True)

    RESULT_COMPLETED = "completed"
    RESULT_NEEDS_ATTENTION = "needs_attention"
    RESULT_FAILED = "failed"
    RESULT_CANCELLED = "cancelled"
    RESULT_CHOICES = [
        (RESULT_COMPLETED, "处理完成"),
        (RESULT_NEEDS_ATTENTION, "需处理"),
        (RESULT_FAILED, "失败"),
        (RESULT_CANCELLED, "已取消"),
    ]

    run = models.ForeignKey(
        ProcessingRun, on_delete=models.CASCADE, related_name="scope_items"
    )
    candidate = models.ForeignKey(
        Candidate, on_delete=models.CASCADE, related_name="processing_scope_items"
    )
    workflow_revision_at_submit = models.PositiveIntegerField(null=True, blank=True)
    workflow_revision_at_prepare = models.PositiveIntegerField(null=True, blank=True)
    status = models.CharField(max_length=32, default="pending")
    skip_reason = models.CharField(max_length=64, blank=True)
    result_type = models.CharField(max_length=32, choices=RESULT_CHOICES, blank=True)
    reason_code = models.CharField(max_length=64, blank=True, db_index=True)
    result_message = models.TextField(blank=True)
    prepared_resume = models.ForeignKey(
        Resume,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="prepared_processing_items",
    )
    prepared_job = models.ForeignKey(
        Job,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="prepared_processing_items",
    )
    prepared_department = models.ForeignKey(
        Department,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="prepared_processing_items",
    )
    matched_rule = models.ForeignKey(
        SchoolTagRule,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="prepared_processing_items",
    )
    dispatch_token = models.UUIDField(null=True, blank=True, editable=False)
    attempt_count = models.PositiveIntegerField(default=0)
    queued_at = models.DateTimeField(null=True, blank=True)
    started_at = models.DateTimeField(null=True, blank=True)
    finished_at = models.DateTimeField(null=True, blank=True)
    error_code = models.CharField(max_length=64, blank=True)
    error_message = models.TextField(blank=True)
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        unique_together = ("run", "candidate")
        indexes = [
            models.Index(fields=["run", "candidate"]),
            models.Index(
                fields=["run", "status", "candidate"],
                name="core_scope_run_st_cand_idx",
            ),
            models.Index(
                fields=["run", "result_type", "reason_code"],
                name="core_scope_run_res_reason",
            ),
        ]


class UsagePageView(models.Model):
    """登录用户页面访问事件；仅保留稳定页面键和统计所需快照。"""

    event_id = models.UUIDField(unique=True, editable=False)
    session_id = models.UUIDField(editable=False)
    user = models.ForeignKey(
        settings.AUTH_USER_MODEL,
        null=True,
        blank=True,
        on_delete=models.SET_NULL,
        related_name="usage_page_views",
    )
    employee_no_snapshot = models.CharField(max_length=150)
    page_key = models.CharField(max_length=64)
    occurred_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        ordering = ["-occurred_at", "-id"]
        indexes = [
            models.Index(fields=["occurred_at"]),
            models.Index(fields=["page_key", "occurred_at"]),
            models.Index(fields=["session_id", "occurred_at"]),
        ]


class Config(models.Model):
    """键值配置。"""

    key = models.CharField(max_length=64, primary_key=True)
    value = models.JSONField(default=dict)

    def __str__(self):
        return self.key
