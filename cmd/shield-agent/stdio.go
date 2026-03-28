package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/middleware"
	"github.com/itdar/shield-agent/internal/monitor"
	"github.com/itdar/shield-agent/internal/process"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/telemetry"
)

// buildRootCmd constructs the root cobra command (stdio mode).
func buildRootCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shield-agent [flags] <command> [args...]",
		Short: "MCP security middleware — wraps an MCP server process",
		Long: `shield-agent transparently wraps an MCP server process, intercepting
JSON-RPC messages for authentication, logging, and telemetry.

Example:
  shield-agent python server.py
  shield-agent --verbose node server.js --port 8080`,

		Args: cobra.ArbitraryArgs,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runWrapper(cmd.Context(), flags, args)
		},

		SilenceUsage: true,
	}

	// Persistent (global) flags — available to all sub-commands.
	pf := cmd.PersistentFlags()
	pf.StringVar(&flags.configFile, "config", "shield-agent.yaml", "config file path")
	pf.StringVar(&flags.logLevel, "log-level", "", "log level: debug/info/warn/error")
	pf.BoolVar(&flags.telemetry, "telemetry", false, "enable anonymous telemetry")
	pf.BoolVar(&flags.verbose, "verbose", false, "verbose output (alias for --log-level debug)")
	pf.StringVar(&flags.monitorAddr, "monitor-addr", "", "monitoring HTTP listen address (e.g. 127.0.0.1:9090)")
	pf.StringSliceVar(&flags.disableMiddlewares, "disable-middleware", nil, "disable named middleware(s) (e.g. --disable-middleware auth,log)")
	pf.StringSliceVar(&flags.enableMiddlewares, "enable-middleware", nil, "enable named middleware(s)")

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
		return errors.New("no child command specified — usage: shield-agent <command> [args...]")
	}

	printBanner(cfg.Security.Mode, cfg.Server.MonitorAddr, "stdio")
	logger.Info("starting shield-agent",
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

	// 4. Create auth KeyStore (file + DB composite).
	fileStore, err := auth.NewFileKeyStore(cfg.Security.KeyStorePath)
	if err != nil {
		return fmt.Errorf("loading key store: %w", err)
	}
	dbStore := auth.NewDBKeyStore(db)
	composite := auth.NewCompositeKeyStore(fileStore, dbStore)
	cachedStore := auth.NewCachedKeyStore(composite, 5*time.Minute)

	// 5. Create TelemetryCollector.
	telCol := telemetry.New(
		cfg.Telemetry.Enabled,
		cfg.Telemetry.Endpoint,
		cfg.Telemetry.BatchInterval,
		cfg.Telemetry.Epsilon,
		"", // salt — could be loaded from config in future
		logger,
	)

	// 5b. Apply DB-persisted middleware overrides (e.g. Web UI toggles).
	if overrides, err := db.LoadConfigPrefix("middleware_enabled_"); err == nil && len(overrides) > 0 {
		config.ApplyDBOverrides(&cfg, overrides)
		logger.Info("applied DB middleware overrides", "count", len(overrides))
	}

	// 6. Build middleware chain from config.
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

	swappableChain := middleware.NewSwappableChain(chain)

	// 7. SIGHUP handler for config reload.
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
				DB:       db,
				Logger:   newLogger,
				Metrics:  metrics,
				KeyStore: newCachedStore,
				TelCol:   telCol,
				SecMode:  newCfg.Security.Mode,
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

	// 9. Create and start monitor server.
	monSrv := monitor.New(cfg.Server.MonitorAddr, metrics, logger)
	monSrv.Start()

	// 10. Start telemetry in background.
	telCtx, telCancel := context.WithCancel(ctx)
	defer telCancel()
	go telCol.Run(telCtx)

	// 11. Run child process with middleware.
	runErr := process.RunWithMiddleware(ctx, childArgs, logger, swappableChain, metrics, monSrv)

	// 12. Shutdown.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	monSrv.Shutdown(shutCtx)
	telCancel()

	return runErr
}
