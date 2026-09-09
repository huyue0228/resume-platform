---
name: smart-resume-offline-deploy
description: 在 Linux 服务器上部署、验证、卸载海纳智聘。支持当前源码仓库 amd64/arm64 构建部署和 amd64 纯镜像离线包部署，并通过固定编号菜单和二次确认控制 Docker 变更及数据清理。
---

# 海纳智聘离线部署

先阅读本文件，再执行 `scripts/` 下的脚本；不得手工省略确认步骤，也不得在日志、对话或截图中输出 `.env` 内的密钥。脚本可从源码仓库的 `skills/` 目录调用，也可随纯镜像离线包一同调用；未能自动判断根目录时，使用 `DEPLOY_ROOT=/path/to/package` 指定部署根目录。

## 固定交互

与用户交互时，只展示当前阶段规定的编号选项，等待用户回复单个编号。收到其它内容时，原样重复该菜单；不要猜测意图、不要改用自由文本确认、不要把不同阶段的选项合并。

- 部署前检查：`1. 已完成检查，继续`、`2. 先修改 .env`、`3. 取消`。
- 未找到环境文件：`1. 创建 .env、自动生成密钥并退出`、`2. 取消`。
- 已有部署：`1. 升级并保留数据`、`2. 仅查看状态`、`3. 取消`。
- 开始部署：`1. 构建/导入镜像、初始化并启动`、`2. 取消`。
- 卸载范围：`1. 常规卸载并保留数据`、`2. 删除容器和数据卷`、`3. 删除容器、数据卷和镜像`、`4. 取消`。
- 不可恢复清理：`1. 确认永久删除`、`2. 返回并保留数据`。

## 包含内容

- `scripts/deploy.sh`：校验、导入镜像、初始化数据库、启动并验证服务。
- `scripts/validate-w3-env.sh`：校验生产 `DJANGO_DEBUG=False`、W3 已启用，并检查登录必填配置、HTTPS 端点、字段路径、客户端认证方式和精确回调 URI；任何失败都发生在 Docker 变更前。
- `scripts/verify.sh`：检查服务状态、Go 运行依赖、协议和嵌入的 React 资源。
- `scripts/uninstall.sh`：停止并卸载服务；默认保留数据库和上传文件。
- `assets/deployment-agent.md`：部署时必须执行的确认与交付口径。
- `assets/uninstall-agent.md`：卸载时必须执行的风险确认口径。

## 部署

1. 确认服务器 CPU 为 `x86_64/amd64` 或 `aarch64/arm64`，Docker Engine 与 Docker Compose v2 已安装。当前纯镜像离线包仅支持 amd64；源码模式支持两种架构，且 `.env` 的 `DOCKER_PLATFORM` 必须与服务器一致。
2. 在源码仓库或离线包根目录执行：

```bash
bash skills/smart-resume-offline-deploy/scripts/deploy.sh
```

若 Skill 随离线包存放在包根目录下一层，则将上面的 `skills/smart-resume-offline-deploy` 改为实际 Skill 目录名。

3. 镜像、端口、Go 后台并发/文本提取、数据库名/用户已经写入模板。脚本首次运行会创建权限为 `600` 的 `.env`，并自动生成互不复用的 `DJANGO_SECRET_KEY`、`POSTGRES_PASSWORD`、`USAGE_METRICS_TOKEN` 和 `AGENT_KERNEL_TOKEN`，密钥不回显。部署人员需把实际域名写入 `DJANGO_ALLOWED_HOSTS`，按下一节完成 DNS、证书和 HTTPS 反向代理，并补齐 W3 OAuth2 配置。
4. `DEPLOY_MODE=auto`（默认）在存在 `smart-resume-filter-images-amd64.tar` 时选择离线模式，否则从当前源码构建。可显式指定 `DEPLOY_MODE=offline` 或 `DEPLOY_MODE=source`。
5. 离线模式要求交付包内的 `docker-compose.yml` 只使用 `image:`，不得保留 `build:`；源码模式使用当前项目的 Compose 构建 app、PostgreSQL、Redis 镜像，并使用独立发布的 Agent Kernel 镜像。
6. 首次部署才会执行 `init` 写入基础权限、账号和预置数据。检测到已有部署时，脚本只更新镜像并启动服务，迁移由 app 自动完成，不会重置管理员在系统设置中维护的配置。
7. 部署不决定 AI 功能是否启用、模型连接或 API Key。服务启动后，由拥有权限的管理员在「系统设置 → AI 模型连接」配置并测试；不要在部署对话、脚本参数或日志中提供 API Key。
8. 生产只提供 W3 登录，因此 W3 OAuth2 是可用部署的必要条件。模板中的 `W3_OAUTH2_ENABLED=False` 只是首次生成 `.env` 时的安全占位；正式部署前必须通过安全渠道补齐配置并改为 `True`，同时保持 `DJANGO_DEBUG=False`。部署脚本会在任何 Docker 变更前执行校验，DEBUG 开启、W3 关闭、缺少必填项、端点非 HTTPS、客户端认证方式无效或回调路径不精确均立即停止。本地密码 API 与 Django Admin 路由均已删除；DEBUG 开发令牌不是生产应急入口。
9. Grafana JSON 数据源使用 `GET /api/analytics/usage/overview/`，以 `.env` 中的 `USAGE_METRICS_TOKEN` 作为 `X-Usage-Metrics-Key` 请求头。密钥只通过安全配置注入，不写入面板 JSON、仓库、工单、对话或命令历史。

