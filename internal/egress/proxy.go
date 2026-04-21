package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bufferSize is the size of each byte slice used by the tunnel copy loops.
// 32 KiB is a common default (net/http's io.Copy buffer) and keeps
// streaming latency low.
const bufferSize = 32 * 1024

// dialTimeout bounds how long we wait for the upstream TCP handshake.
const dialTimeout = 15 * time.Second

// Proxy is a forward HTTP proxy. It accepts plaintext HTTP requests
// (for debug/localhost use) and CONNECT requests (for HTTPS) and
// transparently shuttles bytes to the upstream destination, logging
// connection metadata through the configured EgressMiddleware chain.
//
// Phase 1 is metadata-only: the Proxy never decrypts TLS. Phase 2 will
// add a TLS MITM layer that substitutes a dynamically-signed server
// certificate for the upstream cert, but that is wired in a separate
// component (to be added alongside internal/egress/mitm.go).
type Proxy struct {
	chain   *SwappableEgressChain
	logger  *slog.Logger
	detect  ProviderDetector
	metrics EgressMetrics

	// Dial is swappable so tests can plug in a loopback dialler.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	// transport is shared across plaintext-HTTP requests to avoid
	// spawning a new connection pool per request.
	transport *http.Transport

	// AllowLoopbackDestinations opens the SSRF guard so loopback/private
	// addresses are reachable. Only used by tests.
	AllowLoopbackDestinations bool
}

// NewProxy builds a Proxy from the given chain.
func NewProxy(chain *SwappableEgressChain, logger *slog.Logger, deps EgressDependencies) *Proxy {
	p := &Proxy{
		chain:   chain,
		logger:  logger,
		detect:  deps.ProviderDetect,
		metrics: deps.Metrics,
	}
	if p.detect == nil {
		p.detect = DefaultProviderDetector()
	}
	if p.metrics == nil {
		p.metrics = NoopMetrics{}
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	// dialGuarded wraps the base dialer with an SSRF check so agents can't
	// use the proxy as a relay to internal / metadata / loopback addresses.
	// SHIELD_AGENT_TEST_ALLOW_LOOPBACK exists for local integration tests
	// that exercise the proxy against httptest loopback servers. Never set
	// this in production — it disables the SSRF guard.
	if os.Getenv("SHIELD_AGENT_TEST_ALLOW_LOOPBACK") == "1" {
		p.AllowLoopbackDestinations = true
	}
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		if !p.AllowLoopbackDestinations {
			if err := p.checkDestination(host); err != nil {
				return nil, err
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
	p.transport = &http.Transport{
		DialContext:         p.Dial,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	return p
}

// checkDestination refuses outbound connections to addresses that would
// let an agent use this proxy to reach internal services or cloud
// metadata endpoints (RFC1918, loopback, link-local, IMDSv1).
func (p *Proxy) checkDestination(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		// DNS failure — let the dialer surface the error so it's logged
		// with the real underlying cause instead of an SSRF block.
		return nil
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("destination %q resolves to restricted IP %s (SSRF guard)", host, ip)
		}
	}
	return nil
}

// ServeHTTP dispatches between CONNECT (HTTPS tunnel) and normal HTTP.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleConnect implements the HTTP CONNECT method for HTTPS tunneling.
// The client sends "CONNECT host:port HTTP/1.1"; we open a TCP socket
// to host:port, return "200 Connection established", then pipe bytes
// in both directions. We never look inside the tunnel in Phase 1.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	destination := r.Host
	host, port := splitHostPort(destination)

	req := &Request{
		Destination: destination,
		Host:        host,
		Port:        port,
		Method:      "CONNECT " + destination,
		Protocol:    "https",
		Provider:    p.detect.Detect(host),
		StartedAt:   time.Now().UTC(),
		ClientRequest: r,
	}

	ctx := r.Context()
	updated, failed, err := p.chain.ProcessRequest(ctx, req)
	if err != nil {
		p.rejectConnect(w, r, req, failed, err)
		return
	}
	req = updated

	// Hijack the client TCP conn so we can write raw bytes.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy: hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		p.logger.Error("egress hijack failed", slog.String("err", err.Error()))
		return
	}
	defer clientConn.Close()

	upstream, err := p.Dial(ctx, "tcp", destination)
	if err != nil {
		p.logger.Warn("egress upstream dial failed",
			slog.String("destination", destination), slog.String("err", err.Error()))
		writeHTTPResponse(clientConn, http.StatusBadGateway, "bad gateway")
		p.afterRequest(ctx, req, &Response{ErrorDetail: "dial: " + err.Error()})
		return
	}
	defer upstream.Close()

	// Let the client know the tunnel is up.
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		p.logger.Warn("egress write 200 failed", slog.String("err", err.Error()))
		p.afterRequest(ctx, req, &Response{ErrorDetail: "write200: " + err.Error()})
		return
	}

	// Drain any buffered client bytes into the upstream before starting
	// the raw copy loop — otherwise part of the first TLS ClientHello
	// can be lost.
	if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
		if _, err := io.CopyN(upstream, clientBuf.Reader, int64(clientBuf.Reader.Buffered())); err != nil {
			p.logger.Warn("egress drain client buffer failed", slog.String("err", err.Error()))
		}
	}

	// Pipe bytes both ways. We instrument byte counts for the audit log.
	var reqBytes, respBytes int64
	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		n, _ := copyBytes(upstream, clientConn)
		reqBytes = n
		// closing the upstream write side nudges the other direction's io.Copy to return.
		_ = closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		n, _ := copyBytes(clientConn, upstream)
		respBytes = n
		_ = closeWrite(clientConn)
	}()
	wg.Wait()

	latency := time.Since(req.StartedAt)
	resp := &Response{
		StatusCode:   0, // tunnel — no HTTP status visible
		RequestSize:  reqBytes,
		ResponseSize: respBytes,
		LatencyMs:    float64(latency.Microseconds()) / 1000.0,
	}
	p.afterRequest(ctx, req, resp)
}

