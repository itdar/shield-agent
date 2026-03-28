package proxy

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/itdar/shield-agent/internal/config"
)

// Route pairs an upstream config with its HTTP handler.
type Route struct {
	Upstream config.UpstreamConfig
	Handler  http.Handler
}

// Router dispatches incoming requests to the correct upstream based on
// Host header and/or path prefix matching.
type Router struct {
	routes []Route
	logger *slog.Logger
}

// HandlerFactory builds an http.Handler for a given upstream config.
type HandlerFactory func(config.UpstreamConfig) http.Handler

// NewRouter creates a Router from upstream configs. handlerFactory is called
// once per upstream to build the transport handler (SSE or Streamable HTTP).
func NewRouter(upstreams []config.UpstreamConfig, factory HandlerFactory, logger *slog.Logger) *Router {
	routes := make([]Route, 0, len(upstreams))
	for _, u := range upstreams {
		routes = append(routes, Route{
			Upstream: u,
			Handler:  factory(u),
		})
	}
	return &Router{routes: routes, logger: logger}
}

// Match finds the best matching route for the request.
// Priority: Host+Path > Host-only > Path-only.
func (r *Router) Match(req *http.Request) *Route {
	host := req.Host
	path := req.URL.Path

	// Pass 1: Host + Path (most specific).
	for i := range r.routes {
		m := r.routes[i].Upstream.Match
		if m.Host != "" && m.PathPrefix != "" {
			if matchHost(host, m.Host) && strings.HasPrefix(path, m.PathPrefix) {
				return &r.routes[i]
			}
		}
	}

	// Pass 2: Host only.
	for i := range r.routes {
		m := r.routes[i].Upstream.Match
		if m.Host != "" && m.PathPrefix == "" {
			if matchHost(host, m.Host) {
				return &r.routes[i]
			}
		}
	}

	// Pass 3: Path only.
	for i := range r.routes {
		m := r.routes[i].Upstream.Match
		if m.Host == "" && m.PathPrefix != "" {
			if strings.HasPrefix(path, m.PathPrefix) {
				return &r.routes[i]
			}
		}
	}

	// Pass 4: Catch-all (no match criteria).
	for i := range r.routes {
		m := r.routes[i].Upstream.Match
		if m.Host == "" && m.PathPrefix == "" {
			return &r.routes[i]
		}
	}

	return nil
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	route := r.Match(req)
	if route == nil {
		r.logger.Warn("no upstream matched", slog.String("host", req.Host), slog.String("path", req.URL.Path))
		http.Error(w, "no upstream matched for this request", http.StatusBadGateway)
		return
	}

	// Strip path prefix if configured.
	if route.Upstream.Match.StripPrefix && route.Upstream.Match.PathPrefix != "" {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, route.Upstream.Match.PathPrefix)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
	}

	route.Handler.ServeHTTP(w, req)
}

// matchHost compares the request host (which may include a port) against the
// configured host pattern.
func matchHost(reqHost, pattern string) bool {
	// Strip port from request host for comparison.
	h := reqHost
	if idx := strings.LastIndex(h, ":"); idx != -1 {
		h = h[:idx]
	}
	return strings.EqualFold(h, pattern)
}
