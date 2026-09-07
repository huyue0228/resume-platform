"""初始化本地开发基础配置、RBAC 和测试账号。"""
from django.contrib.auth.models import Group
from django.core.management.base import BaseCommand

from apps.accounts.models import User
from apps.accounts.permissions import ensure_rbac_defaults
from apps.core import models as m
from apps.pipeline.ai_config import PUBLIC_AI_CONFIG_REGISTRY

MAJOR_DICTIONARY = [
    (
        "CS_SOFTWARE",
        "计算机与软件类",
        "计算机、软件、大数据、人工智能、网络安全等方向。",
        True,
        [
            "计算机", "计算机科学与技术", "软件工程", "网络工程", "信息安全",
            "网络空间安全", "物联网工程", "数字媒体技术", "数据科学与大数据技术",
            "大数据", "人工智能", "智能科学与技术", "空间信息与数字技术",
            "区块链工程", "密码科学与技术",
        ],
    ),
    (
        "ELECTRONICS_COMM",
        "电子信息与通信类",
        "电子、通信、微电子、集成电路和光电信息等方向。",
        True,
        [
            "电子信息", "电子科学与技术", "电子信息工程", "通信工程", "信息工程",
            "微电子科学与工程", "集成电路设计与集成系统", "集成电路科学与工程",
            "光电信息科学与工程", "电子封装技术", "电磁场与无线技术",
            "广播电视工程", "医学信息工程",
        ],
    ),
    (
        "AUTOMATION_CONTROL",
        "自动化与控制类",
        "自动化、控制、机器人、测控仪器和智能装备等方向。",
        True,
        [
            "自动化", "控制科学与工程", "控制工程", "机器人工程", "智能装备",
            "智能制造工程", "测控技术与仪器", "仪器科学与技术", "精密仪器",
            "导航工程", "轨道交通信号与控制",
        ],
    ),
    (
        "ELECTRICAL_ENERGY",
        "电气与能源动力类",
        "电气工程、电力能源、新能源、储能和动力工程等方向。",
        True,
        [
            "电气工程", "电气工程及其自动化", "电机电器", "智能电网信息工程",
            "能源与动力工程", "热能与动力工程", "新能源科学与工程",
            "储能科学与工程", "能源互联网工程", "核工程与核技术",
        ],
    ),
    (
        "MECHANICAL_VEHICLE",
        "机械与车辆类",
        "机械设计制造、车辆、机电和装备工程等方向。",
        True,
        [
            "机械工程", "机械设计制造及其自动化", "机械电子工程",
            "过程装备与控制工程", "车辆工程", "汽车服务工程", "智能车辆工程",
            "工业设计", "机电一体化", "农业机械化及其自动化",
        ],
    ),
    (
        "MATERIAL_CHEM",
        "材料与化工类",
        "材料、化学、化工、冶金、制药和精细化工等方向。",
        True,
        [
            "材料科学与工程", "材料物理", "材料化学", "金属材料工程",
            "无机非金属材料工程", "高分子材料与工程", "复合材料与工程",
            "新能源材料与器件", "冶金工程", "化学", "应用化学",
            "化学工程与工艺", "制药工程", "能源化学工程", "精细化工",
        ],
    ),
    (
        "CIVIL_ARCH_TRAFFIC",
        "土建建筑交通类",
        "土木、建筑、规划、交通运输和工程管理等方向。",
        True,
        [
            "土木工程", "建筑学", "城乡规划", "风景园林", "给排水科学与工程",
            "建筑环境与能源应用工程", "道路桥梁与渡河工程", "城市地下空间工程",
            "工程管理", "工程造价", "交通工程", "交通运输", "智慧交通", "铁道工程",
        ],
    ),
    (
        "MATH_STATS_PHYSICS",
        "数学统计物理类",
        "数学、统计、物理和系统科学等基础理科方向。",
        True,
        [
            "数学", "数学与应用数学", "信息与计算科学", "统计学", "应用统计学",
            "数据计算及应用", "物理学", "应用物理学", "核物理", "声学", "系统科学",
        ],
    ),
    (
        "GEO_ENV_SAFETY",
        "地理环境安全类",
        "环境、地理、测绘、遥感、安全和应急管理等方向。",
        True,
        [
            "环境科学", "环境工程", "环境科学与工程", "资源环境科学", "地理科学",
            "地理信息科学", "自然地理与资源环境", "人文地理与城乡规划",
            "测绘工程", "遥感科学与技术", "安全工程", "应急技术与管理", "消防工程",
        ],
    ),
    (
        "BIO_MED_FOOD",
        "生物医药食品类",
        "生物、医学、药学、护理、食品工程和食品安全等方向。",
        True,
        [
            "生物科学", "生物技术", "生物信息学", "生物工程", "生物医学工程",
            "基础医学", "临床医学", "口腔医学", "公共卫生与预防医学", "预防医学",
            "药学", "药物制剂", "中药学", "护理学", "医学检验技术",
            "食品科学与工程", "食品质量与安全", "食品营养与健康",
        ],
    ),
    (
        "AGRI_FORESTRY",
        "农林牧渔类",
        "农学、园艺、植保、动医、林学、水产和草业等方向。",
        True,
        [
            "农学", "园艺", "植物保护", "种子科学与工程", "农业资源与环境",
            "动物科学", "动物医学", "兽医学", "林学", "园林", "森林保护",
            "水产养殖学", "海洋渔业科学与技术", "草业科学",
        ],
    ),
    (
        "ECON_FINANCE",
        "经济金融类",
        "经济、财政、金融、保险、贸易、精算和金融科技等方向。",
        True,
        [
            "经济学", "经济统计学", "国民经济学", "应用经济学", "财政学",
            "金融学", "金融工程", "保险学", "投资学", "国际经济与贸易",
            "贸易经济", "数字经济", "精算学", "金融科技",
        ],
    ),
    (
        "ACCOUNTING_AUDIT",
        "财会审计税务类",
        "会计、财务管理、审计、税务和资产评估等方向。",
        True,
        [
            "会计学", "财务管理", "审计学", "税收学", "税务", "资产评估",
            "财务会计教育", "大数据与会计",
        ],
    ),
    (
        "BUSINESS_MANAGEMENT",
        "工商管理与市场类",
        "工商管理、市场营销、人力资源和国际商务等方向。",
        True,
        [
            "工商管理", "市场营销", "人力资源管理", "劳动关系", "创业管理",
            "国际商务", "零售业管理", "企业管理", "组织行为学",
        ],
    ),
    (
        "SUPPLY_CHAIN_IE",
        "物流供应链与工业工程类",
        "物流、供应链、采购、工业工程、质量和电商等方向。",
        True,
        [
            "物流管理", "物流工程", "采购管理", "供应链管理", "工业工程",
            "质量管理工程", "标准化工程", "电子商务", "跨境电子商务",
        ],
    ),
    (
        "PUBLIC_ADMIN",
        "公共管理类",
        "行政、公共事业、社保、土地资源、城市管理和健康服务等方向。",
        True,
        [
            "行政管理", "公共事业管理", "劳动与社会保障", "土地资源管理",
            "城市管理", "海关管理", "公共关系学", "健康服务与管理",
        ],
    ),
    (
        "LAW_COMPLIANCE",
        "法学合规类",
        "法律、知识产权、合规、政治社会和公安侦查等方向。",
        True,
        [
            "法学", "法律", "知识产权", "信用风险管理与法律防控",
            "国际经贸规则", "政治学", "社会学", "社会工作", "公安学", "治安学", "侦查学",
        ],
    ),
    (
        "LANGUAGE_LITERATURE",
        "语言文学类",
        "中文、秘书学、英语、翻译和外国语言文学等方向。",
        True,
        [
            "汉语言文学", "汉语言", "中文", "中国语言文学", "秘书学", "英语",
            "商务英语", "翻译", "日语", "德语", "法语", "俄语", "西班牙语",
            "外国语言文学",
        ],
    ),
    (
        "NEWS_MEDIA",
        "新闻传播与传媒类",
        "新闻、传播、广告、编辑出版、新媒体和会展等方向。",
        True,
        [
            "新闻学", "传播学", "广播电视学", "广告学", "编辑出版学",
            "网络与新媒体", "数字出版", "国际新闻与传播", "会展",
        ],
    ),
    (
        "DESIGN_ART",
        "设计艺术类",
        "艺术、视觉传达、环境设计、产品设计、动画和美术等方向。",
        True,
        [
            "艺术学", "视觉传达设计", "环境设计", "产品设计", "服装与服饰设计",
            "数字媒体艺术", "动画", "美术学", "绘画", "摄影", "艺术设计学", "工业设计",
        ],
    ),
    (
        "EDUCATION_PSY",
        "教育心理体育类",
        "教育学、心理学、教育技术、体育教育和运动训练等方向。",
        True,
        [
            "教育学", "学前教育", "小学教育", "特殊教育", "教育技术学",
            "科学教育", "心理学", "应用心理学", "体育教育", "运动训练",
            "社会体育指导与管理",
        ],
    ),
    (
        "HISTORY_PHILOSOPHY",
        "历史哲学类",
        "历史、考古、文博、哲学、逻辑、宗教和伦理学等方向。",
        True,
        [
            "历史学", "世界史", "考古学", "文物与博物馆学", "哲学",
            "逻辑学", "宗教学", "伦理学",
        ],
    ),
    (
        "OTHER_GENERAL",
        "其他通用类（默认停用）",
        "泛称模板，默认停用；需 HR 明确启用或拆分后才参与分配。",
        False,
        ["相关专业", "理工类", "工科类", "文科类", "经管类", "管理类", "工程类", "技术类", "复合型专业"],
    ),
]


