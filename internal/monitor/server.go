package monitor

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"syscall"
	"sync/atomic"

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
	MessagesTotal      *prometheus.CounterVec
	AuthTotal          *prometheus.CounterVec
	MessageLatency     *prometheus.HistogramVec
	ChildProcessUp     prometheus.Gauge
	RateLimitRejected  *prometheus.CounterVec
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
	}

	reg.MustRegister(
		m.MessagesTotal,
		m.AuthTotal,
		m.MessageLatency,
		m.ChildProcessUp,
		m.RateLimitRejected,
	)

	return m
}

// Server is the monitoring HTTP server.
type Server struct {
	addr     string
	metrics  *Metrics
	childPID atomic.Int64
	logger   *slog.Logger
	srv      *http.Server
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

// Start launches the monitoring HTTP server in a background goroutine.
func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.Handle("/metrics", promhttp.Handler())

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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    status,
		"child_pid": pid,
	})
}
