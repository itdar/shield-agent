# shield-agent

Security middleware for the AI Agent era.
Sits transparently between agents and servers to provide **authentication, protection, logging, and monitoring**.

A **~10MB single binary** written in Go. 30 seconds to install, 1 minute to configure.

```
 INGRESS MODE — protect your MCP / A2A / API servers
 ──────────────────────────────────────────────────────────
  External             ┌──────────────────┐             Your
  Agent   ── req  ──▶  │   shield-agent   │  ── req ──▶ MCP /
          ◀── res  ──  │   :8888 proxy    │  ◀── res ── A2A /
                       │ auth·guard·log   │             API
                       └──────────────────┘

 EGRESS MODE — audit outbound calls to external AI APIs
 ──────────────────────────────────────────────────────────
  Your AI              ┌──────────────────┐             OpenAI /
  Agent   ── HTTPS ──▶ │   shield-agent   │  ─ HTTPS ─▶ Anthropic /
          ◀──────────  │   :8889 egress   │  ◀───────── Google /
  (HTTPS_PROXY)        │ MITM·log·policy  │             private LLM
                       └──────────────────┘

                 both modes write to  ▼
              ┌────────────────────────────────┐
              │ SQLite + hash chain + digest   │
              │ Prometheus :9090  ·  Web /ui   │
              └────────────────────────────────┘
```

🌐 [English](README.md) | [한국어](README.ko.md)

---

## Table of Contents

