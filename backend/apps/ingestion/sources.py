"""数据源适配器：表格解析 + 身份归并入库。

Excel 仅是一种数据源实现；下游业务只依赖 core 模型。
"""
import io
import os
import re
import zipfile

import pandas as pd
from django.conf import settings
from django.core.exceptions import ValidationError
from django.core.validators import validate_email
from django.db import transaction

from apps.accounts.contact_users import sync_contact_user
from apps.accounts.models import User
from apps.accounts.protected_users import PROTECTED_ADMIN_USERNAME
from apps.accounts.permissions import ensure_rbac_defaults
from apps.core import models as m
from apps.core.departments import resolve_department_hierarchy

from .identity import identity_hash, normalize_phone
from .tabular_imports import validate_table_headers

# 简历文件落盘子目录（相对 MEDIA_ROOT），导出接口复用
RESUME_SUBDIR = "resumes"
XLS_MAGIC = b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"
XLSX_MAGIC = b"PK\x03\x04"
CSV_ENCODINGS = ("utf-8-sig", "utf-8", "gb18030")
EDUCATION_RANK = {
    m.Candidate.EDUCATION_ASSOCIATE: 1,
    m.Candidate.EDUCATION_BACHELOR: 2,
    m.Candidate.EDUCATION_MASTER: 3,
    m.Candidate.EDUCATION_DOCTOR: 4,
}


def _val(row, key):
    """安全取单元格：NaN/None → ''，其余转为去空白字符串。"""
    v = row.get(key, "")
    if v is None or (isinstance(v, float) and pd.isna(v)):
        return ""
    return str(v).strip()


def _to_bool(s):
    return _val_str_truthy(s)


def _val_str_truthy(s):
    return str(s).strip() in ("是", "Y", "y", "true", "True", "1", "YES", "yes")


def _to_int(s, default=0):
    try:
        return int(float(s))
    except (TypeError, ValueError):
        return default


def _to_date(s):
    if s in ("", None):
        return None
    try:
        d = pd.to_datetime(s, errors="coerce")
        return None if pd.isna(d) else d.date()
    except Exception:
        return None


def normalize_highest_education(value):
    """把简历列表中的常见学历写法映射为领域固定编码。"""
    text = re.sub(r"[\s　()（）\[\]【】]", "", str(value or "")).lower()
    if not text:
        return ""
    aliases = (
        (m.Candidate.EDUCATION_DOCTOR, ("博士研究生", "博士", "phd")),
        (m.Candidate.EDUCATION_MASTER, ("硕士研究生", "硕士", "研究生", "master")),
        (m.Candidate.EDUCATION_BACHELOR, ("大学本科", "本科", "学士", "bachelor")),
        (m.Candidate.EDUCATION_ASSOCIATE, ("大学专科", "高职", "大专", "专科", "associate")),
    )
    for code, names in aliases:
        if any(name in text for name in names):
            return code
    return ""


def _excel_name(file_obj):
    return os.path.basename(str(getattr(file_obj, "name", "") or "")).lower()


def _file_bytes(file_obj):
    file_obj.seek(0)
    data = file_obj.read()
    file_obj.seek(0)
    return data


def _csv_encoding(data):
    if not data:
        return None
    for encoding in CSV_ENCODINGS:
        try:
            text = data.decode(encoding)
        except UnicodeDecodeError:
            continue
        if "\x00" in text:
            return None
        if any(sep in text for sep in (",", "\t", ";", "，")) or "\n" in text:
            return encoding
    return None


def _table_format(file_obj):
    data = _file_bytes(file_obj)
    header = data[:8]
    name = _excel_name(file_obj)
    if header.startswith(XLSX_MAGIC):
        return "excel", "openpyxl", None, data
    if header.startswith(XLS_MAGIC):
        return "excel", "xlrd", None, data
    if name.endswith(".csv"):
        encoding = _csv_encoding(data)
        if encoding:
            return "csv", None, encoding, data
        raise ValueError("CSV 文件编码无法识别，请使用 UTF-8 或 GB18030 编码")
    encoding = _csv_encoding(data)
    if encoding:
        return "csv", None, encoding, data
    if name.endswith((".xlsx", ".xlsm")):
        return "excel", "openpyxl", None, data
    if name.endswith(".xls"):
        return "excel", "xlrd", None, data
    raise ValueError("无法识别表格文件格式，请上传 .xlsx、.xls 或 .csv 文件")


