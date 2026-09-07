from django.contrib.auth.models import AbstractUser
from django.db import models


class User(AbstractUser):
    """系统用户。角色区分 HR / 二级接口人 / 三级接口人 / 管理员。"""

    ROLE_HR = "hr"
    ROLE_SECONDARY_CONTACT = "secondary_contact"
    ROLE_TERTIARY_CONTACT = "tertiary_contact"
    ROLE_ADMIN = "admin"
    ROLE_CHOICES = [
        (ROLE_HR, "HR"),
        (ROLE_SECONDARY_CONTACT, "二级接口人"),
        (ROLE_TERTIARY_CONTACT, "三级接口人"),
        (ROLE_ADMIN, "管理员"),
    ]

    role = models.CharField(max_length=32, choices=ROLE_CHOICES, default=ROLE_HR)
    contact = models.ForeignKey(
        "core.Contact", null=True, blank=True, on_delete=models.SET_NULL,
        related_name="users",
    )

    def set_password(self, raw_password):
        """系统账号不接受本地密码，仅保留 Django 的不可用密码标记。"""
        self.set_unusable_password()

    def save(self, *args, **kwargs):
        """阻止绕过 set_password 直接保存可用密码哈希。"""
        password_was_usable = self.has_usable_password()
        if password_was_usable:
            self.set_unusable_password()
            update_fields = kwargs.get("update_fields")
            if update_fields is not None:
                kwargs["update_fields"] = set(update_fields) | {"password"}
        return super().save(*args, **kwargs)