// handleHTTP implements plain HTTP forward-proxy semantics. The client
// sends an absolute-URI request (RFC 7230 §5.3.2) like:
//    GET http://example.com/foo HTTP/1.1
// We strip proxy-hop headers, forward to the upstream, and stream back.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.Scheme == "" {
		http.Error(w, "proxy: absolute URI required", http.StatusBadRequest)
		return
	}
	destination := r.URL.Host
	host, port := splitHostPort(destination)
	if port == 0 {
		if r.URL.Scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}

	req := &Request{
		Destination:   fmt.Sprintf("%s:%d", host, port),
		Host:          host,
		Port:          port,
		Method:        r.Method + " " + r.URL.RequestURI(),
		Protocol:      "http",
		Provider:      p.detect.Detect(host),
		StartedAt:     time.Now().UTC(),
		ClientRequest: r,
	}

	ctx := r.Context()
	updated, failed, err := p.chain.ProcessRequest(ctx, req)
	if err != nil {
		p.rejectHTTP(w, req, failed, err)
		return
	}
	req = updated

	// Build the outbound request. Strip proxy-only headers.
	outReq := r.Clone(ctx)
	outReq.RequestURI = ""
	removeHopByHopHeaders(outReq.Header)

	client := &http.Client{
		Transport: p.transport,
		Timeout:   0, // streaming — let the caller cancel via context
	}
	upResp, err := client.Do(outReq)
	if err != nil {
		p.logger.Warn("egress upstream request failed",
			slog.String("destination", req.Destination), slog.String("err", err.Error()))
		http.Error(w, "bad gateway", http.StatusBadGateway)
		p.afterRequest(ctx, req, &Response{ErrorDetail: err.Error()})
		return
	}
	defer upResp.Body.Close()

	removeHopByHopHeaders(upResp.Header)
	for k, values := range upResp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)
	respBytes, _ := io.Copy(w, upResp.Body)

	latency := time.Since(req.StartedAt)
	resp := &Response{
		StatusCode:   upResp.StatusCode,
		RequestSize:  outReq.ContentLength,
		ResponseSize: respBytes,
		LatencyMs:    float64(latency.Microseconds()) / 1000.0,
		Headers:      upResp.Header,
	}
	p.afterRequest(ctx, req, resp)
}

