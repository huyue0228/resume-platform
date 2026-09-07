"""冻结快照上的招聘规则；由业务平台维护，不调用模型或外部服务。"""
from copy import deepcopy


def normalized(value):
    return "".join(str(value or "").lower().split())


def prepare_snapshot(snapshot):
    s = deepcopy(snapshot)
    c = s["candidate"]
    if s.get("non_target_school_tag_ref"):
        schools = {normalized(x["name"]): x for x in s.get("schools", [])}
        for prefix in ("first", "highest"):
            name = normalized(c.get(f"{prefix}_degree_school"))
            school = schools.get(name) if name else None
            c[f"{prefix}_degree_tag_ref"] = (school.get("tag_ref") or s.get("default_school_tag_ref") or s["non_target_school_tag_ref"]) if school else s["non_target_school_tag_ref"]
    preferred = ""
    for province in (c.get("household_province", ""), c.get("highest_degree_province", ""), c.get("first_degree_province", "")):
        if any(n in province for n in "北京 天津 河北 山西 内蒙古 辽宁 吉林 黑龙江 山东 河南 陕西 甘肃 宁夏 新疆 青海".split()):
            preferred = "GW"
        elif any(n in province for n in "上海 江苏 浙江 安徽 福建 江西 湖北 湖南 广东 广西 海南 重庆 四川 贵州 云南 西藏".split()):
            preferred = "YLS"
        if preferred:
            break
    volunteers = sorted(s["volunteers"], key=lambda v: (not (preferred and v["entity"] == preferred), v["apply_date"] or "9999-12-31"))
    d = dict(first_degree_tag_ref=c.get("first_degree_tag_ref", ""), highest_degree_tag_ref=c.get("highest_degree_tag_ref", ""),
             volunteer_order=[v["ref"] for v in volunteers], current_volunteer_ref="", current_rank=0,
             admission_passed=False, admission_rule_ref="", job_refs=[], status="no_effective_volunteer")
    retry = s["workflow"].get("retry_volunteer_ref", "")
    current = next(((i, v) for i, v in enumerate(volunteers, 1) if not v["rejected"] and (not retry or v["ref"] == retry)), None)
    if current is None:
        d["admission_passed"] = True
        return d
    d["current_rank"], v = current
    d["current_volunteer_ref"] = v["ref"]
    rules = sorted(s["admission_rules"], key=lambda r: r["priority"])
    d["admission_passed"] = not rules
    tag_match = False
    for rule in rules:
        if d["first_degree_tag_ref"] in rule["first_tag_refs"] and d["highest_degree_tag_ref"] in rule["highest_tag_refs"]:
            tag_match = True
            if not rule["educations"] or c["highest_education"] in rule["educations"]:
                d.update(admission_passed=True, admission_rule_ref=rule["ref"])
                break
    if not d["admission_passed"]:
        d["status"] = "education_not_eligible" if tag_match else "school_not_eligible"
        return d
    jobs = s["jobs"]
    mapped = [j for j in jobs if normalized(v["position_name"]) and normalized(j["public_name"]) == normalized(v["position_name"]) and (not normalized(v["entity"]) or normalized(j["entity"]) == normalized(v["entity"]))]
    names, entities = {normalized(j["position_name"]) for j in mapped}, {normalized(j["entity"]) for j in mapped}
    if not mapped:
        d["status"] = "job_not_found"
    elif names == {""}:
        d["status"] = "internal_position_name_missing"
    elif len(names) != 1 or "" in names or len(entities) != 1:
        d["status"] = "job_mapping_ambiguous"
    else:
        d["job_refs"] = sorted(j["ref"] for j in jobs if normalized(j["entity"]) in entities and normalized(j["position_name"]) in names and j["department_ref"] and j["department_level"] == 2)
        d["status"] = "ready" if d["job_refs"] else "job_pool_empty"
        if any(not j.get("responsibilities", "").strip() for j in jobs if j["ref"] in d["job_refs"]):
            d["status"] = "job_responsibility_missing"
    return d
