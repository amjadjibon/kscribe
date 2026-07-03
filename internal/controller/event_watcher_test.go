package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/amjadjibon/kscribe/internal/config"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = kscribev1alpha1.AddToScheme(s)
	return s
}

func testCfg() config.Config {
	return config.Config{
		LLMProvider:          "openai",
		LLMModel:             "gpt-4o-mini",
		MaxIterations:        5,
		RedactEnabled:        true,
		EventReasonAllowlist: []string{"BackOff", "OOMKilling", "Failed"},
	}
}

func buildWatcher(cl client.Client, dedup *Deduper, cfg config.Config) *eventWatcher {
	return &eventWatcher{deps: EventWatcherDeps{
		Client:            cl,
		Deduper:           dedup,
		OperatorNamespace: "kscribe-system",
		Cfg:               cfg,
	}}
}

func makeEvent(uid, namespace, evType, reason string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "evt-" + uid,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Type:    evType,
		Reason:  reason,
		Message: "test message",
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Name:      "my-pod",
			Namespace: namespace,
		},
		Count: 1,
	}
}

func listDiagnoses(t *testing.T, cl client.Client) []kscribev1alpha1.KscribeDiagnosis {
	t.Helper()
	var list kscribev1alpha1.KscribeDiagnosisList
	if err := cl.List(context.Background(), &list); err != nil {
		t.Fatalf("list KscribeDiagnosis: %v", err)
	}
	return list.Items
}

// (a) non-Warning event is ignored — no CR created.
func TestNonWarningIgnored(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	w := buildWatcher(cl, NewDeduper(time.Hour), testCfg())
	ev := makeEvent("uid-1", "default", corev1.EventTypeNormal, "BackOff")

	_ = w.processEvent(context.Background(), ev)

	if items := listDiagnoses(t, cl); len(items) != 0 {
		t.Fatalf("got %d CRs, want 0", len(items))
	}
}

// (b) policy with Enabled=false in event namespace produces no CR.
func TestPolicyDisabledNamespace(t *testing.T) {
	disabled := false
	policy := &kscribev1alpha1.DiagnosisPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "any-policy", Namespace: "blocked-ns"},
		Spec:       kscribev1alpha1.DiagnosisPolicySpec{Enabled: &disabled},
	}
	cl := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(policy).Build()
	w := buildWatcher(cl, NewDeduper(time.Hour), testCfg())
	ev := makeEvent("uid-2", "blocked-ns", corev1.EventTypeWarning, "BackOff")

	_ = w.processEvent(context.Background(), ev)

	if items := listDiagnoses(t, cl); len(items) != 0 {
		t.Fatalf("got %d CRs, want 0", len(items))
	}
}

// (c) reason not in the allowlist produces no CR.
func TestReasonNotInAllowlist(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	cfg := testCfg()
	cfg.EventReasonAllowlist = []string{"BackOff"}
	w := buildWatcher(cl, NewDeduper(time.Hour), cfg)
	ev := makeEvent("uid-3", "default", corev1.EventTypeWarning, "Unrelated")

	_ = w.processEvent(context.Background(), ev)

	if items := listDiagnoses(t, cl); len(items) != 0 {
		t.Fatalf("got %d CRs, want 0", len(items))
	}
}

// (d) duplicate event within TTL window creates only one CR.
func TestDuplicateCreatesOnlyOne(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	w := buildWatcher(cl, NewDeduper(time.Hour), testCfg())
	ev := makeEvent("uid-4", "default", corev1.EventTypeWarning, "BackOff")

	_ = w.processEvent(context.Background(), ev)
	_ = w.processEvent(context.Background(), ev) // duplicate — deduper blocks

	if items := listDiagnoses(t, cl); len(items) != 1 {
		t.Fatalf("got %d CRs, want 1", len(items))
	}
}

// (e-pre) HIGH-002: Create returns AlreadyExists (post-restart deduper fresh, CR already exists)
// — must not return an error and must not create a second CR.
func TestAlreadyExistsIsSuccess(t *testing.T) {
	scheme := testScheme()
	ev := makeEvent("uid-dup", "default", corev1.EventTypeWarning, "BackOff")

	// Pre-create the CR that diagnosisName would produce (simulates prior reconcile surviving a restart).
	existing := &kscribev1alpha1.KscribeDiagnosis{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diagnosisName(ev),
			Namespace: "kscribe-system",
		},
		Spec: kscribev1alpha1.KscribeDiagnosisSpec{
			Reason:  ev.Reason,
			Message: ev.Message,
		},
		Status: kscribev1alpha1.KscribeDiagnosisStatus{Phase: kscribev1alpha1.DiagnosisPhaseDone},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kscribev1alpha1.KscribeDiagnosis{}).WithObjects(existing).Build()
	// Fresh deduper (simulates restart): Deduper has no entry for this event yet.
	w := buildWatcher(cl, NewDeduper(time.Hour), testCfg())

	if err := w.processEvent(context.Background(), ev); err != nil {
		t.Fatalf("processEvent must return nil on AlreadyExists, got: %v", err)
	}

	// Still only the one pre-existing CR.
	if items := listDiagnoses(t, cl); len(items) != 1 {
		t.Fatalf("got %d CRs after AlreadyExists, want 1", len(items))
	}
}