// afterRequest drives the ProcessResponse half of the chain and updates
// metrics. Errors inside ProcessResponse never surface to the client —
// by the time we call this, the client has already received its bytes.
// If the log writer fails (fail-closed block mode), the policy middleware
// is responsible for rejecting the request *before* we get here.
func (p *Proxy) afterRequest(ctx context.Context, req *Request, resp *Response) {
	if _, err := p.chain.ProcessResponse(ctx, req, resp); err != nil {
		p.logger.Warn("egress ProcessResponse error",
			slog.String("destination", req.Destination), slog.String("err", err.Error()))
	}
	p.metrics.IncRequest(req.Provider, req.Host, "allow")
	p.metrics.ObserveLatency(req.Provider, req.Host, resp.LatencyMs/1000.0)
	if resp.RequestSize > 0 {
		p.metrics.AddBytes("request", resp.RequestSize)
	}
	if resp.ResponseSize > 0 {
		p.metrics.AddBytes("response", resp.ResponseSize)
	}
}

// rejectConnect rejects a CONNECT request that was denied by a middleware
// (typically policy). It writes an HTTP/1.1 error response inline (since
// we haven't hijacked yet) and records the outcome.
func (p *Proxy) rejectConnect(w http.ResponseWriter, r *http.Request, req *Request, failed EgressMiddleware, err error) {
	status := http.StatusForbidden
	if errors.Is(err, errLogWriteFailed) {
		status = http.StatusServiceUnavailable
	}
	http.Error(w, err.Error(), status)

	action := "block"
	rule := ""
	if failed != nil {
		rule = failed.Name()
	}
	p.metrics.IncRequest(req.Provider, req.Host, action)
	p.metrics.IncPolicyViolation(rule, action)

	// Even for a rejected request, run ProcessResponse so the log middleware
	// persists the decision.
	_, _ = p.chain.ProcessResponse(r.Context(), req, &Response{
		StatusCode:  status,
		ErrorDetail: err.Error(),
	})
}

// rejectHTTP is the plaintext-HTTP variant of rejectConnect.
func (p *Proxy) rejectHTTP(w http.ResponseWriter, req *Request, failed EgressMiddleware, err error) {
	status := http.StatusForbidden
	if errors.Is(err, errLogWriteFailed) {
		status = http.StatusServiceUnavailable
	}
	http.Error(w, err.Error(), status)

	action := "block"
	rule := ""
	if failed != nil {
		rule = failed.Name()
	}
	p.metrics.IncRequest(req.Provider, req.Host, action)
	p.metrics.IncPolicyViolation(rule, action)

	_, _ = p.chain.ProcessResponse(context.Background(), req, &Response{
		StatusCode:  status,
		ErrorDetail: err.Error(),
	})
}

// errLogWriteFailed is returned by middlewares whose DB write failed in
// policy_mode=block. Handlers translate it to HTTP 503 (fail-closed).
var errLogWriteFailed = errors.New("compliance log write failed")

// ErrLogWriteFailed exposes errLogWriteFailed to compliance middleware
// packages so they can return it from ProcessResponse.
var ErrLogWriteFailed = errLogWriteFailed

// splitHostPort returns (host, port) from a "host:port" string. If the
// input has no port, port is 0.
func splitHostPort(s string) (string, int) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s, 0
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// removeHopByHopHeaders strips connection-scoped headers that must not
// be forwarded across the proxy (RFC 7230 §6.1).
func removeHopByHopHeaders(h http.Header) {
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
	// Connection: close may list additional headers to drop.
	for _, extra := range h.Values("Connection") {
		for _, name := range strings.Split(extra, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				h.Del(name)
			}
		}
	}
}

var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
	"Keep-Alive",
}

// copyBytes is io.Copy with a reusable buffer sized for streaming.
func copyBytes(dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, bufferSize)
	return io.CopyBuffer(dst, src, buf)
}

// closeWrite closes the write half of a TCP connection when possible.
// Used to signal EOF on half-open copies.
func closeWrite(c net.Conn) error {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := c.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

// writeHTTPResponse writes a minimal HTTP/1.1 status response to a raw
// TCP conn (used after hijack when we need to signal an error).
func writeHTTPResponse(c net.Conn, status int, msg string) {
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, msg)
}
