#!/usr/bin/env bash
# bench-egress.sh — run a p99-grade load test against the egress proxy.
#
# Requirements: hey (https://github.com/rakyll/hey) or vegeta on PATH, go,
# and a writable /tmp. Prints hey's latency breakdown (p50/p95/p99) and
# exits non-zero when p99 exceeds the target.
#
# Usage:
#   ./scripts/bench-egress.sh [REQUESTS] [CONCURRENCY] [P99_MS]
#   REQUESTS     default 10000
#   CONCURRENCY  default 50
#   P99_MS       default 5  (Phase 1 AC-10 target)
#
# AC-12 covers this harness — the Makefile / CI can invoke it with
# tighter bounds once the machine-baseline varianceis understood.

set -euo pipefail

REQUESTS=${1:-10000}
CONCURRENCY=${2:-50}
P99_TARGET_MS=${3:-5}

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$(mktemp -t shield-egress-bench.XXXX)"
trap 'rm -f "$BIN"' EXIT

echo "==> building shield-agent"
(cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/shield-agent)

# Minimal config: egress on loopback, ingress disabled, no auth.
WORKDIR=$(mktemp -d -t shield-egress-bench.XXXX)
trap 'rm -rf "$WORKDIR"' EXIT
cat >"$WORKDIR/shield-agent.yaml" <<EOF
server:
  monitor_addr: "127.0.0.1:19090"
security:
  mode: "open"
  key_store_path: "$WORKDIR/keys.yaml"
logging:
  level: "error"
  format: "text"
telemetry:
  enabled: false
  endpoint: "http://localhost:8080"
  batch_interval: 60
  epsilon: 1.0
storage:
  db_path: "$WORKDIR/shield.db"
  retention_days: 1
egress:
  enabled: true
  listen: "127.0.0.1:18889"
  policy_mode: "warn"
  hash_chain:
    enabled: true
    algorithm: "sha256"
  middlewares:
    - name: egress_log
      enabled: true
EOF
echo 'keys: []' >"$WORKDIR/keys.yaml"

echo "==> starting local upstream"
python3 -c '
import http.server, socketserver, threading
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.send_header("Content-Type","text/plain")
        self.send_header("Content-Length","2"); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
s = socketserver.ThreadingTCPServer(("127.0.0.1", 18890), H)
threading.Thread(target=s.serve_forever, daemon=True).start()
import time
while True: time.sleep(3600)
' &
UPSTREAM_PID=$!

echo "==> starting egress proxy"
SHIELD_AGENT_TEST_ALLOW_LOOPBACK=1 "$BIN" --config "$WORKDIR/shield-agent.yaml" egress \
  >"$WORKDIR/egress.log" 2>&1 &
SHIELD_PID=$!
trap 'kill $UPSTREAM_PID $SHIELD_PID 2>/dev/null || true; rm -rf "$WORKDIR"' EXIT

# Wait for the proxy to listen.
for _ in $(seq 1 40); do
  nc -z 127.0.0.1 18889 && break
  sleep 0.1
done

echo "==> warming proxy (50 reqs)"
for _ in $(seq 1 50); do
  curl -sS -x http://127.0.0.1:18889 http://127.0.0.1:18890/ >/dev/null || true
done

if command -v hey >/dev/null; then
  echo "==> hey -n $REQUESTS -c $CONCURRENCY (p99 target ${P99_TARGET_MS}ms)"
  HEY_OUT=$(hey -n "$REQUESTS" -c "$CONCURRENCY" -x http://127.0.0.1:18889 \
    http://127.0.0.1:18890/ 2>&1)
  echo "$HEY_OUT"
  P99_SEC=$(echo "$HEY_OUT" | awk '/99% in /{print $3}' | tr -d '[:alpha:]')
  if [[ -n "$P99_SEC" ]]; then
    P99_MS=$(awk -v s="$P99_SEC" 'BEGIN{printf "%.2f", s*1000}')
    echo "p99 = ${P99_MS}ms (target ${P99_TARGET_MS}ms)"
    awk -v p="$P99_MS" -v t="$P99_TARGET_MS" 'BEGIN{ if (p+0 > t+0) exit 1 }' \
      || { echo "FAIL: p99 regression"; exit 1; }
  fi
else
  echo "hey not on PATH, falling back to 'go test -bench=. ./internal/egress/'"
  (cd "$REPO_ROOT" && go test -bench=. -benchmem -run=^$ ./internal/egress/ -count=1)
fi

echo "==> bench complete"
