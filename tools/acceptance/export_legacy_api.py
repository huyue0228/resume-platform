"""在指定验收数据库中读取 Django API，生成 Go 迁移兼容性基准。禁止连接生产数据库。"""
import argparse
import json
import os
import sys
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--backend', type=Path, required=True, help='外部旧版本检出目录中的 backend；当前 Go 仓不包含 Django')
parser.add_argument('--username', required=True)
parser.add_argument('--output', type=Path, required=True)
args = parser.parse_args()
if not os.environ.get('POSTGRES_DB', '').endswith('_verify'):
    raise SystemExit('POSTGRES_DB 必须是名称以 _verify 结尾的隔离验收数据库')
sys.path.insert(0, str(args.backend.resolve()))
os.environ.setdefault('DJANGO_SETTINGS_MODULE', 'config.settings')
import django
django.setup()
from rest_framework.test import APIClient
from apps.accounts.models import User
from apps.core import models as m
client = APIClient()
client.force_authenticate(user=User.objects.get(username=args.username))
paths = ['/api/', '/api/me/', '/api/permissions/', '/api/configs/', '/api/schools/', '/api/school-tags/', '/api/school-tag-rules/', '/api/departments/', '/api/jobs/', '/api/contacts/', '/api/users/', '/api/roles/', '/api/workflows/', '/api/major-categories/', '/api/major-aliases/', '/api/pipeline/runs/']
for model, resource in [(m.Candidate, 'candidates'), (m.AssignmentAttempt, 'workflow-attempts'), (m.AgentDispatchDecision, 'agent-decisions'), (m.Resume, 'resumes')]:
    ids = model.objects.order_by('-id').values_list('id', flat=True)[:2]
    paths.extend('/api/' + resource + '/' + str(pk) + '/' for pk in ids)
    paths.append('/api/' + resource + '/?page_size=2')
data = {}
for path in paths:
    response = client.get(path)
    data[path] = {'status': response.status_code, 'body': json.loads(response.content)}
args.output.write_text(json.dumps(data, ensure_ascii=False))
args.output.chmod(0o600)
print(f'Exported {len(data)} API responses to {args.output}')
