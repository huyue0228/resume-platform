from io import BytesIO
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch
import zipfile

import pandas as pd
from django.contrib.auth.models import Group, Permission
from django.test import TestCase

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults, permission_codename
from apps.core import models as m
from apps.ingestion.sources import (
    _read_excel,
    import_files,
    normalize_highest_education,
)
from apps.ingestion.tabular_imports import (
    get_import_table_schema,
    validate_table_headers,
)


def _schema_key_for_rows(rows):
    fields = {field for row in rows for field in row}
    if "学校" in fields:
        return "schools"
    if "工号" in fields or "邮箱" in fields:
        return "contacts"
    if fields.intersection({"职位名称", "岗位类别", "工作职责", "HC"}):
        return "jobs"
    return "resume_list"


def _excel_file(rows):
    buf = BytesIO()
    schema = get_import_table_schema(_schema_key_for_rows(rows))
    pd.DataFrame(rows, columns=schema.headers).to_excel(buf, index=False)
    buf.seek(0)
    buf.name = "简历信息列表.xlsx"
    return buf


def _raw_excel_file(rows):
    buf = BytesIO()
    pd.DataFrame(rows).to_excel(buf, index=False)
    buf.seek(0)
    buf.name = "非标准模板.xlsx"
    return buf


def _excel_file_without_name(rows):
    buf = BytesIO()
    pd.DataFrame(rows).to_excel(buf, index=False)
    buf.seek(0)
    return buf


