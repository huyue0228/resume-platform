from django.conf import settings
from django.core.management.base import BaseCommand, CommandError
from django.db import transaction
from rest_framework.authtoken.models import Token

from apps.accounts.models import User


class Command(BaseCommand):
    help = "为本地 DEBUG 环境中的启用账号重新签发开发 Token。"

    def add_arguments(self, parser):
        parser.add_argument("--username", required=True, help="现有系统账号用户名/工号")

    @transaction.atomic
    def handle(self, *args, **options):
        if not settings.DEBUG:
            raise CommandError("issue_dev_token 仅允许在 DEBUG=True 的开发环境使用")

        username = str(options["username"]).strip()
        user = User.objects.filter(username=username).first()
        if user is None:
            raise CommandError(f"账号不存在：{username}")
        if not user.is_active:
            raise CommandError(f"账号已停用：{username}")

        Token.objects.filter(user=user).delete()
        token = Token.objects.create(user=user)
        self.stdout.write(token.key)
