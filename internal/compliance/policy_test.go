package compliance

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/itdar/shield-agent/internal/egress"
)

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPolicyAllowEmptyListPassesAll(t *testing.T) {
	p := NewPolicyMiddleware("warn", nil, logger(), egress.NoopMetrics{})
	req := &egress.Request{Host: "any.example.com"}
	if _, err := p.ProcessRequest(context.Background(), req); err != nil {
		t.Fatalf("empty allowlist should not block: %v", err)
	}
}

func TestPolicyExactMatch(t *testing.T) {
	p := NewPolicyMiddleware("block", []string{"api.openai.com"}, logger(), egress.NoopMetrics{})
	if _, err := p.ProcessRequest(context.Background(), &egress.Request{Host: "api.openai.com"}); err != nil {
		t.Errorf("exact match should pass: %v", err)
	}
	if _, err := p.ProcessRequest(context.Background(), &egress.Request{Host: "evil.com"}); err == nil {
		t.Error("non-matching host should block")
	}
}

func TestPolicySubdomainMatch(t *testing.T) {
	p := NewPolicyMiddleware("block", []string{"example.com"}, logger(), egress.NoopMetrics{})
	if _, err := p.ProcessRequest(context.Background(), &egress.Request{Host: "api.example.com"}); err != nil {
		t.Errorf("subdomain should match: %v", err)
	}
	if _, err := p.ProcessRequest(context.Background(), &egress.Request{Host: "example.org"}); err == nil {
		t.Error("different TLD should block")
	}
}

func TestPolicyWarnModeNeverBlocks(t *testing.T) {
	p := NewPolicyMiddleware("warn", []string{"api.openai.com"}, logger(), egress.NoopMetrics{})
	req := &egress.Request{Host: "evil.com"}
	if _, err := p.ProcessRequest(context.Background(), req); err != nil {
		t.Errorf("warn mode should not return err: %v", err)
	}
}

func TestPolicyCaseInsensitive(t *testing.T) {
	p := NewPolicyMiddleware("block", []string{"API.OpenAI.COM"}, logger(), egress.NoopMetrics{})
	if _, err := p.ProcessRequest(context.Background(), &egress.Request{Host: "api.openai.com"}); err != nil {
		t.Errorf("case mismatch should still allow: %v", err)
	}
}
