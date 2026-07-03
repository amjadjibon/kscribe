package controller

import (
	"context"
	"fmt"
	"hash/fnv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/amjadjibon/kscribe/internal/config"
)

// EventWatcherDeps carries the dependencies for the event watcher controller.
type EventWatcherDeps struct {
	Client            client.Client
	Deduper           *Deduper
	OperatorNamespace string
	Cfg               config.Config
}

// SetupEventWatcherWithManager registers a controller that watches core v1 Warning Events
// and creates one deduplicated KscribeDiagnosis CR per accepted event (REQ-001).
// Events are NOT set as owners of the CR (ADR-001).
func SetupEventWatcherWithManager(mgr ctrl.Manager, deps EventWatcherDeps) error {
	r := &eventWatcher{deps: deps}
	return builder.ControllerManagedBy(mgr).
		Named("event-watcher").
		For(&corev1.Event{}).
		WithEventFilter(warningEventPredicate()).
		Complete(r)
}

type eventWatcher struct {
	deps EventWatcherDeps
}

func (r *eventWatcher) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var ev corev1.Event
	if err := r.deps.Client.Get(ctx, req.NamespacedName, &ev); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	return reconcile.Result{}, r.processEvent(ctx, &ev)
}

// processEvent is the core ingestion path, exposed for direct testing.
func (r *eventWatcher) processEvent(ctx context.Context, ev *corev1.Event) error {
	if ev.Type != corev1.EventTypeWarning {
		return nil
	}
	if !r.deps.Deduper.ShouldProcess(EventKey(ev)) {
		return nil
	}
	policy, err := ResolvePolicy(ctx, r.deps.Client, ev.Namespace, r.deps.OperatorNamespace, r.deps.Cfg)
	if err != nil {
		return fmt.Errorf("resolve policy: %w", err)
	}
	if !policy.Enabled {
		return nil
	}
	if !reasonAllowed(ev.Reason, policy.EventReasons) {
		return nil
	}
	return r.createDiagnosis(ctx, ev, policy)
}

func (r *eventWatcher) createDiagnosis(ctx context.Context, ev *corev1.Event, policy ResolvedPolicy) error {
	ns := r.deps.OperatorNamespace
	if ns == "" {
		ns = ev.Namespace
	}
	ksd := &kscribev1alpha1.KscribeDiagnosis{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diagnosisName(ev),
			Namespace: ns,
			Labels: map[string]string{
				"kscribe.amjadjibon.dev/event-namespace": ev.Namespace,
				"kscribe.amjadjibon.dev/reason":          ev.Reason,
			},
		},
		Spec: kscribev1alpha1.KscribeDiagnosisSpec{
			InvolvedObjectName:      ev.InvolvedObject.Name,
			InvolvedObjectNamespace: ev.InvolvedObject.Namespace,
			InvolvedObjectKind:      ev.InvolvedObject.Kind,
			Reason:                  ev.Reason,
			Message:                 ev.Message,
			EventUID:                string(ev.UID),
			Count:                   ev.Count,
			PolicyRef:               policy.PolicyRef,
			LLMProvider:             policy.LLMProvider,
			LLMModel:                policy.LLMModel,
			LLMBaseURL:              policy.LLMBaseURL,
			MaxIterations:           policy.MaxIterations,
			Redact:                  policy.Redact,
		},
		Status: kscribev1alpha1.KscribeDiagnosisStatus{
			Phase: kscribev1alpha1.DiagnosisPhasePending,
		},
	}
	if !ev.FirstTimestamp.IsZero() {
		ksd.Spec.FirstTimestamp = &ev.FirstTimestamp
	}
	if !ev.LastTimestamp.IsZero() {
		ksd.Spec.LastTimestamp = &ev.LastTimestamp
	}
	// Treat AlreadyExists as success: after a restart the Deduper is empty but the CR
	// may already exist — returning an error here would cause an infinite backoff storm (HIGH-002).
	// If the existing CR came from an older controller or a partial create, fill missing
	// event metadata so the dashboard can show Object and Reason after the CR mirrors to SQLite.
	if err := r.deps.Client.Create(ctx, ksd); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		return r.backfillExistingDiagnosis(ctx, ns, ksd.Name, ev, policy)
	}
	return nil
}

func (r *eventWatcher) backfillExistingDiagnosis(ctx context.Context, namespace, name string, ev *corev1.Event, policy ResolvedPolicy) error {
	var existing kscribev1alpha1.KscribeDiagnosis
	if err := r.deps.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &existing); err != nil {
		return client.IgnoreNotFound(err)
	}
	changed := false
	setString := func(field *string, value string) {
		if *field == "" && value != "" {
			*field = value
			changed = true
		}
	}
	setString(&existing.Spec.InvolvedObjectName, ev.InvolvedObject.Name)
	setString(&existing.Spec.InvolvedObjectNamespace, ev.InvolvedObject.Namespace)
	setString(&existing.Spec.InvolvedObjectKind, ev.InvolvedObject.Kind)
	setString(&existing.Spec.Reason, ev.Reason)
	setString(&existing.Spec.Message, ev.Message)
	setString(&existing.Spec.EventUID, string(ev.UID))
	setString(&existing.Spec.PolicyRef, policy.PolicyRef)
	setString(&existing.Spec.LLMProvider, policy.LLMProvider)
	setString(&existing.Spec.LLMModel, policy.LLMModel)
	setString(&existing.Spec.LLMBaseURL, policy.LLMBaseURL)
	if existing.Spec.Count == 0 && ev.Count != 0 {
		existing.Spec.Count = ev.Count
		changed = true
	}
	if existing.Spec.FirstTimestamp == nil && !ev.FirstTimestamp.IsZero() {
		existing.Spec.FirstTimestamp = &ev.FirstTimestamp
		changed = true
	}
	if existing.Spec.LastTimestamp == nil && !ev.LastTimestamp.IsZero() {
		existing.Spec.LastTimestamp = &ev.LastTimestamp
		changed = true
	}
	if existing.Spec.MaxIterations == nil && policy.MaxIterations != nil {
		existing.Spec.MaxIterations = policy.MaxIterations
		changed = true
	}
	if existing.Spec.Redact == nil && policy.Redact != nil {
		existing.Spec.Redact = policy.Redact
		changed = true
	}
	if !changed {
		return nil
	}
	return r.deps.Client.Update(ctx, &existing)
}

// diagnosisName returns a stable, lowercase RFC-1123-safe name for the CR.
// Prefers event UID; falls back to FNV hash of the composite key.
func diagnosisName(ev *corev1.Event) string {
	if ev.UID != "" {
		uid := string(ev.UID)
		if len(uid) > 53 {
			uid = uid[:53]
		}
		return "ksd-" + uid
	}
	h := fnv.New32a()
	fmt.Fprintf(h, "%s/%s/%s/%s", ev.Namespace, ev.InvolvedObject.Kind, ev.InvolvedObject.Name, ev.Reason)
	return fmt.Sprintf("ksd-%08x", h.Sum32())
}

// reasonAllowed returns true if reason appears in the allowlist, or the list is empty.
func reasonAllowed(reason string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == reason {
			return true
		}
	}
	return false
}

// warningEventPredicate passes only Warning-typed core v1 Events.
func warningEventPredicate() predicate.Predicate {
	isWarning := func(obj client.Object) bool {
		ev, ok := obj.(*corev1.Event)
		return ok && ev.Type == corev1.EventTypeWarning
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isWarning(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isWarning(e.ObjectNew) },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return isWarning(e.Object) },
	}
}
