"""导入 backend/sample_data/ 下的样例数据（便于快速填充 demo）。"""
from pathlib import Path

from django.conf import settings
from django.core.management.base import BaseCommand

from apps.ingestion.sources import import_files

MAPPING = {
    "resume_list": "简历信息列表.xlsx",
    "jobs": "校招岗位分类及专业要求.xlsx",
    "schools": "院校分类.xlsx",
    "contacts": "部门接口人信息.xlsx",
    "resume_package": "简历包.zip",
}


class Command(BaseCommand):
    help = "导入 sample_data 下的样例数据"

    def add_arguments(self, parser):
        parser.add_argument("--mode", default="incremental", choices=["incremental", "replace"])

    def handle(self, *args, **options):
        d = Path(settings.BASE_DIR) / "sample_data"
        if not d.exists():
            self.stderr.write("未找到 sample_data，请先运行 gen_sample")
            return
        files, opened = {}, []
        for key, fn in MAPPING.items():
            p = d / fn
            if p.exists():
                f = open(p, "rb")
                files[key] = f
                opened.append(f)
        counts = import_files(files, mode=options["mode"])
        for f in opened:
            f.close()
        self.stdout.write(self.style.SUCCESS(f"导入完成: {counts}"))