### 模型路由器的企业 CA（Agent 分析前必须核对）

平台「模型 TEST」和 Kernel 实际分析均默认校验 TLS。两者分别运行在 app 与 agent-kernel 容器中，需要各自信任模型路由器的证书。模型 TEST 成功和 Kernel `/healthz` 正常仍不能代替真实 Agent 分析验收。

模型路由器使用企业/私有 CA 时，使用现场已有的受信任 CA PEM 文件，包含所需根 CA 和中间 CA 证书；不要放入私钥。将宿主机文件的绝对路径写入 `.env`：

```dotenv
AGENT_KERNEL_CA_BUNDLE=/etc/company-ca/model-router-ca.pem
AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY=False
```

CA 文件应可被容器内非 root 的 `agent` 用户读取，例如公开 CA 文件权限为 `644`。部署、验证和卸载脚本检测到该配置后，会自动叠加 `assets/compose.model-ca.yml`，把文件分别只读挂载到 app 的 `/etc/resume-platform/model-ca.pem` 和 Kernel 的 `/etc/agent-kernel/model-ca.pem`，并设置各自的 `SSL_CERT_FILE`。挂载禁止自动创建宿主机路径，避免缺失的证书被当成目录创建。使用公网受信任证书时，此项可以留空。交付包包含挂载模板，现场 CA 文件单独提供。

直接使用 Docker Compose 运维时，每次都必须带上同一份 CA 覆盖文件，否则可能重建为未挂载 CA 的容器。以下为源码目录命令；离线包中将 Skill 路径换成 `smart-resume-offline-deploy-skill`：

```bash
docker compose --project-name smart-resume-filter --env-file .env \
  -f docker-compose.yml \
  -f skills/smart-resume-offline-deploy/assets/compose.model-ca.yml \
  up -d --force-recreate app agent-kernel
```

沿用实际部署的 Compose project name。CA 内容更新后也要重建 app 与 Kernel 容器，确保进程重新加载信任库。`verify.sh` 会检查容器内 CA 文件可读且非空；这一步只验证挂载，不验证远端证书。管理员还须在系统设置完成模型 TEST，再从简历库提交一条真实 Agent 分析，确认模型调用和结果完成。若仍失败，核对路由器完整证书链、Base URL 域名与证书 SAN，以及服务器时间。

### 域名与 HTTPS 反向代理

W3 生产回调必须使用完整 HTTPS 域名，因此生产部署默认需要在 Compose 的 app 容器前放置企业网关、WAF、Nginx、Caddy 或等价反向代理，由它管理域名证书并终止 TLS。标准链路为：

```text
浏览器 / W3
  -> https://海纳智聘域名:443
  -> HTTPS 反向代理或企业网关
  -> http://app宿主机地址:5173
  -> app 容器内 Go 程序（React 页面与 /api/*）
```

部署人员必须完成以下事项：

1. 将生产域名 DNS 解析到反向代理或企业网关。
2. 为域名配置受客户端信任且未过期的 TLS 证书；80 端口只允许跳转到 HTTPS，不把 OAuth2 回调降级到 HTTP。
3. 反向代理把 `/`、`/api/`、`/media/` 等所有路径统一转发到 app 暴露端口。
4. 转发时保留 `Host`，并设置 `X-Real-IP`、`X-Forwarded-For`、`X-Forwarded-Proto=https`；上传大小不得低于业务文件要求，读写超时建议不低于 1800 秒。
5. 同机反代时建议设置 `FRONTEND_BIND=127.0.0.1`，避免绕过 HTTPS 直接访问容器端口。若企业网关位于其它机器，则 frontend 只绑定受控内网地址，并用防火墙仅允许网关访问。
6. `.env` 中 `DJANGO_ALLOWED_HOSTS` 填生产域名，`W3_OAUTH2_REDIRECT_URI` 必须填写并在 W3 平台登记为同一域名下的 `https://生产域名/api/auth/w3/callback/`，域名、协议、端口和路径必须逐字一致。

