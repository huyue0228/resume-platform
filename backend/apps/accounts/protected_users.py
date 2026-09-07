"""代码内置且不允许通过用户管理接口修改的系统账号。"""

PROTECTED_ADMIN_USERNAME = "012358"
PROTECTED_ADMIN_EMAIL = "huyue2@ueascend.com"


def is_protected_admin(user):
    return bool(user and user.username == PROTECTED_ADMIN_USERNAME)
