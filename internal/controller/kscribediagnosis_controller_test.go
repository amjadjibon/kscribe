package controller_test

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/amjadjibon/kscribe/internal/agent"
	"github.com/amjadjibon/kscribe/internal/controller"
	"github.com/amjadjibon/kscribe/internal/store"
)

// ---- fakes ----

type fakeStore struct {
	incidents    []store.Incident
	diagnoses    []store.Diagnosis
	insertErr    error
	insertCalled int
	upsertCalled int
}

func (f *fakeStore) UpsertIncident(_ context.Context, inc store.Incident) error {
	f.upsertCalled++
	f.incidents = append(f.incidents, inc)
	return nil
}

func (f *fakeStore) InsertDiagnosis(_ context.Context, d store.Diagnosis, _ any) error {
	f.insertCalled++
	if f.insertErr != nil {
		return f.insertErr
	}
	f.diagnoses = append(f.diagnoses, d)
	return nil
}

type fixedProvider struct {
	resp agent.Response
	err  error
}

func (p *fixedProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	return p.resp, p.err
}

// ---- helpers ----

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kscribev1alpha1.AddToScheme(s)
	return s
}

const goodRCA = `{"summary":"test summary","rootCause":"test cause","confidence":0.9}`

func goodProvider() *fixedProvider {
	return &fixedProvider{resp: agent.Response{
		Choices: []agent.Choice{{
			Message:      agent.Message{Role: "assistant", Content: goodRCA},
			FinishReason: "stop",
		}},
		Usage: agent.Usage{TotalTokens: 42},
	}}
}

func newKD(name, ns string) *kscribev1alpha1.KscribeDiagnosis {
	return &kscribev1alpha1.KscribeDiagnosis{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: kscribev1alpha1.KscribeDiagnosisSpec{
			InvolvedObjectName:      "pod-1",
			InvolvedObjectNamespace: ns,
			InvolvedObjectKind:      "Pod",
			Reason:                  "BackOff",
			Message:                 "back-off restarting",
			EventUID:                "uid-test",
		},
	}
}

func reconcilerFor(st controller.DiagnosisStore, prov agent.Provider) *controller.KscribeDiagnosisReconciler {
	return &controller.KscribeDiagnosisReconciler{
		Scheme:        testScheme(),
		Store:         st,
		AgentProvider: prov,
		MaxIter:       3,
	}
}

func buildClient(scheme *runtime.Scheme, obj *kscribev1alpha1.KscribeDiagnosis) *fake.ClientBuilder {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kscribev1alpha1.KscribeDiagnosis{}).
		WithObjects(obj)
}

// ---- tests ----

// TestReconcile_SuccessWritesSQLiteBeforeDone proves:
// (a) InsertDiagnosis is called
// (b) CR reaches Done
// (c) audit fields are set on the CR
func TestReconcile_SuccessWritesSQLiteBeforeDone(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-ok", "default")
	fc := buildClient(scheme, kd).Build()

	st := &fakeStore{}
	r := reconcilerFor(st, goodProvider())
	r.Client = fc

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-ok", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	// InsertDiagnosis must have been called once.
	if st.insertCalled != 1 {
		t.Fatalf("want insertCalled=1, got %d", st.insertCalled)
	}

	// CR must be Done with audit fields set.
	var got kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-ok", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if got.Status.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("want Done, got %s", got.Status.Phase)
	}
	if got.Status.TokensUsed != 42 {
		t.Fatalf("want TokensUsed=42, got %d", got.Status.TokensUsed)
	}
	if !got.Status.Persisted {
		t.Fatal("want Persisted=true")
	}
	if got.Status.Summary == "" {
		t.Fatal("want non-empty Summary")
	}
}

// TestReconcile_SQLiteFailureKeepsDiagnosing is the ADR-003 gate test:
// when InsertDiagnosis fails, the reconciler MUST:
//   - return an error or set RequeueAfter (never silently swallow it)
//   - NOT advance CR to Done or Partial
//   - set Persisted condition to False
func TestReconcile_SQLiteFailureKeepsDiagnosing(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-fail", "default")
	fc := buildClient(scheme, kd).Build()

	st := &fakeStore{insertErr: errors.New("disk full")}
	r := reconcilerFor(st, goodProvider())
	r.Client = fc

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-fail", Namespace: "default"},
	})

	// Must signal requeue via error or RequeueAfter.
	if err == nil && result.RequeueAfter == 0 {
		t.Fatal("expected error or RequeueAfter when SQLite write fails; got neither")
	}

	// CR must NOT be Done or Partial.
	var got kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-fail", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	switch got.Status.Phase {
	case kscribev1alpha1.DiagnosisPhaseDone, kscribev1alpha1.DiagnosisPhasePartial:
		t.Fatalf("CR must not be Done/Partial after SQLite failure, got %s", got.Status.Phase)
	}

	// Persisted condition must be False (or absent — but must not be True).
	for _, c := range got.Status.Conditions {
		if c.Type == kscribev1alpha1.ConditionPersisted && c.Status == metav1.ConditionTrue {
			t.Fatal("Persisted condition must not be True after SQLite failure")
		}
	}
}

// TestReconcile_IdempotentOnNonPending proves the reconciler skips CRs not in Pending/empty phase.
func TestReconcile_IdempotentOnNonPending(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-done", "default")
	kd.Status.Phase = kscribev1alpha1.DiagnosisPhaseDone
	fc := buildClient(scheme, kd).Build()

	st := &fakeStore{}
	r := reconcilerFor(st, goodProvider())
	r.Client = fc

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-done", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Store must not have been touched.
	if st.upsertCalled != 0 || st.insertCalled != 0 {
		t.Fatalf("store must not be called for non-Pending CR; upsert=%d insert=%d",
			st.upsertCalled, st.insertCalled)
	}
}
