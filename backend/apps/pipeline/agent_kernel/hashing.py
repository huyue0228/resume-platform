"""平台本地快照指纹，不定义 Kernel 内部版本。"""
import hashlib
import json


def fingerprint(payload):
    return hashlib.sha256(json.dumps(payload, sort_keys=True, ensure_ascii=False, separators=(",", ":"), default=str).encode()).hexdigest()
