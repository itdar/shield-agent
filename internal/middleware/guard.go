package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/itdar/shield-agent/internal/jsonrpc"
)

// GuardMiddleware enforces rate limits, request size limits, and IP-based access control.
type GuardMiddleware struct {
	PassthroughMiddleware
	limiter     *rateLimiter
	maxBodySize int64
	blocklist   []net.IPNet
	allowlist   []net.IPNet
	logger      *slog.Logger
	onReject    func() // called when a request is rejected by rate limit
}

// GuardConfig holds configuration for the guard middleware.
type GuardConfig struct {
	RateLimitPerMin int      // requests per minute per method (0 = unlimited)
	MaxBodySize     int64    // max request body size in bytes (0 = unlimited)
	IPBlocklist     []string // CIDR strings to block
	IPAllowlist     []string // CIDR strings to allow (empty = allow all)
}

// NewGuardMiddleware creates a GuardMiddleware from the given config.
func NewGuardMiddleware(cfg GuardConfig, logger *slog.Logger, onReject func()) *GuardMiddleware {
	g := &GuardMiddleware{
		maxBodySize: cfg.MaxBodySize,
		logger:      logger,
		onReject:    onReject,
	}

	if cfg.RateLimitPerMin > 0 {
		g.limiter = newRateLimiter(cfg.RateLimitPerMin, time.Minute)
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

// ProcessRequest enforces size limits and rate limits on incoming requests.
func (g *GuardMiddleware) ProcessRequest(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error) {
	// Check request body size
	if g.maxBodySize > 0 && int64(len(req.Params)) > g.maxBodySize {
		g.logger.Warn("request exceeds size limit",
			slog.String("method", req.Method),
			slog.Int("size", len(req.Params)),
			slog.Int64("limit", g.maxBodySize),
		)
		return nil, fmt.Errorf("request body size %d exceeds limit %d", len(req.Params), g.maxBodySize)
	}

	// Rate limiting keyed by method
	if g.limiter != nil {
		key := req.Method
		if !g.limiter.allow(key) {
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
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok || now.Sub(b.windowStart) >= rl.window {
		rl.buckets[key] = &bucket{count: 1, windowStart: now}
		return true
	}

	if b.count >= rl.maxRequests {
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
