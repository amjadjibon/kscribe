package controller_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

type fakePublisher struct{ calls int }

func (p *fakePublisher) Publish(_, _ string) { p.calls++ }

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
		LLMProvider:   "openai",
		LLMModel:      "gpt-4o-mini",
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
	if got.Status.LLMProvider != "openai" {
		t.Fatalf("want LLMProvider=openai, got %q", got.Status.LLMProvider)
	}
	if got.Status.LLMModel != "gpt-4o-mini" {
		t.Fatalf("want LLMModel=gpt-4o-mini, got %q", got.Status.LLMModel)
	}
	if !got.Status.Persisted {
		t.Fatal("want Persisted=true")
	}
	if got.Status.Summary == "" {
		t.Fatal("want non-empty Summary")
	}
	last := st.incidents[len(st.incidents)-1]
	if last.LLMProvider != "openai" || last.LLMModel != "gpt-4o-mini" {
		t.Fatalf("store LLM fields = %q/%q, want openai/gpt-4o-mini", last.LLMProvider, last.LLMModel)
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

func TestReconcile_FreshDiagnosingDoesNotDuplicateDiagnosis(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-inflight", "default")
	kd.Status.Phase = kscribev1alpha1.DiagnosisPhaseDiagnosing
	kd.Status.StartedAt = &metav1.Time{Time: time.Now().UTC()}
	kd.Status.Persisted = false
	fc := buildClient(scheme, kd).Build()

	st := &fakeStore{}
	r := reconcilerFor(st, goodProvider())
	r.Client = fc

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-inflight", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if st.insertCalled != 0 {
		t.Fatalf("fresh Diagnosing reconcile must not insert duplicate diagnosis, got %d inserts", st.insertCalled)
	}
	if st.upsertCalled != 0 {
		t.Fatalf("fresh Diagnosing reconcile must not rewrite incident, got %d upserts", st.upsertCalled)
	}
}

// TestReconcile_SQLiteFailureThenRecovery proves HIGH-001:
// after InsertDiagnosis fails the CR is left Diagnosing/Persisted=false,
// and a subsequent reconcile retries the persist path and reaches Done.
func TestReconcile_SQLiteFailureThenRecovery(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-recover", "default")
	fc := buildClient(scheme, kd).Build()

	st := &fakeStore{insertErr: errors.New("disk full")}
	r := reconcilerFor(st, goodProvider())
	r.Client = fc

	// First reconcile: InsertDiagnosis fails.
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-recover", Namespace: "default"},
	})
	if err == nil && result.RequeueAfter == 0 {
		t.Fatal("expected error or RequeueAfter on first reconcile with store failure")
	}

	// CR should be Diagnosing and not persisted.
	var mid kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-recover", Namespace: "default"}, &mid); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if mid.Status.Phase != kscribev1alpha1.DiagnosisPhaseDiagnosing {
		t.Fatalf("after store failure: want Diagnosing, got %s", mid.Status.Phase)
	}
	if mid.Status.Persisted {
		t.Fatal("after store failure: Persisted must be false")
	}

	// Clear the store error — simulates transient failure resolved.
	st.insertErr = nil

	// Second reconcile: should succeed and reach Done.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-recover", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("second reconcile unexpected error: %v", err)
	}

	var got kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-recover", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get CR after recovery: %v", err)
	}
	if got.Status.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("after recovery: want Done, got %s", got.Status.Phase)
	}
	if !got.Status.Persisted {
		t.Fatal("after recovery: Persisted must be true")
	}
	// InsertDiagnosis should have been called twice (once failing, once succeeding).
	if st.insertCalled != 2 {
		t.Fatalf("want insertCalled=2, got %d", st.insertCalled)
	}
}

// TestReconcile_PublishesOnSuccess proves MED-002: a successful reconcile emits at least one SSE publish.
func TestReconcile_PublishesOnSuccess(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-pub", "default")
	fc := buildClient(scheme, kd).Build()

	pub := &fakePublisher{}
	st := &fakeStore{}
	r := reconcilerFor(st, goodProvider())
	r.Client = fc
	r.Publisher = pub

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-pub", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pub.calls == 0 {
		t.Fatal("expected at least one Publish call on successful diagnosis")
	}
}

