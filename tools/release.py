"""仓库自有的镜像构建与部署模板打包；不依赖任何 CI 服务商。

push 必须显式选择；认证由运行环境提供，绝不读取或生成用户凭据。
"""
import argparse
import hashlib
import json
import re
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
COMPONENTS = {
    "app": (".", "Dockerfile"),
    "postgres": ("docker/postgres", "docker/postgres/Dockerfile"),
    "redis": ("docker/redis", "docker/redis/Dockerfile"),
}

def version(value):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", value):
        raise ValueError("版本必须是安全的单段 Docker tag")
    return value

def image_reference(item):
    image = item["image"]
    if not isinstance(image, str) or not re.fullmatch(r"[a-z0-9][a-z0-9./:_-]*:[A-Za-z0-9_.-]+", image):
        raise ValueError("非法镜像引用")
    if item["published"]:
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", item["digest"]):
            raise ValueError("发布镜像必须提供 SHA-256 digest")
        # registry.company:5000/team/repo:tag 中第一个冒号是端口。
        return image.rsplit(":", 1)[0] + "@" + item["digest"]
    return image

def checksums(directory):
    files = sorted(p for p in directory.rglob("*") if p.is_file() and p.name != "SHA256SUMS")
    lines = [hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.relative_to(directory).as_posix() for p in files]
    (directory / "SHA256SUMS").write_text("\n".join(lines) + "\n")

def build_images(args):
    version(args.version)
    output = ROOT / "release" / args.version / "images"
    output.mkdir(parents=True, exist_ok=True)
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    for component in ([args.component] if args.component else COMPONENTS):
        record = output / (component + ".json")
        if record.exists():
            raise ValueError(f"拒绝覆盖已有镜像记录：{record}")
        image = f"{args.image_prefix}-{component}:{args.version}"
        image_reference({"image": image, "published": False})
        context, dockerfile = COMPONENTS[component]
        with tempfile.TemporaryDirectory(prefix="resume-image-") as temporary:
            metadata = Path(temporary) / "metadata.json"
            subprocess.run(["docker", "buildx", "build", "--platform", args.platform,
                "--file", dockerfile, "--tag", image, "--metadata-file", str(metadata),
                "--push" if args.push else "--load", context], cwd=ROOT, check=True)
            digest = json.loads(metadata.read_text()).get("containerimage.digest", "")
        item = dict(component=component, version=args.version, image=image, digest=digest,
                    published=args.push, platform=args.platform, commit=commit)
        image_reference(item)
        record.write_text(json.dumps(item, indent=2) + "\n")
        print(record)

def package(version_name, images_dir, output_dir=None):
    version(version_name)
    items = [json.loads(p.read_text()) for p in sorted(Path(images_dir).glob("*.json"))]
    if len(items) != len(COMPONENTS) or {x["component"] for x in items} != set(COMPONENTS):
        raise ValueError("平台包必须包含且仅包含三个平台镜像（app、postgres、redis）")
    for item in items:
        if item["version"] != version_name or item["platform"] != "linux/amd64" or type(item["published"]) is not bool:
            raise ValueError("镜像版本、架构或发布标识不一致")
        image_reference(item)
    if len({x["commit"] for x in items}) != 1 or len({x["published"] for x in items}) != 1:
        raise ValueError("禁止混合不同提交或演练/正式镜像")
    output = Path(output_dir) if output_dir else ROOT / "dist"
    output.mkdir(parents=True, exist_ok=True)
    archive = output / f"resume-platform-{version_name}.tar.gz"
    if archive.exists() or archive.with_suffix(archive.suffix + ".sha256").exists():
        raise ValueError("拒绝覆盖已有发布包，请使用新版本")
    assets = ROOT / "skills/smart-resume-offline-release/assets"
    with tempfile.TemporaryDirectory(prefix="resume-package-") as temporary:
        staging = Path(temporary)
        for source, name in [(assets / "docker-compose.yml", "compose.yml"),
                (ROOT / "skills/smart-resume-offline-deploy/assets/compose.model-ca.yml", "compose.model-ca.yml"),
                (ROOT / "ops/delivery/README.md", "README.md"),
                (ROOT / "backend/resume_contracts/bundle/manifest.json", "contract-manifest.json")]:
            shutil.copyfile(source, staging / name)
        env = (assets / "env.example").read_text().replace("__APP_VERSION__", version_name)
        env = env.replace("__AGENT_KERNEL_VERSION__", "").replace("__AGENT_KERNEL_IMAGE__", "")
        (staging / "env.example").write_text(env)
        settings = ["APP_VERSION=" + version_name]
        (staging / "images").mkdir()
        for item in items:
            key = item["component"].upper() + "_IMAGE" + ("_RELEASE" if item["component"] in ("postgres", "redis") else "")
            settings.append(key + "=" + image_reference(item))
            (staging / "images" / (item["component"] + ".json")).write_text(json.dumps(item, indent=2) + "\n")
        settings.extend(["# 独立选择已验收的 Kernel：", "AGENT_KERNEL_IMAGE=", "AGENT_KERNEL_VERSION="])
        (staging / "images.env").write_text("\n".join(settings) + "\n")
        checksums(staging)
        with tarfile.open(archive, "x:gz") as tar:
            for path in sorted(staging.rglob("*")):
                if path.is_file():
                    tar.add(path, arcname=path.relative_to(staging), recursive=False)
    archive.with_suffix(archive.suffix + ".sha256").write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")
    print(archive)
    return archive

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    images = sub.add_parser("images")
    images.add_argument("--version", required=True)
    images.add_argument("--image-prefix", required=True)
    images.add_argument("--platform", default="linux/amd64")
    images.add_argument("--component", choices=COMPONENTS)
    images.add_argument("--push", action="store_true")
    bundle = sub.add_parser("package")
    bundle.add_argument("--version", required=True)
    bundle.add_argument("--images-dir", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "images":
        build_images(args)
    else:
        package(args.version, args.images_dir)

if __name__ == "__main__":
    main()
