package monitor

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"log/slog"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNewMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	if m == nil {
		t.Fatal("expected non-nil Metrics")
	}
	if m.MessagesTotal == nil {
		t.Error("MessagesTotal is nil")
	}
	if m.AuthTotal == nil {
		t.Error("AuthTotal is nil")
	}
	if m.MessageLatency == nil {
		t.Error("MessageLatency is nil")
	}
	if m.ChildProcessUp == nil {
		t.Error("ChildProcessUp is nil")
	}
}

func TestHealthzHealthy(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	s := New("127.0.0.1:0", m, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if _, ok := body["status"]; !ok {
		t.Error("response JSON missing 'status' field")
	}
}

func TestHealthzWithPID(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	s := New("127.0.0.1:0", m, newTestLogger())
	s.SetChildPID(os.Getpid())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	pidVal, ok := body["child_pid"]
	if !ok {
		t.Fatal("response JSON missing 'child_pid' field")
	}
	// JSON numbers decode as float64.
	pid, ok := pidVal.(float64)
	if !ok {
		t.Fatalf("child_pid is not a number, got %T", pidVal)
	}
	if int(pid) != os.Getpid() {
		t.Errorf("expected child_pid=%d, got %d", os.Getpid(), int(pid))
	}
}

func TestServerStartShutdown(t *testing.T) {
	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	s := New(addr, m, newTestLogger())

	s.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Shutdown(ctx)
}