def _read_excel(file_obj, schema_key=None):
    kind, engine, encoding, data = _table_format(file_obj)
    try:
        if kind == "csv":
            table = pd.read_csv(
                io.BytesIO(data),
                dtype=object,
                encoding=encoding,
                sep=None,
                engine="python",
            )
        else:
            table = pd.read_excel(io.BytesIO(data), dtype=object, engine=engine)
        if schema_key:
            validate_table_headers(table, schema_key)
        return table
    except zipfile.BadZipFile as exc:
        raise ValueError("Excel 文件不是有效的 .xlsx 文件，请检查文件内容或另存后重新上传") from exc
    except ImportError as exc:
        if engine == "xlrd":
            raise ValueError("服务端缺少 .xls 读取依赖 xlrd，请先安装后再导入") from exc
        raise
    except ValueError as exc:
        if "Excel file format cannot be determined" in str(exc):
            raise ValueError("无法识别表格文件格式，请上传 .xlsx、.xls 或 .csv 文件") from exc
        raise


def _get_department(level1_name, level2_name, entity="", level3_name=""):
    """按 一层/二层/三级 名称建 department 树，默认返回最末级部门。"""
    l1 = None
    if level1_name:
        l1, _ = m.Department.objects.get_or_create(
            name=level1_name, parent=None, defaults={"level": 1, "entity": entity}
        )
        if l1.entity != entity and entity:
            l1.entity = entity
            l1.save(update_fields=["entity"])
    if level2_name:
        l2, _ = m.Department.objects.get_or_create(
            name=level2_name, parent=l1, defaults={"level": 2, "entity": entity}
        )
        if l2.entity != entity and entity:
            l2.entity = entity
            l2.save(update_fields=["entity"])
        if level3_name:
            l3, _ = m.Department.objects.get_or_create(
                name=level3_name,
                parent=l2,
                defaults={"level": 3, "entity": entity or l2.entity},
            )
            return l3
        return l2
    return l1


def _contact_level(row, dept):
    raw = _val(row, "接口人层级") or _val(row, "层级")
    if "三级" in raw or "3" == raw:
        return m.Contact.LEVEL_TERTIARY
    if "二级" in raw or "2" == raw:
        return m.Contact.LEVEL_SECONDARY
    return (
        m.Contact.LEVEL_TERTIARY
        if dept and dept.level == 3
        else m.Contact.LEVEL_SECONDARY
    )


def _disable_contact_users(contact):
    User.objects.filter(contact=contact).exclude(
        username=PROTECTED_ADMIN_USERNAME
    ).update(is_active=False)


def _deactivate_contact(contact):
    _disable_contact_users(contact)
    if contact.is_active:
        contact.is_active = False
        contact.save(update_fields=["is_active"])
    return contact


def _split_majors(text):
    if not text:
        return []
    parts = re.split(r"[、,，/;；\s]+", text)
    return [p for p in parts if p]


def _normalized_job_key_part(value):
    """岗位业务键按去空白、忽略大小写的口径比较。"""
    return "".join(str(value or "").casefold().split())


def _job_row_department_names(row):
    return (
        _val(row, "一层部门"),
        _val(row, "二层部门"),
    )


def _job_model_department_names(job):
    hierarchy = resolve_department_hierarchy(job.department)
    return (
        hierarchy.primary.name if hierarchy.primary else "",
        hierarchy.secondary.name if hierarchy.secondary else "",
    )


def _job_business_key(
    entity,
    primary_department_name,
    secondary_department_name,
    public_name,
    position_name,
    category,
):
    """岗位完整业务键，所有部分统一去空白并忽略大小写。"""
    return tuple(
        _normalized_job_key_part(value)
        for value in (
            entity,
            primary_department_name,
            secondary_department_name,
            public_name,
            position_name,
            category,
        )
    )


