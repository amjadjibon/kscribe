package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/bytedance/sonic"

	"github.com/amjadjibon/kscribe/internal/agent"
	"github.com/amjadjibon/kscribe/internal/enricher"
	"github.com/amjadjibon/kscribe/internal/store"
)

// DiagnosisStore is the storage interface required by the reconciler.
// ponytail: narrow interface — only what the reconciler uses, not the full *store.Store.
type DiagnosisStore interface {
	UpsertIncident(ctx context.Context, inc store.Incident) error
	InsertDiagnosis(ctx context.Context, d store.Diagnosis, rcaPayload any) error
}

// Publisher is the SSE producer interface. *web.Broker satisfies this via a thin adapter
// in main.go to avoid an import cycle (MED-002). html is a pre-rendered HTML fragment.
type Publisher interface {
	Publish(id, html string)
}

// KscribeDiagnosisReconciler reconciles KscribeDiagnosis objects.
type KscribeDiagnosisReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Store         DiagnosisStore
	AgentProvider agent.Provider
	Publisher     Publisher // may be nil; no-op when absent
	MaxIter       int       // default max tool-call iterations; overridable via CR spec
	Concurrency   int       // MaxConcurrentReconciles; 0 defaults to 1
	Tools         []agent.ToolDefinition
	ToolExecutor  agent.ToolExecutor   // nil falls back to stub error in agent loop
	KubeClient    kubernetes.Interface // nil → falls back to minimal spec-only snapshot
}

// publish emits an SSE fragment if a Publisher is wired; no-op otherwise.
func (r *KscribeDiagnosisReconciler) publish(id, html string) {
	if r.Publisher != nil {
		r.Publisher.Publish(id, html)
	}
}

func incidentFromDiagnosis(kd *kscribev1alpha1.KscribeDiagnosis) store.Incident {
	return store.Incident{
		Namespace:               kd.Namespace,
		Name:                    kd.Name,
		EventUID:                kd.Spec.EventUID,
		InvolvedObjectKind:      kd.Spec.InvolvedObjectKind,
		InvolvedObjectName:      kd.Spec.InvolvedObjectName,
		InvolvedObjectNamespace: kd.Spec.InvolvedObjectNamespace,
		Reason:                  kd.Spec.Reason,
		Message:                 kd.Spec.Message,
		Phase:                   string(kd.Status.Phase),
		StartedAt:               metaTimePtr(kd.Status.StartedAt),
		CompletedAt:             metaTimePtr(kd.Status.CompletedAt),
		LLMProvider:             kd.Status.LLMProvider,
		LLMModel:                kd.Status.LLMModel,
		TokensUsed:              kd.Status.TokensUsed,
		PromptRedacted:          kd.Status.PromptRedacted,
		Persisted:               kd.Status.Persisted,
	}
}

func metaTimePtr(t *metav1.Time) *time.Time {
	if t == nil {
		return nil
	}
	out := t.Time.UTC()
	return &out
}

