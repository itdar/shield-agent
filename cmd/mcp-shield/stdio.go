package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"rua/internal/auth"
	"rua/internal/middleware"
	"rua/internal/monitor"
	"rua/internal/process"
	"rua/internal/storage"
	"rua/internal/telemetry"
)

// buildRootCmd constructs the root cobra command (stdio mode).
func buildRootCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp-shield [flags] <command> [args...]",
		Short: "MCP security middleware — wraps an MCP server process",
		Long: `mcp-shield transparently wraps an MCP server process, intercepting
JSON-RPC messages for authentication, logging, and telemetry.

Example:
  mcp-shield python server.py
  mcp-shield --verbose node server.js --port 8080`,

		Args: cobra.ArbitraryArgs,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runWrapper(cmd.Context(), flags, args)
		},

		SilenceUsage: true,
	}

	// Persistent (global) flags — available to all sub-commands.
	pf := cmd.PersistentFlags()
	pf.StringVar(&flags.configFile, "config", "mcp-shield.yaml", "config file path")
	pf.StringVar(&flags.logLevel, "log-level", "", "log level: debug/info/warn/error")
	pf.BoolVar(&flags.telemetry, "telemetry", false, "enable anonymous telemetry")
	pf.BoolVar(&flags.verbose, "verbose", false, "verbose output (alias for --log-level debug)")
	pf.StringVar(&flags.monitorAddr, "monitor-addr", "", "monitoring HTTP listen address (e.g. 127.0.0.1:9090)")

	return cmd
}

// runWrapper is the main execution path for stdio mode:
// load config, init logger, run child process with middleware.
func runWrapper(ctx context.Context, flags *globalFlags, childArgs []string) error {
	cfg, logger, err := initFromFlags(flags)
	if err != nil {
		return err
	}

	if len(childArgs) == 0 {
		return errors.New("no child command specified — usage: mcp-shield <command> [args...]")
	}

	printBanner(cfg.Security.Mode, cfg.Server.MonitorAddr, "stdio")
	logger.Info("starting mcp-shield",
		"mode", cfg.Security.Mode,
		"monitor_addr", cfg.Server.MonitorAddr,
		"child", childArgs,
	)

	// 1. Initialize DB.
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	// 2. Purge old logs.
	if n, err := db.Purge(cfg.Storage.RetentionDays); err != nil {
		logger.Warn("purge failed", slog.String("error", err.Error()))
	} else if n > 0 {
		logger.Info("purged old log entries", slog.Int64("count", n))
	}

	// 3. Initialize Prometheus metrics.
	metrics := monitor.NewMetrics(monitor.DefaultRegisterer())

	// 4. Create auth KeyStore.
	fileStore, err := auth.NewFileKeyStore(cfg.Security.KeyStorePath)
	if err != nil {
		return fmt.Errorf("loading key store: %w", err)
	}
	cachedStore := auth.NewCachedKeyStore(fileStore, 5*time.Minute)

	// 5. Create AuthMiddleware.
	authMW := middleware.NewAuthMiddleware(cachedStore, cfg.Security.Mode, logger, func(status string) {
		metrics.AuthTotal.WithLabelValues(status).Inc()
	})

	// 6. Create TelemetryCollector.
	telCol := telemetry.New(
		cfg.Telemetry.Enabled,
		cfg.Telemetry.Endpoint,
		cfg.Telemetry.BatchInterval,
		cfg.Telemetry.Epsilon,
		"", // salt — could be loaded from config in future
		logger,
	)

	// 7. Create LogMiddleware.
	logMW := middleware.NewLogMiddleware(db, logger, telCol)
	defer logMW.Close()

	// 8. Build middleware chain (Auth → Log).
	chain := middleware.NewChain(authMW, logMW)

	// 9. Create and start monitor server.
	monSrv := monitor.New(cfg.Server.MonitorAddr, metrics, logger)
	monSrv.Start()

	// 10. Start telemetry in background.
	telCtx, telCancel := context.WithCancel(ctx)
	defer telCancel()
	go telCol.Run(telCtx)

	// 11. Run child process with middleware.
	runErr := process.RunWithMiddleware(ctx, childArgs, logger, chain, metrics, monSrv)

	// 12. Shutdown.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	monSrv.Shutdown(shutCtx)
	telCancel()

	return runErr
}