def normalize_major_name(value):
    return "".join((value or "").lower().split())


class Command(BaseCommand):
    help = "初始化基础配置、RBAC 角色权限和本地测试账号"

    def handle(self, *args, **options):
        ensure_rbac_defaults()
        defaults = {
            **{
                key: item["default"]
                for key, item in PUBLIC_AI_CONFIG_REGISTRY.items()
            },
            "welink_enabled": False,
        }
        for key, value in defaults.items():
            m.Config.objects.update_or_create(key=key, defaults={"value": value})

        # 院校清单之外的学校统一使用该预置标签，作为院校准入规则的稳定输入。
        m.SchoolTag.objects.update_or_create(
            code="NON_TARGET",
            defaults={"name": "非目标院校", "is_default": False, "is_active": True},
        )

        for index, (code, name, description, is_active, aliases) in enumerate(
            MAJOR_DICTIONARY, start=1
        ):
            category, _ = m.MajorCategory.objects.get_or_create(
                code=code,
                defaults={
                    "name": name,
                    "description": description,
                    "is_active": is_active,
                    "sort_order": index * 10,
                },
            )
            for alias in aliases:
                normalized = normalize_major_name(alias)
                m.MajorAlias.objects.update_or_create(
                    category=category,
                    normalized_name=normalized,
                    source=m.MajorAlias.SOURCE_BUILTIN,
                    defaults={
                        "name": alias,
                        "match_type": m.MajorAlias.MATCH_CONTAINS,
                        "note": "内置第一版词表",
                        "is_active": True,
                    },
                )

        tech_l2, _ = m.Department.objects.update_or_create(
            name="技术二部", parent=None, defaults={"level": 2}
        )
        product_l2, _ = m.Department.objects.update_or_create(
            name="产品二部", parent=None, defaults={"level": 2}
        )
        tech_l3, _ = m.Department.objects.update_or_create(
            name="技术平台组", parent=tech_l2, defaults={"level": 3}
        )
        algo_l3, _ = m.Department.objects.update_or_create(
            name="算法应用组", parent=tech_l2, defaults={"level": 3}
        )
        product_l3, _ = m.Department.objects.update_or_create(
            name="产品运营组", parent=product_l2, defaults={"level": 3}
        )

        contacts = {
            "L2001": ("技术二级接口人A", "l2001@example.com", tech_l2, m.Contact.LEVEL_SECONDARY),
            "L2002": ("产品二级接口人B", "l2002@example.com", product_l2, m.Contact.LEVEL_SECONDARY),
            "T3001": ("技术三级接口人A", "t3001@example.com", tech_l3, m.Contact.LEVEL_TERTIARY),
            "T3002": ("算法三级接口人B", "t3002@example.com", algo_l3, m.Contact.LEVEL_TERTIARY),
            "T3003": ("产品三级接口人C", "t3003@example.com", product_l3, m.Contact.LEVEL_TERTIARY),
        }
        contact_objects = {}
        for employee_no, (name, email, department, level) in contacts.items():
            contact, _ = m.Contact.objects.update_or_create(
                employee_no=employee_no,
                defaults={
                    "name": name,
                    "email": email,
                    "department": department,
                    "contact_level": level,
                    "is_active": True,
                },
            )
            contact_objects[employee_no] = contact

        users = [
            ("admin", "管理员", User.ROLE_ADMIN, None),
            ("hr", "HR", User.ROLE_HR, None),
            ("L2001", "二级接口人", User.ROLE_SECONDARY_CONTACT, contact_objects["L2001"]),
            ("L2002", "二级接口人", User.ROLE_SECONDARY_CONTACT, contact_objects["L2002"]),
            ("T3001", "三级接口人", User.ROLE_TERTIARY_CONTACT, contact_objects["T3001"]),
            ("T3002", "三级接口人", User.ROLE_TERTIARY_CONTACT, contact_objects["T3002"]),
            ("T3003", "三级接口人", User.ROLE_TERTIARY_CONTACT, contact_objects["T3003"]),
        ]
        for username, group_name, role, contact in users:
            user, _ = User.objects.update_or_create(
                username=username,
                defaults={
                    "email": contact.email if contact else f"{username}@example.com",
                    "role": role,
                    "contact": contact,
                    "is_active": True,
                    "is_staff": group_name == "管理员",
                },
            )
            user.set_unusable_password()
            user.save(update_fields=["password"])
            user.groups.set([Group.objects.get(name=group_name)])

        self.stdout.write(
            self.style.SUCCESS(
                "基础数据已初始化（配置 + RBAC + 多接口人测试账号，账号密码均不可用）"
            )
        )