// TestReconcile_ToolCallInvokedAndReachsDone proves that when the provider issues a tool call,
// the executor is invoked and the final CR still reaches Done (executor is wired).
func TestReconcile_ToolCallInvokedAndReachsDone(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-tool", "default")
	fc := buildClient(scheme, kd).Build()

	// spyExecutor records that Execute was called; always returns a safe string.
	spy := &spyExecutor{result: "log output"}

	// toolCallProvider: first call returns a tool-call message; second returns RCA.
	tcProv := &toolCallProvider{
		toolResp: agent.Response{
			Choices: []agent.Choice{{
				Message: agent.Message{
					Role: "assistant",
					ToolCalls: []agent.ToolCall{{
						ID:   "tc-1",
						Type: "function",
						Function: agent.FunctionCall{
							Name:      "get_pod_logs",
							Arguments: `{"namespace":"default","pod":"pod-1"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
			Usage: agent.Usage{TotalTokens: 10},
		},
		rcaResp: agent.Response{
			Choices: []agent.Choice{{
				Message:      agent.Message{Role: "assistant", Content: goodRCA},
				FinishReason: "stop",
			}},
			Usage: agent.Usage{TotalTokens: 20},
		},
	}

	st := &fakeStore{}
	r := &controller.KscribeDiagnosisReconciler{
		Client:        fc,
		Scheme:        scheme,
		Store:         st,
		AgentProvider: tcProv,
		ToolExecutor:  spy,
		Tools:         agent.KubeTools(),
		MaxIter:       5,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-tool", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	if spy.calls == 0 {
		t.Fatal("expected executor to be invoked for tool call; got 0 calls")
	}

	var got kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-tool", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if got.Status.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("want Done, got %s", got.Status.Phase)
	}
}

// spyExecutor records Execute invocations and returns a fixed result.
type spyExecutor struct {
	calls  int
	result string
}

func (s *spyExecutor) Execute(_ context.Context, _, _ string) (string, error) {
	s.calls++
	return s.result, nil
}

// toolCallProvider returns toolResp on first Complete, rcaResp on subsequent calls.
type toolCallProvider struct {
	toolResp agent.Response
	rcaResp  agent.Response
	called   int
}

func (p *toolCallProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	p.called++
	if p.called == 1 {
		return p.toolResp, nil
	}
	return p.rcaResp, nil
}

// TestReconcile_MirrorsTerminalIncidentMetadata proves terminal CRs do not rerun diagnosis,
// but still refresh the SQLite incident mirror from the CR spec/status.
func TestReconcile_MirrorsTerminalIncidentMetadata(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-done", "default")
	kd.Status.Phase = kscribev1alpha1.DiagnosisPhaseDone
	kd.Status.TokensUsed = 99
	kd.Status.Persisted = true
	fc := buildClient(scheme, kd).Build()

	st := &fakeStore{}
	r := reconcilerFor(st, &fixedProvider{err: errors.New("provider should not be called")})
	r.Client = fc

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-done", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.upsertCalled != 1 || st.insertCalled != 0 {
		t.Fatalf("terminal CR should only upsert incident mirror; upsert=%d insert=%d",
			st.upsertCalled, st.insertCalled)
	}
	got := st.incidents[0]
	if got.InvolvedObjectKind != "Pod" || got.InvolvedObjectName != "pod-1" || got.Reason != "BackOff" {
		t.Fatalf("terminal mirror missing spec metadata: %+v", got)
	}
	if got.Phase != "Done" || got.TokensUsed != 99 || !got.Persisted {
		t.Fatalf("terminal mirror missing status metadata: %+v", got)
	}
}

// TestReconcile_PersistsContextReasoningTrace asserts that the three new fields
// (ContextJSON, Reasoning, TraceJSON) are populated on the Diagnosis passed to InsertDiagnosis.
func TestReconcile_PersistsContextReasoningTrace(t *testing.T) {
	const rcaWithReasoning = `{"summary":"test summary","rootCause":"test cause","confidence":0.9,"reasoning":"based on repeated OOM events"}`
	scheme := testScheme()
	kd := newKD("diag-crt", "default")
	fc := buildClient(scheme, kd).Build()

	prov := &fixedProvider{resp: agent.Response{
		Choices: []agent.Choice{{
			Message:      agent.Message{Role: "assistant", Content: rcaWithReasoning},
			FinishReason: "stop",
		}},
		Usage: agent.Usage{TotalTokens: 10},
	}}

	st := &fakeStore{}
	r := reconcilerFor(st, prov)
	r.Client = fc

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-crt", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if st.insertCalled != 1 {
		t.Fatalf("want insertCalled=1, got %d", st.insertCalled)
	}

	d := st.diagnoses[0]
	if len(d.ContextJSON) == 0 {
		t.Error("ContextJSON must be non-empty")
	}
	if d.Reasoning != "based on repeated OOM events" {
		t.Errorf("Reasoning = %q, want 'based on repeated OOM events'", d.Reasoning)
	}
	// No tool calls → TraceJSON must be the marshalled empty slice "[]".
	if string(d.TraceJSON) != "[]" {
		t.Errorf("TraceJSON = %q, want []", d.TraceJSON)
	}
}

// capturingProvider records every Request passed to Complete and returns a fixed response.
type capturingProvider struct {
	requests []agent.Request
	resp     agent.Response
}

func (p *capturingProvider) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	p.requests = append(p.requests, req)
	return p.resp, nil
}

// enrichedScheme returns a scheme with both client-go core types (corev1 etc.) and kscribe CRDs,
// so the fake controller-runtime client can list Events and serve KD status sub-resources.
func enrichedScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(sch)  // corev1, appsv1, …
	_ = kscribev1alpha1.AddToScheme(sch) // KscribeDiagnosis
	return sch
}

// TestReconcile_EnrichedSnapshotFeedsEvents proves the KubeClient != nil branch runs
// BuildSnapshot, collects the seeded related Event, and its Reason appears in the prompt
// sent to the provider — confirming the enriched path (not the fallback) executed.
func TestReconcile_EnrichedSnapshotFeedsEvents(t *testing.T) {
	sch := enrichedScheme(t)
	kd := newKD("diag-enrich", "default")

	seededEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "ev-seeded", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Name:      "pod-1", // matches kd.Spec.InvolvedObjectName
			Namespace: "default",
		},
		Reason:  "SeededOOMReason",
		Message: "seeded event detail",
		Count:   1,
	}

	fc := buildClient(sch, kd).WithObjects(seededEvent).Build()

	cap := &capturingProvider{resp: agent.Response{
		Choices: []agent.Choice{{
			Message:      agent.Message{Role: "assistant", Content: goodRCA},
			FinishReason: "stop",
		}},
		Usage: agent.Usage{TotalTokens: 5},
	}}

	st := &fakeStore{}
	r := &controller.KscribeDiagnosisReconciler{
		Client:        fc,
		Scheme:        sch,
		Store:         st,
		AgentProvider: cap,
		MaxIter:       3,
		KubeClient:    fakeLogServer(t, ""), // non-nil → triggers BuildSnapshot branch
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-enrich", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	// CR must reach Done.
	var got kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-enrich", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if got.Status.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("want Done, got %s", got.Status.Phase)
	}
	if !got.Status.Persisted {
		t.Fatal("want Persisted=true")
	}

	// The seeded event Reason must appear in the captured prompt (enriched branch ran).
	var prompt strings.Builder
	for _, req := range cap.requests {
		for _, msg := range req.Messages {
			prompt.WriteString(msg.Content)
		}
	}
	if !strings.Contains(prompt.String(), "SeededOOMReason") {
		t.Errorf("SeededOOMReason not found in prompt — enriched snapshot branch did not run; prompt: %q",
			prompt.String())
	}
}

// TestReconcile_ProviderFailure_ConflictSafeReachFailed proves the retry-storm fix:
// when the first Status().Update for the Failed transition hits an optimistic-lock conflict,
// patchStatus retries and the CR still lands on Failed — returning nil so there is no requeue.
func TestReconcile_ProviderFailure_ConflictSafeReachFailed(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-conflict", "default")

	// failingProvider always returns a provider error → outcome.Phase == Failed.
	failProv := &fixedProvider{err: errors.New("provider 401 unauthorized")}

	// The interceptor fires a Conflict error on the very first SubResource Update (status),
	// then delegates all subsequent calls to the real fake client.
	var conflictFired atomic.Bool
	fc := buildClient(scheme, kd).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if subResourceName == "status" && !conflictFired.Swap(true) {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "kscribe.io", Resource: "kscribediagnoses"},
						obj.GetName(),
						errors.New("test-injected conflict"),
					)
				}
				return c.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
		}).
		Build()

	st := &fakeStore{}
	r := reconcilerFor(st, failProv)
	r.Client = fc

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-conflict", Namespace: "default"},
	})

	// patchStatus must have retried through the conflict → nil error, no requeue storm.
	if err != nil {
		t.Fatalf("Reconcile returned error after conflict retry: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("Reconcile must not requeue on provider failure: %+v", result)
	}

	// The injected conflict must have fired at least once.
	if !conflictFired.Load() {
		t.Fatal("conflict interceptor never fired — test did not exercise the retry path")
	}

	// CR must have reached Failed (not stuck on Diagnosing).
	var got kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-conflict", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if got.Status.Phase != kscribev1alpha1.DiagnosisPhaseFailed {
		t.Fatalf("want Failed, got %s — patchStatus did not retry through the conflict", got.Status.Phase)
	}
}

// TestReconcile_RateLimited proves a throttled diagnosis stays Pending with a
// requeue and never reaches the provider or the store.
func TestReconcile_RateLimited(t *testing.T) {
	scheme := testScheme()
	kd := newKD("diag-throttled", "default")
	fc := buildClient(scheme, kd).Build()

	st := &fakeStore{}
	prov := goodProvider()
	r := reconcilerFor(st, prov)
	r.Client = fc
	lim := controller.NewRateLimiter(1)
	lim.Allow() // exhaust the budget
	r.RateLimiter = lim

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "diag-throttled", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if res.RequeueAfter < 2*time.Minute || res.RequeueAfter > 5*time.Minute {
		t.Fatalf("RequeueAfter = %v, want between 2m and 5m", res.RequeueAfter)
	}
	if st.insertCalled != 0 {
		t.Fatalf("store must not be written when throttled; insertCalled=%d", st.insertCalled)
	}

	var got kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "diag-throttled", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if got.Status.Phase != kscribev1alpha1.DiagnosisPhasePending {
		t.Fatalf("throttled CR phase = %s, want Pending", got.Status.Phase)
	}
	var reason string
	for _, c := range got.Status.Conditions {
		if c.Type == kscribev1alpha1.ConditionDiagnosed {
			reason = c.Reason
		}
	}
	if reason != "RateLimited" {
		t.Fatalf("want Diagnosed condition with reason RateLimited, got %q", reason)
	}
}
