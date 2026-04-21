package monitor

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DefaultRegisterer returns the default Prometheus registerer.
// Exposed as a function so callers don't need to import prometheus directly.
func DefaultRegisterer() prometheus.Registerer {
	return prometheus.DefaultRegisterer
}

// Metrics holds all Prometheus metric collectors.
type Metrics struct {
	MessagesTotal     *prometheus.CounterVec
	AuthTotal         *prometheus.CounterVec
	MessageLatency    *prometheus.HistogramVec
	ChildProcessUp    prometheus.Gauge
	RateLimitRejected *prometheus.CounterVec

	// Egress (Phase 1). Satisfies egress.EgressMetrics via method receivers below.
	EgressRequests          *prometheus.CounterVec
	EgressLatency           *prometheus.HistogramVec
	EgressPolicyViolations  *prometheus.CounterVec
	EgressBytes             *prometheus.CounterVec
	EgressLogWriteErrors    prometheus.Counter
	EgressLogQueueLength    prometheus.Gauge
	EgressHashchainLength   prometheus.Gauge
}

// NewMetrics creates and registers all metrics with the default registerer.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		MessagesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shield_agent_messages_total",
			Help: "Total number of JSON-RPC messages processed.",
		}, []string{"direction", "method"}),

		AuthTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shield_agent_auth_total",
			Help: "Total number of authentication events.",
		}, []string{"status"}),

		MessageLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "shield_agent_message_latency_seconds",
			Help:    "Latency of JSON-RPC message processing.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),

		ChildProcessUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "shield_agent_child_process_up",
			Help: "1 if the child process is running, 0 otherwise.",
		}),

		RateLimitRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shield_agent_rate_limit_rejected_total",
			Help: "Total number of requests rejected by rate limiting.",
		}, []string{"method"}),

		// destination is intentionally NOT a label — cardinality could
		// explode with attacker-controlled hostnames. Use provider
		// (a small, well-known set) instead.
		EgressRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shield_agent_egress_requests_total",
			Help: "Total number of egress requests processed.",
		}, []string{"provider", "policy_action"}),

		EgressLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "shield_agent_egress_latency_seconds",
			Help:    "Egress request round-trip time.",
			Buckets: prometheus.DefBuckets,
		}, []string{"provider"}),

		EgressPolicyViolations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shield_agent_egress_policy_violations_total",
			Help: "Total number of egress policy violations (block or warn).",
		}, []string{"rule", "action"}),

		EgressBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shield_agent_egress_bytes_total",
			Help: "Bytes forwarded via the egress proxy.",
		}, []string{"direction"}),

		EgressLogWriteErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "shield_agent_egress_log_write_errors_total",
			Help: "Egress compliance log writes that failed after retries.",
		}),

		EgressLogQueueLength: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "shield_agent_egress_log_queue_length",
			Help: "Current queue depth of the egress compliance log writer.",
		}),

		EgressHashchainLength: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "shield_agent_egress_hashchain_length",
			Help: "Rows currently present in the egress hash chain.",
		}),
	}

	reg.MustRegister(
		m.MessagesTotal,
		m.AuthTotal,
		m.MessageLatency,
		m.ChildProcessUp,
		m.RateLimitRejected,
		m.EgressRequests,
		m.EgressLatency,
		m.EgressPolicyViolations,
		m.EgressBytes,
		m.EgressLogWriteErrors,
		m.EgressLogQueueLength,
		m.EgressHashchainLength,
	)

	return m
}

// IncRequest implements egress.EgressMetrics.
func (m *Metrics) IncRequest(provider, policyAction string) {
	m.EgressRequests.WithLabelValues(provider, policyAction).Inc()
}

// ObserveLatency implements egress.EgressMetrics.
func (m *Metrics) ObserveLatency(provider string, seconds float64) {
	m.EgressLatency.WithLabelValues(provider).Observe(seconds)
}

// IncPolicyViolation implements egress.EgressMetrics.
func (m *Metrics) IncPolicyViolation(rule, action string) {
	m.EgressPolicyViolations.WithLabelValues(rule, action).Inc()
}

// AddBytes implements egress.EgressMetrics.
func (m *Metrics) AddBytes(direction string, n int64) {
	m.EgressBytes.WithLabelValues(direction).Add(float64(n))
}

// IncLogWriteError implements compliance.WriterMetrics.
func (m *Metrics) IncLogWriteError() {
	m.EgressLogWriteErrors.Inc()
}

// ObserveQueueLength implements compliance.WriterMetrics.
func (m *Metrics) ObserveQueueLength(n int) {
	m.EgressLogQueueLength.Set(float64(n))
}

// Server is the monitoring HTTP server.
type Server struct {
	addr        string
	metrics     *Metrics
	childPID    atomic.Int64
	upstreamURL atomic.Value // stores string; empty means no upstream check
	logger      *slog.Logger
	srv         *http.Server
	setupMux    func(mux *http.ServeMux) // optional: register extra routes before starting
}

// New creates a new monitoring Server.
func New(addr string, metrics *Metrics, logger *slog.Logger) *Server {
	return &Server{
		addr:    addr,
		metrics: metrics,
		logger:  logger,
	}
}

// SetChildPID records the child process PID for health checks.
func (s *Server) SetChildPID(pid int) {
	s.childPID.Store(int64(pid))
}

// SetUpstreamURL sets the upstream URL to probe during health checks (proxy mode).
// An empty string disables upstream probing.
func (s *Server) SetUpstreamURL(url string) {
	s.upstreamURL.Store(url)
}

// SetMuxSetup registers a callback to add extra routes before the server starts.
func (s *Server) SetMuxSetup(fn func(mux *http.ServeMux)) {
	s.setupMux = fn
}

// Start launches the monitoring HTTP server in a background goroutine.
func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.Handle("/metrics", promhttp.Handler())

	if s.setupMux != nil {
		s.setupMux(mux)
	}

	s.srv = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	go func() {
		s.logger.Info("monitor server starting", slog.String("addr", s.addr))
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("monitor server error", slog.String("error", err.Error()))
		}
	}()
}

// Shutdown gracefully stops the monitoring server.
func (s *Server) Shutdown(ctx context.Context) {
	if s.srv == nil {
		return
	}
	if err := s.srv.Shutdown(ctx); err != nil {
		s.logger.Warn("monitor server shutdown error", slog.String("error", err.Error()))
	}
}

// handleRoot returns a simple index page listing available endpoints.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"service":   "shield-agent",
		"endpoints": []string{"/healthz", "/metrics"},
	})
}

// handleHealthz returns a JSON health status.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	pid := int(s.childPID.Load())
	alive := false
	if pid > 0 {
		proc, err := os.FindProcess(pid)
		if err == nil {
			if err2 := proc.Signal(syscall.Signal(0)); err2 == nil {
				alive = true
			}
		}
	}

	status := "healthy"
	if pid > 0 && !alive {
		status = "degraded"
	}

	if upVal := s.upstreamURL.Load(); upVal != nil {
		if upURL, _ := upVal.(string); upURL != "" {
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(upURL)
			if err != nil || resp.StatusCode >= 500 {
				status = "degraded"
			}
			if resp != nil {
				resp.Body.Close()
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    status,
		"child_pid": pid,
	})
}