// Reconcile drives a KscribeDiagnosis CR from Pending → Diagnosing → Done/Partial/Failed.
// Write ordering (ADR-003): SQLite upsert(Diagnosing) → run LLM → SQLite InsertDiagnosis → CR Done/Partial.
func (r *KscribeDiagnosisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var kd kscribev1alpha1.KscribeDiagnosis
	if err := r.Get(ctx, req.NamespacedName, &kd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Proceed for Pending/unset, or Diagnosing when not yet persisted (ADR-003 crash recovery).
	switch kd.Status.Phase {
	case "", kscribev1alpha1.DiagnosisPhasePending:
	// proceed
	case kscribev1alpha1.DiagnosisPhaseDiagnosing:
		if kd.Status.Persisted {
			if err := r.Store.UpsertIncident(ctx, incidentFromDiagnosis(&kd)); err != nil {
				return ctrl.Result{}, fmt.Errorf("upsert incident (terminal mirror): %w", err)
			}
			return ctrl.Result{}, nil
		}
		// Unpersisted Diagnosing: re-run diagnosis+persist so a transient store failure recovers.
	default:
		if err := r.Store.UpsertIncident(ctx, incidentFromDiagnosis(&kd)); err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert incident (terminal mirror): %w", err)
		}
		return ctrl.Result{}, nil
	}

	logger.Info("starting diagnosis", "name", req.Name, "namespace", req.Namespace, "reason", kd.Spec.Reason)

	now := time.Now().UTC()

	// ADR-003 step 1: mirror to SQLite as Diagnosing before touching CR.
	if err := r.Store.UpsertIncident(ctx, store.Incident{
		Namespace:               kd.Namespace,
		Name:                    kd.Name,
		EventUID:                kd.Spec.EventUID,
		InvolvedObjectKind:      kd.Spec.InvolvedObjectKind,
		InvolvedObjectName:      kd.Spec.InvolvedObjectName,
		InvolvedObjectNamespace: kd.Spec.InvolvedObjectNamespace,
		Reason:                  kd.Spec.Reason,
		Message:                 kd.Spec.Message,
		Phase:                   string(kscribev1alpha1.DiagnosisPhaseDiagnosing),
		StartedAt:               &now,
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("upsert incident (diagnosing): %w", err)
	}

	// Transition CR to Diagnosing.
	if err := r.patchStatus(ctx, req.NamespacedName, func(o *kscribev1alpha1.KscribeDiagnosis) {
		o.Status.Phase = kscribev1alpha1.DiagnosisPhaseDiagnosing
		o.Status.StartedAt = &metav1.Time{Time: now}
		o.Status.ObservedGeneration = o.Generation
		kscribev1alpha1.SetCondition(&o.Status, metav1.Condition{
			Type:               kscribev1alpha1.ConditionDiagnosing,
			Status:             metav1.ConditionTrue,
			Reason:             "DiagnosisStarted",
			Message:            "Diagnosis loop started",
			ObservedGeneration: o.Generation,
		})
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status (diagnosing): %w", err)
	}
	r.publish(req.Namespace+"/"+req.Name, fmt.Sprintf(`<span data-phase="Diagnosing">%s</span>`, kscribev1alpha1.DiagnosisPhaseDiagnosing))
	// Re-fetch for fresh ResourceVersion before the next status update.
	if err := r.Get(ctx, req.NamespacedName, &kd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Build enriched snapshot; fall back to spec-only if KubeClient is absent or collection fails.
	ref := enricher.ObjectRef{
		Kind:      kd.Spec.InvolvedObjectKind,
		Namespace: kd.Spec.InvolvedObjectNamespace,
		Name:      kd.Spec.InvolvedObjectName,
		EventUID:  kd.Spec.EventUID,
		Reason:    kd.Spec.Reason,
		Message:   kd.Spec.Message,
	}
	var snap *enricher.Snapshot
	if r.KubeClient != nil {
		var buildErr error
		snap, buildErr = enricher.BuildSnapshot(ctx, r.Client, r.KubeClient, ref, 100)
		if buildErr != nil {
			logger.Info("BuildSnapshot failed, using minimal snapshot", "error", buildErr)
			snap = nil
		}
	}
	if snap == nil {
		snap = &enricher.Snapshot{
			EventUID:   kd.Spec.EventUID,
			Reason:     kd.Spec.Reason,
			Message:    kd.Spec.Message,
			Namespace:  kd.Spec.InvolvedObjectNamespace,
			ObjectKind: kd.Spec.InvolvedObjectKind,
			ObjectName: kd.Spec.InvolvedObjectName,
		}
	}
	snapshotJSON, encErr := enricher.EncodeSnapshot(snap)
	if encErr != nil {
		snapshotJSON = []byte("{}")
	}

	// ADR-003 step 2: run diagnosis.
	maxIter := r.MaxIter
	if kd.Spec.MaxIterations != nil {
		maxIter = int(*kd.Spec.MaxIterations)
	}
	ag := &agent.DiagnosisAgent{
		Provider: r.AgentProvider,
		Executor: r.ToolExecutor,
		Tools:    r.Tools,
		MaxIter:  maxIter,
	}
	outcome := ag.Run(ctx, snapshotJSON)

	completedAt := time.Now().UTC()

	// Provider failure: record audit fields and set Failed; skip InsertDiagnosis.
	if outcome.Phase == kscribev1alpha1.DiagnosisPhaseFailed {
		logger.Info("provider failure", "error", outcome.RawError)
		_ = r.Store.UpsertIncident(ctx, store.Incident{
			Namespace:               kd.Namespace,
			Name:                    kd.Name,
			EventUID:                kd.Spec.EventUID,
			InvolvedObjectKind:      kd.Spec.InvolvedObjectKind,
			InvolvedObjectName:      kd.Spec.InvolvedObjectName,
			InvolvedObjectNamespace: kd.Spec.InvolvedObjectNamespace,
			Reason:                  kd.Spec.Reason,
			Message:                 kd.Spec.Message,
			Phase:                   string(kscribev1alpha1.DiagnosisPhaseFailed),
			StartedAt:               &now,
			CompletedAt:             &completedAt,
			TokensUsed:              outcome.TokensUsed,
		})
		r.publish(req.Namespace+"/"+req.Name, fmt.Sprintf(`<span data-phase="Failed">%s</span>`, kscribev1alpha1.DiagnosisPhaseFailed))
		// ponytail: patchStatus retries on conflict so the Failed phase always lands,
		// stopping the retry storm (provider-failure CR stays Diagnosing → requeues forever).
		return ctrl.Result{}, r.patchStatus(ctx, req.NamespacedName, func(o *kscribev1alpha1.KscribeDiagnosis) {
			o.Status.Phase = kscribev1alpha1.DiagnosisPhaseFailed
			o.Status.CompletedAt = &metav1.Time{Time: completedAt}
			o.Status.TokensUsed = outcome.TokensUsed
			o.Status.ObservedGeneration = o.Generation
			kscribev1alpha1.SetCondition(&o.Status, metav1.Condition{
				Type:               kscribev1alpha1.ConditionDiagnosed,
				Status:             metav1.ConditionFalse,
				Reason:             "ProviderError",
				Message:            outcome.RawError,
				ObservedGeneration: o.Generation,
			})
		})
	}

	// ADR-003 step 3: write final RCA to SQLite BEFORE updating CR phase.
	traceJSON, _ := sonic.Marshal(outcome.Trace)
	if len(traceJSON) == 0 {
		traceJSON = []byte("[]")
	}
	d := store.Diagnosis{
		Namespace:   kd.Namespace,
		Name:        kd.Name,
		EventUID:    kd.Spec.EventUID,
		ContextJSON: snapshotJSON,
		Reasoning:   outcome.Reasoning,
		TraceJSON:   traceJSON,
	}
	var rcaPayload any = map[string]string{"error": outcome.RawError}
	if outcome.RCA != nil {
		d.Summary = outcome.RCA.Summary
		d.RootCause = outcome.RCA.RootCause
		d.Remediation = strings.Join(outcome.RCA.RemediationSteps, "; ")
		d.Confidence = outcome.RCA.Confidence
		rcaPayload = outcome.RCA
	}

	if err := r.Store.InsertDiagnosis(ctx, d, rcaPayload); err != nil {
		// SQLite write failed — stay Diagnosing, set Persisted=False, requeue (ADR-003).
		logger.Error(err, "sqlite final write failed; requeueing")
		storeErr := err
		_ = r.patchStatus(ctx, req.NamespacedName, func(o *kscribev1alpha1.KscribeDiagnosis) {
			o.Status.Persisted = false
			kscribev1alpha1.SetCondition(&o.Status, metav1.Condition{
				Type:               kscribev1alpha1.ConditionPersisted,
				Status:             metav1.ConditionFalse,
				Reason:             "StorageError",
				Message:            storeErr.Error(),
				ObservedGeneration: o.Generation,
			})
		})
		return ctrl.Result{RequeueAfter: 30 * time.Second}, storeErr
	}

	// ADR-003 step 4: SQLite write succeeded — now update CR to Done/Partial.
	_ = r.Store.UpsertIncident(ctx, store.Incident{
		Namespace:               kd.Namespace,
		Name:                    kd.Name,
		EventUID:                kd.Spec.EventUID,
		InvolvedObjectKind:      kd.Spec.InvolvedObjectKind,
		InvolvedObjectName:      kd.Spec.InvolvedObjectName,
		InvolvedObjectNamespace: kd.Spec.InvolvedObjectNamespace,
		Reason:                  kd.Spec.Reason,
		Message:                 kd.Spec.Message,
		Phase:                   string(outcome.Phase),
		StartedAt:               &now,
		CompletedAt:             &completedAt,
		TokensUsed:              outcome.TokensUsed,
		Persisted:               true,
	})

	r.publish(req.Namespace+"/"+req.Name, fmt.Sprintf(`<span data-phase="%s">%s</span>`, outcome.Phase, outcome.Phase))
	return ctrl.Result{}, r.patchStatus(ctx, req.NamespacedName, func(o *kscribev1alpha1.KscribeDiagnosis) {
		o.Status.Phase = outcome.Phase
		o.Status.CompletedAt = &metav1.Time{Time: completedAt}
		o.Status.TokensUsed = outcome.TokensUsed
		o.Status.Persisted = true
		o.Status.ObservedGeneration = o.Generation
		if outcome.RCA != nil {
			o.Status.Summary = outcome.RCA.Summary
			o.Status.RootCause = outcome.RCA.RootCause
		}
		kscribev1alpha1.SetCondition(&o.Status, metav1.Condition{
			Type:               kscribev1alpha1.ConditionPersisted,
			Status:             metav1.ConditionTrue,
			Reason:             "Persisted",
			Message:            "RCA written to state DB",
			ObservedGeneration: o.Generation,
		})
		kscribev1alpha1.SetCondition(&o.Status, metav1.Condition{
			Type:               kscribev1alpha1.ConditionDiagnosed,
			Status:             metav1.ConditionTrue,
			Reason:             "Diagnosed",
			Message:            "Diagnosis complete",
			ObservedGeneration: o.Generation,
		})
	})
}

// patchStatus re-fetches the CR and applies mutate under conflict retry, so a
// concurrent modification doesn't drop the status transition (prevents the
// provider-failure retry storm).
func (r *KscribeDiagnosisReconciler) patchStatus(ctx context.Context, key types.NamespacedName, mutate func(*kscribev1alpha1.KscribeDiagnosis)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur kscribev1alpha1.KscribeDiagnosis
		if err := r.Get(ctx, key, &cur); err != nil {
			return err
		}
		mutate(&cur)
		return r.Status().Update(ctx, &cur)
	})
}

// SetupWithManager registers the reconciler with the controller-manager.
func (r *KscribeDiagnosisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	concurrency := r.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kscribev1alpha1.KscribeDiagnosis{}).
		WithOptions(ctrlutil.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}
