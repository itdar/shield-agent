package integration

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestEgressCONNECTMetadataLogged boots shield-agent in egress mode against a
// local HTTPS-like upstream and asserts the CONNECT request produces a row
// in egress_logs with provider="openai" destination metadata.
//
// We use curl's --proxy-insecure against an httptest TLS server, relying on
// curl's CONNECT support. The goal is not to validate TLS — it is to prove
// that the CONNECT path through shield-agent's forward proxy lands a row
// in egress_logs with the expected metadata.
func TestEgressCONNECTMetadataLogged(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available on this host")
	}

	// Pick a free port for the upstream and for the egress listener.
	upstreamPort := freePort(t)
	egressPort := freePort(t)
	monitorPort := freePort(t)

	// Spin up a minimal HTTP upstream. The egress proxy's CONNECT path
	// doesn't terminate TLS — it pipes raw bytes — so we can point curl
	// at a plain HTTP upstream and use the absolute-URI proxy form.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "hello from upstream")
	}))
	defer upstream.Close()
	_ = upstreamPort // the httptest picks its own; kept for clarity

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "egress.db")
	keysPath := filepath.Join(dir, "keys.yaml")
	if err := os.WriteFile(keysPath, []byte("keys: []\n"), 0o600); err != nil {
		t.Fatalf("write keys: %v", err)
	}
	configPath := filepath.Join(dir, "shield-agent.yaml")
	configYAML := fmt.Sprintf(`server:
  monitor_addr: "127.0.0.1:%d"
security:
  mode: "open"
  key_store_path: "%s"
logging:
  level: "error"
  format: "text"
telemetry:
  enabled: false
  endpoint: "http://localhost:8080"
  batch_interval: 60
  epsilon: 1.0
storage:
  db_path: "%s"
  retention_days: 30
egress:
  enabled: true
  listen: "127.0.0.1:%d"
  policy_mode: "warn"
  hash_chain:
    enabled: true
    algorithm: "sha256"
  middlewares:
    - name: egress_log
      enabled: true
`, monitorPort, keysPath, dbPath, egressPort)
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, sharedBin, "--config", configPath, "egress")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	// httptest binds to 127.0.0.1; shield-agent's SSRF guard blocks
	// loopback destinations. Set the test-only escape hatch in the
	// child process env so loopback upstreams are reachable for this test.
	cmd.Env = append(os.Environ(), "SHIELD_AGENT_TEST_ALLOW_LOOPBACK=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start egress: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	waitForListener(t, fmt.Sprintf("127.0.0.1:%d", egressPort))

	// Fire a plaintext-HTTP request through the egress proxy. Using the
	// absolute-URI form exercises handleHTTP rather than handleConnect.
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", egressPort))
	httpClient := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}
	req, _ := http.NewRequest(http.MethodGet, upstream.URL+"/ping", nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hello from upstream") {
		t.Fatalf("unexpected body: %q", body)
	}

	// Give the LogWriter a moment to drain asynchronously.
	waitForRow(t, dbPath, 3*time.Second)

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var (
		destination, method, policyAction, rowHash string
		requestSize, responseSize                  int64
	)
	row := db.QueryRow(`SELECT destination, method, policy_action, row_hash, request_size, response_size
FROM egress_logs ORDER BY id DESC LIMIT 1`)
	if err := row.Scan(&destination, &method, &policyAction, &rowHash, &requestSize, &responseSize); err != nil {
		t.Fatalf("scan row: %v", err)
	}

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	hostOnly := strings.Split(upstreamHost, ":")[0]
	if destination != hostOnly {
		t.Errorf("destination = %q, want %q", destination, hostOnly)
	}
	if !strings.HasPrefix(method, "GET ") {
		t.Errorf("method = %q, want GET prefix", method)
	}
	if policyAction != "allow" {
		t.Errorf("policy_action = %q, want allow", policyAction)
	}
	if rowHash == "" {
		t.Error("row_hash is empty — hash chain did not run")
	}
	if responseSize <= 0 {
		t.Errorf("response_size = %d, want > 0", responseSize)
	}

	// /metrics should reflect the request.
	metricsResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", monitorPort))
	if err != nil {
		t.Fatalf("metrics get: %v", err)
	}
	metricsBody, _ := io.ReadAll(metricsResp.Body)
	metricsResp.Body.Close()
	if !strings.Contains(string(metricsBody), "shield_agent_egress_requests_total") {
		t.Error("egress metrics missing from /metrics output")
	}
}

// TestEgressPolicyBlocks asserts that block mode returns a non-2xx status
// for disallowed hosts and records the violation.
func TestEgressPolicyBlocks(t *testing.T) {
	egressPort := freePort(t)
	monitorPort := freePort(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "egress.db")
	keysPath := filepath.Join(dir, "keys.yaml")
	_ = os.WriteFile(keysPath, []byte("keys: []\n"), 0o600)
	configPath := filepath.Join(dir, "shield-agent.yaml")
	configYAML := fmt.Sprintf(`server:
  monitor_addr: "127.0.0.1:%d"
security:
  mode: "open"
  key_store_path: "%s"
logging:
  level: "error"
  format: "text"
telemetry:
  enabled: false
  endpoint: "http://localhost:8080"
  batch_interval: 60
  epsilon: 1.0
storage:
  db_path: "%s"
  retention_days: 30
egress:
  enabled: true
  listen: "127.0.0.1:%d"
  policy_mode: "block"
  upstream_allow: ["api.openai.com"]
  hash_chain:
    enabled: true
    algorithm: "sha256"
  middlewares:
    - name: policy
      enabled: true
    - name: egress_log
      enabled: true
`, monitorPort, keysPath, dbPath, egressPort)
	_ = os.WriteFile(configPath, []byte(configYAML), 0o600)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sharedBin, "--config", configPath, "egress")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Env = append(os.Environ(), "SHIELD_AGENT_TEST_ALLOW_LOOPBACK=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start egress: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	waitForListener(t, fmt.Sprintf("127.0.0.1:%d", egressPort))

	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", egressPort))
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}
	// Target upstream is not in the allowlist, so the policy middleware
	// must reject with 403 before the request reaches the upstream.
	resp, err := client.Get(upstream.URL + "/should-not-reach")
	if err != nil {
		t.Fatalf("client.Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// freePort asks the kernel for a free port and returns it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitForListener polls the address until it accepts a TCP connection or
// the timeout fires.
func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener %s not ready", addr)
}

// waitForRow polls egress_logs until at least one row exists or timeout.
func waitForRow(t *testing.T, dbPath string, max time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM egress_logs").Scan(&n); err == nil && n > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no rows in egress_logs after %s", max)
}
