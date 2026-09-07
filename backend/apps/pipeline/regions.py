"""运行时判定候选人南北：优先户籍省份，再看院校所在省份。"""
from apps.core import models as m

NORTH_PROVINCES = {
    "北京", "天津", "河北", "山西", "内蒙古", "辽宁", "吉林", "黑龙江",
    "山东", "河南", "陕西", "甘肃", "宁夏", "新疆", "青海",
}
SOUTH_PROVINCES = {
    "上海", "江苏", "浙江", "安徽", "福建", "江西", "湖北", "湖南",
    "广东", "广西", "海南", "重庆", "四川", "贵州", "云南", "西藏",
}


def province_region(value: str) -> str:
    text = (value or "").strip()
    if any(province in text for province in NORTH_PROVINCES):
        return "北"
    if any(province in text for province in SOUTH_PROVINCES):
        return "南"
    return ""


def candidate_region(cand) -> str:
    region = province_region(cand.household_province)
    if region:
        return region
    for sname in (cand.highest_degree_school, cand.first_degree_school):
        if sname:
            school = m.School.objects.filter(name=sname).only("province").first()
            if school:
                region = province_region(school.province)
                if region:
                    return region
    return ""
