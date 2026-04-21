package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/middleware"
	"github.com/itdar/shield-agent/internal/monitor"
	"github.com/itdar/shield-agent/internal/reputation"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/telemetry"
	"github.com/itdar/shield-agent/internal/token"
	"github.com/itdar/shield-agent/internal/transport/proxy"
	"github.com/itdar/shield-agent/internal/webui"
)

// buildProxyCmd builds the `shield-agent proxy` sub-command.
func buildProxyCmd(flags *globalFlags) *cobra.Command {
	var (
		listenAddr    string
		upstream      string
		transportType string
		tlsCert       string
		tlsKey        string
		withEgress    bool
		egressListen  string
	)

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "HTTP proxy mode: sits between an MCP client and an upstream MCP server",
		Long: `Runs an HTTP proxy server between an MCP client (e.g. Claude Desktop) and an
upstream MCP server (e.g. fastmcp). AuthMiddleware and LogMiddleware are applied in the middle.

Supported transports:
  sse             — Server-Sent Events (GET /sse + POST /messages)
  streamable-http — Streamable HTTP (POST /mcp)

Example (local fastmcp SSE):
  shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

Example (cloud MCP Streamable HTTP):
  shield-agent proxy --listen :8888 --upstream https://mcp.example.com --transport streamable-http`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runProxy(cmd.Context(), flags, listenAddr, upstream, transportType, tlsCert, tlsKey, withEgress, egressListen)
		},
		SilenceUsage: true,
	}

	f := cmd.Flags()
	f.StringVar(&listenAddr, "listen", ":8888", "listen address (e.g. :8888 or 127.0.0.1:8888)")
	f.StringVar(&upstream, "upstream", "", "upstream MCP server base URL (e.g. http://localhost:8000); optional when upstreams config is set")
	f.StringVar(&transportType, "transport", "streamable-http", "transport type: sse or streamable-http")
	f.StringVar(&tlsCert, "tls-cert", "", "path to TLS certificate file (enables HTTPS when set with --tls-key)")
	f.StringVar(&tlsKey, "tls-key", "", "path to TLS key file (enables HTTPS when set with --tls-cert)")
	f.BoolVar(&withEgress, "with-egress", false, "also start the egress forward proxy in this process (shares DB with ingress)")
	f.StringVar(&egressListen, "egress-listen", "", "egress listen address (overrides egress.listen in config)")

	return cmd
}

