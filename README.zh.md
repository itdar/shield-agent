# shield-agent

AI Agent 时代的安全中间件。
透明地位于 Agent 与 Server 之间，提供**认证、防护、日志记录和监控**。

使用 Go 编写的 **~10MB 单一二进制文件**。30 秒安装，1 分钟配置。

```
┌──────────┐         ┌──────────────────────────┐         ┌──────────────┐
│ AI Agent │ ──────> │      shield-agent        │ ──────> │ Target Server│
│ MCP/A2A  │ <────── │  [auth] [guard] [log]    │ <────── │ MCP/A2A/API  │
└──────────┘         │  monitor :9090 /metrics  │         └──────────────┘
                     │  Web UI  :9090 /ui       │
                     └──────────────────────────┘
```

🌐 [English](README.md) | [한국어](README.ko.md) | [日本語](README.ja.md) | [中文](README.zh.md)

---

## 目录

- [适用场景](#适用场景)
- [安装](#安装)
- [按使用场景快速开始](#按使用场景快速开始)
- [认证方式选择指南](#认证方式选择指南)
- [部署模式](#部署模式)
- [配置参考](#配置参考)
- [监控](#监控)
- [Web UI](#web-ui)
- [开发路线图](#开发路线图)

---

## 适用场景

| 场景 | shield-agent 的作用 |
|------|---------------------|
| 将 MCP 服务器公开暴露，但需要限制访问权限 | Ed25519 签名 / 基于 Token 的认证 |
| Agent 的 API 调用次数过多 | 频率限制 + 每小时/每月配额 |
| 需要记录谁在何时调用了什么 | SQLite 审计日志 + Prometheus 指标 |
| 需要屏蔽特定 IP 地址的请求 | IP 黑名单/白名单 |
| 希望将多个 MCP 服务器整合到单一端点 | 网关模式（基于 Host/路径的路由） |
| 需要便捷的 Web 管理界面 | 内置仪表板，访问 `/ui` |

### 可保护的通信路径

| 通信路径 | 协议 | 模式 |
|----------|------|------|
| Agent → MCP Server | JSON-RPC (stdio / SSE / Streamable HTTP) | stdio, proxy |
| Agent → Agent (A2A) | Google A2A 协议 | HTTP 中间件 |
| Agent → API Server | REST / GraphQL | HTTP 中间件 |

---

## 安装

```bash
# Homebrew (macOS / Linux)
brew tap itdar/tap && brew install shield-agent

# curl 安装脚本
curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh

# Go install
go install github.com/itdar/shield-agent/cmd/shield-agent@latest

# Docker
docker pull ghcr.io/itdar/shield-agent:latest

# 从源码构建
git clone https://github.com/itdar/shield-agent.git
cd shield-agent && go build -o shield-agent ./cmd/shield-agent
```

---

## 按使用场景快速开始

### 场景 1：保护本地 MCP 服务器（stdio 模式）

> 适用于将 Python/Node.js MCP 服务器包装起来，添加认证和日志功能

```bash
# 最简单的用法 — 直接包装 MCP 服务器进程
shield-agent python my_mcp_server.py

# 使用详细模式调试
shield-agent --verbose node server.js --port 8080
```

**工作原理：**
```
MCP Client ──stdin──> shield-agent ──stdin──> MCP Server (child process)
MCP Client <─stdout── shield-agent <─stdout── MCP Server
                          │
                     middleware chain
                   [auth] [guard] [log]
```

- shield-agent 将 MCP 服务器作为子进程运行
- 拦截 stdin/stdout 并应用中间件链
- stderr 原样透传
- 自动转发 SIGINT/SIGTERM 并传播退出码

### 场景 2：在远程 MCP 服务器前端代理（proxy 模式）

> 适用于在已运行的 MCP 服务器前添加安全层

```bash
# Streamable HTTP（默认）
shield-agent proxy --listen :8888 --upstream http://localhost:8000

# SSE 传输
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

# HTTPS
shield-agent proxy --listen :8888 --upstream http://localhost:8000 \
  --tls-cert cert.pem --tls-key key.pem
```

**工作原理：**
```
MCP Client ──HTTP──> shield-agent :8888 ──HTTP──> MCP Server :8000
                          │
                     middleware chain
                     monitoring :9090
```

### 场景 3：多服务器统一网关（Gateway 模式）

> 适用于将多个 MCP/API 服务器整合到单一 shield-agent 端点

`shield-agent.yaml`：
```yaml
upstreams:
  - name: mcp-server-a
    url: http://10.0.1.1:8000
    match:
      host: mcp-a.example.com       # 基于 Host 头的路由
    transport: sse

  - name: api-server-b
    url: http://10.0.2.1:3000
    match:
      path_prefix: /api-b           # 基于路径的路由
      strip_prefix: true

  - name: default-mcp
    url: http://10.0.3.1:8000       # 无匹配时的兜底
```

```bash
shield-agent proxy --listen :8888
```

**工作原理：**
```
Agent A ──mcp-a.example.com──> shield-agent :8888 ──> MCP Server A
Agent B ──/api-b/v1/data─────> shield-agent :8888 ──> API Server B (/v1/data)
Agent C ──other requests─────> shield-agent :8888 ──> Default MCP
```

- 通过 Host 头或 URL 路径前缀进行路由
- 匹配优先级：Host+Path > 仅 Host > 仅 Path > 兜底
- 设置 `strip_prefix: true` 时，前缀在转发到上游前会被去除
- 可通过 Web UI（`/ui`）动态添加或删除上游服务器

### 场景 4：使用 Docker Compose 部署

```yaml
services:
  shield:
    image: ghcr.io/itdar/shield-agent:latest
    command: proxy --listen :8888 --upstream http://mcp-server:8000
    ports:
      - "8888:8888"
      - "9090:9090"
    volumes:
      - ./shield-agent.yaml:/shield-agent.yaml:ro
      - ./keys.yaml:/keys.yaml:ro
      - shield-data:/data
    environment:
      - SHIELD_AGENT_DB_PATH=/data/shield-agent.db

  mcp-server:
    image: your-mcp-server:latest

volumes:
  shield-data:
```

### 场景 5：通过 Token 控制 Agent 访问权限

> 适用于向外部 Agent 签发类似 API Key 的 Token 并管理其配额

```bash
# 签发 Token
shield-agent token create --name "partner-agent" --quota-hourly 1000
# → Token: a3f8c1...  (仅显示一次，请妥善保存)

# 查看 Token 列表
shield-agent token list

# 查看使用情况
shield-agent token stats <token-id> --since 24h

# 撤销 Token（立即生效）
shield-agent token revoke <token-id>
```

Agent 在请求头中携带 Token：
```
Authorization: Bearer a3f8c1...
```

---

## 认证方式选择指南

shield-agent 支持 **3 种认证方式**，请根据实际情况选择：

```
┌─────────────────────────────────────────────────────────────────┐
│ Ed25519 + keys.yaml    — 高安全性，适合少量 Agent               │
│ Ed25519 + DID          — 开放生态，无需预先注册                 │
│ Bearer Token           — 简单易用，类似 API Key，支持配额       │
└─────────────────────────────────────────────────────────────────┘
```

| | Ed25519 + keys.yaml | Ed25519 + DID | Bearer Token |
|---|---|---|---|
| **密钥创建方** | Agent 生成，管理员注册 | Agent 生成，无需注册 | 管理员签发 |
| **预先注册** | 必须（keys.yaml 或 Web UI） | 不需要 | 需要分发 Token |
| **逐请求签名** | 是 | 是 | 否 |
| **Token 盗用风险** | 需要盗取私钥（难度大） | 需要盗取私钥 | 盗取 Token 即可 |
| **配额/有效期** | 无 | 无 | 有（每小时/每月） |
| **即时撤销** | 从 keys.yaml 删除 | 加入黑名单 | `token revoke`（即时） |
| **推荐场景** | 5–10 个内部 Agent | 大量 Agent，开放生态 | 外部合作方，API Key 风格 |

### 通过 keys.yaml 注册

```yaml
# keys.yaml
keys:
  - id: "agent-1"
    key: "base64-encoded-Ed25519-public-key"
```

Agent 发起请求时，在 JSON-RPC params 中包含以下字段：
```json
{
  "method": "tools/list",
  "params": {
    "_mcp_agent_id": "agent-1",
    "_mcp_signature": "hex-encoded-signature"
  }
}
```

### 通过 Web UI 注册密钥（替代 keys.yaml）

在 `/ui` 的 Web UI 中，使用 **Agent Keys** 菜单注册或删除公钥。
系统会同时从 keys.yaml 和数据库中查找密钥，两处均可使用。

### DID 方式（无需预先注册）

当 Agent 使用 `did:key:z6Mk...` 格式的 ID 时，公钥会直接从 ID 中提取。
无需在 keys.yaml 中注册，适合大规模 Agent 环境。

### 安全模式

| 模式 | 行为 |
|------|------|
| `open`（默认） | 无签名也可通过，仅记录警告日志 |
| `verified` | 需要有效签名；未注册的 DID 可以访问（但可设置差异化频率限制） |
| `closed` | 仅允许已注册的 Agent 访问 |

```yaml
# shield-agent.yaml
security:
  mode: verified
  did_blocklist:            # 仅屏蔽恶意 DID（非白名单机制）
    - "did:key:z6Mk..."
```

---

## 部署模式

### 模式 1：Sidecar（每台服务器独立部署）

```
[Agent] → [shield-agent :8881] → [MCP Server A]
[Agent] → [shield-agent :8882] → [MCP Server B]
```

- 架构最简单
- 适合 2–3 个服务
- 每个 shield-agent 独立处理认证和日志

### 模式 2：Gateway（单一中心实例）

```
[Agent A] ──> [shield-agent :8888] ──> [MCP Server A]
[Agent B] ──>       (gateway)     ──> [API Server B]
```

- 适合 5 个以上的服务
- 认证/Token/日志集中管理
- 通过 `upstreams` 配置实现 Host/路径路由

### 模式 3：nginx + shield-agent

```
[Agent] → [nginx (TLS)] → [shield-agent :8888 (HTTP)] → [upstream]
```

- TLS 终止由 nginx 负责
- shield-agent 仅处理 HTTP
- 无需配置 `--tls-cert/--tls-key`

### 集中监控（即使使用 Sidecar 模式）

```
[shield-agent A :9090/metrics] ──┐
[shield-agent B :9091/metrics] ──┼── Prometheus ──> Grafana
[shield-agent C :9092/metrics] ──┘
```

当 Prometheus 从各个 shield-agent 抓取 `/metrics` 数据时，
即使在 Sidecar 架构下，也能集中查看所有服务的请求数、错误率和延迟。

---

## 配置参考

**优先级：** CLI 参数 > 环境变量（`SHIELD_AGENT_*`） > YAML 配置文件 > 默认值

将 `shield-agent.example.yaml` 复制为 `shield-agent.yaml` 即可开始使用。

### 主要配置项

| 配置项 | 默认值 | 环境变量 | 说明 |
|--------|--------|----------|------|
| `security.mode` | `open` | `SHIELD_AGENT_SECURITY_MODE` | `open` / `verified` / `closed` |
| `security.key_store_path` | `keys.yaml` | `SHIELD_AGENT_KEY_STORE_PATH` | 公钥文件路径 |
| `security.did_blocklist` | `[]` | — | 需要屏蔽的 DID 列表 |
| `storage.db_path` | `shield-agent.db` | `SHIELD_AGENT_DB_PATH` | SQLite 数据库路径 |
| `storage.retention_days` | `30` | `SHIELD_AGENT_RETENTION_DAYS` | 日志保留天数 |
| `server.monitor_addr` | `127.0.0.1:9090` | `SHIELD_AGENT_MONITOR_ADDR` | 监控/UI 地址 |
| `logging.level` | `info` | `SHIELD_AGENT_LOG_LEVEL` | `debug`/`info`/`warn`/`error` |
| `logging.format` | `json` | `SHIELD_AGENT_LOG_FORMAT` | `json` / `text` |

### 中间件配置

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 60          # 每个方法每分钟的请求数
      max_body_size: 65536            # 最大请求体大小（字节）
      ip_blocklist: ["203.0.113.0/24"]
      ip_allowlist: ["10.0.0.0/8"]    # 为空表示允许所有
      brute_force_max_fails: 5        # 连续失败 5 次后自动封禁
      validate_jsonrpc: true          # 拒绝格式错误的 JSON-RPC
  - name: token
    enabled: false                    # Token 中间件（按需启用）
  - name: log
    enabled: true
```

### CLI 参数

| 参数 | 说明 |
|------|------|
| `--config <path>` | 配置文件路径（默认：`shield-agent.yaml`） |
| `--log-level <level>` | 日志级别 |
| `--verbose` | 等同于 `--log-level debug` |
| `--monitor-addr <addr>` | 监控地址 |
| `--disable-middleware <name>` | 禁用某个中间件 |
| `--enable-middleware <name>` | 启用某个中间件 |

### SIGHUP 热重载

无需重启进程即可更新配置：

```bash
kill -HUP $(pgrep shield-agent)
```

中间件链、keys.yaml 和 DID 黑名单将被重新加载。

---

## 监控

### 接口端点

| 路径 | 说明 |
|------|------|
| `/healthz` | 健康检查（`healthy` / `degraded`） |
| `/metrics` | Prometheus 指标 |
| `/ui` | Web UI 仪表板 |

### Prometheus 指标

| 指标名 | 类型 | 标签 |
|--------|------|------|
| `shield_agent_messages_total` | Counter | `direction`, `method` |
| `shield_agent_auth_total` | Counter | `status` |
| `shield_agent_message_latency_seconds` | Histogram | `method` |
| `shield_agent_child_process_up` | Gauge | — |
| `shield_agent_rate_limit_rejected_total` | Counter | `method` |

### 日志查询 CLI

```bash
shield-agent logs                              # 最近 50 条记录
shield-agent logs --last 100                   # 最近 100 条记录
shield-agent logs --agent <id> --since 1h      # 指定 Agent 最近 1 小时
shield-agent logs --method tools/call           # 指定方法
shield-agent logs --format json                # JSON 格式输出
```

---

## Web UI

访问地址：`http://localhost:9090/ui`

- **初始密码**：`admin`（首次登录强制修改）
- **仪表板**：实时请求数、错误率、平均延迟
- **日志**：可筛选的审计日志表格
- **Token 管理**：签发/撤销 Token，查看使用统计
- **中间件**：启用/禁用开关（重启后配置持久保留）
- **Agent Keys**：注册/删除公钥（可通过 Web UI 管理，无需编辑 keys.yaml）
- **Upstreams**：添加/编辑/删除网关模式的上游服务器

---

## 开发路线图

详情请参阅 [ROADMAP.md](ROADMAP.md)。

| 阶段 | 状态 | 说明 |
|------|------|------|
| Phase 1 — 核心 MVP | **已完成** | 传输层、认证、防护、日志、中间件链 |
| Phase 2 — 部署与安装 | **已完成** | Docker、Homebrew、GoReleaser、CI/CD |
| Phase 3 — Token 与 Web UI | **已完成** | Token 管理、Web UI 仪表板 |
| Phase 3.5 — Gateway 与 DID | **已完成** | 多上游路由、DID 黑名单、verified 模式 |
| Phase 4 — 高级功能 | 计划中 | Agent 信誉系统、协议自动检测、WebSocket |

---

## 许可证

[MIT](LICENSE)
