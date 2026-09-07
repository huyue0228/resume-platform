"""Name pinyin helpers used for candidate search."""
import re

try:
    from pypinyin import Style, lazy_pinyin
except ImportError:  # pragma: no cover - exercised in local envs without optional dep
    Style = None
    lazy_pinyin = None


_FALLBACK_PINYIN = {
    "张": "zhang",
    "三": "san",
    "李": "li",
    "四": "si",
    "王": "wang",
    "五": "wu",
    "赵": "zhao",
    "六": "liu",
    "陈": "chen",
    "刘": "liu",
    "杨": "yang",
    "黄": "huang",
    "周": "zhou",
    "吴": "wu",
    "徐": "xu",
    "孙": "sun",
    "胡": "hu",
    "朱": "zhu",
    "高": "gao",
    "林": "lin",
    "何": "he",
    "郭": "guo",
    "马": "ma",
    "罗": "luo",
    "梁": "liang",
    "宋": "song",
    "郑": "zheng",
    "谢": "xie",
    "韩": "han",
    "唐": "tang",
    "冯": "feng",
    "于": "yu",
    "董": "dong",
    "萧": "xiao",
    "程": "cheng",
    "曹": "cao",
    "袁": "yuan",
    "邓": "deng",
    "许": "xu",
    "傅": "fu",
    "沈": "shen",
    "曾": "zeng",
    "彭": "peng",
    "吕": "lv",
    "苏": "su",
    "卢": "lu",
    "蒋": "jiang",
    "蔡": "cai",
    "贾": "jia",
    "丁": "ding",
    "魏": "wei",
    "薛": "xue",
    "叶": "ye",
    "阎": "yan",
    "余": "yu",
    "潘": "pan",
    "杜": "du",
    "戴": "dai",
    "夏": "xia",
    "钟": "zhong",
    "汪": "wang",
    "田": "tian",
    "任": "ren",
    "姜": "jiang",
    "范": "fan",
    "方": "fang",
    "石": "shi",
    "姚": "yao",
    "谭": "tan",
    "廖": "liao",
    "邹": "zou",
    "熊": "xiong",
    "金": "jin",
    "陆": "lu",
    "郝": "hao",
    "孔": "kong",
    "白": "bai",
    "崔": "cui",
    "康": "kang",
    "毛": "mao",
    "邱": "qiu",
    "秦": "qin",
    "江": "jiang",
    "史": "shi",
    "顾": "gu",
    "侯": "hou",
    "邵": "shao",
    "孟": "meng",
    "龙": "long",
    "万": "wan",
    "段": "duan",
    "漕": "cao",
    "钱": "qian",
    "汤": "tang",
    "尹": "yin",
    "黎": "li",
    "易": "yi",
    "常": "chang",
    "武": "wu",
    "乔": "qiao",
    "贺": "he",
    "赖": "lai",
    "龚": "gong",
    "文": "wen",
}


def _ascii_token(value):
    return re.sub(r"[^a-z0-9]", "", value.lower())


def name_to_pinyin(name):
    """Return compact full pinyin and initials for a candidate name."""
    if not name:
        return "", ""
    if lazy_pinyin:
        syllables = lazy_pinyin(name, style=Style.NORMAL, errors="ignore")
    else:
        syllables = []
        for char in name:
            if char in _FALLBACK_PINYIN:
                syllables.append(_FALLBACK_PINYIN[char])
            elif char.isascii():
                token = _ascii_token(char)
                if token:
                    syllables.append(token)
    full = _ascii_token("".join(syllables))
    initials = _ascii_token("".join(item[0] for item in syllables if item))
    return full, initials
