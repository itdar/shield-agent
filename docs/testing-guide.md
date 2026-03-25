# shield-agent Testing Guide

A practical, hands-on guide for testing all major features of shield-agent.

---

## Prerequisites

- **Go 1.22+** (the module requires Go 1.25, but 1.22+ will fetch the toolchain automatically)
- A terminal with `curl` and `jq` installed
- (Optional) A running MCP server for proxy-mode tests (e.g. [fastmcp](https://github.com/jlowin/fastmcp))

### Build from source

```bash
git clone https://github.com/itdar/shield-agent.git
cd shield-agent
go build -o shield-agent ./cmd/shield-agent
```

Verify the binary:

```bash
./shield-agent --help
```

### Create a minimal config

Copy the example configuration:

```bash
cp shield-agent.example.yaml shield-agent.yaml
```

---

## 1. stdio Mode -- Wrapping an MCP Server

In stdio mode, shield-agent wraps a child process and intercepts JSON-RPC messages on stdin/stdout.

### Basic test with `echo`

```bash
echo '{"jsonrpc":"2.0","method":"initialize","id":1}' | ./shield-agent cat
```

`cat` echoes stdin back to stdout, so shield-agent intercepts the JSON-RPC message in both directions.

### Wrapping a real MCP server

```bash
./shield-agent python server.py
./shield-agent --verbose node server.js --port 8080
```

The `--verbose` flag sets the log level to `debug`, so you can see every intercepted message.

### Verify monitoring is running

While shield-agent is running in stdio mode, the monitoring server starts on the default address:

```bash
curl -s http://127.0.0.1:9090/healthz | jq .
```

Expected output:

```json
{
  "child_pid": 12345,
  "status": "healthy"
}
```

---

## 2. Proxy Mode -- Proxying an Upstream MCP Server

Proxy mode sits between an MCP client and an upstream HTTP-based MCP server.

### Start an upstream MCP server

If you have a fastmcp server:

```bash
# In a separate terminal
python -m fastmcp run server.py --port 8000
```

### SSE transport

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse
```

Test the SSE endpoint:

```bash
# Open the SSE stream (will block, printing events)
curl -N http://localhost:8888/sse
```

In another terminal, send a message (replace `<sessionId>` with the value from the SSE stream):

```bash
curl -X POST "http://localhost:8888/messages?sessionId=<sessionId>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"initialize","id":1}'
```

### Streamable HTTP transport

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport streamable-http
```

Test with a POST request:

```bash
curl -X POST http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"initialize","id":1}'
```

---

## 3. Log Query CLI

After running shield-agent (in either mode) and sending some messages, query the stored logs.

### Show the last 10 log entries

```bash
./shield-agent logs --last 10
```

### Logs from the last hour in JSON format

```bash
./shield-agent logs --since 1h --format json
```

### Filter by agent ID and method

```bash
./shield-agent logs --agent <id> --method tools/call
```

### Combine filters

```bash
./shield-agent logs --last 5 --since 30m --method initialize --format json
```

The default output is a table:

```
TIMESTAMP          DIRECTION  METHOD                         OK    LATENCY_MS AUTH
2026-03-25T10:...  in         initialize                     true  0.0        unsigned
2026-03-25T10:...  out        initialize                     true  12.3       unsigned
```

---

## 4. Rate Limit Verification

The guard middleware enforces per-method rate limiting. To test it, set a low rate limit in your config.

### Step 1: Configure a low rate limit

Edit `shield-agent.yaml`:

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 3
  - name: log
    enabled: true
```

### Step 2: Start the proxy

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport streamable-http
```

### Step 3: Send requests in a rapid loop

```bash
for i in $(seq 1 5); do
  echo "--- Request $i ---"
  curl -s -X POST http://localhost:8888/mcp \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"method\":\"tools/list\",\"id\":$i}"
  echo
done
```

The first 3 requests should succeed. Requests 4 and 5 should return a JSON-RPC error response like:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "error": {
    "code": -32600,
    "message": "rate limit exceeded for \"tools/list\""
  }
}
```

### Step 4: Verify the Prometheus counter

```bash
curl -s http://127.0.0.1:9090/metrics | grep shield_agent_rate_limit_rejected_total
```

You should see a counter for the `tools/list` method with a value matching the number of rejected requests.

---

## 5. SIGHUP Config Reload

shield-agent supports hot-reloading configuration, key stores, and the middleware chain by sending `SIGHUP` -- no restart required.

### Step 1: Start the proxy with verbose logging

```bash
./shield-agent --verbose proxy --listen :8888 --upstream http://localhost:8000
```

Note the PID from the log output, or find it:

```bash
pgrep -f "shield-agent proxy"
```

### Step 2: Edit the config while running

For example, change the security mode from `open` to `closed` in `shield-agent.yaml`:

```yaml
security:
  mode: "closed"
```

Or change the log level:

```yaml
logging:
  level: "debug"
```

Or enable/disable a middleware:

```yaml
middlewares:
  - name: guard
    enabled: false
```

### Step 3: Send SIGHUP

```bash
kill -SIGHUP $(pgrep -f "shield-agent proxy")
```

### Step 4: Check the logs

You should see log output confirming the reload:

```
{"level":"info","msg":"received SIGHUP, reloading configuration"}
{"level":"info","msg":"configuration reloaded successfully"}
```

The new settings take effect immediately for all subsequent requests.

---

## 6. TLS Mode

The proxy supports HTTPS when you provide a certificate and key.

### Generate a self-signed certificate (for testing)

```bash
openssl req -x509 -newkey rsa:2048 -keyout key.pem -out cert.pem \
  -days 365 -nodes -subj "/CN=localhost"
```

### Start the proxy with TLS

```bash
./shield-agent proxy \
  --listen :8888 \
  --upstream http://localhost:8000 \
  --transport streamable-http \
  --tls-cert cert.pem \
  --tls-key key.pem
```

You should see a log message: `"msg":"TLS enabled","cert":"cert.pem"`.

### Test with curl

```bash
curl -k -X POST https://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"initialize","id":1}'
```

The `-k` flag tells curl to accept the self-signed certificate. In production, use a proper CA-signed certificate.

### TLS via config file

Instead of CLI flags, you can set TLS in `shield-agent.yaml`:

```yaml
server:
  tls_cert: "/path/to/cert.pem"
  tls_key: "/path/to/key.pem"
```

CLI flags (`--tls-cert`, `--tls-key`) override config file values when both are provided.

---

## 7. Monitoring Endpoints

The monitoring server runs on `127.0.0.1:9090` by default (configurable via `--monitor-addr` or `server.monitor_addr` in the config).

### Root endpoint -- service index

```bash
curl -s http://127.0.0.1:9090/ | jq .
```

Expected:

```json
{
  "endpoints": ["/healthz", "/metrics"],
  "service": "shield-agent"
}
```

### Health check

```bash
curl -s http://127.0.0.1:9090/healthz | jq .
```

In stdio mode (child process running):

```json
{
  "child_pid": 54321,
  "status": "healthy"
}
```

In proxy mode, the health check also probes the upstream server. If the upstream is down:

```json
{
  "child_pid": 0,
  "status": "degraded"
}
```

### Prometheus metrics

```bash
curl -s http://127.0.0.1:9090/metrics
```

Key metrics to look for:

| Metric | Type | Description |
|--------|------|-------------|
| `shield_agent_messages_total` | Counter | Total JSON-RPC messages (labels: `direction`, `method`) |
| `shield_agent_auth_total` | Counter | Authentication events (labels: `status`) |
| `shield_agent_message_latency_seconds` | Histogram | Processing latency per method |
| `shield_agent_child_process_up` | Gauge | 1 if the child process is alive (stdio mode) |
| `shield_agent_rate_limit_rejected_total` | Counter | Rate-limited requests (labels: `method`) |

Filter for shield-agent metrics only:

```bash
curl -s http://127.0.0.1:9090/metrics | grep "^shield_agent_"
```

### Custom monitor address

```bash
./shield-agent --monitor-addr 0.0.0.0:9191 proxy --listen :8888 --upstream http://localhost:8000
```

Then query on the custom port:

```bash
curl -s http://localhost:9191/healthz | jq .
```

---

## Quick Reference

| Scenario | Command |
|----------|---------|
| stdio wrap | `./shield-agent <command> [args...]` |
| proxy (SSE) | `./shield-agent proxy --listen :8888 --upstream <url> --transport sse` |
| proxy (Streamable HTTP) | `./shield-agent proxy --listen :8888 --upstream <url> --transport streamable-http` |
| proxy (TLS) | `./shield-agent proxy --listen :8888 --upstream <url> --tls-cert cert.pem --tls-key key.pem` |
| query logs | `./shield-agent logs --last 10 --format json` |
| health check | `curl http://127.0.0.1:9090/healthz` |
| Prometheus metrics | `curl http://127.0.0.1:9090/metrics` |
| config reload | `kill -SIGHUP $(pgrep -f "shield-agent")` |
| disable middleware | `./shield-agent --disable-middleware guard proxy --upstream <url>` |