def _job_row_business_key(row):
    primary, secondary = _job_row_department_names(row)
    return _job_business_key(
        _val(row, "招聘主体"),
        primary,
        secondary,
        _val(row, "对外发布名称"),
        _val(row, "职位名称"),
        _val(row, "岗位类别"),
    )


def _job_model_business_key(job):
    primary, secondary = _job_model_department_names(job)
    return _job_business_key(
        job.entity,
        primary,
        secondary,
        job.public_name,
        job.position_name,
        job.category,
    )


def _job_legacy_business_key(
    entity, secondary_department_name, public_name, position_name, category
):
    """旧岗位键，用于把历史聚合岗位安全迁移到唯一完整路径。"""
    return tuple(
        _normalized_job_key_part(value)
        for value in (
            entity,
            secondary_department_name,
            public_name,
            position_name,
            category,
        )
    )


def _job_row_legacy_business_key(row):
    _primary, secondary = _job_row_department_names(row)
    return _job_legacy_business_key(
        _val(row, "招聘主体"),
        secondary,
        _val(row, "对外发布名称"),
        _val(row, "职位名称"),
        _val(row, "岗位类别"),
    )


def _job_model_legacy_business_key(job):
    _primary, secondary = _job_model_department_names(job)
    return _job_legacy_business_key(
        job.entity,
        secondary,
        job.public_name,
        job.position_name,
        job.category,
    )


def _assert_unique_job_rows(job_rows):
    rows_by_key = {}
    for item in job_rows:
        rows_by_key.setdefault(item["business_key"], []).append(item["excel_row"])
    duplicates = [rows for rows in rows_by_key.values() if len(rows) > 1]
    if duplicates:
        row_text = "；".join("、".join(str(row) for row in rows) for rows in duplicates)
        raise ValueError(f"岗位文件存在重复业务键，重复行：{row_text}")


def _existing_jobs_by_business_key():
    jobs_by_key = {}
    jobs_by_legacy_key = {}
    duplicate_ids = []
    jobs = list(
        m.Job.objects.select_related(
            "department", "department__parent", "department__parent__parent"
        ).order_by("id")
    )
    for job in jobs:
        key = _job_model_business_key(job)
        if key in jobs_by_key:
            duplicate_ids.extend([jobs_by_key[key].id, job.id])
        else:
            jobs_by_key[key] = job
        jobs_by_legacy_key.setdefault(
            _job_model_legacy_business_key(job), []
        ).append(job)
    if duplicate_ids:
        ids = "、".join(str(job_id) for job_id in sorted(set(duplicate_ids)))
        raise ValueError(f"数据库岗位存在重复业务键，岗位 ID：{ids}")
    return jobs_by_key, jobs_by_legacy_key


def _legacy_job_matches_path(job, item):
    existing_primary, _existing_secondary = _job_model_department_names(job)
    primary, _secondary = item["department_names"]
    primary_matches = not existing_primary or (
        _normalized_job_key_part(existing_primary)
        == _normalized_job_key_part(primary)
    )
    return primary_matches and not existing_primary and bool(primary)


def _prepare_legacy_job_migrations(job_rows, jobs_by_key, jobs_by_legacy_key):
    """决定唯一补齐与一拆多，返回完整键到可原位更新岗位的映射。"""
    imported_keys = {item["business_key"] for item in job_rows}
    rows_by_legacy_key = {}
    for item in job_rows:
        rows_by_legacy_key.setdefault(item["legacy_business_key"], []).append(item)

    reusable_jobs = {}
    for legacy_key, items in rows_by_legacy_key.items():
        unmatched_items = [
            item for item in items if item["business_key"] not in jobs_by_key
        ]
        candidates = [
            job
            for job in jobs_by_legacy_key.get(legacy_key, [])
            if _job_model_business_key(job) not in imported_keys
        ]
        if len(items) == 1 and items[0]["business_key"] in jobs_by_key:
            for job in candidates:
                if _legacy_job_matches_path(job, items[0]) and job.is_active:
                    job.is_active = False
                    job.save(update_fields=["is_active"])
            continue
        if len(unmatched_items) == 1:
            matching = [
                job
                for job in candidates
                if _legacy_job_matches_path(job, unmatched_items[0])
            ]
            if len(matching) == 1:
                reusable_jobs[unmatched_items[0]["business_key"]] = matching[0]
            continue

        if len(items) > 1:
            for job in candidates:
                if any(_legacy_job_matches_path(job, item) for item in items):
                    if job.is_active:
                        job.is_active = False
                        job.save(update_fields=["is_active"])
    return reusable_jobs


