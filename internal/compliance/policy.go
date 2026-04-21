package compliance

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/egress"
)

// PolicyMiddleware enforces the egress allowlist.
//
// Modes:
//   - warn:  record every violation but let the request through
//   - block: reject violations with HTTP 403 before the tunnel opens
//
// The middleware is intentionally small: allowlist membership is the only
// rule in Phase 1. Richer rules (rate-per-provider, model-level blocks)
// belong in a follow-up and live alongside content_tag / pii_scrub.
type PolicyMiddleware struct {
	egress.PassthroughEgressMiddleware
	mode      string
	allow     []string
	allowSet  map[string]struct{}
	logger    *slog.Logger
	metrics   egress.EgressMetrics
}

// NewPolicyMiddleware wires the middleware. An empty allow list means
// "allow everything, still log" — this is the default for Phase 1 roll-outs
// where the operator wants observability before enforcement.
func NewPolicyMiddleware(mode string, allow []string, logger *slog.Logger, metrics egress.EgressMetrics) *PolicyMiddleware {
	set := make(map[string]struct{}, len(allow))
	cleaned := make([]string, 0, len(allow))
	for _, a := range allow {
		a = strings.TrimSpace(strings.ToLower(a))
		if a == "" {
			continue
		}
		set[a] = struct{}{}
		cleaned = append(cleaned, a)
	}
	if mode == "" {
		mode = "warn"
	}
	return &PolicyMiddleware{
		mode:     mode,
		allow:    cleaned,
		allowSet: set,
		logger:   logger,
		metrics:  metrics,
	}
}

// Name identifies this middleware in config.
func (*PolicyMiddleware) Name() string { return "policy" }

// ProcessRequest evaluates the allowlist. In block mode a violation aborts
// the chain; in warn mode it annotates the request and metrics counter.
func (p *PolicyMiddleware) ProcessRequest(_ context.Context, req *egress.Request) (*egress.Request, error) {
	if p.allowed(req.Host) {
		return req, nil
	}
	rule := "upstream_allow"
	action := "warn"
	if p.mode == "block" {
		action = "block"
	}
	p.metrics.IncPolicyViolation(rule, action)
	p.logger.Warn("egress policy violation",
		slog.String("destination", req.Host),
		slog.String("rule", rule),
		slog.String("action", action),
	)
	if p.mode == "block" {
		return nil, fmt.Errorf("destination %q not in egress.upstream_allow", req.Host)
	}
	return req, nil
}

// allowed returns true when host matches an allowlist entry. Matching is
// case-insensitive and supports exact host equality or subdomain suffix
// (".example.com" in the allowlist matches "api.example.com").
func (p *PolicyMiddleware) allowed(host string) bool {
	if len(p.allow) == 0 {
		return true
	}
	host = strings.ToLower(host)
	if _, ok := p.allowSet[host]; ok {
		return true
	}
	for _, a := range p.allow {
		if strings.HasPrefix(a, ".") && strings.HasSuffix(host, a) {
			return true
		}
		if strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// registerPolicy exposes the middleware to the egress factory registry.
// Called from RegisterEgressMiddlewares so one Register call covers both.
func registerPolicy() {
	egress.Register("policy", func(_ config.MiddlewareEntry, deps egress.EgressDependencies) (egress.EgressMiddleware, error) {
		return NewPolicyMiddleware(deps.Cfg.PolicyMode, deps.Cfg.UpstreamAllow, deps.Logger, deps.Metrics), nil
	})
}