class ResumeImportDesignContractTests(TestCase):
    def test_school_import_tracks_blank_province_for_background_enrichment(self):
        schools = _excel_file(
            [
                {"学校": "北京大学", "院校标签": "重点院校"},
                {"学校": "南京大学", "院校标签": "重点院校"},
            ]
        )

        counts = import_files({"schools": schools}, mode="incremental")

        blank = m.School.objects.get(name="北京大学")
        other = m.School.objects.get(name="南京大学")
        self.assertEqual(blank.province, "")
        self.assertEqual(
            counts["_school_ids_missing_province"], sorted([blank.id, other.id])
        )

    def test_blank_import_province_preserves_existing_value(self):
        existing = m.School.objects.create(name="北京大学", province="北京")
        schools = _excel_file(
            [{"学校": "北京大学", "院校标签": "重点院校"}]
        )

        counts = import_files({"schools": schools}, mode="incremental")

        existing.refresh_from_db()
        self.assertEqual(existing.province, "北京")
        self.assertEqual(counts["_school_ids_missing_province"], [])

    def test_school_import_has_no_province_field_and_preserves_existing_value(self):
        existing = m.School.objects.create(name="测试大学", province="北京")
        schools = _excel_file(
            [{"学校": "测试大学", "院校标签": "普通院校"}]
        )

        import_files({"schools": schools}, mode="incremental")

        existing.refresh_from_db()
        self.assertEqual(existing.province, "北京")

    def test_highest_education_aliases_are_normalized(self):
        self.assertEqual(normalize_highest_education("高职（专科）"), "associate")
        self.assertEqual(normalize_highest_education("大学本科 / 学士"), "bachelor")
        self.assertEqual(normalize_highest_education("硕士研究生"), "master")
        self.assertEqual(normalize_highest_education("博士研究生"), "doctor")
        self.assertEqual(normalize_highest_education("其它"), "")

    def test_resume_import_keeps_highest_recognized_education_per_candidate(self):
        resume_list = _excel_file(
            [
                {
                    "姓名": "张三",
                    "手机号": "13800000000",
                    "应聘ID": "EDU1001",
                    "学历": "本科",
                },
                {
                    "姓名": "张三",
                    "手机号": "13800000000",
                    "应聘ID": "EDU1002",
                    "学历": "硕士研究生",
                },
            ]
        )

        import_files({"resume_list": resume_list}, mode="incremental")

        candidate = m.Candidate.objects.get()
        self.assertEqual(candidate.highest_education, m.Candidate.EDUCATION_MASTER)
        self.assertEqual(candidate.resumes.count(), 2)

    def test_unknown_education_does_not_overwrite_existing_value(self):
        candidate = m.Candidate.objects.create(
            identity_hash="placeholder",
            name="李四",
            phone="13900000000",
            highest_education=m.Candidate.EDUCATION_DOCTOR,
        )
        from apps.ingestion.identity import identity_hash

        candidate.identity_hash = identity_hash(candidate.name, candidate.phone)
        candidate.save(update_fields=["identity_hash"])
        resume_list = _excel_file(
            [
                {
                    "姓名": "李四",
                    "手机号": "13900000000",
                    "应聘ID": "EDU2001",
                    "学历": "未知",
                }
            ]
        )

        import_files({"resume_list": resume_list}, mode="incremental")

        candidate.refresh_from_db()
        self.assertEqual(candidate.highest_education, m.Candidate.EDUCATION_DOCTOR)

    def test_read_excel_uses_content_detection_when_name_is_missing(self):
        excel = _excel_file_without_name([{"姓名": "张三"}])

        df = _read_excel(excel)

        self.assertEqual(df.iloc[0]["姓名"], "张三")

    def test_read_excel_supports_csv_content_even_with_xlsx_suffix(self):
        csv_file = BytesIO("姓名,手机号\n张三,13800000000\n".encode("utf-8-sig"))
        csv_file.name = "简历信息列表.xlsx"

        df = _read_excel(csv_file)

        self.assertEqual(df.iloc[0]["姓名"], "张三")
        self.assertEqual(str(df.iloc[0]["手机号"]), "13800000000")

    def test_read_excel_supports_gb18030_csv(self):
        csv_file = BytesIO("姓名,手机号\n李四,13900000000\n".encode("gb18030"))
        csv_file.name = "简历信息列表.csv"

        df = _read_excel(csv_file)

        self.assertEqual(df.iloc[0]["姓名"], "李四")
        self.assertEqual(str(df.iloc[0]["手机号"]), "13900000000")

    def test_read_excel_routes_legacy_xls_to_xlrd(self):
        legacy_xls = BytesIO(b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1legacy")
        legacy_xls.name = "简历信息列表.xls"
        expected = pd.DataFrame([{"姓名": "王五"}])

        with patch("apps.ingestion.sources.pd.read_excel", return_value=expected) as read_excel:
            df = _read_excel(legacy_xls)

        self.assertEqual(df.iloc[0]["姓名"], "王五")
        read_excel.assert_called_once()
        self.assertEqual(read_excel.call_args.kwargs["engine"], "xlrd")

    def test_job_import_persists_responsibilities_and_reports_skipped_rows(self):
        jobs = _excel_file(
            [
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "职位名称": "后端工程师",
                    "对外发布名称": "后端开发",
                    "工作职责": "负责服务设计、接口开发和性能优化。",
                },
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "职位名称": "测试工程师",
                    "对外发布名称": "测试开发",
                    "工作职责": "",
                },
            ]
        )

        counts = import_files({"jobs": jobs}, mode="incremental")

        self.assertEqual((counts["jobs"], counts["jobs_skipped"]), (1, 1))
        self.assertEqual(
            m.Job.objects.get().responsibilities,
            "负责服务设计、接口开发和性能优化。",
        )
        self.assertEqual(
            counts["_warnings"],
            [
                {
                    "code": "job_responsibility_missing",
                    "count": 1,
                    "rows": [3],
                    "message": "工作职责为空的岗位已跳过",
                }
            ],
        )

    def test_replace_job_import_with_no_valid_rows_preserves_existing_jobs(self):
        existing = m.Job.objects.create(
            position_name="已有岗位",
            public_name="已有岗位",
            responsibilities="已有职责",
        )
        jobs = _excel_file(
            [{
                "一层部门": "技术中心",
                "二层部门": "平台部",
                "职位名称": "新岗位",
                "对外发布名称": "新岗位",
            }]
        )

        counts = import_files({"jobs": jobs}, mode="replace")

        self.assertEqual((counts["jobs"], counts["jobs_skipped"]), (0, 1))
        self.assertEqual(list(m.Job.objects.values_list("id", flat=True)), [existing.id])

    def test_replace_job_import_keeps_only_valid_rows(self):
        existing = m.Job.objects.create(
            position_name="已有岗位",
            public_name="已有岗位",
            responsibilities="已有职责",
        )
        jobs = _excel_file(
            [
                {
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "职位名称": "新岗位",
                    "对外发布名称": "新岗位",
                    "工作职责": "负责新业务建设。",
                },
                {
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "职位名称": "缺失职责岗位",
                    "对外发布名称": "缺失职责岗位",
                    "工作职责": "",
                },
            ]
        )

        counts = import_files({"jobs": jobs}, mode="replace")

        self.assertEqual((counts["jobs"], counts["jobs_skipped"]), (1, 1))
        self.assertEqual(
            list(
                m.Job.objects.filter(is_active=True).values_list(
                    "position_name", flat=True
                )
            ),
            ["新岗位"],
        )
        existing.refresh_from_db()
        self.assertFalse(existing.is_active)

    def test_replace_job_import_updates_creates_and_deactivates_without_deleting_hc_snapshot(self):
        parent = m.Department.objects.create(name="技术中心", level=1)
        department = m.Department.objects.create(
            name="平台部", level=2, parent=parent, entity="GW"
        )
        existing = m.Job.objects.create(
            entity="GW",
            department=department,
            category="技术类",
            public_name="后端开发",
            position_name="后端工程师",
            job_family="旧岗位族",
            location="北京",
            education="本科",
            responsibilities="旧职责",
            headcount=1,
            is_active=False,
        )
        m.JobMajor.objects.create(job=existing, major="旧专业")
        stale = m.Job.objects.create(
            entity="GW",
            department=department,
            category="技术类",
            public_name="测试开发",
            position_name="测试工程师",
            responsibilities="测试职责",
            headcount=3,
        )
        run = m.ProcessingRun.objects.create(step="step2", mode="rule")
        capacity = m.ProcessingRunJobCapacity.objects.create(
            run=run,
            job=stale,
            headcount_snapshot=3,
            capacity=3,
            used_count=1,
        )
        jobs = _excel_file(
            [
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "对外发布名称": "后端开发",
                    "职位名称": "后端工程师",
                    "岗位类别": "技术类",
                    "岗位族": "研发族",
                    "工作地点": "上海",
                    "学历": "硕士",
                    "是否对外发布": "是",
                    "工作职责": "负责核心服务建设。",
                    "HC": 7,
                    "需求专业": "计算机、软件工程",
                },
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "对外发布名称": "算法开发",
                    "职位名称": "算法工程师",
                    "岗位类别": "技术类",
                    "是否对外发布": "是",
                    "工作职责": "负责算法平台建设。",
                    "HC": 2,
                    "需求专业": "人工智能",
                },
            ]
        )

        counts = import_files({"jobs": jobs}, mode="replace")

        self.assertEqual(counts["jobs"], 2)
        existing.refresh_from_db()
        self.assertTrue(existing.is_active)
        self.assertEqual(existing.job_family, "研发族")
        self.assertEqual(existing.location, "上海")
        self.assertEqual(existing.education, "硕士")
        self.assertEqual(existing.responsibilities, "负责核心服务建设。")
        self.assertEqual(existing.headcount, 7)
        self.assertEqual(
            list(existing.majors.order_by("id").values_list("major", flat=True)),
            ["计算机", "软件工程"],
        )
        self.assertTrue(
            m.Job.objects.filter(position_name="算法工程师", headcount=2, is_active=True).exists()
        )
        stale.refresh_from_db()
        capacity.refresh_from_db()
        self.assertFalse(stale.is_active)
        self.assertEqual(capacity.job_id, stale.id)
        self.assertEqual(
            (capacity.headcount_snapshot, capacity.capacity, capacity.used_count),
            (3, 3, 1),
        )

    def test_job_import_rejects_duplicate_business_key_in_file(self):
        existing = m.Job.objects.create(
            entity="GW",
            category="技术类",
            public_name="已有岗位",
            position_name="已有岗位",
            responsibilities="已有职责",
            headcount=1,
        )
        jobs = _excel_file(
            [
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "三级部门": "研发组",
                    "对外发布名称": "后端开发",
                    "职位名称": "后端工程师",
                    "岗位类别": "技术类",
                    "工作职责": "职责一",
                },
                {
                    "招聘主体": " gw ",
                    "一层部门": "技 术中心",
                    "二层部门": "平台 部",
                    "三级部门": "研 发组",
                    "对外发布名称": "后端开发",
                    "职位名称": "后端工程师",
                    "岗位类别": "技术类",
                    "工作职责": "职责二",
                },
            ]
        )

        with self.assertRaisesRegex(ValueError, "岗位文件存在重复业务键"):
            import_files({"jobs": jobs}, mode="replace")

        existing.refresh_from_db()
        self.assertTrue(existing.is_active)
        self.assertEqual(existing.headcount, 1)
        self.assertEqual(m.Job.objects.count(), 1)

    def test_job_import_rejects_nonstandard_headers_before_business_key_check(self):
        schema = get_import_table_schema("jobs")
        row = dict.fromkeys(schema.headers, "")
        row.update(
            {
                "招聘主体": "GW",
                "二层部门": "平台部",
                "对外发布名称": "后端开发",
                "职位名称": "后端工程师",
                "工作职责": "负责后端研发。",
                "HC": 2,
            }
        )
        row["主体"] = row.pop("招聘主体")
        row["二级组织"] = row.pop("二层部门")
        row["需求数量"] = row.pop("HC")

        with self.assertRaisesRegex(
            ValueError,
            "缺少字段【招聘主体、二层部门、HC】.*未知字段【主体、二级组织、需求数量】",
        ):
            import_files({"jobs": _raw_excel_file([row])}, mode="incremental")

        self.assertFalse(m.Job.objects.exists())

    def test_standard_header_validation_reports_duplicate_fields(self):
        schema = get_import_table_schema("schools")
        table = pd.DataFrame(
            [["北京大学", "重复学校", "重点院校"]],
            columns=("学校", "学校", "院校标签"),
        )

        with self.assertRaisesRegex(ValueError, "重复字段【学校】"):
            validate_table_headers(table, schema.key)

    def test_school_import_rejects_user_provided_province_column(self):
        with self.assertRaisesRegex(ValueError, "未知字段【所在省份】"):
            import_files(
                {
                    "schools": _raw_excel_file(
                        [
                            {
                                "学校": "测试大学",
                                "院校标签": "目标院校",
                                "所在省份": "湖北",
                            }
                        ]
                    )
                },
                mode="incremental",
            )

    def test_job_import_business_key_distinguishes_entity_primary_and_secondary(self):
        common = {
            "对外发布名称": "后端开发",
            "职位名称": "后端工程师",
            "岗位类别": "技术类",
            "工作职责": "负责后端研发。",
        }
        jobs = _excel_file(
            [
                {
                    **common,
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                },
                {
                    **common,
                    "招聘主体": "YLS",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                },
                {
                    **common,
                    "招聘主体": "GW",
                    "一层部门": "数据中心",
                    "二层部门": "平台部",
                },
                {
                    **common,
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "应用部",
                },
            ]
        )

        counts = import_files({"jobs": jobs}, mode="incremental")

        self.assertEqual(counts["jobs"], 4)
        self.assertEqual(m.Job.objects.filter(is_active=True).count(), 4)

    def test_job_import_rejects_tertiary_department_header(self):
        row = dict.fromkeys(get_import_table_schema("jobs").headers, "")
        row.update(
            {
                "招聘主体": "GW",
                "一层部门": "技术中心",
                "二层部门": "平台部",
                "三级部门": "研发组",
                "对外发布名称": "后端开发",
                "职位名称": "后端工程师",
                "岗位类别": "技术类",
                "工作职责": "负责后端研发。",
            }
        )

        with self.assertRaisesRegex(ValueError, "未知字段【三级部门】"):
            import_files({"jobs": _raw_excel_file([row])}, mode="incremental")

        self.assertFalse(m.Job.objects.exists())

    def test_job_import_updates_secondary_job_in_place(self):
        primary = m.Department.objects.create(name="技术中心", level=1)
        secondary = m.Department.objects.create(
            name="平台部", level=2, parent=primary, entity="GW"
        )
        existing = m.Job.objects.create(
            entity="GW",
            department=secondary,
            category="技术类",
            public_name="后端开发",
            position_name="后端工程师",
            responsibilities="旧职责",
            headcount=1,
        )
        jobs = _excel_file(
            [
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "岗位类别": "技术类",
                    "对外发布名称": "后端开发",
                    "职位名称": "后端工程师",
                    "工作职责": "新职责",
                    "HC": 3,
                }
            ]
        )

        counts = import_files({"jobs": jobs}, mode="incremental")

        existing.refresh_from_db()
        self.assertEqual(counts["jobs"], 1)
        self.assertEqual(m.Job.objects.count(), 1)
        self.assertEqual(existing.department.level, 2)
        self.assertEqual(existing.department.name, "平台部")
        self.assertEqual((existing.responsibilities, existing.headcount), ("新职责", 3))

    def test_job_import_never_creates_tertiary_department(self):
        primary = m.Department.objects.create(name="技术中心", level=1)
        secondary = m.Department.objects.create(
            name="平台部", level=2, parent=primary, entity="GW"
        )
        existing = m.Job.objects.create(
            entity="GW",
            department=secondary,
            category="技术类",
            public_name="后端开发",
            position_name="后端工程师",
            responsibilities="聚合职责",
            headcount=9,
        )
        jobs = _excel_file(
            [
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "岗位类别": "技术类",
                    "对外发布名称": "后端开发",
                    "职位名称": "后端工程师",
                    "工作职责": "二级部门职责",
                    "HC": 5,
                }
            ]
        )

        counts = import_files({"jobs": jobs}, mode="incremental")

        existing.refresh_from_db()
        self.assertEqual(counts["jobs"], 1)
        self.assertTrue(existing.is_active)
        self.assertEqual(existing.department, secondary)
        self.assertEqual(existing.headcount, 5)
        self.assertFalse(m.Department.objects.filter(level=3).exists())

    def test_job_import_rejects_duplicate_business_key_in_database(self):
        primary = m.Department.objects.create(name="技术中心", level=1)
        secondary = m.Department.objects.create(
            name="平台部", level=2, parent=primary
        )
        first = m.Job.objects.create(
            entity="GW",
            department=secondary,
            category="技术类",
            public_name="后端开发",
            position_name="后端工程师",
            responsibilities="职责一",
            headcount=1,
        )
        second = m.Job.objects.create(
            entity="gw",
            department=secondary,
            category="技术类",
            public_name="后端开发",
            position_name="后端工程师",
            responsibilities="职责二",
            headcount=2,
            is_active=False,
        )
        jobs = _excel_file(
            [
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "对外发布名称": "新岗位",
                    "职位名称": "新岗位",
                    "岗位类别": "技术类",
                    "工作职责": "新职责",
                }
            ]
        )

        with self.assertRaisesRegex(ValueError, "数据库岗位存在重复业务键"):
            import_files({"jobs": jobs}, mode="replace")

        first.refresh_from_db()
        second.refresh_from_db()
        self.assertTrue(first.is_active)
        self.assertFalse(second.is_active)
        self.assertFalse(m.Job.objects.filter(public_name="新岗位").exists())

    def test_replace_job_import_rolls_back_updates_when_major_sync_fails(self):
        primary = m.Department.objects.create(name="技术中心", level=1)
        secondary = m.Department.objects.create(
            name="平台部", level=2, parent=primary
        )
        existing = m.Job.objects.create(
            entity="GW",
            department=secondary,
            category="技术类",
            public_name="后端开发",
            position_name="后端工程师",
            responsibilities="旧职责",
            headcount=1,
        )
        m.JobMajor.objects.create(job=existing, major="旧专业")
        stale = m.Job.objects.create(
            entity="GW",
            department=secondary,
            category="产品类",
            public_name="产品经理",
            position_name="产品经理",
            responsibilities="产品职责",
            headcount=2,
        )
        jobs = _excel_file(
            [
                {
                    "招聘主体": "GW",
                    "一层部门": "技术中心",
                    "二层部门": "平台部",
                    "对外发布名称": "后端开发",
                    "职位名称": "后端工程师",
                    "岗位类别": "技术类",
                    "工作职责": "新职责",
                    "HC": 9,
                    "需求专业": "计算机",
                }
            ]
        )

        with patch.object(
            m.JobMajor.objects, "bulk_create", side_effect=RuntimeError("sync failed")
        ), self.assertRaisesRegex(RuntimeError, "sync failed"):
            import_files({"jobs": jobs}, mode="replace")

        existing.refresh_from_db()
        stale.refresh_from_db()
        self.assertEqual((existing.responsibilities, existing.headcount), ("旧职责", 1))
        self.assertEqual(
            list(existing.majors.values_list("major", flat=True)), ["旧专业"]
        )
        self.assertTrue(stale.is_active)

    def test_resume_import_skips_missing_phone_and_maps_gender_codes(self):
        resume_list = _excel_file(
            [
                {
                    "姓名": "张三",
                    "手机号": "13800000000",
                    "应聘ID": "A1001",
                    "性别": "男",
                    "对外职位名称": "后端工程师",
                },
                {
                    "姓名": "李四",
                    "手机号": "",
                    "应聘ID": "A1002",
                    "性别": "女",
                    "对外职位名称": "产品经理",
                },
            ]
        )

        counts = import_files({"resume_list": resume_list}, mode="incremental")

        self.assertEqual(counts["candidates_created"], 1)
        self.assertEqual(counts["resumes_created"], 1)
        self.assertEqual(counts["candidates_skipped"], 1)
        candidate = m.Candidate.objects.get()
        self.assertEqual(candidate.name, "张三")
        self.assertEqual(candidate.gender, "M")

    def test_resume_package_links_pdf_by_apply_id_and_persists_file(self):
        resume_list = _excel_file(
            [
                {
                    "姓名": "张三",
                    "手机号": "13800000000",
                    "应聘ID": "PDF1001",
                    "对外职位名称": "后端工程师",
                }
            ]
        )
        package = BytesIO()
        with zipfile.ZipFile(package, "w", zipfile.ZIP_DEFLATED) as archive:
            archive.writestr("简历/张三（PDF1001）.pdf", b"%PDF-1.4\n% test pdf\n")
        package.seek(0)
        package.name = "简历包.zip"

        with TemporaryDirectory() as media_root, self.settings(MEDIA_ROOT=media_root):
            import_files(
                {"resume_list": resume_list, "resume_package": package},
                mode="incremental",
            )
            resume = m.Resume.objects.get(apply_id="PDF1001")
            stored_file = Path(media_root) / "resumes" / resume.resume_file

            self.assertEqual(resume.resume_file, "张三（PDF1001）.pdf")
            self.assertTrue(stored_file.exists())
            self.assertTrue(stored_file.read_bytes().startswith(b"%PDF"))

    def test_replace_contacts_does_not_clear_existing_resume_pool(self):
        candidate = m.Candidate.objects.create(
            identity_hash="candidate-keep",
            name="张三",
            phone="13800000000",
        )
        m.Resume.objects.create(
            candidate=candidate,
            apply_id="A1001",
            position_name="后端工程师",
        )
        old_department = m.Department.objects.create(name="旧部门", level=2)
        old_contact = m.Contact.objects.create(
            name="旧接口人",
            employee_no="OLD001",
            department=old_department,
        )
        old_user = User.objects.create_user(
            username="OLD001",
            password="old-pass",
            role=User.ROLE_SECONDARY_CONTACT,
            contact=old_contact,
        )
        contacts = _excel_file(
            [
                {
                    "工号": "NEW001",
                    "邮箱": "new001@example.com",
                    "姓名": "新接口人",
                    "一层部门": "技术中心",
                    "二层部门": "后端组",
                    "接口人层级": "二级接口人",
                }
            ]
        )

        counts = import_files({"contacts": contacts}, mode="replace")

        self.assertEqual(counts["contacts"], 1)
        self.assertEqual(m.Candidate.objects.count(), 1)
        self.assertEqual(m.Resume.objects.count(), 1)
        old_contact = m.Contact.objects.get(employee_no="OLD001")
        self.assertFalse(old_contact.is_active)
        old_user.refresh_from_db()
        self.assertFalse(old_user.is_active)
        self.assertTrue(m.Contact.objects.get(employee_no="NEW001").is_active)

    def test_contact_import_creates_contact_user_with_employee_no_and_email(self):
        contacts = _excel_file(
            [
                {
                    "工号": "L9001",
                    "邮箱": "l9001@example.com",
                    "姓名": "二级接口人",
                    "一层部门": "技术中心",
                    "二层部门": "平台组",
                    "接口人层级": "二级接口人",
                },
                {
                    "工号": "T9001",
                    "邮箱": "t9001@example.com",
                    "姓名": "三级接口人",
                    "一层部门": "技术中心",
                    "二层部门": "平台组",
                    "三级部门": "服务端组",
                    "接口人层级": "三级接口人",
                },
            ]
        )

        counts = import_files({"contacts": contacts}, mode="incremental")

        self.assertEqual(counts["contacts"], 2)
        secondary_contact = m.Contact.objects.get(employee_no="L9001")
        tertiary_contact = m.Contact.objects.get(employee_no="T9001")
        secondary_user = User.objects.get(username="L9001")
        tertiary_user = User.objects.get(username="T9001")
        self.assertEqual(secondary_contact.email, "l9001@example.com")
        self.assertEqual(tertiary_contact.email, "t9001@example.com")
        self.assertEqual(secondary_user.contact, secondary_contact)
        self.assertEqual(secondary_user.email, "l9001@example.com")
        self.assertEqual(secondary_user.role, User.ROLE_SECONDARY_CONTACT)
        self.assertFalse(secondary_user.has_usable_password())
        self.assertIn(
            "二级接口人",
            list(secondary_user.groups.values_list("name", flat=True)),
        )
        self.assertEqual(tertiary_user.contact, tertiary_contact)
        self.assertEqual(tertiary_user.email, "t9001@example.com")
        self.assertEqual(tertiary_user.role, User.ROLE_TERTIARY_CONTACT)
        self.assertFalse(tertiary_user.has_usable_password())
        self.assertIn(
            "三级接口人",
            list(tertiary_user.groups.values_list("name", flat=True)),
        )
        self.assertTrue(Group.objects.filter(name="二级接口人").exists())
        self.assertTrue(Group.objects.filter(name="三级接口人").exists())

    def test_contact_import_requires_email_and_rolls_back_replace(self):
        department = m.Department.objects.create(name="保留部门", level=2)
        old_contact = m.Contact.objects.create(
            name="保留接口人",
            employee_no="KEEP001",
            email="keep001@example.com",
            department=department,
            is_active=True,
        )
        contacts = _excel_file(
            [
                {
                    "工号": "NEW-NO-EMAIL",
                    "姓名": "缺邮箱接口人",
                    "一层部门": "技术中心",
                    "二层部门": "平台组",
                }
            ]
        )

        with self.assertRaisesRegex(ValueError, "第 2 行缺少邮箱"):
            import_files({"contacts": contacts}, mode="replace")

        old_contact.refresh_from_db()
        self.assertTrue(old_contact.is_active)
        self.assertFalse(
            m.Contact.objects.filter(employee_no="NEW-NO-EMAIL").exists()
        )

    def test_contact_import_rejects_email_owned_by_another_account(self):
        User.objects.create_user(
            username="EMAIL-OWNER",
            email="owned@example.com",
            password="pass",
        )
        contacts = _excel_file(
            [
                {
                    "工号": "OTHER-EMPLOYEE",
                    "邮箱": "OWNED@EXAMPLE.COM",
                    "姓名": "邮箱冲突接口人",
                    "一层部门": "技术中心",
                    "二层部门": "平台组",
                }
            ]
        )

        with self.assertRaisesRegex(ValueError, "邮箱已被其他账号使用"):
            import_files({"contacts": contacts}, mode="incremental")

        self.assertFalse(
            m.Contact.objects.filter(employee_no="OTHER-EMPLOYEE").exists()
        )

    def test_contact_import_keeps_existing_user_password_unusable(self):
        existing_user = User.objects.create_user(
            username="L9002",
            password="custom-pass",
            role=User.ROLE_HR,
        )
        contacts = _excel_file(
            [
                {
                    "工号": "L9002",
                    "邮箱": "l9002@example.com",
                    "姓名": "已有用户接口人",
                    "一层部门": "技术中心",
                    "二层部门": "平台组",
                    "接口人层级": "二级接口人",
                }
            ]
        )

        import_files({"contacts": contacts}, mode="incremental")

        existing_user.refresh_from_db()
        self.assertFalse(existing_user.has_usable_password())
        self.assertEqual(existing_user.contact.employee_no, "L9002")
        self.assertEqual(existing_user.role, User.ROLE_SECONDARY_CONTACT)

    def test_contact_import_preserves_existing_non_contact_roles(self):
        ensure_rbac_defaults()
        extra_group = Group.objects.create(name="临时业务角色")
        existing_user = User.objects.create_user(
            username="T9003",
            password="custom-pass",
            role=User.ROLE_HR,
        )
        existing_user.groups.add(extra_group, Group.objects.get(name="二级接口人"))
        contacts = _excel_file(
            [
                {
                    "工号": "T9003",
                    "邮箱": "t9003@example.com",
                    "姓名": "改为三级接口人",
                    "一层部门": "技术中心",
                    "二层部门": "平台组",
                    "三级部门": "服务端组",
                    "接口人层级": "三级接口人",
                }
            ]
        )

        import_files({"contacts": contacts}, mode="incremental")

        group_names = set(existing_user.groups.values_list("name", flat=True))
        self.assertIn("临时业务角色", group_names)
        self.assertIn("三级接口人", group_names)
        self.assertNotIn("二级接口人", group_names)

    def test_contact_import_preserves_configured_builtin_role_permissions(self):
        ensure_rbac_defaults()
        role = Group.objects.get(name="二级接口人")
        selected_permissions = Permission.objects.filter(
            content_type__app_label="accounts",
            content_type__model="user",
            codename__in=[
                permission_codename("attempt.view_department"),
                permission_codename("settings.manage_ai_connection"),
            ],
        )
        role.permissions.set(selected_permissions)
        expected_ids = set(role.permissions.values_list("id", flat=True))
        contacts = _excel_file(
            [
                {
                    "工号": "L9004",
                    "邮箱": "l9004@example.com",
                    "姓名": "权限保留接口人",
                    "一层部门": "技术中心",
                    "二层部门": "平台组",
                    "接口人层级": "二级接口人",
                }
            ]
        )

        import_files({"contacts": contacts}, mode="incremental")

        self.assertEqual(
            set(role.permissions.values_list("id", flat=True)),
            expected_ids,
        )
