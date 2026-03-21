package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"rua/internal/auth"
	"rua/internal/config"
	"rua/internal/logging"
	"rua/internal/middleware"
	"rua/internal/monitor"
	"rua/internal/process"
	"rua/internal/storage"
	"rua/internal/telemetry"
)

// globalFlags holds values bound to persistent (global) flags.
type globalFlags struct {
	configFile  string
	logLevel    string
	telemetry   bool
	verbose     bool
	monitorAddr string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-shield: %v\n", err)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

func run() error {
	flags := &globalFlags{}

	root := buildRootCmd(flags)
	root.AddCommand(buildLogsCmd(flags))

	// Allow unknown flags so that child command flags (e.g. --port 8080)
	// are not rejected by cobra.
	root.FParseErrWhitelist.UnknownFlags = true

	return root.Execute()
}

// buildRootCmd constructs the root cobra command.
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

// buildLogsCmd returns the `logs` sub-command.
func buildLogsCmd(flags *globalFlags) *cobra.Command {
	var (
		last   int
		agent  string
		since  string
		method string
		format string
	)

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Query intercepted MCP message logs",
		Long:  `Display stored MCP message logs with optional filtering.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := initFromFlags(flags)
			if err != nil {
				return err
			}

			db, err := storage.Open(cfg.Storage.DBPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer db.Close()

			opts := storage.QueryOptions{
				Last:   last,
				Method: method,
			}
			if agent != "" {
				opts.AgentHash = auth.AgentIDHash(agent)
			}
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("invalid --since value %q: %w", since, err)
				}
				opts.Since = d
			}

			logs, err := db.QueryLogs(opts)
			if err != nil {
				return fmt.Errorf("querying logs: %w", err)
			}

			switch format {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(logs)
			default:
				// Table format.
				fmt.Fprintf(os.Stdout, "%-24s %-10s %-30s %-5s %-10s %s\n",
					"TIMESTAMP", "DIRECTION", "METHOD", "OK", "LATENCY_MS", "AUTH")
				for _, l := range logs {
					fmt.Fprintf(os.Stdout, "%-24s %-10s %-30s %-5v %-10.1f %s\n",
						l.Timestamp.Format(time.RFC3339),
						l.Direction,
						l.Method,
						l.Success,
						l.LatencyMs,
						l.AuthStatus,
					)
				}
			}
			return nil
		},
		SilenceUsage: true,
	}

	f := cmd.Flags()
	f.IntVar(&last, "last", 50, "number of most recent log entries to show")
	f.StringVar(&agent, "agent", "", "filter by agent ID")
	f.StringVar(&since, "since", "", "show logs since duration (e.g. 1h, 30m)")
	f.StringVar(&method, "method", "", "filter by JSON-RPC method name")
	f.StringVar(&format, "format", "table", "output format: json or table")

	return cmd
}

// runWrapper is the main execution path: load config, init logger, run child.
func runWrapper(ctx context.Context, flags *globalFlags, childArgs []string) error {
	cfg, logger, err := initFromFlags(flags)
	if err != nil {
		return err
	}

	if len(childArgs) == 0 {
		return errors.New("no child command specified — usage: mcp-shield <command> [args...]")
	}

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
	authMW := auth.NewAuthMiddleware(cachedStore, cfg.Security.Mode, logger, func(status string) {
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
	logMW := storage.NewLogMiddleware(db, logger, telCol)
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

	// 11-12. Run child process with middleware.
	runErr := process.RunWithMiddleware(ctx, childArgs, logger, chain, metrics, monSrv)

	// 13. Shutdown.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	monSrv.Shutdown(shutCtx)
	telCancel()

	return runErr
}

// initFromFlags loads config and initialises the logger, applying any CLI
// flag overrides. It is shared between the root command and sub-commands.
func initFromFlags(flags *globalFlags) (config.Config, *slog.Logger, error) {
	cliOverrides := map[string]string{}

	effectiveLevel := flags.logLevel
	if flags.verbose && effectiveLevel == "" {
		effectiveLevel = "debug"
	}
	if effectiveLevel != "" {
		cliOverrides["log-level"] = effectiveLevel
	}
	if flags.monitorAddr != "" {
		cliOverrides["monitor-addr"] = flags.monitorAddr
	}
	if flags.telemetry {
		cliOverrides["telemetry"] = "true"
	}

	cfg, err := config.Load(flags.configFile, cliOverrides)
	if err != nil {
		return config.Config{}, nil, fmt.Errorf("configuration error: %w", err)
	}

	logger := logging.InitLogger(cfg.Logging)
	logger = logging.WithComponent(logger, "mcp-shield")

	return cfg, logger, nil
}
