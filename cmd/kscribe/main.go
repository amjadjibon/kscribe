package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/amjadjibon/kscribe/internal/config"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		flagConfigFile        string
		flagLeaderElect       bool
		flagLeaderElectSet    bool
		flagAddr              string
		flagOperatorNamespace string
	)

	cmd := &cobra.Command{
		Use:   "kscribe",
		Short: "Kubernetes AI diagnosis operator",
		Long: `kscribe is a Kubernetes operator that watches cluster events,
diagnoses failures using an LLM backend, and surfaces remediation guidance.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			// Build flag overrides — only apply values the user explicitly set.
			overrides := config.FlagOverrides{
				Addr:              flagAddr,
				OperatorNamespace: flagOperatorNamespace,
			}
			if flagLeaderElectSet {
				overrides.LeaderElect = &flagLeaderElect
			}
			cfg.ApplyFlags(overrides)

			_ = flagConfigFile // phase 2: load kubeconfig path from here

			slog.Info("kscribe starting",
				"addr", cfg.Addr,
				"operator_namespace", cfg.OperatorNamespace,
				"leader_elect", cfg.LeaderElect,
				"llm_provider", cfg.LLMProvider,
				"llm_model", cfg.LLMModel,
				"max_iterations", cfg.MaxIterations,
				"diagnosis_concurrency", cfg.DiagnosisConcurrency,
				"redact_enabled", cfg.RedactEnabled,
				"db_path", cfg.DBPath,
				"resync_period", cfg.ResyncPeriod.String(),
				"event_reason_allowlist", cfg.EventReasonAllowlist,
			)

			// ponytail: manager start goes here in phase 2
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&flagConfigFile, "config", "", "Path to kubeconfig file (defaults to in-cluster config)")
	flags.StringVar(&flagAddr, "addr", "", "Web server listen address (overrides KSCRIBE_ADDR)")
	flags.StringVar(&flagOperatorNamespace, "operator-namespace", "", "Namespace to watch (overrides KSCRIBE_OPERATOR_NAMESPACE; empty = cluster-wide)")

	// Use PreRun to detect whether --leader-elect was actually provided.
	flags.BoolVar(&flagLeaderElect, "leader-elect", false, "Enable leader election (overrides KSCRIBE_LEADER_ELECT)")
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		flagLeaderElectSet = cmd.Flags().Changed("leader-elect")
		return nil
	}

	return cmd
}
