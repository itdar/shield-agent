package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/itdar/shield-agent/internal/compliance"
	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/egress"
	"github.com/itdar/shield-agent/internal/logging"
	"github.com/itdar/shield-agent/internal/monitor"
	"github.com/itdar/shield-agent/internal/storage"
)

// buildEgressCmd builds the `shield-agent egress` stand-alone sub-command.
// It shares the same startEgressListener function as proxy/stdio's
// --with-egress flag, so behaviour is identical across entry points.
func buildEgressCmd(flags *globalFlags) *cobra.Command {
	var listen string
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Egress forward-proxy mode: record and verify outbound AI traffic",
		Long: `Runs shield-agent as a forward HTTP proxy on the egress path.
Configure agents with HTTPS_PROXY=http://<listen>/ to route outbound calls
through shield-agent for metadata logging and policy enforcement.

Phase 1 performs CONNECT tunneling with metadata-only capture (destination,
timing, byte counts). TLS bodies are never decrypted. Phase 2 adds
per-host TLS MITM, PII scrubbing, and content tagging — enabled via
egress.mitm_hosts in the config file.

Example:
  shield-agent egress --listen :8889 \
    --config shield-agent.yaml`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runEgressStandalone(cmd.Context(), flags, listen)
		},
		SilenceUsage: true,
	}
	cmd.Flags().StringVar(&listen, "listen", "", "listen address (overrides egress.listen in config)")
	return cmd
}

// runEgressStandalone runs shield-agent with only the egress listener.
// Use this when you want a dedicated egress process with no ingress.
func runEgressStandalone(ctx context.Context, flags *globalFlags, listenOverride string) error {
	cfg, logger, err := initFromFlags(flags)
	if err != nil {
		return err
	}

	// Enable egress regardless of config.egress.enabled — the user
	// explicitly invoked the egress subcommand.
	if listenOverride != "" {
		cfg.Egress.Listen = listenOverride
	}
	if cfg.Egress.Listen == "" {
		cfg.Egress.Listen = ":8889"
	}
	cfg.Egress.Enabled = true

	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	// Retention sweep (metadata rows are cheap but still bound by policy).
	retention := cfg.Egress.RetentionDays
	if retention == 0 {
		retention = cfg.Storage.RetentionDays
	}
	if n, err := db.PurgeEgress(retention); err == nil && n > 0 {
		logger.Info("purged old egress log entries", "count", n)
	}

	// Standalone egress mode gets its own Prometheus metrics registerer so
	// it can expose /metrics without conflicting with a running proxy.
	metrics := monitor.NewMetrics(monitor.DefaultRegisterer())

	// Monitor server (exposes /metrics and /healthz) so operators can
	// observe the egress proxy without wiring up an ingress path.
	monSrv := monitor.New(cfg.Server.MonitorAddr, metrics, logger)
	monSrv.Start()

	stopper, err := startEgressListenerWithMetrics(ctx, db, cfg, logger, flags, metrics)
	if err != nil {
		monSrv.Shutdown(context.Background())
		return err
	}
	printBanner(cfg.Security.Mode, cfg.Server.MonitorAddr, "egress")
	logger.Info("egress proxy listening", "addr", cfg.Egress.Listen)

	<-ctx.Done()
	stopper()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	monSrv.Shutdown(shutCtx)
	return nil
}

// egressListener bundles the running egress HTTP server and the shared
// resources (log writer, hash chain, chain). Callers hold the returned
// shutdown function and invoke it to drain the pipeline cleanly.
type egressListener struct {
	Server    *http.Server
	Chain     *egress.SwappableEgressChain
	LogWriter *compliance.LogWriter
	HashChain *compliance.HashChain
	logger    *slog.Logger
	sighupCh  chan os.Signal
	closeChain func()
}

// startEgressListener builds the egress chain + proxy and kicks off the
// HTTP listener. Returns a shutdown function the caller invokes during
// graceful shutdown. Safe to call alongside an ingress listener — both
// share the same *storage.DB thanks to SetMaxOpenConns(1) serialisation.
func startEgressListener(ctx context.Context, db *storage.DB, cfg config.Config, parentLogger *slog.Logger, flags *globalFlags) (func(), error) {
	return startEgressListenerWithMetrics(ctx, db, cfg, parentLogger, flags, nil)
}

