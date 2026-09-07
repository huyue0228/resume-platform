"""历史 Rule 记录续办兼容策略；新任务只允许进入 Agent Kernel。"""

from apps.core import models as m

from .services.job_mapping import JobMappingError, normalized, resolve_job_pool


class RuleStrategy:
    """续办历史 Rule 尝试时执行确定性专业校验。

    Rule 策略仍然不做 AI 语义扩展，但会使用 HR 可维护的专业大类词表做
    第一层确定性归类：候选人最高学历专业和岗位需求专业映射到同一启用
    大类时放行；词表不能证明通过时，再退回专业名双向包含兜底。返回的
    reason 会进入 Resume.category_reason，并被分配尝试的 match_reason
    复用，方便 HR 追溯为什么命中某个岗位。
    """

    mode = "rule"
    WILDCARD_MAJORS = {"专业不限", "不限", "不限专业"}

    def __init__(self):
        self._major_alias_cache = None

    def _normalized(self, value):
        """统一文本比较口径：忽略大小写和空白，但保留原始语义。"""
        return normalized(value)

    def _is_wildcard_major(self, value):
        """判断岗位需求专业是否表达“专业不限”。

        “相关专业”不属于这里的通配词，它只会作为可维护别名参与词表匹配；
        默认停用的 OTHER_GENERAL 不会让它自动放行。
        """
        return self._normalized(value) in {
            self._normalized(item) for item in self.WILDCARD_MAJORS
        }

    def _major_categories_for_text(self, value):
        """把一个专业文本解析为启用的专业大类列表。

        匹配过程只读取启用别名和启用大类：
        - exact：规范化文本完全一致。
        - contains：别名与输入专业允许双向包含，兼容“计算机类”和
          “计算机科学与技术”这类粒度不同的表达。

        这里故意不把未命中的文本归到“其他”，因为“其他/相关专业”过宽，
        一旦默认参与分配就会造成误放行。
        """
        normalized_value = self._normalized(value)
        if not normalized_value:
            return []
        if self._major_alias_cache is None:
            self._major_alias_cache = list(
                m.MajorAlias.objects.select_related("category")
                .filter(is_active=True, category__is_active=True)
                .order_by("category__sort_order", "category__code", "id")
            )
        categories = []
        seen = set()
        for alias in self._major_alias_cache:
            alias_name = alias.normalized_name or self._normalized(alias.name)
            if not alias_name:
                continue
            if alias.match_type == m.MajorAlias.MATCH_EXACT:
                matched = alias_name == normalized_value
            else:
                matched = alias_name in normalized_value or normalized_value in alias_name
            if matched and alias.category_id not in seen:
                seen.add(alias.category_id)
                categories.append(alias.category)
        return categories

    def _major_category_match_reason(self, candidate_major, required_major):
        """返回专业大类命中原因；未命中时返回空字符串。

        候选人专业和岗位需求专业都必须能映射到启用大类，且大类存在交集，
        才能用词表直接判定通过。若任一侧没映射、或映射后没有交集，调用方
        会继续执行专业名包含兜底。
        """
        candidate_categories = self._major_categories_for_text(candidate_major)
        required_categories = self._major_categories_for_text(required_major)
        required_ids = {category.id for category in required_categories}
        matched_categories = [
            category for category in candidate_categories if category.id in required_ids
        ]
        if not matched_categories:
            return ""
        names = "、".join(category.name for category in matched_categories)
        return f"专业大类匹配：{candidate_major} 与 {required_major} 同属 {names}"

    def _major_match_reason(self, resume, job):
        """校验候选人最高学历专业是否满足岗位需求专业。

        判断顺序与设计文档一致：
        1. 岗位没有维护需求专业时放行，避免历史岗位需求数据被误拦截。
        2. 需求专业是“专业不限 / 不限 / 不限专业”时放行。
        3. 有非通配需求专业时，候选人最高学历专业缺失不自动放行。
        4. 先用启用专业词表匹配大类；大类能证明同类时放行。
        5. 词表无交集或任一侧未映射时，保留原专业名双向包含兜底。
        """
        required_majors = [major.major for major in job.majors.all() if major.major]
        if not required_majors:
            return True, "岗位未配置需求专业，放行"
        if any(self._is_wildcard_major(major) for major in required_majors):
            return True, "岗位需求专业为不限，放行"

        candidate_major_text = resume.candidate.highest_major or ""
        candidate_major = self._normalized(candidate_major_text)
        if not candidate_major:
            return False, "候选人最高学历专业缺失，未命中岗位需求专业"
        for required_major in required_majors:
            category_reason = self._major_category_match_reason(
                candidate_major_text, required_major
            )
            if category_reason:
                return True, category_reason

            normalized_required = self._normalized(required_major)
            if normalized_required and (
                normalized_required in candidate_major
                or candidate_major in normalized_required
            ):
                return (
                    True,
                    f"专业匹配（名称兜底）：{candidate_major_text} 命中 {required_major}",
                )
        return False, "候选人最高学历专业未命中岗位需求专业"

    def filter_major_eligible_jobs(self, resume, jobs, mapping):
        """在已完成精确映射的岗位池上执行 Rule 专业审核。"""
        eligible = []
        major_reasons = {}
        for job in jobs:
            matched, major_reason = self._major_match_reason(resume, job)
            if matched:
                eligible.append(job)
                major_reasons[job.id] = major_reason
        if not eligible:
            candidate_major = (resume.candidate.highest_major or "").strip() or "未填写"
            raise JobMappingError(
                "major_not_matched",
                f"已映射内部职位“{mapping['internal_name']}”，但最高学历专业"
                f"“{candidate_major}”不符合岗位需求专业",
            )
        return eligible, {**mapping, "major_reasons": major_reasons}

    def classify(self, resume, jobs):
        """按严格职位映射返回稳定的首个专业合格岗位。"""
        try:
            jobs, mapping = resolve_job_pool(resume, jobs)
            jobs, mapping = self.filter_major_eligible_jobs(resume, jobs, mapping)
        except JobMappingError as exc:
            return None, "未匹配", exc.detail
        job = jobs[0]
        reason = (
            f"对外职位名称精确映射内部职位：{mapping['internal_name']}；"
            f"岗位名精确命中对外发布名称：{mapping['public_name']}；"
            f"{mapping['major_reasons'].get(job.id, '')}"
        ).rstrip("；")
        return job, job.category or "未分类", reason


def get_rule_strategy():
    return RuleStrategy()
