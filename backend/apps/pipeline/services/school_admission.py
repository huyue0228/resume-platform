"""院校标签准入判断。

Rule 和 AI 分配共用同一套硬规则：存在启用规则时，候选人的第一学历
标签、最高学历标签和允许最高学历必须同时命中同一条规则；没有启用规则时通过。
"""
from dataclasses import dataclass
from typing import Optional

from apps.core import models as m


@dataclass(frozen=True)
class SchoolAdmissionResult:
    passed: bool
    matched_rule: Optional[m.SchoolTagRule]
    has_active_rules: bool
    failure_detail: str = ""
    reason_code: str = ""


def active_rules():
    return list(
        m.SchoolTagRule.objects.filter(is_active=True)
        .prefetch_related("tag_links__school_tag", "education_links")
        .order_by("priority", "id")
    )


def rule_matches_candidate(candidate, rule):
    first_tag_ids = set()
    highest_tag_ids = set()
    for link in rule.tag_links.all():
        if link.degree_type == m.SchoolTagRuleTag.DEGREE_FIRST:
            first_tag_ids.add(link.school_tag_id)
        elif link.degree_type == m.SchoolTagRuleTag.DEGREE_HIGHEST:
            highest_tag_ids.add(link.school_tag_id)
    tags_match = (
        candidate.first_degree_tag_id in first_tag_ids
        and candidate.highest_degree_tag_id in highest_tag_ids
    )
    if not tags_match:
        return False
    allowed_educations = {
        link.education for link in rule.education_links.all()
    }
    return not allowed_educations or candidate.highest_education in allowed_educations


def evaluate(candidate, rules=None):
    rules = active_rules() if rules is None else list(rules)
    if not rules:
        return SchoolAdmissionResult(
            passed=True,
            matched_rule=None,
            has_active_rules=False,
        )
    tag_matched_restricted_rules = []
    for rule in rules:
        if rule_matches_candidate(candidate, rule):
            return SchoolAdmissionResult(
                passed=True,
                matched_rule=rule,
                has_active_rules=True,
            )
        first_tag_ids = {
            link.school_tag_id
            for link in rule.tag_links.all()
            if link.degree_type == m.SchoolTagRuleTag.DEGREE_FIRST
        }
        highest_tag_ids = {
            link.school_tag_id
            for link in rule.tag_links.all()
            if link.degree_type == m.SchoolTagRuleTag.DEGREE_HIGHEST
        }
        if (
            candidate.first_degree_tag_id in first_tag_ids
            and candidate.highest_degree_tag_id in highest_tag_ids
            and list(rule.education_links.all())
        ):
            tag_matched_restricted_rules.append(rule)
    if tag_matched_restricted_rules and not candidate.highest_education:
        failure_detail = "候选人最高学历缺失，不符合已命中的院校准入规则"
        reason_code = "education_not_eligible"
    elif tag_matched_restricted_rules:
        failure_detail = "候选人最高学历不在已命中院校准入规则的允许范围"
        reason_code = "education_not_eligible"
    else:
        failure_detail = "候选人第一学历标签和最高学历标签未命中任何启用规则"
        reason_code = "school_not_eligible"
    return SchoolAdmissionResult(
        passed=False,
        matched_rule=None,
        has_active_rules=True,
        failure_detail=failure_detail,
        reason_code=reason_code,
    )