// startEgressListenerWithMetrics is the same as startEgressListener but
// allows callers to inject a shared Prometheus registry (used by the
// proxy command when --with-egress is set so ingress and egress share
// one /metrics endpoint).
func startEgressListenerWithMetrics(ctx context.Context, db *storage.DB, cfg config.Config, parentLogger *slog.Logger, flags *globalFlags, metrics *monitor.Metrics) (func(), error) {
	logger := logging.WithComponent(parentLogger, "egress")

	hashChain, err := compliance.NewHashChain(db)
	if err != nil {
		return nil, fmt.Errorf("hash chain init: %w", err)
	}
	writerOpts := compliance.LogWriterOptions{BufferSize: 512}
	if metrics != nil {
		writerOpts.Metrics = metrics
	}
	writer := compliance.NewLogWriter(db, logger, writerOpts)

	buildDeps := func(c config.Config) egress.EgressDependencies {
		d := egress.EgressDependencies{
			DB:        db,
			Logger:    logger,
			Cfg:       c.Egress,
			LogWriter: writer,
			HashChain: hashChain,
		}
		if metrics != nil {
			d.Metrics = metrics
		}
		return d
	}
	deps := buildDeps(cfg)
	chain, closeChain, err := egress.BuildEgressChain(cfg.Egress.Middlewares, deps)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("building egress chain: %w", err)
	}
	swappable := egress.NewSwappableEgressChain(chain)

	proxy := egress.NewProxy(swappable, logger, deps)
	// Test-only escape hatch for MITM E2E against httptest TLS upstreams.
	// Never set in production deployments.
	if os.Getenv("SHIELD_AGENT_TEST_UPSTREAM_SKIP_VERIFY") == "1" {
		proxy.UpstreamTLSSkipVerify = true
	}

	// External daily digest (Phase 2 defense-in-depth). Writer runs in
	// the background until the stopper fires.
	var digestWriter *compliance.DigestWriter
	if path := cfg.Egress.HashChain.DigestPath; path != "" {
		interval := time.Duration(cfg.Egress.HashChain.DigestIntervalHours) * time.Hour
		digestWriter = compliance.NewDigestWriter(db, hashChain, path, interval, logger)
		go digestWriter.Run()
	}

	// MITM path (Phase 2). When cfg.Egress.MITMHosts is non-empty we
	// load the CA and build a minter so those hosts get TLS-terminated
	// and their bodies exposed to the middleware chain.
	if len(cfg.Egress.MITMHosts) > 0 {
		ca, err := egress.LoadOrGenerate(egress.CAOptions{
			CertPath:     cfg.Egress.CACert,
			KeyPath:      cfg.Egress.CAKey,
			Generate:     cfg.Egress.CAAutoGenerate,
			ValidityDays: cfg.Egress.CAValidityDays,
		})
		if err != nil {
			writer.Close()
			return nil, fmt.Errorf("loading egress CA: %w", err)
		}
		cacheTTL := time.Duration(cfg.Egress.LeafCacheTTLMin) * time.Minute
		minter := egress.NewMITMMinter(ca, 0, cacheTTL)
		proxy.Minter = minter
		proxy.MITMHosts = map[string]struct{}{}
		for _, host := range cfg.Egress.MITMHosts {
			proxy.MITMHosts[strings.ToLower(host)] = struct{}{}
		}
		logger.Info("egress MITM enabled", "hosts", len(cfg.Egress.MITMHosts))
	}

	srv := &http.Server{
		Addr:    cfg.Egress.Listen,
		Handler: proxy,
	}

	// SIGHUP reloads the middleware chain (not the LogWriter or HashChain —
	// those are process-global audit state).
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	currentClose := closeChain
	go func() {
		for range sighupCh {
			logger.Info("received SIGHUP, reloading egress chain")
			newCfg, _, err := initFromFlags(flags)
			if err != nil {
				logger.Error("egress reload failed", "err", err)
				continue
			}
			newChain, newClose, err := egress.BuildEgressChain(newCfg.Egress.Middlewares, buildDeps(newCfg))
			if err != nil {
				logger.Error("egress rebuild failed", "err", err)
				continue
			}
			swappable.Swap(newChain)
			if currentClose != nil {
				currentClose()
			}
			currentClose = newClose
			logger.Info("egress chain reloaded")
		}
	}()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("egress server exited", "err", err)
		}
	}()

	stop := func() {
		signal.Stop(sighupCh)
		close(sighupCh)
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		if currentClose != nil {
			currentClose()
		}
		// Close the LogWriter *after* the HTTP server has drained so the
		// writer sees all final Enqueues.
		writer.Close()
		if digestWriter != nil {
			digestWriter.Stop()
		}
	}

	return stop, nil
}
