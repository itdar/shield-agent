package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
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
// Phase 1 is metadata-only: the Proxy never decrypts TLS. Phase 2 adds
// per-host TLS MITM via Minter + MITMHosts, so the body is exposed to
// the middleware chain for PII scrub, content tagging, and prompt
// hashing. Hosts outside the MITM set fall back to the Phase 1
// CONNECT-pipe path.
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

	// Minter is the MITM certificate factory (Phase 2). nil means MITM
	// is disabled; all CONNECTs fall through to the pipe path.
	Minter *MITMMinter
	// MITMHosts is the lowercased set of destination hostnames to MITM.
	// Empty means "MITM nothing, pipe everything".
	MITMHosts map[string]struct{}
	// UpstreamTLSSkipVerify allows tests to point at self-signed
	// upstreams. Never enable in production.
	UpstreamTLSSkipVerify bool
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
// to host:port, return "200 Connection established", then either (a)
// pipe raw bytes (Phase 1 metadata-only path) or (b) terminate TLS on
// both sides and route the decrypted stream through the middleware
// chain (Phase 2 MITM path).
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

	// Tell the client the tunnel is established before TLS / raw bytes
	// flow. Both paths need this handshake.
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		p.logger.Warn("egress write 200 failed", slog.String("err", err.Error()))
		p.afterRequest(ctx, req, &Response{ErrorDetail: "write200: " + err.Error()})
		return
	}

	if p.shouldMITM(host) {
		p.handleMITM(ctx, req, clientConn, clientBuf)
		return
	}

	upstream, err := p.Dial(ctx, "tcp", destination)
	if err != nil {
		p.logger.Warn("egress upstream dial failed",
			slog.String("destination", destination), slog.String("err", err.Error()))
		writeHTTPResponse(clientConn, http.StatusBadGateway, "bad gateway")
		p.afterRequest(ctx, req, &Response{ErrorDetail: "dial: " + err.Error()})
		return
	}
	defer upstream.Close()

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

// shouldMITM reports whether the given host is in the MITM allow set and
// the proxy has been configured with a minter.
func (p *Proxy) shouldMITM(host string) bool {
	if p.Minter == nil || len(p.MITMHosts) == 0 {
		return false
	}
	_, ok := p.MITMHosts[strings.ToLower(host)]
	return ok
}

