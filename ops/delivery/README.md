# 平台在线部署配置包

本目录对应 `make package` 生成的在线配置小包，含三个平台镜像的发布 digest、Compose、环境模板、CA 覆盖文件、协议 manifest 和校验清单。它不含 Docker 镜像；内网离线部署请下载平台 Release 的 `smart-resume-filter-offline-*.tar.gz` 和 `.sha256`。

app 镜像由 Go 提供嵌入 React、业务 API 和后台任务；另有独立 Kernel、PostgreSQL、Redis，共四个常驻容器。首次 init 为一次性命令。平台和 Kernel 均默认校验模型 TLS，企业 CA 使用附带的覆盖文件只读挂载。

本版本面向新环境，使用独立数据库和媒体目录，配置 HTTPS 域名及 W3 OAuth2；模型连接在授权的系统设置页面填写。平台与 Kernel 必须同时支持协议包 4.0.0（resume-analysis/v4、resume-allocation/v1）。所有范围使用独立分配；不提供旧词表、HC 容量或历史分配模式迁移。不要覆盖已有环境或删除已有数据卷。

代码回退使用 Git 提交和标签。发布附件在 GitHub 校验通过后删除本地临时副本；运行数据不随源码清理。
