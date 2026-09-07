"""Step2 院校分类：按当前有效简历的教育经历生成候选人院校标签。"""

from apps.core import candidate_summary
from apps.core import models as m


NON_TARGET_TAG_NAME = "非目标院校"
NON_TARGET_TAG_CODE = "NON_TARGET"


def normalize_school_name(value):
    return "".join((value or "").lower().split())


def _non_target_school_tag():
    non_target = normalize_school_name(NON_TARGET_TAG_NAME)
    configured = next(
        (
            tag
            for tag in m.SchoolTag.objects.filter(is_active=True).order_by("id")
            if normalize_school_name(tag.name) == non_target
            or tag.code == NON_TARGET_TAG_CODE
        ),
        None,
    )
    if configured:
        return configured
    # 兼容未执行 seed_base 的旧环境，首次院校分类时补齐同一预置标签。
    return m.SchoolTag.objects.create(
        code=NON_TARGET_TAG_CODE,
        name=NON_TARGET_TAG_NAME,
        is_default=False,
        is_active=True,
    )


def _default_school_tag(non_target_tag):
    default_tag = m.SchoolTag.objects.filter(is_default=True, is_active=True).first()
    return default_tag or non_target_tag


def _school_tag(school, default_tag, non_target_tag):
    if school and school.school_tag:
        return school.school_tag
    # 不在导入院校清单中的学校统一视为非目标院校；清单中未配置标签时才用默认标签。
    if not school:
        return non_target_tag
    return default_tag


def _tag_name(tag, fallback="非目标院校"):
    return tag.name if tag else fallback


def _school_platform_name(tag, school):
    if tag:
        return _tag_name(tag)
    if school and school.platform:
        return school.platform
    return "非目标院校"


def _education_school_names(candidate):
    """返回当前有效简历中已知的全部院校名称，保留导入字段作为基础兜底。"""
    names = []
    seen = set()

    def add(value):
        text = str(value or "").strip()
        normalized = normalize_school_name(text)
        if text and normalized not in seen:
            seen.add(normalized)
            names.append(text)

    add(candidate.first_degree_school)
    add(candidate.highest_degree_school)
    resume = candidate_summary.current_resume(candidate)
    try:
        profile = resume.profile if resume else None
    except m.ResumeProfile.DoesNotExist:
        profile = None
    for experience in getattr(profile, "education_experiences", []) or []:
        if not isinstance(experience, dict):
            continue
        add(
            experience.get("school_name")
            or experience.get("school")
            or experience.get("institution")
        )
    return names


def sync_candidate_school_tags(candidate, school_map=None):
    """按所有已知教育经历重建候选人的去重多标签集合。"""
    if school_map is None:
        school_map = {
            normalize_school_name(school.name): school
            for school in m.School.objects.select_related("school_tag")
        }
    non_target_tag = _non_target_school_tag()
    default_tag = _default_school_tag(non_target_tag)
    tags = []
    seen_ids = set()
    for name in _education_school_names(candidate):
        school = school_map.get(normalize_school_name(name))
        tag = _school_tag(school, default_tag, non_target_tag)
        if tag and tag.id not in seen_ids:
            seen_ids.add(tag.id)
            tags.append(tag)
    candidate.school_tags.set(tags)
    return tags


def classify_candidates(candidates, *, overwrite=True):
    """按当前院校清单为指定候选人完整生成并固化院校标签。

    正式流水线在 Step2 使用 ``overwrite=True``，保证准入检查和固化标签来自
    同一版基础数据；Step3 与 Step4 不再调用本服务。``overwrite=False`` 仅为
    兼容内部工具保留，不属于正式处理流程。
    """
    count = 0
    schools = list(m.School.objects.select_related("school_tag"))
    school_map = {normalize_school_name(s.name): s for s in schools}
    non_target_tag = _non_target_school_tag()
    default_tag = _default_school_tag(non_target_tag)
    for cand in candidates:
        first_school = (
            school_map.get(normalize_school_name(cand.first_degree_school))
            if cand.first_degree_school
            else None
        )
        highest_school = (
            school_map.get(normalize_school_name(cand.highest_degree_school))
            if cand.highest_degree_school
            else None
        )
        first_tag = _school_tag(first_school, default_tag, non_target_tag)
        highest_tag = _school_tag(highest_school, default_tag, non_target_tag)
        should_update_first = overwrite or not cand.first_degree_tag_id
        should_update_highest = overwrite or not cand.highest_degree_tag_id
        # 正式 Step2 总是覆盖生成；非覆盖模式仅供兼容内部工具使用。
        should_update_first_platform = overwrite or not cand.first_degree_platform
        should_update_highest_platform = overwrite or not cand.highest_degree_platform
        if should_update_first:
            cand.first_degree_tag = first_tag
        if should_update_highest:
            cand.highest_degree_tag = highest_tag
        if should_update_first_platform:
            cand.first_degree_platform = _school_platform_name(
                cand.first_degree_tag, first_school
            )
        if should_update_highest_platform:
            cand.highest_degree_platform = _school_platform_name(
                cand.highest_degree_tag, highest_school
            )
        update_fields = []
        if should_update_first:
            update_fields.append("first_degree_tag")
        if should_update_highest:
            update_fields.append("highest_degree_tag")
        if should_update_first_platform:
            update_fields.append("first_degree_platform")
        if should_update_highest_platform:
            update_fields.append("highest_degree_platform")
        if update_fields:
            cand.save(update_fields=update_fields)
        sync_candidate_school_tags(cand, school_map)
        count += 1
    return count


def run(scope=None):
    count = classify_candidates(m.Candidate.objects.all())
    return f"已为 {count} 名候选人完成院校分类打标"
