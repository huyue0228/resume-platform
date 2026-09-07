"""当前志愿到内部职位岗位池的确定性映射服务。"""


class JobMappingError(ValueError):
    """岗位精确映射失败；code 供流程结果稳定落库。"""

    def __init__(self, code, detail):
        super().__init__(detail)
        self.code = code
        self.detail = detail


def normalized(value):
    """统一岗位映射文本口径：忽略大小写和空白。"""
    return "".join((value or "").lower().split())


def resolve_job_pool(resume, jobs):
    """把当前对外职位精确映射到唯一内部职位岗位池。

    这里仅处理招聘主体、对外发布名称和内部职位名称，不读取候选人专业，
    因而 Rule 与 AI 可以共享同一份确定性岗位映射，再分别进入各自后续流程。
    """
    public_name = (resume.position_name or "").strip()
    rank_label = (
        f"第{resume.volunteer_rank}志愿" if resume.volunteer_rank else "当前志愿"
    )
    normalized_public_name = normalized(public_name)
    if not normalized_public_name:
        raise JobMappingError(
            "job_not_found", "当前志愿未填写对外职位名称，无法匹配岗位需求"
        )

    resume_entity = normalized(resume.entity)
    mappings = [
        job
        for job in jobs
        if normalized(job.public_name) == normalized_public_name
        and (not resume_entity or normalized(job.entity) == resume_entity)
    ]
    if not mappings:
        raise JobMappingError(
            "job_not_found",
            f"{rank_label}：岗位需求中未配置与对外职位名称“{public_name}”"
            "精确匹配的同主体启用岗位",
        )

    missing_internal = [job for job in mappings if not normalized(job.position_name)]
    internal_names = {
        normalized(job.position_name): job.position_name.strip()
        for job in mappings
        if normalized(job.position_name)
    }
    if missing_internal and not internal_names:
        raise JobMappingError(
            "internal_position_name_missing",
            f"对外职位名称“{public_name}”对应岗位未配置内部职位名称",
        )
    if missing_internal or len(internal_names) != 1:
        names = "、".join(sorted(internal_names.values())) or "空值"
        raise JobMappingError(
            "job_mapping_ambiguous",
            f"对外职位名称“{public_name}”映射到多个内部职位：{names}",
        )

    internal_key, internal_name = next(iter(internal_names.items()))
    mapping_entities = {normalized(job.entity) for job in mappings}
    if len(mapping_entities) != 1:
        raise JobMappingError(
            "job_mapping_ambiguous",
            f"对外职位名称“{public_name}”缺少唯一招聘主体映射",
        )
    entity_key = next(iter(mapping_entities))
    pool = sorted(
        [
            job
            for job in jobs
            if normalized(job.position_name) == internal_key
            and normalized(job.entity) == entity_key
        ],
        key=lambda item: item.id or 0,
    )
    return pool, {
        "public_name": public_name,
        "internal_name": internal_name,
        "entity": mappings[0].entity,
    }
