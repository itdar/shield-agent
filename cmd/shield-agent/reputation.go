package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/itdar/shield-agent/internal/reputation"
	"github.com/itdar/shield-agent/internal/storage"
)

// buildReputationCmd builds the `shield-agent reputation` sub-command.
func buildReputationCmd(flags *globalFlags) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "reputation [agent-hash]",
		Short: "Query agent reputation scores",
		Long: `Display reputation scores for agents. Without arguments, lists all agents.
With an agent hash argument, shows the detailed score for that agent.

Example:
  shield-agent reputation
  shield-agent reputation abc123def --format json`,

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

			provider := reputation.NewLocalProvider(db.Conn(), nil, cfg.Reputation)

			if len(args) > 0 {
				return showAgentReputation(provider, args[0], format)
			}
			return listReputations(provider, format)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVar(&format, "format", "table", "output format: json or table")
	return cmd
}

func showAgentReputation(provider *reputation.LocalProvider, agentHash, format string) error {
	// Try live recalculation first.
	score, err := provider.RecalculateOne(agentHash)
	if err != nil {
		return fmt.Errorf("recalculating reputation: %w", err)
	}
	if score == nil {
		fmt.Fprintf(os.Stderr, "No activity found for agent %s in the configured time window.\n", agentHash)
		return nil
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(score)
	default:
		fmt.Fprintf(os.Stdout, "Agent:          %s\n", score.AgentIDHash)
		fmt.Fprintf(os.Stdout, "Trust Level:    %s\n", score.TrustLevel)
		fmt.Fprintf(os.Stdout, "Trust Score:    %.3f\n", score.TrustScore)
		fmt.Fprintf(os.Stdout, "Success Rate:   %.1f%%\n", score.SuccessRate*100)
		fmt.Fprintf(os.Stdout, "Error Rate:     %.1f%%\n", score.ErrorRate*100)
		fmt.Fprintf(os.Stdout, "Avg Latency:    %.1f ms\n", score.AvgLatencyMs)
		fmt.Fprintf(os.Stdout, "Requests:       %d\n", score.RequestCount)
		fmt.Fprintf(os.Stdout, "Rate Limit:     %d hits\n", score.RateLimitHits)
		fmt.Fprintf(os.Stdout, "Auth Failures:  %d\n", score.AuthFailures)
		fmt.Fprintf(os.Stdout, "Window:         %s\n", score.WindowDuration)
		fmt.Fprintf(os.Stdout, "First Seen:     %s\n", score.FirstSeen.Format("2006-01-02 15:04:05"))
		fmt.Fprintf(os.Stdout, "Last Seen:      %s\n", score.LastSeen.Format("2006-01-02 15:04:05"))
		return nil
	}
}

func listReputations(provider *reputation.LocalProvider, format string) error {
	// Force a recalculation to get fresh data.
	if err := provider.Recalculate(); err != nil {
		return fmt.Errorf("recalculating reputations: %w", err)
	}

	scores, err := provider.ListScores(nil)
	if err != nil {
		return fmt.Errorf("listing reputations: %w", err)
	}

	if len(scores) == 0 {
		fmt.Fprintln(os.Stderr, "No agent activity found in the configured time window.")
		return nil
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(scores)
	default:
		fmt.Fprintf(os.Stdout, "%-16s %-12s %-8s %-10s %-10s %-8s\n",
			"AGENT_HASH", "TRUST", "SCORE", "SUCCESS%", "REQUESTS", "ERRORS")
		for _, s := range scores {
			hash := s.AgentIDHash
			if len(hash) > 14 {
				hash = hash[:14] + ".."
			}
			fmt.Fprintf(os.Stdout, "%-16s %-12s %-8.3f %-10.1f %-10d %-8d\n",
				hash,
				s.TrustLevel,
				s.TrustScore,
				s.SuccessRate*100,
				s.RequestCount,
				int(s.ErrorRate*float64(s.RequestCount)),
			)
		}
		return nil
	}
}