同机 Nginx 外层反代可参考：

```nginx
server {
    listen 80;
    server_name resume.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name resume.example.com;

    ssl_certificate     /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:5173;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_connect_timeout 60s;
        proxy_send_timeout 1800s;
        proxy_read_timeout 1800s;
    }
}
```

证书路径、域名和 frontend 端口必须按现场修改。若使用企业统一网关，不要求重复安装 Nginx，但必须满足相同的 TLS、路径、请求头、超时和访问控制要求。

### W3 OAuth2 环境变量

正式部署前必须明确填写：

- `DJANGO_DEBUG=False`
- `W3_OAUTH2_ENABLED=True`
- `W3_OAUTH2_CLIENT_ID`
- `W3_OAUTH2_AUTHORIZE_URL`
- `W3_OAUTH2_TOKEN_URL`
- `W3_OAUTH2_USERINFO_URL`
- `W3_OAUTH2_REDIRECT_URI`：必须是在 W3 平台登记的完整 HTTPS 地址，路径精确为 `/api/auth/w3/callback/`
- `W3_OAUTH2_EMPLOYEE_NO_FIELD`：UserInfo 中工号的点路径；当前 W3 返回顶层 `employeeNumber`
- `W3_OAUTH2_EMAIL_FIELD`：UserInfo 中邮箱的点路径；当前 W3 返回顶层 `email`
- `W3_OAUTH2_CLIENT_AUTH_METHOD`：仅允许 `client_secret_basic`、`client_secret_post` 或 `none`
- `W3_OAUTH2_TIMEOUT_SECONDS`
- `W3_OAUTH2_TRANSACTION_TTL_SECONDS`

当客户端认证方式为 `client_secret_basic` 或 `client_secret_post` 时，`W3_OAUTH2_CLIENT_SECRET` 也必须填写；只有 W3 明确登记为公开客户端且认证方式为 `none` 时才可留空。`W3_OAUTH2_SCOPE` 按 W3 实际要求填写；协议允许为空，但若 W3 要求 scope，则它也是现场必填项。

模板已预填当前 UserInfo 映射 `W3_OAUTH2_EMPLOYEE_NO_FIELD=employeeNumber`、`W3_OAUTH2_EMAIL_FIELD=email`，并提供以下安全默认值，通常不修改：`W3_OAUTH2_FRONTEND_CALLBACK_URL=/login`、`W3_OAUTH2_USE_PKCE=True`。模板不含本地登录开关；`tenantId`、`uuid`、`globalUserID` 当前不参与账号匹配，也不落库。客户端密钥不得出现在对话、日志或截图中。

检测到已有同项目容器或数据卷时，脚本不会替换已有安全密钥；`DJANGO_SECRET_KEY` 或 `POSTGRES_PASSWORD` 缺失/仍为占位值时必须恢复原 `.env`。旧环境仅缺新增的 `USAGE_METRICS_TOKEN` 时，脚本会补齐该项且不修改其它密钥。

部署脚本会先显示部署前检查菜单；若检测到同项目已有容器，会说明升级会保留数据卷与配置并让操作者选择升级、仅查看状态或取消。

## 验证

```bash
bash skills/smart-resume-offline-deploy/scripts/verify.sh
```

成功条件：`app`、`agent-kernel`、`db`、`redis` 四个常驻服务均处于运行状态；app 的 `resume-platform healthcheck` 验证 HTTP、PostgreSQL 与 Redis，容器内 `resume-platform check` 验证 Go 运行依赖、协议和嵌入的 React 资源。默认 NAME 为 `smart-resume-filter-app`、`-agent-kernel`、`-postgres`、`-redis`，可用 `COMPOSE_PROJECT_NAME` 指定统一前缀；升级必须沿用原项目名。生产环境还必须从客户端网络访问 `https://生产域名/` 和 `https://生产域名/api/auth/w3/status/`，确认使用有效证书、HTTP 自动跳转 HTTPS、响应经过 frontend 且 W3 状态就绪；仅验证 `http://服务器IP:5173` 不视为生产验收完成。

通过安全方式把监控密钥注入当前 shell 后，可做 Grafana 查询接口的最小验证：

```bash
curl --fail --silent --show-error \
  -H "X-Usage-Metrics-Key: ${USAGE_METRICS_TOKEN}" \
  "https://resume.example.com/api/analytics/usage/overview/?granularity=day"
```

## 卸载

```bash
bash skills/smart-resume-offline-deploy/scripts/uninstall.sh
```

默认卸载只停止并删除容器、网络，保留 PostgreSQL 数据卷和上传文件卷。若选择删除数据卷或镜像，脚本会再显示“确认永久删除 / 返回并保留数据”菜单；未选择确认不会执行清理。
