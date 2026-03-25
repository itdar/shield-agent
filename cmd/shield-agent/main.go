package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/logging"
	"github.com/itdar/shield-agent/internal/storage"
)

// globalFlags holds values bound to persistent (global) flags.
type globalFlags struct {
	configFile         string
	logLevel           string
	telemetry          bool
	verbose            bool
	monitorAddr        string
	disableMiddlewares []string
	enableMiddlewares  []string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "shield-agent: %v\n", err)
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
	root.AddCommand(buildProxyCmd(flags))

	// Allow unknown flags so that child command flags (e.g. --port 8080)
	// are not rejected by cobra.
	root.FParseErrWhitelist.UnknownFlags = true

	return root.Execute()
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
				fmt.Fprintf(os.Stdout, "%-24s %-10s %-30s %-5s %-10s %-16s %s\n",
					"TIMESTAMP", "DIRECTION", "METHOD", "OK", "LATENCY_MS", "IP", "AUTH")
				for _, l := range logs {
					fmt.Fprintf(os.Stdout, "%-24s %-10s %-30s %-5v %-10.1f %-16s %s\n",
						l.Timestamp.Format(time.RFC3339),
						l.Direction,
						l.Method,
						l.Success,
						l.LatencyMs,
						l.IPAddress,
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

// initFromFlags loads config and initialises the logger, applying any CLI
// flag overrides. It is shared between all sub-commands.
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

	// Apply middleware enable/disable flags directly (after Load, to support multiple names).
	for _, name := range flags.disableMiddlewares {
		config.SetMiddlewareEnabled(&cfg, name, false)
	}
	for _, name := range flags.enableMiddlewares {
		config.SetMiddlewareEnabled(&cfg, name, true)
	}

	logger := logging.InitLogger(cfg.Logging)
	logger = logging.WithComponent(logger, "shield-agent")

	return cfg, logger, nil
}
