package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/amjadjibon/kscribe/internal/agent"
	"github.com/amjadjibon/kscribe/internal/config"
	"github.com/amjadjibon/kscribe/internal/controller"
	"github.com/amjadjibon/kscribe/internal/store"
	"github.com/amjadjibon/kscribe/internal/web"
)

var scheme = runtime.NewScheme()

// brokerPublisher adapts *web.Broker to controller.Publisher, avoiding an import cycle.
// ponytail: thin adapter — no state beyond the broker reference.
type brokerPublisher struct{ b *web.Broker }

func (p *brokerPublisher) Publish(id, html string) { p.b.Publish(id, web.Event{HTML: html}) }

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kscribev1alpha1.AddToScheme(scheme))
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runPruner loops hourly until ctx is done, deleting SQLite history rows and
// terminal-phase KscribeDiagnosis CRs older than the retention window.
// ponytail: per-replica, best-effort — errors are logged and retried next tick.
func runPruner(ctx context.Context, st *store.Store, c client.Client, retention time.Duration) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		cutoff := time.Now().Add(-retention)

		n, err := st.Prune(ctx, cutoff)
		if err != nil {
			slog.Error("prune sqlite history", "error", err)
		} else if n > 0 {
			slog.Info("pruned sqlite history", "incidents_deleted", n, "cutoff", cutoff.UTC().Format(time.RFC3339))
		}

		var list kscribev1alpha1.KscribeDiagnosisList
		if err := c.List(ctx, &list); err != nil {
			slog.Error("prune list diagnoses", "error", err)
		} else {
			deleted := 0
			for i := range list.Items {
				d := &list.Items[i]
				switch d.Status.Phase {
				case kscribev1alpha1.DiagnosisPhaseDone, kscribev1alpha1.DiagnosisPhasePartial, kscribev1alpha1.DiagnosisPhaseFailed:
				default:
					continue // only terminal phases are pruned
				}
				finished := d.CreationTimestamp.Time
				if d.Status.CompletedAt != nil {
					finished = d.Status.CompletedAt.Time
				}
				if finished.After(cutoff) {
					continue
				}
				if err := c.Delete(ctx, d); err != nil && !apierrors.IsNotFound(err) {
					slog.Error("prune delete diagnosis", "namespace", d.Namespace, "name", d.Name, "error", err)
					continue
				}
				deleted++
			}
			if deleted > 0 {
				slog.Info("pruned finished diagnosis CRs", "deleted", deleted)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
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

			overrides := config.FlagOverrides{
				Addr:              flagAddr,
				OperatorNamespace: flagOperatorNamespace,
			}
			if flagLeaderElectSet {
				overrides.LeaderElect = &flagLeaderElect
			}
			cfg.ApplyFlags(overrides)

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

			// Route controller-runtime's logr logging through slog so its internal
			// logs (reconcile errors, requeues, leader election) are visible and
			// to silence the "log.SetLogger(...) was never called" warning.
			ctrl.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

			// Build rest.Config — prefer explicit kubeconfig file over in-cluster auto-detect.
			restCfg, cfgErr := ctrl.GetConfig()
			if flagConfigFile != "" {
				restCfg, cfgErr = clientcmd.BuildConfigFromFlags("", flagConfigFile)
			}
			if cfgErr != nil {
				return fmt.Errorf("rest config: %w", cfgErr)
			}

			// Leader-election lock lives in the operator namespace (or kscribe-system as fallback).
			leaderNS := cfg.OperatorNamespace
			if leaderNS == "" {
				leaderNS = "kscribe-system"
			}

			mgrOpts := ctrl.Options{
				Scheme:                  scheme,
				LeaderElection:          cfg.LeaderElect,
				LeaderElectionID:        "kscribe.amjadjibon.dev",
				LeaderElectionNamespace: leaderNS,
				Metrics:                 metricsserver.Options{BindAddress: cfg.MetricsAddr}, // dashboard owns /healthz
			}
			// Restrict cache to operator namespace when set; empty = cluster-wide.
			if cfg.OperatorNamespace != "" {
				mgrOpts.Cache = cache.Options{
					DefaultNamespaces: map[string]cache.Config{
						cfg.OperatorNamespace: {},
					},
				}
			}
			mgr, err := ctrl.NewManager(restCfg, mgrOpts)
			if err != nil {
				return fmt.Errorf("create manager: %w", err)
			}

			// Open SQLite store.
			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}

			// SSE broker shared between reconciler and web server.
			broker := web.NewBroker()

			// OpenAI-compatible provider built from config (CON-003: sonic used inside package).
			// provider=google/gemini auto-targets Gemini's OpenAI-compatible endpoint.
			baseURL := agent.ResolveBaseURL(cfg.LLMProvider, cfg.LLMBaseURL)
			provider := agent.NewOpenAIClient(baseURL, cfg.LLMAPIKey, cfg.LLMModel)

			// Event watcher — creates KscribeDiagnosis CRs from Warning Events.
			if err := controller.SetupEventWatcherWithManager(mgr, controller.EventWatcherDeps{
				Client:            mgr.GetClient(),
				Deduper:           controller.NewDeduper(0),
				OperatorNamespace: cfg.OperatorNamespace,
				Cfg:               cfg,
			}); err != nil {
				return fmt.Errorf("setup event watcher: %w", err)
			}

			// Build a native clientset for log streaming and tool calls.
			kcs, err := kubernetes.NewForConfig(restCfg)
			if err != nil {
				return fmt.Errorf("build kubernetes clientset: %w", err)
			}

			// KscribeDiagnosis reconciler.
			reconciler := &controller.KscribeDiagnosisReconciler{
				Client:        mgr.GetClient(),
				Scheme:        mgr.GetScheme(),
				Store:         st,
				AgentProvider: provider,
				Publisher:     &brokerPublisher{b: broker},
				LLMProvider:   cfg.LLMProvider,
				LLMModel:      cfg.LLMModel,
				MaxIter:       cfg.MaxIterations,
				Concurrency:   cfg.DiagnosisConcurrency,
				Tools:         agent.KubeTools(),
				KubeClient:    kcs,
				ToolExecutor:  &controller.KubeToolExecutor{Client: mgr.GetClient(), Kube: kcs},
			}
			if err := reconciler.SetupWithManager(mgr); err != nil {
				return fmt.Errorf("setup diagnosis reconciler: %w", err)
			}

			// Web dashboard alongside the manager, bound to the manager context.
			// provider is shared with the reconciler (same client, no extra connections).
			webSrv := web.New(st, broker, provider).WithAuthToken(cfg.DashboardToken)
			if err := mgr.Add(ctrlmgr.RunnableFunc(func(ctx context.Context) error {
				srv := &http.Server{
					Addr:    cfg.Addr,
					Handler: webSrv.Handler(),
				}
				go func() {
					<-ctx.Done()
					_ = srv.Shutdown(context.Background()) //nolint:contextcheck
				}()
				slog.Info("web server listening", "addr", cfg.Addr)
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					return err
				}
				return nil
			})); err != nil {
				return fmt.Errorf("add web server runnable: %w", err)
			}

			// Retention pruner — hourly sweep of old SQLite rows and finished CRs.
			if cfg.RetentionPeriod > 0 {
				if err := mgr.Add(ctrlmgr.RunnableFunc(func(ctx context.Context) error {
					runPruner(ctx, st, mgr.GetClient(), cfg.RetentionPeriod)
					return nil
				})); err != nil {
					return fmt.Errorf("add pruner runnable: %w", err)
				}
			}

			slog.Info("starting manager")
			return mgr.Start(ctrl.SetupSignalHandler())
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&flagConfigFile, "config", "", "Path to kubeconfig file (defaults to in-cluster config)")
	flags.StringVar(&flagAddr, "addr", "", "Web server listen address (overrides KSCRIBE_ADDR)")
	flags.StringVar(&flagOperatorNamespace, "operator-namespace", "", "Namespace to watch (overrides KSCRIBE_OPERATOR_NAMESPACE; empty = cluster-wide)")
	flags.BoolVar(&flagLeaderElect, "leader-elect", false, "Enable leader election (overrides KSCRIBE_LEADER_ELECT)")

	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		flagLeaderElectSet = cmd.Flags().Changed("leader-elect")
		return nil
	}

	return cmd
}
