---
name: smart-resume-offline-release
description: 为 smart-resume-filter 构建、验证、封装并交付 linux/amd64 Docker 离线包。用户要求打包 Docker 镜像、生成内网部署包、复制到移动硬盘、校验离线交付物或重新发布当前工作区时使用；默认交付到 /Volumes/ZiTai，并保留未提交源码改动。
---

# 离线镜像发布

在仓库根目录执行统一入口，不要手工重写构建和校验命令：

```bash
bash skills/smart-resume-offline-release/scripts/release.sh
```

先设置 `AGENT_KERNEL_IMAGE` 和 `AGENT_KERNEL_VERSION`，并拉取或加载已独立发布的 Kernel 镜像。脚本自动完成：生成平台时间戳版本、构建三个 `linux/amd64` 平台镜像（app、PostgreSQL、Redis）（不构建 Kernel）、将指定 Kernel 镜像一起封装、容器内检查、生成纯 `image:` Compose 离线包、计算双层 SHA-256、回读镜像、复制到移动硬盘并复验。

## 参数

```bash
# 只检查环境，不生成产物
bash skills/smart-resume-offline-release/scripts/release.sh --check

# 指定版本或移动硬盘
bash skills/smart-resume-offline-release/scripts/release.sh \
  --version 20260714-1800-amd64 \
  --drive /Volumes/ZiTai

# 只生成本地 release，不复制到移动硬盘
bash skills/smart-resume-offline-release/scripts/release.sh --no-copy

# 镜像已经构建完成时仅重新封装
bash skills/smart-resume-offline-release/scripts/release.sh \
  --version 20260714-1800-amd64 \
  --skip-build
```

默认版本为 `YYYYMMDD-HHMM-amd64`，默认目标盘为 `/Volumes/ZiTai`。目标服务器架构固定为 `linux/amd64`；不要根据本机 Docker 架构改为 arm64。

## 执行约束

- 先运行 `--check` 或由脚本自动执行同等前置检查。
- 保留当前 Git 工作区，不 stash、不 reset、不删除旧发布包。
- 若目标目录或同名压缩包已存在，停止并使用新版本号，不覆盖。
- Docker 不可用、目标盘未挂载、镜像架构错误或任何校验失败时立即停止。
- 不在命令、日志或发布包中写入真实密钥；构建检查仅使用临时占位值。
- 只将 `.tar.gz` 和配套 `.sha256` 复制到移动硬盘。

## 验收与回报

仅在以下项目全部通过后报告完成：

- app、PostgreSQL、Redis 与独立 Kernel 镜像均为 `linux/amd64`。
- 后端 `python manage.py check` 和前端 `nginx -t` 通过。
- 包内 `SHA256SUMS`、外层 `.sha256` 和 `docker load` 回读通过。
- 离线 Compose 不含 `build:`，引用本次版本镜像，默认四个常驻服务；app 内部分别消费 `default` 与 `ai`，并检查两个队列的健康状态。
- 随包部署 Skill 包含 `assets/compose.model-ca.yml` 和 `scripts/compose.sh`，环境模板包含 `AGENT_KERNEL_CA_BUNDLE`；现场 CA 不封装入通用发布包。部署指南必须强调模型 TEST 当前跳过 TLS 校验，真实 Agent 分析才覆盖 Kernel 到模型路由器的 TLS 链路。
- 移动硬盘副本 SHA-256 通过，并与本地压缩包逐字节一致。

最终只需报告版本、目标盘、两个交付文件的绝对路径、大小、外层 SHA-256 和验收结果。
