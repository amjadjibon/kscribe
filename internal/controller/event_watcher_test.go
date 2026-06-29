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