// handleMITM terminates TLS on the client side with a freshly-minted
// leaf cert and opens an outbound TLS connection to the real upstream.
// Requests flowing in are parsed as HTTP/1.1, replayed onto the
// upstream connection (with hop-by-hop headers stripped), and the
// response is buffered back to the client. Each decrypted request/
// response pair generates one middleware round-trip with req.Protocol
// set to "mitm".
func (p *Proxy) handleMITM(ctx context.Context, req *Request, clientConn net.Conn, _ *bufio.ReadWriter) {
	mitmConfig := &tls.Config{
		GetCertificate: p.Minter.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	tlsConn := tls.Server(clientConn, mitmConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		p.logger.Warn("egress MITM handshake failed",
			slog.String("destination", req.Destination), slog.String("err", err.Error()))
		p.afterRequest(ctx, req, &Response{ErrorDetail: "mitm handshake: " + err.Error()})
		return
	}
	defer tlsConn.Close()

	// Open the real upstream TLS connection once and reuse for the
	// (likely single) request. Keep-alive to the upstream is handled by
	// p.transport when we use the stdlib client; here we stay at the
	// socket level for minimum overhead.
	upstreamTLS, err := p.dialUpstreamTLS(ctx, req.Host, req.Port)
	if err != nil {
		p.logger.Warn("egress MITM upstream dial failed",
			slog.String("destination", req.Destination), slog.String("err", err.Error()))
		p.afterRequest(ctx, req, &Response{ErrorDetail: "mitm dial: " + err.Error()})
		return
	}
	defer upstreamTLS.Close()

	// Parse client HTTP/1.1 request, forward, and stream response back.
	// If the client pipelines multiple requests we serve them one at a
	// time over the same TLS session.
	clientReader := bufio.NewReader(tlsConn)
	for {
		clientReq, err := http.ReadRequest(clientReader)
		if err != nil {
			if err != io.EOF {
				p.logger.Debug("egress MITM read client request end",
					slog.String("err", err.Error()))
			}
			return
		}

		innerReq := &Request{
			Destination:   req.Destination,
			Host:          req.Host,
			Port:          req.Port,
			Method:        clientReq.Method + " " + clientReq.URL.RequestURI(),
			Protocol:      "mitm",
			Provider:      req.Provider,
			PolicyAction:  req.PolicyAction,
			PolicyRule:    req.PolicyRule,
			CorrelationID: req.CorrelationID,
			StartedAt:     time.Now().UTC(),
			ClientRequest: clientReq,
		}
		if v := clientReq.Header.Get("X-Shield-Correlation-Id"); v != "" {
			innerReq.CorrelationID = v
		}

		body, err := io.ReadAll(clientReq.Body)
		clientReq.Body.Close()
		if err != nil {
			p.logger.Warn("egress MITM read body", slog.String("err", err.Error()))
			return
		}
		innerReq.Body = body

		updated, failed, err := p.chain.ProcessRequest(ctx, innerReq)
		if err != nil {
			// Policy rejection after decrypt — respond 403 on the TLS
			// channel and record the outcome.
			writeTLSError(tlsConn, http.StatusForbidden)
			innerReq.PolicyAction = "block"
			if failed != nil {
				innerReq.PolicyRule = failed.Name()
			}
			p.afterRequest(ctx, innerReq, &Response{StatusCode: http.StatusForbidden, ErrorDetail: err.Error()})
			return
		}
		innerReq = updated

		// Replay request on the upstream TLS socket.
		forward := clientReq.Clone(ctx)
		forward.Body = io.NopCloser(bytes.NewReader(body))
		forward.RequestURI = ""
		forward.URL.Scheme = "https"
		forward.URL.Host = req.Destination
		removeHopByHopHeaders(forward.Header)
		if err := forward.Write(upstreamTLS); err != nil {
			p.logger.Warn("egress MITM upstream write", slog.String("err", err.Error()))
			return
		}

		upstreamReader := bufio.NewReader(upstreamTLS)
		upResp, err := http.ReadResponse(upstreamReader, forward)
		if err != nil {
			p.logger.Warn("egress MITM upstream read", slog.String("err", err.Error()))
			return
		}

		respBody, err := io.ReadAll(upResp.Body)
		upResp.Body.Close()
		if err != nil {
			p.logger.Warn("egress MITM response read", slog.String("err", err.Error()))
			return
		}

		// Prepare the Response for the middleware chain (body available).
		latency := time.Since(innerReq.StartedAt)
		innerResp := &Response{
			StatusCode:   upResp.StatusCode,
			RequestSize:  int64(len(body)),
			ResponseSize: int64(len(respBody)),
			LatencyMs:    float64(latency.Microseconds()) / 1000.0,
			Headers:      upResp.Header.Clone(),
			Body:         respBody,
		}
		if _, err := p.chain.ProcessResponse(ctx, innerReq, innerResp); err != nil {
			p.logger.Warn("egress MITM ProcessResponse", slog.String("err", err.Error()))
		}

		// Replay response back to the client, honouring any header
		// mutations middleware may have done.
		upResp.Body = io.NopCloser(bytes.NewReader(innerResp.Body))
		upResp.ContentLength = int64(len(innerResp.Body))
		removeHopByHopHeaders(upResp.Header)
		if innerResp.Headers != nil {
			upResp.Header = innerResp.Headers
			removeHopByHopHeaders(upResp.Header)
		}
		if err := upResp.Write(tlsConn); err != nil {
			p.logger.Warn("egress MITM client write", slog.String("err", err.Error()))
			return
		}

		p.metrics.IncRequest(innerReq.Provider, pickAction(innerReq.PolicyAction))
		p.metrics.ObserveLatency(innerReq.Provider, innerResp.LatencyMs/1000.0)
		if innerResp.RequestSize > 0 {
			p.metrics.AddBytes("request", innerResp.RequestSize)
		}
		if innerResp.ResponseSize > 0 {
			p.metrics.AddBytes("response", innerResp.ResponseSize)
		}

		// Stop if the client asked to close.
		if clientReq.Close || !keepAliveResponse(upResp) {
			return
		}
	}
}

// dialUpstreamTLS opens a TLS connection to the real upstream, applying
// the same SSRF guard as the plaintext dialer.
func (p *Proxy) dialUpstreamTLS(ctx context.Context, host string, port int) (*tls.Conn, error) {
	if port == 0 {
		port = 443
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	raw, err := p.Dial(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: p.UpstreamTLSSkipVerify,
	}
	c := tls.Client(raw, tlsCfg)
	if err := c.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("upstream tls handshake: %w", err)
	}
	return c, nil
}

func pickAction(a string) string {
	if a == "" {
		return "allow"
	}
	return a
}

func keepAliveResponse(resp *http.Response) bool {
	if resp.Close {
		return false
	}
	if resp.ProtoMajor == 1 && resp.ProtoMinor == 0 {
		// HTTP/1.0 defaults to close.
		return strings.EqualFold(resp.Header.Get("Connection"), "keep-alive")
	}
	return !strings.EqualFold(resp.Header.Get("Connection"), "close")
}

func writeTLSError(conn net.Conn, status int) {
	fmt.Fprintf(conn,
		"HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
		status, http.StatusText(status))
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
		p.rejectHTTP(ctx, w, req, failed, err)
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
	action := req.PolicyAction
	if action == "" {
		action = "allow"
	}
	p.metrics.IncRequest(req.Provider, action)
	p.metrics.ObserveLatency(req.Provider, resp.LatencyMs/1000.0)
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
	http.Error(w, "request denied by policy", status)

	action := req.PolicyAction
	if action == "" {
		action = "block"
	}
	rule := req.PolicyRule
	if rule == "" && failed != nil {
		rule = failed.Name()
	}
	req.PolicyAction = action
	req.PolicyRule = rule
	// rejectConnect is called with a pseudo-generic error string; the
	// client-facing message stays minimal so the allowlist isn't leaked.
	p.metrics.IncRequest(req.Provider, action)
	p.metrics.IncPolicyViolation(rule, action)

	// Even for a rejected request, run ProcessResponse so the log middleware
	// persists the decision.
	_, _ = p.chain.ProcessResponse(r.Context(), req, &Response{
		StatusCode:  status,
		ErrorDetail: err.Error(),
	})
}

// rejectHTTP is the plaintext-HTTP variant of rejectConnect.
func (p *Proxy) rejectHTTP(ctx context.Context, w http.ResponseWriter, req *Request, failed EgressMiddleware, err error) {
	status := http.StatusForbidden
	if errors.Is(err, errLogWriteFailed) {
		status = http.StatusServiceUnavailable
	}
	http.Error(w, "request denied by policy", status)

	action := req.PolicyAction
	if action == "" {
		action = "block"
	}
	rule := req.PolicyRule
	if rule == "" && failed != nil {
		rule = failed.Name()
	}
	req.PolicyAction = action
	req.PolicyRule = rule
	p.metrics.IncRequest(req.Provider, action)
	p.metrics.IncPolicyViolation(rule, action)

	_, _ = p.chain.ProcessResponse(ctx, req, &Response{
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
