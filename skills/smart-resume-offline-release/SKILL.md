---
name: smart-resume-offline-release
description: 构建、验证和封装海纳智聘四镜像 linux/amd64 离线包，发布到 GitHub；只有用户明确要求时复制到移动硬盘。
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

默认版本为 `YYYYMMDD-HHMM-amd64`，只有显式传入 `--drive` 才复制到移动硬盘。目标服务器架构固定为 `linux/amd64`；不要根据本机 Docker 架构改为 arm64。

## 执行约束

- 先运行 `--check` 或由脚本自动执行同等前置检查。
- 保留尚未交付的源码修改，不 stash、不 reset。代码回退依赖 Git 提交和不可变标签，不创建源码备份；GitHub 上传并回下载验证后清理本地临时发布包。
- 若目标目录或同名压缩包已存在，停止并使用新版本号，不覆盖。
- Docker 不可用、目标盘未挂载、镜像架构错误或任何校验失败时立即停止。
- 不在命令、日志或发布包中写入真实密钥；构建检查仅使用临时占位值。
- 只将 `.tar.gz` 和配套 `.sha256` 复制到移动硬盘。

## 验收与回报

仅在以下项目全部通过后报告完成：

- app、PostgreSQL、Redis 与独立 Kernel 镜像均为 `linux/amd64`。
- 镜像内 `resume-platform check` 通过，验证 Go 程序、嵌入 React、协议、Poppler 和 CA。
- 包内 `SHA256SUMS`、外层 `.sha256` 和 `docker load` 回读通过。
- 离线 Compose 不含 `build:`，引用本次版本镜像，默认四个常驻服务；Go app 同时提供页面、API 和后台任务，健康检查覆盖 HTTP、PostgreSQL 和 Redis。
- 随包部署 Skill 包含 `assets/compose.model-ca.yml` 和 `scripts/compose.sh`，环境模板包含 `AGENT_KERNEL_CA_BUNDLE`；现场 CA 不封装入通用发布包。平台 TEST 和 Kernel 均默认验证 TLS，企业 CA 需挂载到两端；真实模型任务验收未做时须明确标注。
- GitHub 附件回下载 SHA-256 通过，并与上传包逐字节一致；明确要求移动硬盘时同样验证副本。

GitHub 附件验证后删除本地临时 release、回下载文件和版本分发目录。最终报告版本、GitHub Release/附件链接、大小与校验结果，不把本地 release 作为长期交付位置。
