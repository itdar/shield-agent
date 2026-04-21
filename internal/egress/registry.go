package egress

import (
	"fmt"
	"log/slog"

	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/storage"
)

// EgressDependencies holds shared resources middleware factories need.
// Unlike middleware.Dependencies (ingress), LogWriter is a hard dependency:
// every egress request must be persisted, so every chain build needs it.
//
// LogWriter and HashChain are typed as `any` to avoid an import cycle with
// the compliance package, which owns those concrete types AND needs to
// implement EgressMiddleware. Factories type-assert back to the real types.
type EgressDependencies struct {
	DB        *storage.DB
	Logger    *slog.Logger
	Cfg       config.EgressConfig
	LogWriter any
	// HashChain records the last row_hash so the chain persists across
	// middleware rebuilds (SIGHUP). It is injected separately so SIGHUP
	// can reuse the same instance.
	HashChain any
	// ProviderDetect maps Host -> provider string. Injected so tests can
	// supply a fake.
	ProviderDetect ProviderDetector
	// Metrics is the Prometheus collector. Nil-safe.
	Metrics EgressMetrics
}

// EgressMetrics is the subset of the Prometheus collector the egress
// pipeline touches. Nil implementation is allowed via NoopMetrics.
type EgressMetrics interface {
	IncRequest(provider, destination, policyAction string)
	ObserveLatency(provider, destination string, seconds float64)
	IncPolicyViolation(rule, action string)
	AddBytes(direction string, n int64)
}

// NoopMetrics implements EgressMetrics as a no-op (tests).
type NoopMetrics struct{}

// IncRequest is a no-op.
func (NoopMetrics) IncRequest(string, string, string) {}

// ObserveLatency is a no-op.
func (NoopMetrics) ObserveLatency(string, string, float64) {}

// IncPolicyViolation is a no-op.
func (NoopMetrics) IncPolicyViolation(string, string) {}

// AddBytes is a no-op.
func (NoopMetrics) AddBytes(string, int64) {}

// ProviderDetector classifies a destination host into a known provider
// name ("openai", "anthropic", etc.) or "unknown".
type ProviderDetector interface {
	Detect(host string) string
}

// MiddlewareFactory produces a named EgressMiddleware. Registering one
// makes the entry valid in config.egress.middlewares.
type MiddlewareFactory func(entry config.MiddlewareEntry, deps EgressDependencies) (EgressMiddleware, error)

// factories is the registry of middleware constructors, keyed by name.
// External packages register their middlewares via Register in init().
var factories = map[string]MiddlewareFactory{}

// Register adds a named middleware factory. It panics on duplicate
// names to surface config errors at startup.
func Register(name string, factory MiddlewareFactory) {
	if _, ok := factories[name]; ok {
		panic(fmt.Sprintf("egress middleware %q already registered", name))
	}
	factories[name] = factory
}

// ResetRegistry is used by tests to undo Register calls.
func ResetRegistry() {
	factories = map[string]MiddlewareFactory{}
}

// BuildEgressChain walks cfg.Middlewares and instantiates each enabled entry.
// Returns the chain plus a cleanup func the caller should call on shutdown
// (closes any middlewares that implement io.Closer semantically).
func BuildEgressChain(entries []config.MiddlewareEntry, deps EgressDependencies) (*EgressChain, func(), error) {
	if deps.Metrics == nil {
		deps.Metrics = NoopMetrics{}
	}
	if deps.ProviderDetect == nil {
		deps.ProviderDetect = DefaultProviderDetector()
	}

	var items []EgressMiddleware
	var closers []func()

	for _, entry := range entries {
		if entry.Enabled != nil && !*entry.Enabled {
			deps.Logger.Info("egress middleware disabled", slog.String("name", entry.Name))
			continue
		}

		factory, ok := factories[entry.Name]
		if !ok {
			for _, c := range closers {
				c()
			}
			return nil, nil, fmt.Errorf("unknown egress middleware: %q", entry.Name)
		}

		mw, err := factory(entry, deps)
		if err != nil {
			for _, c := range closers {
				c()
			}
			return nil, nil, fmt.Errorf("creating egress middleware %q: %w", entry.Name, err)
		}
		items = append(items, mw)
		if closer, ok := mw.(interface{ Close() }); ok {
			closers = append(closers, closer.Close)
		}
		deps.Logger.Info("egress middleware enabled", slog.String("name", entry.Name))
	}

	cleanup := func() {
		for _, c := range closers {
			c()
		}
	}
	return NewEgressChain(items...), cleanup, nil
}
