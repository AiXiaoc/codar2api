<div align="center">

<img src="docs/images/codar-icon.png" alt="Codar2API" width="120">

# Codar2API

**把科搭 Codar 账号变成 OpenAI / Anthropic 兼容 API 的多账号网关 · 附 Web 管理面板**

多账号轮询 · 会话粘滞 · 失败换号 · 每账号请求记录 · IP 封控识别 · 代理池 · Docker 部署

[![Go](https://img.shields.io/badge/Go-1.26.4-00ADD8?logo=go&logoColor=white&style=flat-square)](go.mod)
[![API](https://img.shields.io/badge/API-OpenAI%20%7C%20Anthropic_Compatible-412991?style=flat-square)](#-api-端点)
[![Deploy](https://img.shields.io/badge/Deploy-Docker-2496ED?logo=docker&logoColor=white&style=flat-square)](#快速开始)
[![Transport](https://img.shields.io/badge/Transport-SSE%20%2F%20Streaming-0DBD8B?style=flat-square)](#流式行为细节)
[![License](https://img.shields.io/badge/License-MIT-F7DF1E?style=flat-square)](LICENSE)

</div>

> [!WARNING]
> 本项目只用于管理**你自己名下**的 Codar 账号。请遵守 [Codar](https://codar.speedai.com/) 的服务条款，不要用于账号买卖、共享转售或绕过平台限制。

## 使用声明（必读）

- 本项目是个人开源工具，与科搭 Codar、SpeedAI 没有任何关联，也不代表其官方立场。Codar 名称与图标归其所有者。
- 网关会以你的账号令牌访问 Codar 上游，产生的调用、扣费与风控后果由使用者自行承担。
- 数据（账号令牌、API Key 哈希、代理）只保存在你部署的机器上，项目不会上传任何数据。

## 项目简介

Codar2API 是一个独立运行的 Go 网关：把多个 Codar 账号汇成一个号池，对外提供 OpenAI Chat / Responses 和 Anthropic Messages 接口，客户端（Claude Code、Cursor、Cherry Studio、各类 SDK）只需填写 Base URL 和 API Key 就能使用。

用 Go 编写，管理界面内嵌在程序里，用 Docker Compose 一条命令即可部署。运行时不需要 Python、Node.js 或外部数据库，数据以 JSON 文件保存在 Docker 数据卷中。

## 核心能力

- **多协议转发**：OpenAI `/v1/chat/completions`、`/v1/responses`，Anthropic `/v1/messages`、`/v1/messages/count_tokens`，流式与非流式都支持，上游自行转换协议。
- **号池调度**：轮询选号、会话粘滞、限流 / 余额 / 错误冷却，收到响应前出错自动换号重试（默认最多 3 个账号）。
- **免费模型与余额判断**：领取了免费模型活动的账号可继续调度免费模型；余额用完只暂停收费模型。
- **账号状态**：只有「正常」和「账号受限」两种。上游明确返回 `account_restricted` 时立即标记受限并持久保存，之后不再派发，可手动或批量解除。
- **IP 封控识别**：上游返回 403 拦截页（例如 nginx 的 `403 Forbidden`）时，把当前出口 IP 标记为封控，停止经它调度，换其他出口的账号重试。
- **每账号请求记录**：模型、时间、输入 / 输出 / 缓存命中、是否成功和失败原因；换号前失败的那次也记在原账号下。
- **账号管理**：微信扫码、手机号验证码、粘贴令牌（支持一次最多 200 个）、读取本机 Codar 客户端登录；批量启用、禁用、删除、解除受限。
- **导入导出**：把全部或所选账号导出为 JSON 文件（含令牌、备注和启用状态），在另一台网关一键导入；导入时逐个向上游校验令牌。
- **调用测试**：账号行的「调用」按钮可以选模型、写提示词（默认 `Hi`）直接调用，显示回复、耗时和输入 / 输出 / 缓存命中。
- **自动任务**：每日签到、领取免费模型活动、令牌到期前自动续期、定时刷新余额与活动。
- **用量与账单**：经网关的请求统计（成功率、缓存命中率、TPS、首字时间、估算花费），以及 Codar 官方的每日用量和额度明细。
- **多 API Key**：名称、启停、到期时间、可调度的模型范围（从模型目录勾选或全部模型）；列表里可以随时复制完整 Key。
- **代理池**：HTTP / HTTPS / SOCKS5 / SOCKS5H，支持认证、连接检测、批量添加、单账号或全部账号绑定；指定代理不可用时直接报错，不会悄悄改走直连。

## 架构总览

```mermaid
flowchart LR
    C["客户端<br/>Claude Code / Cursor / SDK"] -->|"Bearer sk-codar-…"| K["API Key 鉴权<br/>模型范围"]
    K --> P["选号<br/>会话粘滞 + 轮询"]
    P -->|"跳过：受限 / 关闭 / 令牌失效<br/>冷却 / 余额 / 出口封控"| E["出口<br/>直连或代理池"]
    E --> U[("Codar 上游")]
    U -->|"可重试错误"| P
    U -->|"响应 / SSE 透传"| C
    A["管理员<br/>Web 面板"] --> M["账号 · Key · 代理 · 设置"]
    M --> D[("数据目录<br/>JSON 文件")]
    B["后台任务<br/>签到 · 领取 · 续期 · 刷新"] --> U
```

请求进来后先校验 API Key 和模型范围，再从号池里挑一个可用账号，经该账号绑定的出口发给上游。收到响应前遇到可重试错误（限流、余额、5xx、网络、IP 封控、账号受限）会记录失败并换下一个账号；响应一旦开始输出就不会再换号。

## 快速开始

### 环境要求

- Docker 24+ 与 Docker Compose v2。
- 支持 `linux/amd64` 和 `linux/arm64`。

### Docker Compose 部署

```sh
git clone https://github.com/AiXiaoc/codar2api.git
cd codar2api
cp .env.example .env          # 可在这里设置初始管理员密码
docker compose up -d --build
docker compose logs codar     # 没设置密码时，首次启动生成的管理员密码在这里
```

默认只映射本机 `127.0.0.1:8801`，数据保存在命名卷 `codar-data`，容器以非 root 用户运行并自带健康检查。

Docker Hub 连不上时，可以指定可访问的镜像仓库后再启动：

```sh
docker compose build --build-arg GO_IMAGE=<镜像仓库>/library/golang:1.26.4-alpine --build-arg RUNTIME_IMAGE=<镜像仓库>/library/alpine:3.22
docker compose up -d
```

### 登录面板并添加账号

1. 打开 `http://127.0.0.1:8801`，用管理员密码登录。初始密码可以在首次启动前写进 `.env` 的 `CODAR_ADMIN_PASSWORD`（至少 10 个字符），之后在面板「设置」修改。
2. 「账号」→「添加账号」，任选一种方式：
   - **微信扫码**（推荐）：用绑定了手机号的微信扫码，登录成功自动入池；没绑手机号或需要新 IP 验证时会转到手机号验证。
   - **手机号**：手机号加短信验证码，获取验证码前要手动完成一次人机验证。
   - **粘贴令牌**：粘贴 Codar 请求里 `Authorization: Bearer` 后面的 JWT，多个每行一个。
   - **本机登录**：读取 Codar 客户端的 `config.json`。容器里默认没有这个文件，需要挂载进去，见下方「其他变量」。
3. 「API Key」→「新建 Key」，选择可调度的模型（全部或指定），复制完整 Key；之后也可以在列表里随时复制。
4. 需要代理时，在「代理池」添加，再在账号详情或「批量 → 全部设置代理」中绑定。

### 验证

```sh
curl http://127.0.0.1:8801/v1/chat/completions \
  -H "Authorization: Bearer $CODAR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

客户端配置：OpenAI 兼容客户端填 Base URL `http://127.0.0.1:8801/v1`；Anthropic 客户端（如 Claude Code）填 `http://127.0.0.1:8801`，Key 可放在 `Authorization: Bearer` 或 `x-api-key`。

## 配置说明

### `.env`

`docker compose` 启动时读取 `.env`，修改后执行 `docker compose up -d` 生效。

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `CODAR_PORT` | `8801` | 映射到本机 `127.0.0.1` 的端口 |
| `CODAR_ADMIN_PASSWORD` | 首次随机生成 | 只在首次初始化管理员密码时使用 |
| `CODAR_SECURE_COOKIE` | `false` | 通过 HTTPS 反向代理访问时设为 `true` |
| `CODAR_MAX_CONCURRENT` | `128` | 模型转发并发上限，超出返回 429 |

### 其他变量

需要时添加到 `compose.yaml` 里 `codar` 服务的 `environment` 下。

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `CODAR_API_BASE` | `https://api.claw.speedai.com` | Codar API 地址 |
| `CODAR_PAY_BASE` | `https://api.kuaipaper.com` | 账号、余额与登录服务地址 |
| `CODAR_WEB_BASE` | `https://codar-web.speedai.com` | 网页账号服务地址 |
| `CODAR_PROXY_CHECK_URL` | Codar 模型目录接口 | 代理连接检测的目标 |
| `CODAR_CONFIG` | `~/.codar/config.json` | 「本机登录」读取的 Codar 客户端配置 |

使用「本机登录」时，把宿主机的配置文件只读挂载进容器并指向它：

```yaml
    environment:
      CODAR_CONFIG: /config/codar.json
    volumes:
      - codar-data:/data
      - ~/.codar/config.json:/config/codar.json:ro
```

容器内的监听地址和数据目录固定为 `0.0.0.0:8801` 与 `/data`，不需要修改。

### 面板设置

在「设置」页修改，保存后立即生效并持久保存。

| 设置 | 默认 | 说明 |
| --- | --- | --- |
| 单次请求最多尝试账号数 | 3 | 收到响应前的可重试错误换到其他可用账号 |
| 会话粘滞 | 开 | 同一会话尽量使用同一账号 |
| 限流冷却 | 60 秒 | 上游返回 429 后暂停调度该账号 |
| 上游错误冷却 | 30 秒 | 网络错误或 5xx 时暂停该账号 |
| 余额不足冷却 | 600 秒 | 上游返回 402 或余额不足时暂停收费模型 |
| IP 封控时停止调度该 IP | 开 | 上游返回 403 拦截时把当前出口 IP 标记为封控，换其他出口的账号重试 |
| 允许使用通用余额 | 开 | Agent 余额用完后继续使用通用余额 |
| 自动每日签到 / 自动领取免费模型 / 令牌自动续期 | 开 | 后台自动执行 |
| 提前续期 | 48 小时 | 令牌剩余时间少于此值时续期，失败 30 分钟后重试 |
| 余额 / 活动刷新间隔 | 30 分钟 | 完整刷新账号信息 |
| 免费活动检查间隔 | 60 分钟 | 检查并领取免费模型活动 |
| 模型目录缓存 | 10 分钟 | 上游模型目录的缓存时间 |

模型价格在「设置 → 模型价格」调整，只用于估算花费，实际以上游账单为准。

## 核心行为语义

### 选号策略

每次请求按以下顺序挑账号：

1. 开启会话粘滞且有会话标识时，优先用该会话上次的账号。
2. 否则从上次位置开始轮询，取第一个可用账号。

以下账号会被跳过：已关闭、账号受限、没有令牌、令牌失效或 JWT 已过期、冷却中、出口 IP 封控（开启该设置时）、请求收费模型但余额 ≤ 0。余额冷却中的账号仍可调度它领到的免费模型。

### 会话粘滞

会话标识依次取：请求头 `X-Session-Id`、`X-Conversation-Id`、`Session-Id`；请求体 `session_id`、`conversation_id`、`prompt_cache_key`、`metadata.user_id`；都没有时用首条用户消息的摘要。不同 API Key 的会话互相隔离。

### 错误分类与账号处置

| 上游返回 | 处置 | 是否换号 |
| --- | --- | --- |
| `account_restricted`（错误响应） | 标记账号受限并持久保存，需手动解除 | 是 |
| `account_restricted`（出现在 HTTP 200 响应或 SSE 流里） | 同上 | 否，本次响应已返回，之后不再派发该账号 |
| 401 | 标记令牌失效，重新导入或续期成功后恢复 | 是 |
| 429 | 限流冷却 | 是 |
| 402，或可重试错误里含余额 / insufficient / quota / balance | 余额冷却，只暂停收费模型 | 是 |
| 403 HTML 拦截页（不是 JSON） | 标记当前出口 IP 封控 | 是，换其他出口的账号 |
| 5xx、网络错误、超时 | 上游错误冷却 | 是 |
| 503 `No available channel` | 模型级错误，不冷却 | 否，直接返回 |
| 400 / 413 / 422 / 普通 403 等请求错误 | 不处理 | 否，原样返回 |

超过「单次请求最多尝试账号数」后，把最后一次的错误返回给调用方。

### IP 封控

上游拦截的是 IP，不是账号，所以封控记在**出口**上：某个代理，或网关本机直连。被封控的出口不再参与调度，所有使用它的账号都会暂停；封控原因形如 `HTTP 403 · 403 Forbidden · nginx/1.24.0 (Ubuntu)`。

在「代理池」或账号详情里点「解除封控」恢复；修改代理地址也会自动清除该代理的封控。只有模型请求会触发封控，签到、刷新余额等其他请求遇到 403 页面时只报错。关闭「IP 封控时停止调度该 IP」后，403 页面会原样返回给调用方。

### 请求记录

点开账号，在「请求记录」页签查看经本网关发给这个账号的每一次尝试：模型、时间、输入 / 输出 / 缓存命中、成功或失败及原因，可以只看失败的。一次请求在 A 账号失败后换到 B 成功，A 的记录里会显示这次失败并标注「已换号」。记录与请求统计一起保留最近 7 天。

### 定时任务

后台每 30 秒检查一次启用的账号，按设置执行：令牌临近到期时续期、领取可领的免费模型活动、到了可签时间签到、按间隔刷新余额 / 活动 / 邀请 / 签到状态。请求统计和 Key 计数每 3 秒批量写盘，正常退出时也会写盘。

## 🔌 API 端点

所有模型接口都需要 API Key，管理面板的登录会话不能代替 Key。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/v1/models` | 当前 Key 可用的模型列表 |
| POST | `/v1/chat/completions` | OpenAI Chat Completions |
| POST | `/v1/responses` | OpenAI Responses |
| POST | `/v1/messages` | Anthropic Messages |
| POST | `/v1/messages/count_tokens` | Anthropic Token 计数（不计入统计） |
| GET | `/ping`、`/health` | 健康检查，无需鉴权，不返回账号数据 |

### 流式行为细节

- SSE 字节原样透传，同时解析其中的用量和错误事件。
- OpenAI Chat 流式请求会自动加上 `stream_options.include_usage`，以便统计用量。
- 流式响应开始输出后不会再换号或重新提交。
- 客户端断开会立即取消上游请求。
- 响应头 `X-Codar-Account` 返回本次使用的账号名，`X-Request-Id` 透传上游的请求 ID。
- 超时：连接 15 秒、TLS 握手 15 秒、等待响应头 90 秒，单次尝试最长 15 分钟。

## 🖥️ Web 管理面板

| 页面 | 内容 |
| --- | --- |
| 账号 | 可用账号、积分余额、消耗速度；按状态筛选和搜索；调用测试；批量启用 / 禁用 / 删除 / 解除受限 / 导出；全部签到、刷新、设置代理、导入导出账号 |
| 账号详情 | 概览、请求记录、签到兑换、活动邀请、官方用量与额度明细、出口代理 |
| 统计 | 经本网关的请求统计（按模型、按账号、最近请求）和 Codar 官方上游用量 |
| 模型 | 上游模型目录：能力、上下文、最大输出、思考档位、价格 |
| API Key | Base URL、Key 的创建 / 启停 / 编辑模型范围 / 删除 |
| 代理池 | 直连与各代理的封控状态、连接检测、批量添加、编辑、启停 |
| 设置 | 调度与自动任务设置、模型价格、管理员密码、退出登录 |

宽度 ≤ 760px 时自动切换为手机布局：底部标签栏、账号卡片、底部弹出的详情和弹窗。

## 代理池

在「代理池」→「添加代理」里一次粘贴多行，每行一个代理，前面可以加名称：

```text
东京-01 socks5h://user:pass@1.2.3.4:1080
1.2.3.4:8080
1.2.3.4:8080:user:pass
```

后两种写法没有协议，按所选的默认协议（http / https / socks5 / socks5h）处理；没写名称时用「地址:端口」作名称。格式正确的行直接加入，已存在的地址跳过，有问题的行留在输入框里并逐行说明原因。

- 账号的对话、测试、余额、签到、领取、兑换、用量查询和续期都走同一个出口。
- 连接池按出口隔离复用，正常校验证书；HTTP 代理支持 CONNECT，HTTPS 代理同时校验代理和目标站点证书。SOCKS5 与 SOCKS5H 都通过代理解析域名。
- 代理密码不会返回给页面。指定代理停用、删除或连不上时请求直接失败，不会改走直连。

## 部署运维

### 数据卷

数据都在命名卷 `codar-data`（容器内 `/data`）里：

| 文件 | 内容 |
| --- | --- |
| `accounts.json` | 账号与上游令牌 |
| `keys.json` | API Key、哈希与调用计数 |
| `admin.json` | 管理员密码派生值 |
| `proxies.json` | 代理、账号绑定与出口封控状态 |
| `settings.json` / `prices.json` / `catalog.json` | 面板设置、自定义价格、模型目录缓存 |
| `usage.jsonl` | 请求记录，保留最近 7 天 |

数据文件用原子替换写入，账号状态同步保存。一个数据卷只能给一个容器使用；横向扩容时给每个实例配置独立的数据卷。

### 升级与备份

升级：

```sh
git pull
docker compose up -d --build
```

备份前先停止容器。卷名一般是 `<目录名>_codar-data`，可以用 `docker volume ls` 查看：

```sh
docker compose stop
docker run --rm -v codar2api_codar-data:/data -v "$PWD":/backup alpine tar czf /backup/codar-data.tgz -C /data .
docker compose start
```

升级前建议先备份；回退时用对应版本的完整备份恢复。

### 反向代理

需要从公网访问时，请在前面放一层 HTTPS 反向代理，并在 `.env` 里设置 `CODAR_SECURE_COOKIE=true`。`deploy/nginx.conf.example` 是 Nginx 示例：流式接口需要关闭缓冲（`proxy_buffering off`），并把读取超时调长。

## 安全与合规

1. **凭据**：账号令牌只保存在数据目录，页面上只显示脱敏后的令牌；API Key 以 `sk-codar-` 开头，请求时按 SHA-256 哈希比对，完整 Key 保存在 `keys.json` 以便在面板里复制，列表接口不返回完整 Key；管理员密码用 PBKDF2-HMAC-SHA256（21 万次迭代）派生。
2. **面板访问**：会话 Cookie 为随机值、HttpOnly、SameSite=Strict，有效期 24 小时；写操作校验同源；同一 IP 5 分钟内最多尝试 5 次登录。
3. **网络暴露**：Compose 默认只把端口映射到本机 `127.0.0.1`。需要公网访问时请放在 HTTPS 反向代理之后，不要把 HTTP 端口直接暴露到公网。
4. **代理凭据**：代理地址里的用户名和密码不会返回给页面；批量添加的结果只显示行号和原因。
5. **导出文件**：账号导出文件里有完整令牌，拿到文件的人可以直接使用这些账号，请像密码一样保管。
6. **使用边界**：只接入你本人有权使用的账号，遵守 Codar 的服务条款与所在地法律法规。

## 常见问题

**忘记管理员密码怎么办？**
停止容器，把数据卷里的 `admin.json` 移走，在 `.env` 里设置新的 `CODAR_ADMIN_PASSWORD` 后启动。其他数据不受影响。

```sh
docker compose stop
docker compose run --rm --entrypoint sh codar -c 'mv /data/admin.json /data/admin.json.bak'
docker compose up -d
```

**请求返回 503「没有可用的 Codar 账号」？**
到「账号」页看状态下面的小字：冷却中、余额用完、令牌过期、IP 封控等都会写在那里。如果提示「有出口 IP 处于封控状态」，到「代理池」检查并解除，或给账号换一个出口。

**为什么所有直连账号同时显示 IP 封控？**
封控记在出口上。直连出口被上游拦截后，所有直连账号都会暂停。可以给这些账号绑定代理，或确认上游放行后在「代理池」解除直连的封控。

**流式输出一段时间后才一次性出现？**
多半是反向代理开着缓冲。Nginx 需要 `proxy_buffering off`，并把 `proxy_read_timeout` 调长。

**Docker 构建时拉不到镜像？**
用 `--build-arg GO_IMAGE=... --build-arg RUNTIME_IMAGE=...` 指定可访问的镜像仓库，见「快速开始」。

## 开发与验证

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

没有第三方 Go 依赖。测试使用本地模拟的 Codar 上游和 HTTP / HTTPS / SOCKS5 代理，不消耗真实短信或模型额度。管理界面文件在 `internal/gateway/web/`，构建时嵌入可执行文件。

## 免责声明

本项目仅供学习和研究使用。使用者需遵守 [Codar](https://codar.speedai.com/) 服务条款，自行承担使用风险（包括账号封禁、条款违约等）。作者不对任何因使用本项目产生的直接或间接损失负责。本项目与科搭 Codar、SpeedAI 无任何关联，Codar 名称与图标归其所有者。

## License

本项目采用 [MIT License](LICENSE) 开源协议。

- 允许任意使用、复制、修改、合并、发布、分发、再授权及销售。
- 再分发（源码或二进制形式）时，请保留原仓库的 MIT 版权声明与许可声明，并在 NOTICE 或 README 中注明原始出处 `https://github.com/AiXiaoc/codar2api`。
