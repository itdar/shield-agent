package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/token"
)

// buildTokenCmd returns the `token` sub-command group.
func buildTokenCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API access tokens",
		Long:  `Create, list, revoke, and inspect usage statistics for API tokens.`,
	}

	cmd.AddCommand(buildTokenCreateCmd(flags))
	cmd.AddCommand(buildTokenListCmd(flags))
	cmd.AddCommand(buildTokenRevokeCmd(flags))
	cmd.AddCommand(buildTokenStatsCmd(flags))

	return cmd
}

func buildTokenCreateCmd(flags *globalFlags) *cobra.Command {
	var (
		name         string
		quotaHourly  int
		quotaMonthly int
		expires      string
		methods      string
		ipAllowlist  string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}

			cfg, _, err := initFromFlags(flags)
			if err != nil {
				return err
			}

			db, err := storage.Open(cfg.Storage.DBPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer db.Close()

			store := token.NewStore(db.Conn())

			rawToken, err := token.GenerateToken()
			if err != nil {
				return fmt.Errorf("generating token: %w", err)
			}
			hash := token.HashToken(rawToken)

			var expiresAt *time.Time
			if expires != "" {
				d, err := time.ParseDuration(expires)
				if err != nil {
					return fmt.Errorf("invalid --expires value %q: %w", expires, err)
				}
				t := time.Now().Add(d)
				expiresAt = &t
			}

			var allowedMethods []string
			if methods != "" {
				allowedMethods = strings.Split(methods, ",")
			}

			var ipList []string
			if ipAllowlist != "" {
				ipList = strings.Split(ipAllowlist, ",")
			}

			id, err := store.Create(name, hash, expiresAt, quotaHourly, quotaMonthly, allowedMethods, ipList)
			if err != nil {
				return fmt.Errorf("creating token: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Token created successfully.\n")
			fmt.Fprintf(os.Stdout, "ID:    %s\n", id)
			fmt.Fprintf(os.Stdout, "Token: %s\n", rawToken)
			fmt.Fprintf(os.Stdout, "\nSave this token now — it will not be shown again.\n")

			return nil
		},
		SilenceUsage: true,
	}

	f := cmd.Flags()
	f.StringVar(&name, "name", "", "human-readable name for the token (required)")
	f.IntVar(&quotaHourly, "quota-hourly", 0, "maximum requests per hour (0 = unlimited)")
	f.IntVar(&quotaMonthly, "quota-monthly", 0, "maximum requests per month (0 = unlimited)")
	f.StringVar(&expires, "expires", "", "token lifetime duration (e.g. 720h, 30d)")
	f.StringVar(&methods, "methods", "", "comma-separated allowed methods (e.g. tools/call,tools/list)")
	f.StringVar(&ipAllowlist, "ip-allowlist", "", "comma-separated CIDR ranges (e.g. 10.0.0.0/8,192.168.1.0/24)")

	return cmd
}

func buildTokenListCmd(flags *globalFlags) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API tokens",
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

			store := token.NewStore(db.Conn())

			tokens, err := store.List(!all)
			if err != nil {
				return fmt.Errorf("listing tokens: %w", err)
			}

			fmt.Fprintf(os.Stdout, "%-18s %-20s %-22s %-22s %-8s %-14s %s\n",
				"ID", "NAME", "CREATED", "EXPIRES", "ACTIVE", "HOURLY_QUOTA", "MONTHLY_QUOTA")
			for _, t := range tokens {
				expiresStr := "-"
				if t.ExpiresAt != nil {
					expiresStr = t.ExpiresAt.Format(time.RFC3339)
				}
				fmt.Fprintf(os.Stdout, "%-18s %-20s %-22s %-22s %-8v %-14d %d\n",
					t.ID,
					t.Name,
					t.CreatedAt.Format(time.RFC3339),
					expiresStr,
					t.Active,
					t.QuotaHourly,
					t.QuotaMonthly,
				)
			}
			return nil
		},
		SilenceUsage: true,
	}

	cmd.Flags().BoolVar(&all, "all", false, "show all tokens including revoked ones")

	return cmd
}

func buildTokenRevokeCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke an API token",
		Args:  cobra.ExactArgs(1),
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

			store := token.NewStore(db.Conn())

			if err := store.Revoke(args[0]); err != nil {
				return fmt.Errorf("revoking token: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Token %s revoked.\n", args[0])
			return nil
		},
		SilenceUsage: true,
	}

	return cmd
}

func buildTokenStatsCmd(flags *globalFlags) *cobra.Command {
	var since string

	cmd := &cobra.Command{
		Use:   "stats <id>",
		Short: "Show usage statistics for a token",
		Args:  cobra.ExactArgs(1),
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

			store := token.NewStore(db.Conn())

			d, err := time.ParseDuration(since)
			if err != nil {
				return fmt.Errorf("invalid --since value %q: %w", since, err)
			}

			stats, err := store.GetStats(args[0], d)
			if err != nil {
				return fmt.Errorf("getting token stats: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Usage statistics for token %s (last %s):\n", args[0], since)
			fmt.Fprintf(os.Stdout, "  Total requests:  %d\n", stats.TotalRequests)
			fmt.Fprintf(os.Stdout, "  Success:         %d\n", stats.SuccessCount)
			fmt.Fprintf(os.Stdout, "  Failures:        %d\n", stats.FailCount)
			fmt.Fprintf(os.Stdout, "  Avg latency:     %.2f ms\n", stats.AvgLatencyMs)
			fmt.Fprintf(os.Stdout, "  Hourly usage:    %d\n", stats.HourlyUsage)
			fmt.Fprintf(os.Stdout, "  Monthly usage:   %d\n", stats.MonthlyUsage)

			return nil
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVar(&since, "since", "24h", "time window for stats (e.g. 24h, 168h)")

	return cmd
}
