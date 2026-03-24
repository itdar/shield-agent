package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/middleware"
	"github.com/itdar/shield-agent/internal/monitor"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/telemetry"
	"github.com/itdar/shield-agent/internal/transport/proxy"
)

// buildProxyCmd builds the `shield-agent proxy` sub-command.
func buildProxyCmd(flags *globalFlags) *cobra.Command {
	var (
		listenAddr    string
		upstream      string
		transportType string
		tlsCert       string
		tlsKey        string
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
			return runProxy(cmd.Context(), flags, listenAddr, upstream, transportType, tlsCert, tlsKey)
		},
		SilenceUsage: true,
	}

	f := cmd.Flags()
	f.StringVar(&listenAddr, "listen", ":8888", "listen address (e.g. :8888 or 127.0.0.1:8888)")
	f.StringVar(&upstream, "upstream", "", "upstream MCP server base URL (required, e.g. http://localhost:8000)")
	f.StringVar(&transportType, "transport", "streamable-http", "transport type: sse or streamable-http")
	f.StringVar(&tlsCert, "tls-cert", "", "path to TLS certificate file (enables HTTPS when set with --tls-key)")
	f.StringVar(&tlsKey, "tls-key", "", "path to TLS key file (enables HTTPS when set with --tls-cert)")
	_ = cmd.MarkFlagRequired("upstream")

	return cmd
}

// runProxy is the main execution logic for proxy mode.
func runProxy(ctx context.Context, flags *globalFlags, listenAddr, upstream, transportType, tlsCert, tlsKey string) error {
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

	// 2. Prometheus metrics.
	metrics := monitor.NewMetrics(monitor.DefaultRegisterer())

	// 3. Auth KeyStore.
	fileStore, err := auth.NewFileKeyStore(cfg.Security.KeyStorePath)
	if err != nil {
		return fmt.Errorf("loading key store: %w", err)
	}
	cachedStore := auth.NewCachedKeyStore(fileStore, 5*time.Minute)

	// 4. Telemetry.
	telCol := telemetry.New(
		cfg.Telemetry.Enabled,
		cfg.Telemetry.Endpoint,
		cfg.Telemetry.BatchInterval,
		cfg.Telemetry.Epsilon,
		"",
		logger,
	)

	// 5. Build middleware chain from config.
	deps := middleware.Dependencies{
		DB:       db,
		Logger:   logger,
		Metrics:  metrics,
		KeyStore: cachedStore,
		TelCol:   telCol,
		SecMode:  cfg.Security.Mode,
	}
	chain, closeMiddlewares, err := middleware.BuildChain(cfg.Middlewares, deps)
	if err != nil {
		return fmt.Errorf("building middleware chain: %w", err)
	}
	defer closeMiddlewares()

	// 6. Start monitoring server.
	monSrv := monitor.New(cfg.Server.MonitorAddr, metrics, logger)
	monSrv.Start()

	// 7. Run telemetry in background.
	telCtx, telCancel := context.WithCancel(ctx)
	defer telCancel()
	go telCol.Run(telCtx)

	// Apply CLI TLS overrides on top of config.
	if tlsCert != "" {
		cfg.Server.TLSCert = tlsCert
	}
	if tlsKey != "" {
		cfg.Server.TLSKey = tlsKey
	}

	// 8. Select transport handler.
	allowedOrigins := cfg.Server.CORSAllowedOrigins
	var handler http.Handler
	switch transportType {
	case "sse":
		handler = proxy.NewSSEProxy(upstream, chain, logger, allowedOrigins).Handler()
	case "streamable-http", "streamable_http", "http":
		handler = proxy.NewStreamableProxy(upstream, chain, logger, allowedOrigins).Handler()
	default:
		return fmt.Errorf("unknown transport %q — use sse or streamable-http", transportType)
	}

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      handler,
		ReadTimeout:  0, // SSE has no timeout (long-lived connection)
		WriteTimeout: 0,
	}

	// 9. Graceful shutdown on context cancellation.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		monSrv.Shutdown(shutCtx)
		srv.Shutdown(shutCtx) //nolint:errcheck
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
