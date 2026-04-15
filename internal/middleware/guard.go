package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/itdar/shield-agent/internal/jsonrpc"
	"github.com/itdar/shield-agent/internal/reputation"
)

// GuardMiddleware enforces rate limits, request size limits, IP-based access control,
// brute force protection, and malformed payload detection.
type GuardMiddleware struct {
	PassthroughMiddleware
	limiter        *rateLimiter
	maxBodySize    int64
	blocklist      []net.IPNet
	allowlist      []net.IPNet
	logger         *slog.Logger
	onReject       func() // called when a request is rejected by rate limit
	bruteForce     *bruteForceTracker
	validateJSON   bool
	reputation     reputation.Provider // nil = no reputation-based adjustment
}

// GuardConfig holds configuration for the guard middleware.
type GuardConfig struct {
	RateLimitPerMin     int      // requests per minute per method (0 = unlimited)
	MaxBodySize         int64    // max request body size in bytes (0 = unlimited)
	IPBlocklist         []string // CIDR strings to block
	IPAllowlist         []string // CIDR strings to allow (empty = allow all)
	BruteForceMaxFails  int      // consecutive failures before auto-block (0 = disabled)
	BruteForceWindow    time.Duration // window for tracking failures (default 5m)
	BruteForceBlockDur  time.Duration // how long to block (default 10m)
	ValidateJSONRPC     bool     // reject malformed JSON-RPC payloads
}

// NewGuardMiddleware creates a GuardMiddleware from the given config.
func NewGuardMiddleware(cfg GuardConfig, logger *slog.Logger, onReject func()) *GuardMiddleware {
	g := &GuardMiddleware{
		maxBodySize:  cfg.MaxBodySize,
		logger:       logger,
		onReject:     onReject,
		validateJSON: cfg.ValidateJSONRPC,
	}

	if cfg.RateLimitPerMin > 0 {
		g.limiter = newRateLimiter(cfg.RateLimitPerMin, time.Minute)
	}

	if cfg.BruteForceMaxFails > 0 {
		window := cfg.BruteForceWindow
		if window == 0 {
			window = 5 * time.Minute
		}
		blockDur := cfg.BruteForceBlockDur
		if blockDur == 0 {
			blockDur = 10 * time.Minute
		}
		g.bruteForce = newBruteForceTracker(cfg.BruteForceMaxFails, window, blockDur)
	}

	for _, cidr := range cfg.IPBlocklist {
		if _, ipNet, err := net.ParseCIDR(cidr); err == nil {
			g.blocklist = append(g.blocklist, *ipNet)
		} else if ip := net.ParseIP(cidr); ip != nil {
			mask := net.CIDRMask(32, 32)
			if ip.To4() == nil {
				mask = net.CIDRMask(128, 128)
			}
			g.blocklist = append(g.blocklist, net.IPNet{IP: ip, Mask: mask})
		}
	}

	for _, cidr := range cfg.IPAllowlist {
		if _, ipNet, err := net.ParseCIDR(cidr); err == nil {
			g.allowlist = append(g.allowlist, *ipNet)
		} else if ip := net.ParseIP(cidr); ip != nil {
			mask := net.CIDRMask(32, 32)
			if ip.To4() == nil {
				mask = net.CIDRMask(128, 128)
			}
			g.allowlist = append(g.allowlist, net.IPNet{IP: ip, Mask: mask})
		}
	}

	return g
}

// Name returns the name of this middleware.
func (g *GuardMiddleware) Name() string { return "guard" }

// ProcessRequest enforces size limits, rate limits, brute force checks,
// and malformed JSON-RPC validation on incoming requests.
func (g *GuardMiddleware) ProcessRequest(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error) {
	// Validate JSON-RPC structure.
	if g.validateJSON {
		if req.JSONRPC != "" && req.JSONRPC != "2.0" {
			g.logger.Warn("malformed JSON-RPC: invalid version",
				slog.String("version", req.JSONRPC),
			)
			return nil, fmt.Errorf("malformed JSON-RPC: version must be \"2.0\", got %q", req.JSONRPC)
		}
		if req.Method == "" {
			g.logger.Warn("malformed JSON-RPC: empty method")
			return nil, fmt.Errorf("malformed JSON-RPC: method must not be empty")
		}
	}

	// Check request body size.
	if g.maxBodySize > 0 && int64(len(req.Params)) > g.maxBodySize {
		g.logger.Warn("request exceeds size limit",
			slog.String("method", req.Method),
			slog.Int("size", len(req.Params)),
			slog.Int64("limit", g.maxBodySize),
		)
		return nil, fmt.Errorf("request body size %d exceeds limit %d", len(req.Params), g.maxBodySize)
	}

	// Brute force check (keyed by method as proxy for source identity).
	if g.bruteForce != nil {
		key := req.Method
		if g.bruteForce.isBlocked(key) {
			g.logger.Warn("temporarily blocked by brute force protection",
				slog.String("method", req.Method),
			)
			if g.onReject != nil {
				g.onReject()
			}
			return nil, fmt.Errorf("temporarily blocked due to repeated failures for %q", req.Method)
		}
	}

	// Rate limiting keyed by method, with reputation-based adjustment.
	if g.limiter != nil {
		key := req.Method
		limit := g.limiter.maxRequests

		if g.reputation != nil {
			if ar := GetAuthResult(ctx); ar != nil && ar.AgentIDHash != "" {
				multiplier := g.reputation.GetRateMultiplier(ctx, ar.AgentIDHash)
				if multiplier == 0 {
					g.logger.Warn("blocked by reputation",
						slog.String("agent_id_hash", ar.AgentIDHash),
						slog.String("method", req.Method),
					)
					if g.onReject != nil {
						g.onReject()
					}
					return nil, fmt.Errorf("blocked by reputation system")
				}
				limit = int(float64(limit) * multiplier)
				if limit < 1 {
					limit = 1
				}
			}
		}

		if !g.limiter.allowWithLimit(key, limit) {
			g.logger.Warn("rate limit exceeded",
				slog.String("method", req.Method),
			)
			if g.onReject != nil {
				g.onReject()
			}
			return nil, fmt.Errorf("rate limit exceeded for %q", req.Method)
		}
	}

	return req, nil
}

