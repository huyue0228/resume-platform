"""生成脱敏样例数据（4 张 xlsx + 简历包 zip）到 backend/sample_data/。"""
import zipfile
from pathlib import Path

import pandas as pd
from django.conf import settings
from django.core.management.base import BaseCommand

from apps.ingestion.tabular_imports import get_import_table_schema

SCHOOLS = [
    {"学校": "清华大学", "院校标签": "平台A"},
    {"学校": "北京大学", "院校标签": "平台A"},
    {"学校": "复旦大学", "院校标签": "平台B"},
    {"学校": "浙江大学", "院校标签": "平台B"},
    {"学校": "武汉大学", "院校标签": "平台C"},
]

CONTACTS = [
    {
        "一层部门": "技术中心",
        "二层部门": "后端组",
        "姓名": "王工",
        "工号": "T001",
        "邮箱": "t001@example.com",
        "接口人层级": "二级接口人",
        "可转派": "是",
        "是否启用": "是",
    },
    {
        "一层部门": "产品中心",
        "二层部门": "产品组",
        "姓名": "李工",
        "工号": "P001",
        "邮箱": "p001@example.com",
        "接口人层级": "二级接口人",
        "可转派": "是",
        "是否启用": "是",
    },
    {
        "一层部门": "数据中心",
        "二层部门": "数据组",
        "姓名": "赵工",
        "工号": "D001",
        "邮箱": "d001@example.com",
        "接口人层级": "二级接口人",
        "可转派": "是",
        "是否启用": "是",
    },
]

JOBS = [
    {"招聘主体": "GW", "一层部门": "技术中心", "二层部门": "后端组", "岗位类别": "技术类",
     "对外发布名称": "后端开发", "是否对外发布": "是", "职位名称": "后端开发工程师",
     "岗位族": "研发", "工作地点": "北京", "学历": "硕士", "需求专业": "计算机、软件工程",
     "工作职责": "负责后端服务设计、接口开发、性能优化和线上问题排查。", "HC": 3},
    {"招聘主体": "YLS", "一层部门": "产品中心", "二层部门": "产品组", "岗位类别": "产品类",
     "对外发布名称": "产品经理", "是否对外发布": "是", "职位名称": "产品经理",
     "岗位族": "产品", "工作地点": "上海", "学历": "本科", "需求专业": "不限",
     "工作职责": "负责用户需求分析、产品方案设计、项目推进和效果复盘。", "HC": 2},
    {"招聘主体": "GW", "一层部门": "数据中心", "二层部门": "数据组", "岗位类别": "技术类",
     "对外发布名称": "数据分析", "是否对外发布": "是", "职位名称": "数据分析师",
     "岗位族": "研发", "工作地点": "北京", "学历": "硕士", "需求专业": "统计学、数学",
     "工作职责": "负责业务指标体系建设、数据分析建模、专题洞察和分析结论落地。", "HC": 2},
]


def _standard_table(rows, schema_key):
    return pd.DataFrame(rows, columns=get_import_table_schema(schema_key).headers)


# (姓名, 手机号, 性别, 户口所在地, 第一学历院校, 最高学历院校, 最高学历专业)
PEOPLE = [
    ("张三", "13800000001", "男", "北京", "清华大学", "清华大学", "计算机"),
    ("李四", "13800000002", "女", "上海", "复旦大学", "复旦大学", "市场营销"),
    ("王五", "13800000003", "男", "浙江", "浙江大学", "浙江大学", "统计学"),
    ("赵六", "13800000004", "男", "河北", "某学院", "某学院", "计算机"),
    ("钱七", "13800000005", "女", "湖北", "武汉大学", "武汉大学", "数学"),
    ("孙八", "13800000006", "男", "北京", "北京大学", "北京大学", "软件工程"),
]

# 投递记录：(姓名索引, 招聘主体, 对外职位名称, 应聘日期)
APPLICATIONS = [
    (0, "GW", "后端开发", "2026-06-01"),   # 张三投GW后端
    (0, "YLS", "产品经理", "2026-06-03"),  # 张三跨主体投YLS产品
    (1, "YLS", "产品经理", "2026-06-02"),
    (2, "GW", "数据分析", "2026-06-02"),
    (3, "GW", "后端开发", "2026-06-04"),
    (4, "GW", "数据分析", "2026-06-05"),
    (5, "GW", "后端开发", "2026-06-01"),
]


class Command(BaseCommand):
    help = "生成样例数据到 backend/sample_data/"

    def handle(self, *args, **options):
        out = Path(settings.BASE_DIR) / "sample_data"
        out.mkdir(exist_ok=True)

        _standard_table(SCHOOLS, "schools").to_excel(
            out / "院校分类.xlsx", index=False
        )
        _standard_table(CONTACTS, "contacts").to_excel(
            out / "部门接口人信息.xlsx", index=False
        )
        _standard_table(JOBS, "jobs").to_excel(
            out / "校招岗位分类及专业要求.xlsx", index=False
        )

        rows = []
        apply_seq = 1001
        name_to_resume_files = []
        for idx, entity, position, date in APPLICATIONS:
            name, phone, gender, prov, fs, hs, major = PEOPLE[idx]
            apply_id = f"A{apply_seq}"
            apply_seq += 1
            rows.append({
                "招聘主体": entity, "所属机构": "校招", "姓名": name, "应聘ID": apply_id,
                "手机号": phone, "性别": gender, "对外职位名称": position, "学历": "硕士",
                "第一学历毕业院校": fs, "最高学历毕业院校": hs, "最高学历专业": major,
                "应聘状态": "待处理", "户口所在地": prov, "应聘日期": date,
            })
            name_to_resume_files.append((name, apply_id))
        _standard_table(rows, "resume_list").to_excel(
            out / "简历信息列表.xlsx", index=False
        )

        # 简历包：文件名 姓名（应聘ID）.txt
        zip_path = out / "简历包.zip"
        with zipfile.ZipFile(zip_path, "w") as zf:
            for name, apply_id in name_to_resume_files:
                zf.writestr(f"{name}（{apply_id}）.txt", f"{name} 的简历内容（样例）")

        self.stdout.write(self.style.SUCCESS(f"样例数据已生成到 {out}"))