def _sync_jobs(job_rows, *, mode):
    """按业务键幂等更新岗位；replace 额外停用文件外岗位。"""
    jobs_by_key, jobs_by_legacy_key = _existing_jobs_by_business_key()
    reusable_jobs = _prepare_legacy_job_migrations(
        job_rows, jobs_by_key, jobs_by_legacy_key
    )
    imported_job_ids = set()
    for item in job_rows:
        row = item["row"]
        entity = _val(row, "招聘主体")
        primary, secondary = item["department_names"]
        department = _get_department(
            primary,
            secondary,
            entity,
        )
        values = {
            "entity": entity,
            "department": department,
            "category": _val(row, "岗位类别"),
            "public_name": _val(row, "对外发布名称"),
            "is_public": _to_bool(_val(row, "是否对外发布")),
            "position_name": _val(row, "职位名称"),
            "job_family": _val(row, "岗位族"),
            "location": _val(row, "工作地点"),
            "education": _val(row, "学历"),
            "responsibilities": _val(row, "工作职责"),
            "headcount": _to_int(_val(row, "HC")),
            "is_active": True,
        }
        job = jobs_by_key.get(item["business_key"]) or reusable_jobs.get(
            item["business_key"]
        )
        if job:
            for field, value in values.items():
                setattr(job, field, value)
            job.save(update_fields=list(values))
        else:
            job = m.Job.objects.create(**values)
            jobs_by_key[item["business_key"]] = job

        job.majors.all().delete()
        m.JobMajor.objects.bulk_create(
            [
                m.JobMajor(job=job, major=major)
                for major in _split_majors(_val(row, "需求专业"))
            ]
        )
        imported_job_ids.add(job.id)

    if mode == "replace":
        m.Job.objects.exclude(id__in=imported_job_ids).filter(is_active=True).update(
            is_active=False
        )
    return len(imported_job_ids)


def _gender_code(value):
    text = str(value or "").strip().lower()
    if text in ("男", "m", "male", "man", "1"):
        return "M"
    if text in ("女", "f", "female", "woman", "0"):
        return "F"
    return "U"


def validate_import_file_headers(files):
    """在快照或业务写入前校验本次上传的全部表格契约。"""
    for schema_key in ("resume_list", "jobs", "schools", "contacts"):
        file_obj = files.get(schema_key)
        if file_obj:
            _read_excel(file_obj, schema_key=schema_key)