// SetReputation sets the reputation provider for dynamic rate limiting.
func (g *GuardMiddleware) SetReputation(p reputation.Provider) {
	g.reputation = p
}

// RecordFailure records a failed request for brute force tracking.
func (g *GuardMiddleware) RecordFailure(key string) {
	if g.bruteForce != nil {
		g.bruteForce.recordFailure(key)
	}
}

// CheckIPAccess checks if an IP is allowed/blocked by the guard's IP lists.
// Returns an error if the IP is blocked.
func (g *GuardMiddleware) CheckIPAccess(ipStr string) error {
	if len(g.blocklist) == 0 && len(g.allowlist) == 0 {
		return nil
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil // unparseable IPs are allowed through (e.g. unix sockets)
	}

	// Check blocklist first.
	for _, blocked := range g.blocklist {
		if blocked.Contains(ip) {
			return fmt.Errorf("IP %s is blocked", ipStr)
		}
	}

	// If allowlist is set, IP must be in it.
	if len(g.allowlist) > 0 {
		for _, allowed := range g.allowlist {
			if allowed.Contains(ip) {
				return nil
			}
		}
		return fmt.Errorf("IP %s is not in allowlist", ipStr)
	}

	return nil
}

// rateLimiter implements a fixed-window rate limiter.
type rateLimiter struct {
	maxRequests int
	window      time.Duration
	mu          sync.Mutex
	buckets     map[string]*bucket
}

type bucket struct {
	count       int
	windowStart time.Time
}

func newRateLimiter(maxRequests int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		maxRequests: maxRequests,
		window:      window,
		buckets:     make(map[string]*bucket),
	}
	// Periodic cleanup of stale buckets
	go func() {
		ticker := time.NewTicker(window)
		defer ticker.Stop()
		for range ticker.C {
			rl.cleanup()
		}
	}()
	return rl
}

func (rl *rateLimiter) allow(key string) bool {
	return rl.allowWithLimit(key, rl.maxRequests)
}

// allowWithLimit checks the rate limit using a dynamic limit value.
func (rl *rateLimiter) allowWithLimit(key string, limit int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok || now.Sub(b.windowStart) >= rl.window {
		rl.buckets[key] = &bucket{count: 1, windowStart: now}
		return true
	}

	if b.count >= limit {
		return false
	}
	b.count++
	return true
}

func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for key, b := range rl.buckets {
		if now.Sub(b.windowStart) >= rl.window*2 {
			delete(rl.buckets, key)
		}
	}
}

// bruteForceTracker tracks consecutive failures and temporarily blocks sources.
type bruteForceTracker struct {
	maxFails int
	window   time.Duration
	blockDur time.Duration
	mu       sync.Mutex
	failures map[string]*failRecord
}

type failRecord struct {
	count     int
	firstFail time.Time
	blockedAt time.Time // zero if not blocked
}

func newBruteForceTracker(maxFails int, window, blockDur time.Duration) *bruteForceTracker {
	bf := &bruteForceTracker{
		maxFails: maxFails,
		window:   window,
		blockDur: blockDur,
		failures: make(map[string]*failRecord),
	}
	go func() {
		ticker := time.NewTicker(window)
		defer ticker.Stop()
		for range ticker.C {
			bf.cleanup()
		}
	}()
	return bf
}

func (bf *bruteForceTracker) recordFailure(key string) {
	bf.mu.Lock()
	defer bf.mu.Unlock()

	now := time.Now()
	rec, ok := bf.failures[key]
	if !ok || now.Sub(rec.firstFail) >= bf.window {
		bf.failures[key] = &failRecord{count: 1, firstFail: now}
		return
	}
	rec.count++
	if rec.count >= bf.maxFails && rec.blockedAt.IsZero() {
		rec.blockedAt = now
	}
}

func (bf *bruteForceTracker) isBlocked(key string) bool {
	bf.mu.Lock()
	defer bf.mu.Unlock()

	rec, ok := bf.failures[key]
	if !ok {
		return false
	}
	if rec.blockedAt.IsZero() {
		return false
	}
	if time.Since(rec.blockedAt) >= bf.blockDur {
		delete(bf.failures, key)
		return false
	}
	return true
}

func (bf *bruteForceTracker) cleanup() {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	now := time.Now()
	for key, rec := range bf.failures {
		if !rec.blockedAt.IsZero() && now.Sub(rec.blockedAt) >= bf.blockDur {
			delete(bf.failures, key)
		} else if rec.blockedAt.IsZero() && now.Sub(rec.firstFail) >= bf.window*2 {
			delete(bf.failures, key)
		}
	}
}
