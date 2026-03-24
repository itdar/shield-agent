# shield-agent

A security middleware proxy for [MCP (Model Context Protocol)](https://modelcontextprotocol.io/) servers, written in Go. shield-agent sits transparently between AI agents and MCP servers, intercepting JSON-RPC messages for authentication, logging, and observability.

## Features

- **Two operating modes** — stdio process wrapping and HTTP reverse proxy
- **Ed25519 authentication** — verify agent identity via cryptographic signatures
- **Guard middleware** — rate limiting, request size enforcement, IP blocklist/allowlist
- **Audit logging** — persist all request/response pairs to SQLite
- **Prometheus metrics** — built-in `/metrics` endpoint for monitoring
- **Dynamic middleware chain** — YAML-configured pipeline, hot-reloadable via SIGHUP
- **TLS support** — HTTPS proxy with `--tls-cert` / `--tls-key`
- **Privacy-first telemetry** — optional anonymous usage stats with differential privacy
- **MCP transport support** — SSE and Streamable HTTP proxy transports
- **A2A & HTTP API middleware** — reusable auth/logging middleware for agent-to-agent and agent-to-API communication

## Quick Start

### Installation

```bash
go install rua/cmd/shield-agent@latest
```

Or build from source:

```bash
git clone https://github.com/user/shield-agent.git
cd shield-agent
go build -o shield-agent ./cmd/shield-agent
```

### stdio Mode — Wrap an MCP Server Process

```bash
shield-agent python server.py
shield-agent --verbose node server.js --port 8080
```

shield-agent launches the MCP server as a child process, piping stdin/stdout through its middleware chain while leaving stderr untouched.

### Proxy Mode — HTTP Reverse Proxy

```bash
# Streamable HTTP (default)
shield-agent proxy --listen :8888 --upstream http://localhost:8000

# SSE transport
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

# HTTPS with TLS
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --tls-cert cert.pem --tls-key key.pem
```

The proxy applies the same middleware chain to HTTP-based MCP servers.

## Operating Modes

### stdio Mode

```
shield-agent [flags] <command> [args...]
```

- Wraps the child process, intercepting stdin/stdout as JSON-RPC pipelines
- Forwards SIGINT/SIGTERM to the child process
- Propagates the child's exit code
- 5-second graceful shutdown timeout before SIGKILL
- stderr passes through without interception

### Proxy Mode

```
shield-agent proxy --listen :8888 --upstream <url> --transport <sse|streamable-http>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--listen` | `:8888` | Listen address |
| `--upstream` | (required) | Upstream MCP server base URL |
| `--transport` | `streamable-http` | `sse` or `streamable-http` |
| `--tls-cert` | — | Path to TLS certificate file (enables HTTPS with `--tls-key`) |
| `--tls-key` | — | Path to TLS key file |

#### SSE Transport

| Endpoint | Description |
|----------|-------------|
| `GET /sse` | Connects to upstream SSE, relays events, rewrites endpoint URLs to local address |
| `POST /messages?sessionId=<id>` | Applies middleware, then forwards to upstream `/messages` |

#### Streamable HTTP Transport

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/mcp` or `/` | Applies middleware, forwards to upstream |
| `GET` | `/mcp` | Opens session SSE stream (raw proxy, no middleware) |
| `DELETE` | `/mcp` | Terminates session (raw proxy, no middleware) |

## Middleware

### Pipeline

Both modes use a configurable middleware chain. The default order is: **auth → guard → log**.

```go
type Middleware interface {
    ProcessRequest(ctx context.Context, req *Request) (*Request, error)
    ProcessResponse(ctx context.Context, resp *Response) (*Response, error)
}
```

- Middlewares execute in registration order
- First error aborts the chain and returns a JSON-RPC error
- Blocked requests generate an error response to the caller (not forwarded to the server)
- Blocked responses are dropped (not forwarded to the caller)
- Non-JSON or unexpected messages are forwarded verbatim

### Dynamic Middleware Chain

The pipeline is configured via the `middlewares` section in YAML. Omitting the section uses the defaults.

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 60
      max_body_size: 65536
      ip_blocklist:
        - "203.0.113.0/24"
      ip_allowlist:
        - "10.0.0.0/8"
  - name: log
    enabled: true
```

Individual middlewares can also be toggled via CLI flags without editing YAML:

```bash
shield-agent proxy --disable-middleware guard --upstream http://localhost:8000
shield-agent proxy --enable-middleware log --upstream http://localhost:8000
```

Sending **SIGHUP** to a running proxy reloads the config file, key store, and middleware chain without restarting the process.

### Authentication (AuthMiddleware)

Ed25519 signature-based agent authentication.

**How it works:**
1. Extracts `_mcp_agent_id` and `_mcp_signature` from JSON-RPC request `params`
2. Computes `sha256(json({method, params without _mcp_signature}))`
3. Verifies the Ed25519 signature against the agent's public key

**Agent ID formats:**

| Format | Resolution |
|--------|------------|
| `did:key:z...` | Base58btc decode + multicodec (0xed01) verification |
| Plain string | Lookup in `keys.yaml` |

**Security modes:**

| Mode | Behavior |
|------|----------|
| `open` (default) | Logs warnings on auth failure but allows requests through (observation mode) |
| `closed` | Rejects unauthenticated requests with a JSON-RPC error |

**Key Store:**
- `FileKeyStore` — loads Ed25519 public keys from a YAML file (`keys.yaml`)
- `CachedKeyStore` — wraps any KeyStore with a 5-minute TTL cache
- Agent IDs are logged as SHA-256 hashes only (never stored in plaintext)

### Guard (GuardMiddleware)

Enforces rate limits, request size limits, and IP-based access control.

| Config key | Default | Description |
|------------|---------|-------------|
| `rate_limit_per_min` | `0` (unlimited) | Max requests per minute per JSON-RPC method |
| `max_body_size` | `0` (unlimited) | Max request body size in bytes |
| `ip_blocklist` | — | CIDR ranges or IPs to block outright |
| `ip_allowlist` | — | CIDR ranges or IPs to allow (empty = allow all) |

Rejected requests increment the `shield_agent_rate_limit_rejected_total` Prometheus counter.

### Logging (LogMiddleware)

Asynchronous request/response logging to SQLite.

- Tracks pending requests by ID, computes latency on response
- Non-blocking write channel (buffer size 512) with a background writer goroutine
- Drops entries with a warning when the channel is full
- Notifications (requests without an ID) are logged immediately

## Log Query CLI

```bash
shield-agent logs [flags]
```

| Flag | Description |
|------|-------------|
| `--last N` | Show the most recent N entries (default: 50) |
| `--agent <id>` | Filter by agent ID (hashed internally) |
| `--since <duration>` | Time filter (e.g. `1h`, `30m`) |
| `--method <name>` | Filter by JSON-RPC method |
| `--format json\|table` | Output format (default: `table`) |

## Storage

SQLite database (default: `shield-agent.db`) with WAL mode and 5-second busy timeout.

**Schema (`action_logs`):**

| Column | Description |
|--------|-------------|
| `timestamp` | Record time |
| `agent_id_hash` | SHA-256 hash of agent ID (anonymized) |
| `method` | JSON-RPC method name |
| `direction` | `in` (request) / `out` (response) |
| `success` | Whether the call succeeded |
| `latency_ms` | Latency in milliseconds (response only) |
| `payload_size` | Size of params/result in bytes |
| `auth_status` | `verified` / `failed` / `unsigned` |
| `error_code` | Error code (if any) |

**Indexes:** `timestamp`, `(agent_id_hash, timestamp)`, `method`

**Auto-purge:** Deletes entries older than `retention_days` (default: 30) on startup.

## Monitoring

Default address: `127.0.0.1:9090`

| Endpoint | Description |
|----------|-------------|
| `/` | JSON index listing available endpoints |
| `/healthz` | Health check — in stdio mode, verifies child process liveness via kill(0); in proxy mode, also probes the upstream server. Returns `healthy` or `degraded` |
| `/metrics` | Prometheus metrics |

### Prometheus Metrics

| Metric | Type | Labels |
|--------|------|--------|
| `shield_agent_messages_total` | Counter | `direction`, `method` |
| `shield_agent_auth_total` | Counter | `status` |
| `shield_agent_message_latency_seconds` | Histogram | `method` |
| `shield_agent_child_process_up` | Gauge | — (stdio mode only) |
| `shield_agent_rate_limit_rejected_total` | Counter | `method` |

## Telemetry

Optional anonymous usage statistics. **Disabled by default.**

- Ring buffer of 10,000 events
- Periodic batch transmission (default: every 60 seconds, gzip-compressed)
- **Differential privacy:** flips the `success` field with probability `1/(1+e^epsilon)`
- **Agent ID anonymization:** `sha256(salt + id)`
- **IP k-anonymity:** IPv4 masked to /24, IPv6 masked to /48
- Endpoint: `POST {endpoint}/telemetry/ingest`

## Configuration

**Priority:** CLI flags > environment variables > YAML config file > defaults

Copy `shield-agent.example.yaml` to `shield-agent.yaml` to get started.

### Config Reference

| Setting | Default | Environment Variable |
|---------|---------|---------------------|
| `server.monitor_addr` | `127.0.0.1:9090` | `SHIELD_AGENT_MONITOR_ADDR` |
| `server.tls_cert` | — | `SHIELD_AGENT_TLS_CERT` |
| `server.tls_key` | — | `SHIELD_AGENT_TLS_KEY` |
| `server.cors_allowed_origins` | `["*"]` | — |
| `security.mode` | `open` | `SHIELD_AGENT_SECURITY_MODE` |
| `security.key_store_path` | `keys.yaml` | `SHIELD_AGENT_KEY_STORE_PATH` |
| `logging.level` | `info` | `SHIELD_AGENT_LOG_LEVEL` |
| `logging.format` | `json` | `SHIELD_AGENT_LOG_FORMAT` |
| `storage.db_path` | `shield-agent.db` | `SHIELD_AGENT_DB_PATH` |
| `storage.retention_days` | `30` | `SHIELD_AGENT_RETENTION_DAYS` |
| `telemetry.enabled` | `false` | `SHIELD_AGENT_TELEMETRY_ENABLED` |

### Global CLI Flags

| Flag | Description |
|------|-------------|
| `--config <path>` | Config file path (default: `shield-agent.yaml`) |
| `--log-level debug\|info\|warn\|error` | Log verbosity |
| `--verbose` | Alias for `--log-level debug` |
| `--telemetry` | Enable anonymous telemetry |
| `--monitor-addr <addr>` | Monitoring HTTP listen address |
| `--disable-middleware <name>` | Disable a named middleware at startup |
| `--enable-middleware <name>` | Enable a named middleware at startup |

## A2A Middleware

Reusable authentication and logging middleware for agent-to-agent (A2A) HTTP communication. Located in `internal/middleware/a2a/`. Authentication logic is shared with the HTTP API middleware via the `internal/middleware/httpauth` package.

```go
type Middleware interface {
    WrapHandler(next http.Handler) http.Handler
}
```

**Auth headers:** `X-Agent-ID`, `X-A2A-Signature`

- Signature payload: `sha256(method + " " + path + "\n" + body)`
- Supports `did:key:` URIs and KeyStore lookups
- Open/closed mode behavior identical to MCP middleware
- `onAuth` callback for event propagation (`verified` / `failed` / `unsigned`)

**Log middleware:** Records request/response pairs to SQLite asynchronously (buffer 512), with optional telemetry forwarding. Extracts the JSON-RPC method from the A2A request body.

## HTTP API Middleware

Reusable authentication and logging middleware for agent-to-external-API HTTP calls. Located in `internal/middleware/httpapi/`. Shares signature verification and key resolution logic with the A2A middleware via the `internal/middleware/httpauth` package.

Same `Middleware` / `Chain` pattern as A2A.

**Auth headers:** `X-Agent-ID`, `X-Agent-Signature`

- Signature and verification identical to A2A middleware
- Open/closed mode behavior identical
- Method label format: `"METHOD /path"` (e.g. `GET /api/v1/repos`)
- Async logging with optional telemetry forwarding

## Current Limitations

- No request content filtering — only metadata is recorded
- No dynamic key registration API (manual `keys.yaml` editing required)
- Telemetry requires a separate ingestion server
- `shield_agent_child_process_up` metric not applicable in proxy mode
- No WebSocket MCP transport support

## Roadmap

### A2A Proxy Transport
- HTTP proxy transport for routing agent traffic via `HTTPS_PROXY`
- Protocol auto-detection (A2A / JSON-RPC / REST)
- Intent verification with agent ID and action type whitelisting
- Bidirectional trust model
- Agent card-based identity verification (A2A spec)

### HTTP API Proxy Transport
- HTTP MITM proxy mode via `HTTP_PROXY` / `HTTPS_PROXY` injection
- TLS interception with CA certificate generation
- Domain/path-based allow/block rules
- Sensitive header masking (Authorization, Cookie)
- Agent identity tracking for outbound HTTP calls

## License

[MIT](LICENSE)