@transaction.atomic
def import_files(files: dict, mode: str = "incremental") -> dict:
    """导入 4 张表 + 简历包。files 的键：resume_list/jobs/schools/contacts/resume_package。"""
    counts = {
        "candidates_created": 0,
        "candidates_updated": 0,
        "resumes_created": 0,
        "resumes_updated": 0,
        "jobs": 0,
        "schools": 0,
        "contacts": 0,
        "candidates_skipped": 0,
        "jobs_skipped": 0,
    }
    affected_candidate_ids = set()
    schools_missing_province = set()
    warnings = []
    job_rows = []

    # 岗位文件必须先完成行级校验，避免 replace 模式在全部岗位无效时先清空旧数据。
    if files.get("jobs"):
        jobs_df = _read_excel(files["jobs"], schema_key="jobs")
        missing_responsibility_rows = []
        candidate_job_rows = []
        for excel_row, (_, row) in enumerate(jobs_df.iterrows(), start=2):
            position = _val(row, "职位名称")
            public_name = _val(row, "对外发布名称")
            if not (position or public_name):
                continue
            department_names = _job_row_department_names(row)
            if not department_names[1]:
                raise ValueError(
                    f"岗位文件第 {excel_row} 行缺少二层部门"
                )
            item = {
                "excel_row": excel_row,
                "row": row,
                "business_key": _job_row_business_key(row),
                "legacy_business_key": _job_row_legacy_business_key(row),
                "department_names": department_names,
            }
            candidate_job_rows.append(item)
            if not _val(row, "工作职责"):
                counts["jobs_skipped"] += 1
                missing_responsibility_rows.append(excel_row)
                continue
            job_rows.append(item)
        _assert_unique_job_rows(candidate_job_rows)
        if missing_responsibility_rows:
            warnings.append(
                {
                    "code": "job_responsibility_missing",
                    "count": len(missing_responsibility_rows),
                    "rows": missing_responsibility_rows,
                    "message": "工作职责为空的岗位已跳过",
                }
            )

    if mode == "replace" and (files.get("resume_list") or files.get("resume_package")):
        m.AssignmentHandlingEvent.objects.all().delete()
        m.AssignmentAttempt.objects.all().delete()
        m.AgentDispatchDecision.objects.all().delete()
        m.ResumeProfile.objects.all().delete()
        m.CandidateWorkflow.objects.all().delete()
        m.Resume.objects.all().delete()
        m.Candidate.objects.all().delete()
    if mode == "replace":
        if files.get("schools"):
            m.School.objects.all().delete()

    # 院校清单
    if files.get("schools"):
        df = _read_excel(files["schools"], schema_key="schools")
        for _, row in df.iterrows():
            name = _val(row, "学校")
            if not name:
                continue
            tag_text = _val(row, "院校标签")
            school_tag = None
            if tag_text:
                school_tag, _ = m.SchoolTag.objects.update_or_create(
                    code=tag_text,
                    defaults={"name": tag_text, "is_active": True},
                )
            school_defaults = {
                "platform": tag_text,
                "school_tag": school_tag,
            }
            school, _ = m.School.objects.update_or_create(
                name=name,
                defaults=school_defaults,
            )
            if not school.province.strip():
                schools_missing_province.add(school.id)
            counts["schools"] += 1

    # 部门接口人
    if files.get("contacts"):
        ensure_rbac_defaults()
        df = _read_excel(files["contacts"], schema_key="contacts")
        contact_rows = []
        email_owners = {}
        for excel_row, (_, row) in enumerate(df.iterrows(), start=2):
            no = _val(row, "工号")
            if not no:
                continue
            email = _val(row, "邮箱").casefold()
            if not email:
                raise ValueError(f"部门接口人第 {excel_row} 行缺少邮箱")
            try:
                validate_email(email)
            except ValidationError as exc:
                raise ValueError(
                    f"部门接口人第 {excel_row} 行邮箱格式无效"
                ) from exc
            owner = email_owners.get(email)
            if owner and owner != no:
                raise ValueError(
                    f"部门接口人文件中邮箱 {email} 对应多个工号"
                )
            email_owners[email] = no
            contact_rows.append((row, no, email))

        imported_employee_nos = {no for _, no, _ in contact_rows}
        if mode == "replace":
            for contact in m.Contact.objects.exclude(
                employee_no__in=imported_employee_nos
            ):
                _deactivate_contact(contact)
        for row, no, email in contact_rows:
            name = _val(row, "姓名")
            duplicate_email = m.Contact.objects.filter(
                email__iexact=email
            ).exclude(employee_no=no)
            if duplicate_email.exists():
                raise ValueError(f"接口人邮箱 {email} 已被其他工号使用")
            dept = _get_department(
                _val(row, "一层部门"),
                _val(row, "二层部门"),
                "",
                _val(row, "三级部门"),
            )
            contact, _ = m.Contact.objects.update_or_create(
                employee_no=no,
                defaults={
                    "name": name,
                    "email": email,
                    "department": dept,
                    "contact_level": _contact_level(row, dept),
                    "can_delegate": not (
                        _val(row, "可转派") and not _to_bool(_val(row, "可转派"))
                    ),
                    "is_active": not (
                        _val(row, "是否启用") and not _to_bool(_val(row, "是否启用"))
                    ),
                },
            )
            sync_contact_user(contact)
            counts["contacts"] += 1

    # 岗位需求
    if files.get("jobs") and job_rows:
        counts["jobs"] = _sync_jobs(job_rows, mode=mode)

    # 简历信息列表 → Candidate + Resume
    resume_by_apply = {}
    if files.get("resume_list"):
        df = _read_excel(files["resume_list"], schema_key="resume_list")
        education_by_identity = {}
        for _, row in df.iterrows():
            name = _val(row, "姓名")
            phone = _val(row, "手机号")
            if not name or not normalize_phone(phone):
                continue
            education = normalize_highest_education(_val(row, "学历"))
            if not education:
                continue
            ihash = identity_hash(name, phone)
            previous = education_by_identity.get(ihash, "")
            if EDUCATION_RANK[education] > EDUCATION_RANK.get(previous, 0):
                education_by_identity[ihash] = education
        for _, row in df.iterrows():
            name = _val(row, "姓名")
            phone = _val(row, "手机号")
            apply_id = _val(row, "应聘ID")
            if not (name and apply_id):
                continue
            if not normalize_phone(phone):
                counts["candidates_skipped"] += 1
                continue
            ihash = identity_hash(name, phone)
            candidate_defaults = {
                "name": name,
                "phone": phone,
                "gender": _gender_code(_val(row, "性别")),
                "household_province": _val(row, "户口所在地"),
                "first_degree_school": _val(row, "第一学历毕业院校"),
                "highest_degree_school": _val(row, "最高学历毕业院校"),
                "highest_major": _val(row, "最高学历专业"),
            }
            if education_by_identity.get(ihash):
                candidate_defaults["highest_education"] = education_by_identity[ihash]
            cand, created = m.Candidate.objects.update_or_create(
                identity_hash=ihash,
                defaults=candidate_defaults,
            )
            counts["candidates_created" if created else "candidates_updated"] += 1
            affected_candidate_ids.add(cand.id)

            resume, r_created = m.Resume.objects.update_or_create(
                apply_id=apply_id,
                defaults={
                    "candidate": cand,
                    "entity": _val(row, "招聘主体"),
                    "org": _val(row, "所属机构"),
                    "position_name": _val(row, "对外职位名称"),
                    "status": _val(row, "应聘状态") or "待处理",
                    "apply_date": _to_date(_val(row, "应聘日期")),
                },
            )
            counts["resumes_created" if r_created else "resumes_updated"] += 1
            resume_by_apply[apply_id] = resume

    # 简历包：文件名 姓名（应聘ID） → 经应聘ID 关联 Resume，并将文件落盘到 media/resumes/
    if files.get("resume_package"):
        pkg = files["resume_package"]
        dest_dir = os.path.join(settings.MEDIA_ROOT, RESUME_SUBDIR)
        os.makedirs(dest_dir, exist_ok=True)
        try:
            pkg.seek(0)
            with zipfile.ZipFile(io.BytesIO(pkg.read())) as zf:
                for fname in zf.namelist():
                    if fname.endswith("/"):
                        continue
                    match = re.search(r"[（(]\s*([^（）()]+?)\s*[）)]", fname)
                    if not match:
                        continue
                    apply_id = match.group(1).strip()
                    resume = m.Resume.objects.filter(apply_id=apply_id).first()
                    if resume:
                        base = os.path.basename(fname)
                        with open(os.path.join(dest_dir, base), "wb") as out:
                            out.write(zf.read(fname))
                        resume.resume_file = base
                        resume.save(update_fields=["resume_file"])
                        affected_candidate_ids.add(resume.candidate_id)
        except zipfile.BadZipFile:
            pass

    # 仅供 API 在创建后台 ProcessingRun 时冻结处理范围，不作为导入统计直接返回。
    counts["_candidate_ids"] = sorted(affected_candidate_ids)
    # 仅供 API 在提交事务完成后投递低优先级 AI 补全，不进入导入统计。
    counts["_school_ids_missing_province"] = sorted(schools_missing_province)
    counts["_warnings"] = warnings
    return counts