- [When should you use this?](#when-should-you-use-this)
- [Installation](#installation)
- [Quick Start by Use Case](#quick-start-by-use-case)
- [Egress Mode (AI Compliance)](#egress-mode-ai-compliance)
- [Authentication Method Selection Guide](#authentication-method-selection-guide)
- [Protocol Auto-Detection](#protocol-auto-detection)
- [Deployment Patterns](#deployment-patterns)
- [Configuration Reference](#configuration-reference)
- [Monitoring](#monitoring)
- [Web UI](#web-ui)
- [Agent Reputation](#agent-reputation)

---

## When should you use this?

| Situation | What shield-agent does |
|-----------|------------------------|
| You're exposing an MCP server publicly but need to restrict access | Ed25519 signature / token-based authentication |
| Your agents are making too many API calls | Rate limiting + hourly/monthly quotas |
| You need a record of who called what and when | SQLite audit log + Prometheus metrics |
| You want to block requests from certain IP addresses | IP blocklist/allowlist |
| You want to consolidate multiple MCP servers behind a single endpoint | Gateway mode (host/path-based routing) |
| You want a convenient Web UI for management | Built-in dashboard at `/ui` |

### What can be protected

| Communication path | Protocol | Mode |
|-------------------|----------|------|
| Agent → MCP Server | JSON-RPC (stdio / SSE / Streamable HTTP) | stdio, proxy |
| Agent → Agent (A2A) | Google A2A protocol | HTTP middleware |
| Agent → API Server | REST / GraphQL | HTTP middleware |
| **Agent → external AI API** | **HTTPS CONNECT / HTTP** | **egress (AI compliance)** |

---

## Installation

```bash
# Homebrew (macOS / Linux)
brew tap itdar/tap && brew install shield-agent

# curl install script
curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh

# Go install
go install github.com/itdar/shield-agent/cmd/shield-agent@latest

# Docker
docker pull ghcr.io/itdar/shield-agent:latest

# Build from source
git clone https://github.com/itdar/shield-agent.git
cd shield-agent && go build -o shield-agent ./cmd/shield-agent
```

---

## Quick Start by Use Case

### Case 1: Protect a local MCP server (stdio mode)

> When you want to wrap a Python/Node.js MCP server to add authentication and logging

```bash
# Simplest usage — just wrap the MCP server process
shield-agent python my_mcp_server.py

# Debug with verbose mode
shield-agent --verbose node server.js --port 8080
```

**How it works:**
```
MCP Client ──stdin──> shield-agent ──stdin──> MCP Server (child process)
MCP Client <─stdout── shield-agent <─stdout── MCP Server
                          │
                     middleware chain
                   [auth] [guard] [log]
```

- shield-agent runs the MCP server as a child process
- Intercepts stdin/stdout and applies the middleware chain
- stderr passes through unchanged
- Automatically forwards SIGINT/SIGTERM and propagates exit codes

### Case 2: Proxy in front of a remote MCP server (proxy mode)

> When you want to add a security layer in front of an already-running MCP server

```bash
# Streamable HTTP (default)
shield-agent proxy --listen :8888 --upstream http://localhost:8000

# SSE transport
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

# HTTPS
shield-agent proxy --listen :8888 --upstream http://localhost:8000 \
  --tls-cert cert.pem --tls-key key.pem
```

**How it works:**
```
MCP Client ──HTTP──> shield-agent :8888 ──HTTP──> MCP Server :8000
                          │
                     middleware chain
                     monitoring :9090
```

### Case 3: Multiple servers behind a single Gateway (Gateway mode)

> When you want to place multiple MCP/API servers behind a single shield-agent endpoint

`shield-agent.yaml`:
```yaml
upstreams:
  - name: mcp-server-a
    url: http://10.0.1.1:8000
    match:
      host: mcp-a.example.com       # Host header-based routing
    transport: sse

  - name: api-server-b
    url: http://10.0.2.1:3000
    match:
      path_prefix: /api-b           # Path-based routing
      strip_prefix: true

  - name: default-mcp
    url: http://10.0.3.1:8000       # Fallback when nothing else matches
```

```bash
shield-agent proxy --listen :8888
```

**How it works:**
```
Agent A ──mcp-a.example.com──> shield-agent :8888 ──> MCP Server A
Agent B ──/api-b/v1/data─────> shield-agent :8888 ──> API Server B (/v1/data)
Agent C ──other requests─────> shield-agent :8888 ──> Default MCP
```

- Routing by Host header or URL path prefix
- Match priority: Host+Path > Host only > Path only > fallback
- With `strip_prefix: true`, the prefix is stripped before forwarding to upstream
- Upstreams can be added/removed dynamically from the Web UI (`/ui`)

### Case 4: Deploy with Docker Compose

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

### Case 5: Control agent access with tokens

> When you want to issue tokens like API keys to external agents and manage their quotas

```bash
# Issue a token
shield-agent token create --name "partner-agent" --quota-hourly 1000
# → Token: a3f8c1...  (shown only once — store it safely)

# List tokens
shield-agent token list

# Check usage
shield-agent token stats <token-id> --since 24h

# Revoke a token (takes effect immediately)
shield-agent token revoke <token-id>
```

Agents include the token in the request header:
```
Authorization: Bearer a3f8c1...
```

---

## Egress Mode (AI Compliance)

> For when you want to intercept **outbound** agent traffic heading to external AI APIs

shield-agent also runs in reverse. The existing proxy/stdio modes defend the
servers your agents *call into*; egress mode captures the requests your agents
*send out* to OpenAI, Anthropic, Google, or private LLMs, and produces an
audit trail shaped for Korea's AI Basic Act (effective 2026-01-22; Art. 23,
27, 34, 35) and the EU AI Act (Art. 12, 13, 26, 50).

```
┌──────────┐          ┌────────────────────────┐          ┌──────────────────┐
│ AI Agent │ ───────> │   shield-agent egress  │ ───────> │ OpenAI /         │
│          │          │   [policy][compliance] │          │ Anthropic /      │
│          │          │   [egress_log]         │          │ Google / etc.    │
└──────────┘          └─────────┬──────────────┘          └──────────────────┘
                                │
                       egress_logs + hash chain + daily digest
```

### Two-phase rollout

| Phase | Mode | Data captured | CA cert |
|-------|------|---------------|---------|
| **Phase 1** | Metadata-only | destination, timing, size, provider, policy action | Not required |
| **Phase 2** | Per-host TLS MITM | above + model, prompt_hash, content_class, PII flags | Required (`shield-agent ca init`) |

Phase 1 alone is enough to answer "which AI services did we call, how often".
Opt into Phase 2 per host only when body-level tracking is needed.

### Quick start — Phase 1 (no TLS decryption)

```bash
# Standalone
shield-agent egress --listen 127.0.0.1:8889

# Or alongside an existing ingress proxy
shield-agent proxy --with-egress --upstream http://mcp-server:8000
```

Point the agent at the proxy with a single env var:
```bash
export HTTPS_PROXY=http://127.0.0.1:8889
```

Every request lands in `egress_logs` with `provider`, `destination`,
`timing`, `policy_action`, and a `row_hash` linking it into the audit chain.

### Audit + verification CLI

```bash
# Browse egress log rows
shield-agent logs --direction egress --last 20

# Hash-chain integrity check (tamper detection)
shield-agent logs --verify
# → OK (100 entries verified, 2 anchors)

# Export a regulator-ready bundle
shield-agent logs --export-audit ./audit-bundle.json --since 30d
```

The bundle carries rows, anchors, a hash-chain proof, and the active policy
snapshot — a regulator can re-run verification with independent tooling.

### Enabling Phase 2 (TLS MITM)

```bash
# 1) Generate a CA
shield-agent ca init
# 2) Install it into the OS trust store (macOS keychain / Linux update-ca-certificates)
shield-agent ca trust
```

Example `shield-agent.yaml`:
```yaml
egress:
  enabled: true
  listen: "127.0.0.1:8889"
  policy_mode: "warn"      # flip to "block" to return HTTP 403 on violations

  # per-host opt-in — only these hosts are TLS-terminated
  mitm_hosts:
    - "api.openai.com"
    - "api.anthropic.com"

  hash_chain:
    enabled: true
    algorithm: "sha256"
    # Optional: append-only digest file outside the DB (tamper defense)
    digest_path: "/var/log/shield-agent/egress-digest.log"
    digest_interval_hours: 24

  pii_scrub:
    enabled: true
    redaction_mode: "mask"   # mask | hash
  content_tagging:
    enabled: true
```

### Security / compliance highlights

- **Drop-free logging**: bounded-but-blocking channel applies backpressure instead of losing audit rows (Principle: Auditability > Performance)
- **Fail-closed**: in `policy_mode: block` a DB write failure returns HTTP 503 rather than proceeding un-logged
- **SSRF guard**: CONNECT destinations resolving to RFC1918 / loopback / link-local / cloud metadata IPs are rejected by default
- **Hash chain + anchor-preserving purge**: old rows can be deleted while keeping the chain verifiable; `--verify` flags any tampering
- **External daily digest**: defense-in-depth against attackers with full DB access
- **Korean-first PII patterns**: national ID, phone, driver's licence regexes ship out of the box

### Regulatory mapping

| Requirement | shield-agent feature |
|-------------|---------------------|
| EU AI Act Art. 12 (automatic event logging) | `egress_logs` + hash chain |
| EU AI Act Art. 12(2) (traceability) | `correlation_id` (ingress↔egress) |
| EU AI Act Art. 50 (generative AI transparency) | Phase 2: `ai_generated_tag`, `content_class`, `X-AI-Generated` header injection |
| EU AI Act Art. 13 / 26 (transparency, deployer monitoring) | Web UI, Prometheus metrics |
| Korea AI Basic Act Art. 23 / 27 / 34 / 35 | Audit log retention, impact-assessment export, generative AI labelling |

### Explicitly out of scope

- Model-internal logging and training-data provenance — the AI service provider's obligation
- Real-time hallucination detection — specialist tooling
- Legal review — a compliance review (PIPA Art. 23 / GDPR Art. 9 impact of TLS MITM) must be completed before enabling Phase 2 in production

---

## Authentication Method Selection Guide

shield-agent supports **3 authentication methods**. Choose the one that fits your situation:

```
┌─────────────────────────────────────────────────────────────────┐
│ Ed25519 + keys.yaml    — high security, small number of agents  │
│ Ed25519 + DID          — open ecosystem, no pre-registration    │
│ Bearer Token           — simple, like an API key, quota support │
└─────────────────────────────────────────────────────────────────┘
```

| | Ed25519 + keys.yaml | Ed25519 + DID | Bearer Token |
|---|---|---|---|
| **Who creates the key** | Agent generates, admin registers | Agent generates, no registration needed | Admin issues |
| **Pre-registration** | Required (keys.yaml or Web UI) | Not required | Token must be distributed |
| **Per-request signing** | Yes | Yes | No |
| **Token theft risk** | Private key must be stolen (hard) | Private key must be stolen | Token theft is sufficient |
| **Quota/expiry** | No | No | Yes (hourly/monthly) |
| **Immediate revocation** | Delete from keys.yaml | Add to blocklist | `token revoke` (instant) |
| **Recommended for** | 5–10 internal agents | Many agents, open ecosystem | External partners, API key-style |

### Registration via keys.yaml

```yaml
# keys.yaml
keys:
  - id: "agent-1"
    key: "base64-encoded-Ed25519-public-key"
```

Agent includes this in JSON-RPC params when making requests:
```json
{
  "method": "tools/list",
  "params": {
    "_mcp_agent_id": "agent-1",
    "_mcp_signature": "hex-encoded-signature"
  }
}
```

### Register keys via Web UI (instead of keys.yaml)

From the Web UI at `/ui`, use the **Agent Keys** menu to register or remove public keys.
Keys are looked up from both keys.yaml and the DB, so either location works.

### DID method (no pre-registration required)

When an agent uses an ID in the format `did:key:z6Mk...`, the public key is extracted directly from the ID.
No keys.yaml registration needed — well-suited for large-scale agent environments.

### Security modes

| Mode | Behavior |
|------|----------|
| `open` (default) | Passes through even without a signature, only logs a warning |
| `verified` | Valid signature required; unregistered DIDs are OK (but can have differential rate limits) |
| `closed` | Only registered agents can access |

```yaml
# shield-agent.yaml
security:
  mode: verified
  did_blocklist:            # Block only malicious DIDs (not an allowlist)
    - "did:key:z6Mk..."
```

---

## Protocol Auto-Detection

shield-agent automatically detects the communication protocol for each request:

| Protocol | Detection Signal |
|----------|-----------------|
| **MCP** (JSON-RPC 2.0) | `Mcp-Session-Id` header, or JSON-RPC with non-A2A method |
| **A2A** (Google Agent-to-Agent) | `X-A2A-Signature` header, or JSON-RPC with `tasks/*` method |
| **HTTP API** (REST/GraphQL) | No JSON-RPC structure detected |

Detection is automatic by default. You can also set per-upstream hints:

```yaml
upstreams:
  - name: mcp-server
    url: http://10.0.1.1:8000
    protocol: mcp        # skip detection, always MCP

  - name: a2a-agent
    url: http://10.0.2.1:3000
    protocol: a2a        # skip detection, always A2A

  - name: mixed
    url: http://10.0.3.1:4000
    protocol: auto       # default: detect per request
```

---

## Deployment Patterns

### Pattern 1: Sidecar (one per server)

```
[Agent] → [shield-agent :8881] → [MCP Server A]
[Agent] → [shield-agent :8882] → [MCP Server B]
```

- Simplest architecture
- Recommended for 2–3 services
- Each shield-agent handles authentication/logging independently

### Pattern 2: Gateway (single central instance)

```
[Agent A] ──> [shield-agent :8888] ──> [MCP Server A]
[Agent B] ──>       (gateway)     ──> [API Server B]
```

- Recommended for 5+ services
- Auth/tokens/logs managed in one place
- Host/path routing via `upstreams` configuration

### Pattern 3: nginx + shield-agent

```
[Agent] → [nginx (TLS)] → [shield-agent :8888 (HTTP)] → [upstream]
```

- TLS termination handled by nginx
- shield-agent operates over HTTP only
- No need for `--tls-cert/--tls-key` configuration

### Centralized monitoring (even with sidecars)

```
[shield-agent A :9090/metrics] ──┐
[shield-agent B :9091/metrics] ──┼── Prometheus ──> Grafana
[shield-agent C :9092/metrics] ──┘
```

When Prometheus scrapes `/metrics` from each shield-agent,
you can centrally view request counts, error rates, and latency across all services even in a sidecar architecture.

---

## Configuration Reference

**Priority:** CLI flags > environment variables (`SHIELD_AGENT_*`) > YAML config > defaults

Copy `shield-agent.example.yaml` to `shield-agent.yaml` to get started.

### Key settings

| Setting | Default | Environment variable | Description |
|---------|---------|---------------------|-------------|
| `security.mode` | `open` | `SHIELD_AGENT_SECURITY_MODE` | `open` / `verified` / `closed` |
| `security.key_store_path` | `keys.yaml` | `SHIELD_AGENT_KEY_STORE_PATH` | Path to public key file |
| `security.did_blocklist` | `[]` | — | List of DIDs to block |
| `storage.db_path` | `shield-agent.db` | `SHIELD_AGENT_DB_PATH` | SQLite DB path |
| `storage.retention_days` | `30` | `SHIELD_AGENT_RETENTION_DAYS` | Log retention in days |
| `server.monitor_addr` | `127.0.0.1:9090` | `SHIELD_AGENT_MONITOR_ADDR` | Monitoring/UI address |
| `logging.level` | `info` | `SHIELD_AGENT_LOG_LEVEL` | `debug`/`info`/`warn`/`error` |
| `logging.format` | `json` | `SHIELD_AGENT_LOG_FORMAT` | `json` / `text` |

### Middleware configuration

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 60          # Per-method requests per minute
      max_body_size: 65536            # Maximum request body size (bytes)
      ip_blocklist: ["203.0.113.0/24"]
      ip_allowlist: ["10.0.0.0/8"]    # Empty means allow all
      brute_force_max_fails: 5        # Auto-block after 5 consecutive failures
      validate_jsonrpc: true          # Reject malformed JSON-RPC
  - name: token
    enabled: false                    # Token middleware (enable if needed)
  - name: log
    enabled: true
```

### CLI flags

| Flag | Description |
|------|-------------|
| `--config <path>` | Config file path (default: `shield-agent.yaml`) |
| `--log-level <level>` | Log level |
| `--verbose` | Alias for `--log-level debug` |
| `--monitor-addr <addr>` | Monitoring address |
| `--disable-middleware <name>` | Disable a middleware |
| `--enable-middleware <name>` | Enable a middleware |

### SIGHUP hot reload

Change configuration without restarting the process:

```bash
kill -HUP $(pgrep shield-agent)
```

The middleware chain, keys.yaml, and DID blocklist will be reloaded.

---

## Monitoring

### Endpoints

| Path | Description |
|------|-------------|
| `/healthz` | Health check (`healthy` / `degraded`) |
| `/metrics` | Prometheus metrics |
| `/ui` | Web UI dashboard |

### Prometheus metrics

| Metric | Type | Labels |
|--------|------|--------|
| `shield_agent_messages_total` | Counter | `direction`, `method` |
| `shield_agent_auth_total` | Counter | `status` |
| `shield_agent_message_latency_seconds` | Histogram | `method` |
| `shield_agent_child_process_up` | Gauge | — |
| `shield_agent_rate_limit_rejected_total` | Counter | `method` |

### Log query CLI

```bash
shield-agent logs                              # Last 50 entries
shield-agent logs --last 100                   # Last 100 entries
shield-agent logs --agent <id> --since 1h      # Specific agent, last 1 hour
shield-agent logs --method tools/call           # Specific method
shield-agent logs --format json                # JSON output
```

---

## Web UI

Access at `http://localhost:9090/ui`.

- **Initial password**: `admin` (forced change on first login)
- **Dashboard**: Real-time request count, error rate, average latency
- **Logs**: Filterable audit log table
- **Token management**: Issue/revoke tokens, usage statistics
- **Middleware**: On/off toggles (persisted across restarts)
- **Agent Keys**: Register/remove public keys (manage via Web UI without keys.yaml)
- **Upstreams**: Add/edit/remove Gateway mode upstreams

---

## Agent Reputation

shield-agent tracks agent behavior and computes trust scores to dynamically adjust rate limits.

### How it works

```
Action Logs → Score Calculator → Trust Level → Dynamic Rate Limit
  (SQLite)    (every 5 min)     trusted/      (2x, 1x, 0.25x, 0x)
                                 normal/
                                 suspicious/
                                 blocked
```

### Enable reputation

```yaml
# shield-agent.yaml
reputation:
  enabled: true
  recalc_interval: 300    # recalculate every 5 minutes
  window_hours: 24        # look at last 24 hours of activity
  thresholds:
    trusted: 0.8
    normal: 0.4
    suspicious: 0.1
  rate_multipliers:
    trusted: 2.0          # 2x base rate limit
    normal: 1.0           # base rate
    suspicious: 0.25      # 1/4 base rate
    blocked: 0.0          # reject all requests
```

### CLI

```bash
# List all agent reputations
shield-agent reputation

# Query a specific agent
shield-agent reputation <agent-hash>

# JSON output
shield-agent reputation --format json
```

### Reputation API

When reputation is enabled, the following endpoints are available on the monitor server (`:9090`):

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/reputation` | List all agent scores |
| `GET` | `/api/reputation/{hash}` | Query single agent score |
| `GET` | `/api/reputation/stats` | Aggregate statistics |
| `POST` | `/api/reputation/report` | Accept scores from remote instances |
| `POST` | `/api/reputation/recalculate` | Trigger immediate recalculation |

### Trust score factors

| Factor | Weight | Description |
|--------|--------|-------------|
| Success rate | +0.35 | Percentage of successful requests |
| Error penalty | -0.25 | Percentage of failed requests |
| Auth failures | -0.15 | Failed signature verifications |
| Volume bonus | +0.10 | Higher volume = more trust data |
| Latency | -0.10 | Slow responses reduce trust |
| Rate limit hits | -0.05 | Exceeding rate limits |

---

## License

[MIT](LICENSE)
