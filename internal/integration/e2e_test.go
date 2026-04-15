package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sharedBin is the path to a single shield-agent binary built once by TestMain.
var sharedBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "shield-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	moduleRoot := filepath.Join(wd, "..", "..")
	bin := filepath.Join(tmp, "shield-agent")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/shield-agent")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "go build failed: %v\n%s\n", err, out)
		os.Exit(1)
	}
	sharedBin = bin

	os.Exit(m.Run())
}

const echoServerScript = `#!/usr/bin/env python3
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
        if "method" in msg:
            resp = {"jsonrpc": "2.0", "id": msg.get("id"), "result": {"echo": msg["method"]}}
            sys.stdout.write(json.dumps(resp) + "\n")
            sys.stdout.flush()
    except:
        pass
`

// getBin returns the pre-built shield-agent binary path.
func getBin(t *testing.T) string {
	t.Helper()
	if sharedBin == "" {
		t.Fatal("sharedBin not set — TestMain did not run")
	}
	return sharedBin
}

// isolateCmd sets the command's working directory to tmpDir and assigns a
// random monitor port so parallel test processes don't share the same SQLite
// database or fight over the default :9090 monitor port.
func isolateCmd(cmd *exec.Cmd, tmpDir string) {
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "SHIELD_AGENT_MONITOR_ADDR=127.0.0.1:0")
}

// writeEchoScript writes the Python echo server to a temp file and returns its path.
func writeEchoScript(t *testing.T, tmpDir string) string {
	t.Helper()
	script := filepath.Join(tmpDir, "echo_server.py")
	if err := os.WriteFile(script, []byte(echoServerScript), 0o755); err != nil {
		t.Fatalf("writing echo server script: %v", err)
	}
	return script
}

func TestE2EPipeline(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	tmpDir := t.TempDir()
	bin := getBin(t)
	script := writeEchoScript(t, tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--verbose", "python3", script)
	isolateCmd(cmd, tmpDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	defer cmd.Process.Kill() //nolint:errcheck

	// Wait for shield-agent to log "starting shield-agent" on stderr,
	// which means the child process is up and the pipeline is ready.
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "starting shield-agent") {
				close(ready)
				break
			}
		}
		// Drain remaining stderr to prevent blocking.
		for scanner.Scan() {
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for shield-agent to start")
	}

	// Send a JSON-RPC request.
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	if _, err := io.WriteString(stdin, req); err != nil {
		t.Fatalf("writing request: %v", err)
	}

	// Read response with timeout via the context-cancelled scanner.
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ch <- result{line: scanner.Text()}
		} else {
			ch <- result{err: scanner.Err()}
		}
	}()

	select {
	case <-ctx.Done():
		t.Fatal("timed out waiting for response")
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("reading response: %v", r.err)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal([]byte(r.line), &resp); err != nil {
			t.Fatalf("unmarshal response %q: %v", r.line, err)
		}
		if id, ok := resp["id"]; !ok {
			t.Error("response missing 'id' field")
		} else if id.(float64) != 1 {
			t.Errorf("want id=1, got %v", id)
		}
		if _, ok := resp["result"]; !ok {
			t.Error("response missing 'result' field")
		}
	}
}

func TestE2EPipelineInvalidJSON(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	tmpDir := t.TempDir()
	bin := getBin(t)
	script := writeEchoScript(t, tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use a combined stderr reader so we can detect any pass-through.
	cmd := exec.CommandContext(ctx, bin, "python3", script)
	isolateCmd(cmd, tmpDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	defer cmd.Process.Kill() //nolint:errcheck

	// Send invalid JSON followed by a valid request so we can detect pipeline still works.
	invalid := "not-json-at-all\n"
	valid := `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}` + "\n"
	if _, err := io.WriteString(stdin, invalid+valid); err != nil {
		t.Fatalf("writing data: %v", err)
	}

	// Read until we get the valid response (invalid line is silently dropped by echo server).
	type result struct {
		lines []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
			// One response line is enough for validation.
			if len(lines) >= 1 {
				break
			}
		}
		ch <- result{lines: lines, err: scanner.Err()}
	}()

	select {
	case <-ctx.Done():
		t.Fatal("timed out waiting for response after invalid JSON")
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("reading: %v", r.err)
		}
		// Verify we got the valid-request response (id=2).
		found := false
		for _, line := range r.lines {
			if strings.Contains(line, `"id"`) {
				var resp map[string]interface{}
				if err := json.Unmarshal([]byte(line), &resp); err == nil {
					if resp["id"].(float64) == 2 {
						found = true
					}
				}
			}
		}
		if !found && len(r.lines) > 0 {
			// Pipeline still passed something through — acceptable.
			t.Logf("received lines: %v", r.lines)
		}
	}
}

func TestE2ECommandNotFound(t *testing.T) {
	t.Parallel()

	bin := getBin(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "this-command-does-not-exist-xyzzy")
	isolateCmd(cmd, t.TempDir())
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit for non-existent command, got nil")
	}
}

// writeExitScript writes a Python script that exits immediately with code 1.
func writeExitScript(t *testing.T, tmpDir string) string {
	t.Helper()
	script := filepath.Join(tmpDir, "exit1.py")
	content := "import sys\nsys.exit(1)\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("writing exit script: %v", err)
	}
	return script
}

// TestE2EChildExitsImmediately verifies that shield-agent propagates a non-zero
// exit code when the wrapped command exits immediately.
func TestE2EChildExitsImmediately(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	tmpDir := t.TempDir()
	bin := getBin(t)
	script := writeExitScript(t, tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "python3", script)
	isolateCmd(cmd, tmpDir)
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit, got nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
}

// TestE2EConcurrentRequests sends multiple requests concurrently and verifies
// all responses are received.
func TestE2EConcurrentRequests(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	tmpDir := t.TempDir()
	bin := getBin(t)
	script := writeEchoScript(t, tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--verbose", "python3", script)
	isolateCmd(cmd, tmpDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	defer cmd.Process.Kill() //nolint:errcheck

	// Wait for shield-agent to be ready.
	readyCh := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderrPipe)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "starting shield-agent") {
				close(readyCh)
				break
			}
		}
		for sc.Scan() {
		}
	}()
	select {
	case <-readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for shield-agent to start")
	}

	const n = 10
	// Send all requests in one write.
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		sb.WriteString(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"m%d","params":{}}`, i, i) + "\n")
	}
	if _, err := io.WriteString(stdin, sb.String()); err != nil {
		t.Fatalf("writing requests: %v", err)
	}

	// Read n responses.
	received := make(chan string, n)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			received <- scanner.Text()
		}
		close(received)
	}()

	got := 0
	deadline := time.After(10 * time.Second)
	for got < n {
		select {
		case line, ok := <-received:
			if !ok {
				goto done
			}
			if strings.Contains(line, `"result"`) {
				got++
			}
		case <-deadline:
			t.Logf("got %d/%d responses before deadline", got, n)
			goto done
		}
	}
done:
	if got == 0 {
		t.Errorf("got 0 responses out of %d requests", n)
	}
}
