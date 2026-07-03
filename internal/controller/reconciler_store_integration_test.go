package controller_test

// Integration test: reconcile → real SQLite store → ListIncidents/GetIncident
// Verifies the reconcile→store→web-read path is consistent end-to-end.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/amjadjibon/kscribe/internal/controller"
	"github.com/amjadjibon/kscribe/internal/store"
)

// TestReconcile_WithRealStore drives the reconcile→SQLite→query path end-to-end:
//   - CR transitions Pending → Done
//   - SQLite incident row has the right phase, TokensUsed, and Persisted=true
//   - SQLite diagnosis row has non-empty Summary and RootCause
//   - ListIncidents and GetIncident both return the incident (dashboard consistency)
func TestReconcile_WithRealStore(t *testing.T) {
	// Open a real SQLite store against a temp file — no mocks, no mocking the store.
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	scheme := testScheme()
	kd := newKD("diag-integrated", "default")
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kscribev1alpha1.KscribeDiagnosis{}).
		WithObjects(kd).
		Build()

	r := &controller.KscribeDiagnosisReconciler{
		Client:        fc,
		Scheme:        scheme,
		Store:         st, // real store
		AgentProvider: goodProvider(),
		LLMProvider:   "openai",
		LLMModel:      "gpt-4o-mini",
		MaxIter:       3,
	}

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-integrated", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// CR must be Done.
	var got kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-integrated", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if got.Status.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("CR phase = %s, want Done", got.Status.Phase)
	}

	// ListIncidents must surface the incident written during reconcile.
	incidents, err := st.ListIncidents(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	var inc *store.Incident
	for i := range incidents {
		if incidents[i].Name == "diag-integrated" {
			inc = &incidents[i]
			break
		}
	}
	if inc == nil {
		t.Fatal("ListIncidents: incident 'diag-integrated' not found")
	}
	if inc.Phase != string(kscribev1alpha1.DiagnosisPhaseDone) {
		t.Errorf("incident phase = %q, want Done", inc.Phase)
	}
	if inc.TokensUsed != 42 {
		t.Errorf("incident TokensUsed = %d, want 42", inc.TokensUsed)
	}
	if inc.LLMProvider != "openai" {
		t.Errorf("incident LLMProvider = %q, want openai", inc.LLMProvider)
	}
	if inc.LLMModel != "gpt-4o-mini" {
		t.Errorf("incident LLMModel = %q, want gpt-4o-mini", inc.LLMModel)
	}
	if inc.CreatedAt.IsZero() {
		t.Error("incident CreatedAt must be set")
	}
	if inc.InvolvedObjectKind != "Pod" {
		t.Errorf("incident InvolvedObjectKind = %q, want Pod", inc.InvolvedObjectKind)
	}
	if inc.InvolvedObjectName != "pod-1" {
		t.Errorf("incident InvolvedObjectName = %q, want pod-1", inc.InvolvedObjectName)
	}
	if inc.InvolvedObjectNamespace != "default" {
		t.Errorf("incident InvolvedObjectNamespace = %q, want default", inc.InvolvedObjectNamespace)
	}
	if inc.Reason != "BackOff" {
		t.Errorf("incident Reason = %q, want BackOff", inc.Reason)
	}
	if !inc.Persisted {
		t.Error("incident Persisted must be true")
	}

	// GetIncident must return the incident with at least one diagnosis row.
	detail, err := st.GetIncident(context.Background(), "default", "diag-integrated")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if len(detail.Diagnoses) == 0 {
		t.Fatal("GetIncident: want ≥1 diagnosis row, got 0")
	}
	d := detail.Diagnoses[0]
	if d.Summary == "" {
		t.Error("diagnosis Summary must not be empty")
	}
	if d.RootCause == "" {
		t.Error("diagnosis RootCause must not be empty")
	}
}

// TestReconcile_ProviderFailed_StoreNotPersisted verifies that when the LLM
// provider fails, the incident is written to SQLite as Failed and Persisted=false.
func TestReconcile_ProviderFailed_StoreNotPersisted(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	scheme := testScheme()
	kd := newKD("diag-failed", "default")
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kscribev1alpha1.KscribeDiagnosis{}).
		WithObjects(kd).
		Build()

	r := &controller.KscribeDiagnosisReconciler{
		Client:        fc,
		Scheme:        scheme,
		Store:         st,
		AgentProvider: &fixedProvider{err: errors.New("provider unavailable")},
		MaxIter:       1,
	}

	_, _ = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-failed", Namespace: "default"},
	})

	incidents, err := st.ListIncidents(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	var inc *store.Incident
	for i := range incidents {
		if incidents[i].Name == "diag-failed" {
			inc = &incidents[i]
			break
		}
	}
	if inc == nil {
		t.Fatal("incident must be written to SQLite even on provider failure")
	}
	if inc.Phase != string(kscribev1alpha1.DiagnosisPhaseFailed) {
		t.Errorf("incident phase = %q, want Failed", inc.Phase)
	}
	if inc.Persisted {
		t.Error("Persisted must be false on provider failure")
	}
}
