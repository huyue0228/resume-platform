# 平台在线部署配置包

本目录对应 `make package` 生成的在线配置小包，含三个平台镜像的发布 digest、Compose、环境模板、CA 覆盖文件、协议 manifest 和校验清单。它不含 Docker 镜像；内网离线部署请下载平台 Release 的 `smart-resume-filter-offline-*.tar.gz` 和 `.sha256`。

app 镜像由 Go 提供嵌入 React、业务 API 和后台任务；另有独立 Kernel、PostgreSQL、Redis，共四个常驻容器。首次 init 为一次性命令。平台和 Kernel 均默认校验模型 TLS，企业 CA 使用附带的覆盖文件只读挂载。

已有环境保留原 Compose 项目名、数据卷、密钥和 CA。新建环境先生成独立密钥、配置 HTTPS 域名及 W3 OAuth2；模型连接在授权的系统设置页面填写。v3 升级前完成或取消旧任务，配套选择支持 resume-analysis/v3 与 resume-allocation/v1 的 Kernel v3.1.0。存量池默认 legacy，先核对接收状态及试算，再显式启用 execute_v1；回退先暂停新分配。

代码回退使用 Git 提交和标签。发布附件在 GitHub 校验通过后删除本地临时副本；运行数据不随源码清理。