func TestAlreadyExistsBackfillsMissingEventMetadata(t *testing.T) {
	scheme := testScheme()
	ev := makeEvent("uid-backfill", "default", corev1.EventTypeWarning, "BackOff")
	ev.Message = "back-off from restarted watcher"
	ev.Count = 3

	existing := &kscribev1alpha1.KscribeDiagnosis{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diagnosisName(ev),
			Namespace: "kscribe-system",
		},
		Status: kscribev1alpha1.KscribeDiagnosisStatus{Phase: kscribev1alpha1.DiagnosisPhasePending},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kscribev1alpha1.KscribeDiagnosis{}).
		WithObjects(existing).
		Build()
	w := buildWatcher(cl, NewDeduper(time.Hour), testCfg())

	if err := w.processEvent(context.Background(), ev); err != nil {
		t.Fatalf("processEvent: %v", err)
	}

	var got kscribev1alpha1.KscribeDiagnosis
	if err := cl.Get(context.Background(), types.NamespacedName{Name: diagnosisName(ev), Namespace: "kscribe-system"}, &got); err != nil {
		t.Fatalf("get diagnosis: %v", err)
	}
	for _, c := range []struct {
		field string
		got   any
		want  any
	}{
		{"Reason", got.Spec.Reason, "BackOff"},
		{"InvolvedObjectKind", got.Spec.InvolvedObjectKind, "Pod"},
		{"InvolvedObjectName", got.Spec.InvolvedObjectName, "my-pod"},
		{"InvolvedObjectNamespace", got.Spec.InvolvedObjectNamespace, "default"},
		{"EventUID", got.Spec.EventUID, "uid-backfill"},
		{"Count", got.Spec.Count, int32(3)},
		{"Message", got.Spec.Message, "back-off from restarted watcher"},
	} {
		if fmt.Sprint(c.got) != fmt.Sprint(c.want) {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

// (e) accepted Warning event creates exactly one KscribeDiagnosis with the right spec fields.
func TestAcceptedWarningCreatesDiagnosis(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	w := buildWatcher(cl, NewDeduper(time.Hour), testCfg())
	ev := makeEvent("uid-5", "default", corev1.EventTypeWarning, "BackOff")
	ev.Message = "container back-off restarting"
	ev.Count = 7

	if err := w.processEvent(context.Background(), ev); err != nil {
		t.Fatalf("processEvent: %v", err)
	}

	items := listDiagnoses(t, cl)
	if len(items) != 1 {
		t.Fatalf("got %d CRs, want 1", len(items))
	}
	ksd := items[0]

	for _, c := range []struct {
		field string
		got   any
		want  any
	}{
		{"Reason", ksd.Spec.Reason, "BackOff"},
		{"InvolvedObjectKind", ksd.Spec.InvolvedObjectKind, "Pod"},
		{"InvolvedObjectName", ksd.Spec.InvolvedObjectName, "my-pod"},
		{"EventUID", ksd.Spec.EventUID, "uid-5"},
		{"Count", ksd.Spec.Count, int32(7)},
		{"Message", ksd.Spec.Message, "container back-off restarting"},
		{"Phase", string(ksd.Status.Phase), string(kscribev1alpha1.DiagnosisPhasePending)},
	} {
		if fmt.Sprint(c.got) != fmt.Sprint(c.want) {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

// TestDeduper_SweepEvictsExpired proves MED-001: expired entries are swept when the map
// exceeds dedupSweepThresh, not just on same-key re-access.
func TestDeduper_SweepEvictsExpired(t *testing.T) {
	// Use an injectable clock so we can advance time without sleeping.
	var fakeNow = time.Now()
	d := NewDeduper(time.Hour)
	d.now = func() time.Time { return fakeNow }

	// Fill the map past the sweep threshold with entries that will expire.
	shortTTL := time.Minute
	for i := range dedupSweepThresh + 1 {
		key := fmt.Sprintf("uid-%d", i)
		d.ttl = shortTTL
		d.ShouldProcess(key)
	}

	// Confirm map is at/above threshold.
	d.mu.Lock()
	sizeBefore := len(d.seen)
	d.mu.Unlock()
	if sizeBefore < dedupSweepThresh {
		t.Fatalf("want len >= %d before sweep, got %d", dedupSweepThresh, sizeBefore)
	}

	// Advance clock past the TTL so all entries are expired.
	fakeNow = fakeNow.Add(shortTTL + time.Second)

	// Adding one more key triggers the sweep.
	d.ttl = time.Hour
	d.ShouldProcess("trigger-sweep")

	d.mu.Lock()
	sizeAfter := len(d.seen)
	d.mu.Unlock()

	// After sweep, only the new entry should remain.
	if sizeAfter != 1 {
		t.Fatalf("want map len=1 after sweep of expired entries, got %d", sizeAfter)
	}
}
