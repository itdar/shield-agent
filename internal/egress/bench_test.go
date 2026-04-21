package egress

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

// BenchmarkPlaintextProxy measures per-request overhead the egress proxy
// adds on top of a direct HTTP hop. The benchmark loop issues a warm-cache
// GET to a local httptest upstream through the proxy; AllowLoopbackDestinations
// bypasses the SSRF guard (this is a benchmark, not production).
//
// Reported ns/op divided by 1e6 is mean request latency in ms. For a
// proper p99 measurement, run scripts/bench-egress.sh with `hey`.
func BenchmarkPlaintextProxy(b *testing.B) {
	os.Setenv("SHIELD_AGENT_TEST_ALLOW_LOOPBACK", "1")
	defer os.Unsetenv("SHIELD_AGENT_TEST_ALLOW_LOOPBACK")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	chain := NewSwappableEgressChain(NewEgressChain())
	deps := EgressDependencies{ProviderDetect: DefaultProviderDetector(), Metrics: NoopMetrics{}}
	proxy := NewProxy(chain, silentBenchLogger(), deps)

	proxySrv := httptest.NewServer(proxy)
	defer proxySrv.Close()

	proxyURL, _ := url.Parse(proxySrv.URL)
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}

	// Warmup — prime the upstream idle-conn pool.
	for i := 0; i < 5; i++ {
		resp, err := client.Get(upstream.URL + "/")
		if err != nil {
			b.Fatalf("warmup: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(upstream.URL + "/")
		if err != nil {
			b.Fatalf("iter %d: %v", i, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("iter %d: status %d", i, resp.StatusCode)
		}
	}
}

// BenchmarkChainTraversal isolates the middleware pipeline cost: no
// network, no upstream — just ProcessRequest → ProcessResponse on a
// short passthrough chain.
func BenchmarkChainTraversal(b *testing.B) {
	chain := NewEgressChain(PassthroughEgressMiddleware{}, PassthroughEgressMiddleware{})
	ctx := context.Background()
	req := &Request{
		Destination: "api.openai.com:443",
		Host:        "api.openai.com",
		Provider:    "openai",
		StartedAt:   time.Now(),
	}
	resp := &Response{StatusCode: 200, LatencyMs: 42}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _ = chain.ProcessRequest(ctx, req)
		_, _ = chain.ProcessResponse(ctx, req, resp)
	}
}

func silentBenchLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
