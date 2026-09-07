"""身份键：姓名 + 手机号 → identity_hash，全局唯一锁定一位同学。"""
import hashlib
import re


def normalize_name(name) -> str:
    """姓名去首尾空格、去内部空白。"""
    return re.sub(r"\s+", "", str(name or "")).strip()


def normalize_phone(phone) -> str:
    """手机号去非数字、去 +86 前缀。"""
    digits = re.sub(r"\D", "", str(phone or ""))
    if digits.startswith("86") and len(digits) > 11:
        digits = digits[2:]
    return digits


def identity_hash(name, phone) -> str:
    base = f"{normalize_name(name)}|{normalize_phone(phone)}"
    return hashlib.sha256(base.encode("utf-8")).hexdigest()