// runProxy is the main execution logic for proxy mode.
func runProxy(ctx context.Context, flags *globalFlags, listenAddr, upstream, transportType, tlsCert, tlsKey string, withEgress bool, egressListen string) error {
	cfg, logger, err := initFromFlags(flags)
	if err != nil {
		return err
	}

	logger.Info("starting shield-agent proxy",
		"transport", transportType,
		"listen", listenAddr,
		"upstream", upstream,
	)

	// 1. Initialize database.
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	if n, err := db.Purge(cfg.Storage.RetentionDays); err == nil && n > 0 {
		logger.Info("purged old log entries", "count", n)
	}

	// Optional egress listener (sharing the same DB).
	// 2. Prometheus metrics.
	metrics := monitor.NewMetrics(monitor.DefaultRegisterer())

	var stopEgress func()
	if withEgress || cfg.Egress.Enabled {
		if egressListen != "" {
			cfg.Egress.Listen = egressListen
		}
		if cfg.Egress.Listen == "" {
			cfg.Egress.Listen = ":8889"
		}
		cfg.Egress.Enabled = true
		retention := cfg.Egress.RetentionDays
		if retention == 0 {
			retention = cfg.Storage.RetentionDays
		}
		if n, err := db.PurgeEgress(retention); err == nil && n > 0 {
			logger.Info("purged old egress log entries", "count", n)
		}
		stopEgress, err = startEgressListenerWithMetrics(ctx, db, cfg, logger, flags, metrics)
		if err != nil {
			return fmt.Errorf("starting egress listener: %w", err)
		}
		logger.Info("egress listener started", "addr", cfg.Egress.Listen)
	}

	// 3. Auth KeyStore (file + DB composite).
	fileStore, err := auth.NewFileKeyStore(cfg.Security.KeyStorePath)
	if err != nil {
		return fmt.Errorf("loading key store: %w", err)
	}
	dbStore := auth.NewDBKeyStore(db)
	composite := auth.NewCompositeKeyStore(fileStore, dbStore)
	cachedStore := auth.NewCachedKeyStore(composite, 5*time.Minute)

	// 4. Telemetry.
	telCol := telemetry.New(
		cfg.Telemetry.Enabled,
		cfg.Telemetry.Endpoint,
		cfg.Telemetry.BatchInterval,
		cfg.Telemetry.Epsilon,
		"",
		logger,
	)

	// 5. Token store.
	tokenStore := token.NewStore(db.Conn())

	// 5b. Apply DB-persisted middleware overrides (e.g. Web UI toggles).
	if overrides, err := db.LoadConfigPrefix("middleware_enabled_"); err == nil && len(overrides) > 0 {
		config.ApplyDBOverrides(&cfg, overrides)
		logger.Info("applied DB middleware overrides", "count", len(overrides))
	}

	// 6. Reputation provider (if enabled).
	var repProvider *reputation.LocalProvider
	if cfg.Reputation.Enabled {
		repProvider = reputation.NewLocalProvider(db.Conn(), logger, cfg.Reputation)
	}

	// 7. Build middleware chain from config.
	deps := middleware.Dependencies{
		DB:           db,
		Logger:       logger,
		Metrics:      metrics,
		KeyStore:     cachedStore,
		TelCol:       telCol,
		SecMode:      cfg.Security.Mode,
		TokenStore:   tokenStore,
		DIDBlocklist: cfg.Security.DIDBlocklist,
	}
	if repProvider != nil {
		deps.ReputationProvider = repProvider
	}
	chain, closeMiddlewares, err := middleware.BuildChain(cfg.Middlewares, deps)
	if err != nil {
		return fmt.Errorf("building middleware chain: %w", err)
	}
	defer closeMiddlewares()

	swappableChain := middleware.NewSwappableChain(chain)

	// 5a. SIGHUP handler for config reload.
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	oldCloser := closeMiddlewares
	go func() {
		for range sighupCh {
			logger.Info("received SIGHUP, reloading configuration")
			newCfg, newLogger, err := initFromFlags(flags)
			if err != nil {
				logger.Error("config reload failed", slog.String("error", err.Error()))
				continue
			}

			newFileStore, err := auth.NewFileKeyStore(newCfg.Security.KeyStorePath)
			if err != nil {
				logger.Error("key store reload failed", slog.String("error", err.Error()))
				continue
			}
			newCachedStore := auth.NewCachedKeyStore(newFileStore, 5*time.Minute)

			newDeps := middleware.Dependencies{
				DB:           db,
				Logger:       newLogger,
				Metrics:      metrics,
				KeyStore:     newCachedStore,
				TelCol:       telCol,
				SecMode:      newCfg.Security.Mode,
				DIDBlocklist: newCfg.Security.DIDBlocklist,
			}
			newChain, newCloser, err := middleware.BuildChain(newCfg.Middlewares, newDeps)
			if err != nil {
				logger.Error("middleware chain rebuild failed", slog.String("error", err.Error()))
				continue
			}

			swappableChain.Swap(newChain)
			if oldCloser != nil {
				oldCloser()
			}
			oldCloser = newCloser
			logger = newLogger
			logger.Info("configuration reloaded successfully")
		}
	}()

	// 6. Start monitoring server with Web UI.
	monSrv := monitor.New(cfg.Server.MonitorAddr, metrics, logger)
	monSrv.SetUpstreamURL(upstream)

	webuiAPI := webui.NewAPI(webui.APIConfig{
		DB:         db,
		TokenStore: tokenStore,
		Logger:     logger,
		GetConfig:  func() config.Config { return cfg },
		ToggleMW: func(name string, enabled bool) {
			config.SetMiddlewareEnabled(&cfg, name, enabled)
		},
	})
	monSrv.SetMuxSetup(func(mux *http.ServeMux) {
		webui.RegisterUI(mux, webuiAPI)
		if repProvider != nil {
			reputation.RegisterAPI(mux, reputation.NewAPI(repProvider, logger))
		}
	})

	monSrv.Start()

	// 7. Run telemetry in background.
	telCtx, telCancel := context.WithCancel(ctx)
	defer telCancel()
	go telCol.Run(telCtx)

	if repProvider != nil {
		go repProvider.RunRecalcLoop(telCtx)
	}

	// Apply CLI TLS overrides on top of config.
	if tlsCert != "" {
		cfg.Server.TLSCert = tlsCert
	}
	if tlsKey != "" {
		cfg.Server.TLSKey = tlsKey
	}

	// 8. HTTP auth dependencies for A2A / HTTP API protocol support.
	httpDeps := &proxy.HTTPAuthDeps{
		Store:      cachedStore,
		Mode:       cfg.Security.Mode,
		Logger:     logger,
		DB:         db,
		Metrics:    metrics,
		Recorder:   telCol,
		Reputation: repProvider,
	}

	// 9. Select transport handler.
	allowedOrigins := cfg.Server.CORSAllowedOrigins
	var handler http.Handler

	if len(cfg.Upstreams) > 0 {
		// Gateway mode: multi-upstream router.
		factory := func(u config.UpstreamConfig) http.Handler {
			t := u.Transport
			if t == "" {
				t = transportType
			}
			var mcpHandler http.Handler
			switch t {
			case "sse":
				mcpHandler = proxy.NewSSEProxy(u.URL, swappableChain, logger, allowedOrigins).Handler()
			default:
				mcpHandler = proxy.NewStreamableProxy(u.URL, swappableChain, logger, allowedOrigins).Handler()
			}
			return proxy.NewProtocolAwareHandler(mcpHandler, u.URL, proxy.ParseProtocolHint(u.Protocol), httpDeps, logger, allowedOrigins)
		}
		handler = proxy.NewRouter(cfg.Upstreams, factory, logger)
		logger.Info("gateway mode enabled", "upstreams", len(cfg.Upstreams))
	} else if upstream != "" {
		// Legacy single-upstream mode.
		var mcpHandler http.Handler
		switch transportType {
		case "sse":
			mcpHandler = proxy.NewSSEProxy(upstream, swappableChain, logger, allowedOrigins).Handler()
		case "streamable-http", "streamable_http", "http":
			mcpHandler = proxy.NewStreamableProxy(upstream, swappableChain, logger, allowedOrigins).Handler()
		default:
			return fmt.Errorf("unknown transport %q — use sse or streamable-http", transportType)
		}
		handler = proxy.NewProtocolAwareHandler(mcpHandler, upstream, proxy.ProtoAuto, httpDeps, logger, allowedOrigins)
	} else {
		return fmt.Errorf("either --upstream flag or upstreams config is required")
	}

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      handler,
		ReadTimeout:  0, // SSE has no timeout (long-lived connection)
		WriteTimeout: 0,
	}

	// 9. Graceful shutdown on context cancellation.
	// ingress + monitor + egress shut down in parallel, each with an
	// independent 5s timeout. Egress is stopped last so its LogWriter
	// sees all in-flight responses before flushing.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		monSrv.Shutdown(shutCtx)
		srv.Shutdown(shutCtx) //nolint:errcheck
		if stopEgress != nil {
			stopEgress()
		}
	}()

	printBanner(cfg.Security.Mode, cfg.Server.MonitorAddr, "proxy("+transportType+")")
	logger.Info("proxy listening", "addr", listenAddr, "transport", transportType)

	cert, key := cfg.Server.TLSCert, cfg.Server.TLSKey
	if cert != "" && key != "" {
		logger.Info("TLS enabled", "cert", cert)
		if err := srv.ListenAndServeTLS(cert, key); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("proxy server (TLS): %w", err)
		}
	} else {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("proxy server: %w", err)
		}
	}
	return nil
}
