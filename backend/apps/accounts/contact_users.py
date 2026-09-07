from django.contrib.auth.models import Group
from django.db import transaction

from apps.accounts.models import User
from apps.accounts.protected_users import (
    PROTECTED_ADMIN_EMAIL,
    PROTECTED_ADMIN_USERNAME,
    is_protected_admin,
)
from apps.core import models as m


CONTACT_GROUP_NAMES = ["二级接口人", "三级接口人"]


def _contact_user_role(contact):
    if contact.contact_level == m.Contact.LEVEL_TERTIARY:
        return User.ROLE_TERTIARY_CONTACT, "三级接口人"
    return User.ROLE_SECONDARY_CONTACT, "二级接口人"


@transaction.atomic
def sync_contact_user(contact):
    """按接口人工号和邮箱同步登录账号，并保持密码不可用。"""

    if not contact.email:
        raise ValueError("接口人邮箱不能为空")
    if (
        contact.employee_no == PROTECTED_ADMIN_USERNAME
        or contact.email.casefold() == PROTECTED_ADMIN_EMAIL
    ):
        raise ValueError("该工号或邮箱属于内置管理员，不允许绑定接口人")

    bound_user = User.objects.filter(contact=contact).order_by("id").first()
    username_user = User.objects.filter(username=contact.employee_no).first()
    email_user = User.objects.filter(email__iexact=contact.email).exclude(email="").first()

    if username_user and username_user.contact_id not in (None, contact.id):
        raise ValueError("该工号已绑定其他接口人")
    if bound_user and username_user and bound_user.id != username_user.id:
        raise ValueError("该工号已被其他账号使用")

    target_user = bound_user or username_user
    if is_protected_admin(target_user):
        raise ValueError("内置管理员不允许绑定接口人")
    if email_user and (not target_user or email_user.id != target_user.id):
        raise ValueError("该邮箱已被其他账号使用")

    user = target_user
    created = user is None
    if created:
        user = User(username=contact.employee_no)

    role, group_name = _contact_user_role(contact)
    user.username = contact.employee_no
    user.email = contact.email
    user.role = role
    user.contact = contact
    user.is_active = contact.is_active
    user.set_unusable_password()
    user.save()

    contact_groups = Group.objects.filter(name__in=CONTACT_GROUP_NAMES)
    user.groups.remove(*contact_groups.exclude(name=group_name))
    target_group, _ = Group.objects.get_or_create(name=group_name)
    user.groups.add(target_group)
    return user
