package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	runtimefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/amjadjibon/kscribe/internal/controller"
	"github.com/amjadjibon/kscribe/internal/enricher"
)

// fakeLogServer returns a test HTTP server that serves logBody for pod log requests.
func fakeLogServer(t *testing.T, logBody string) kubernetes.Interface {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/log") {
			fmt.Fprint(w, logBody)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	cfg := &rest.Config{Host: srv.URL}
	kcs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build fake kube client: %v", err)
	}
	return kcs
}

func ctrlScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	return sch
}

// TestToolExecutor_GetPodLogs_RedactsSecret is the SEC-001 gate test:
// a secret in pod logs must be absent from the tool result.
func TestToolExecutor_GetPodLogs_RedactsSecret(t *testing.T) {
	secret := "hunter2"
	logBody := "starting app\nDATABASE_PASSWORD=" + secret + "\nlistening on :8080\n"

	kcs := fakeLogServer(t, logBody)
	fc := runtimefake.NewClientBuilder().WithScheme(ctrlScheme(t)).Build()

	exec := &controller.KubeToolExecutor{Client: fc, Kube: kcs}
	result, err := exec.Execute(context.Background(), "get_pod_logs",
		`{"namespace":"default","pod":"app-pod","container":"app"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, secret) {
		t.Errorf("secret %q still present in tool result: %q", secret, result)
	}
	if !strings.Contains(result, enricher.RedactedPlaceholder) {
		t.Errorf("placeholder not found in result: %q", result)
	}
}

// TestToolExecutor_GetPodLogs_TailCap asserts tail > 200 is capped to 200.
func TestToolExecutor_GetPodLogs_TailCap(t *testing.T) {
	kcs := fakeLogServer(t, "ok\n")
	fc := runtimefake.NewClientBuilder().WithScheme(ctrlScheme(t)).Build()

	exec := &controller.KubeToolExecutor{Client: fc, Kube: kcs}
	// Tail=999 — should not error; server returns static content regardless.
	_, err := exec.Execute(context.Background(), "get_pod_logs",
		`{"namespace":"default","pod":"app-pod","tail":999}`)
	if err != nil {
		t.Fatalf("unexpected error with large tail: %v", err)
	}
}

// TestToolExecutor_GetEvents_FormatAndRedact verifies event listing and message redaction.
func TestToolExecutor_GetEvents_FormatAndRedact(t *testing.T) {
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "ev-1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Name:      "pod-x",
			Namespace: "default",
		},
		Reason:  "BackOff",
		Message: "token=supersecretapikey123",
		Count:   3,
	}
	sch := ctrlScheme(t)
	fc := runtimefake.NewClientBuilder().WithScheme(sch).WithObjects(ev).Build()
	exec := &controller.KubeToolExecutor{
		Client: fc,
		Kube:   fakeLogServer(t, ""),
	}

	result, err := exec.Execute(context.Background(), "get_events",
		`{"namespace":"default","object_name":"pod-x"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "supersecretapikey123") {
		t.Errorf("secret still present in events result: %q", result)
	}
	if !strings.Contains(result, "BackOff") {
		t.Errorf("reason BackOff not in result: %q", result)
	}
	if !strings.Contains(result, "pod-x") {
		t.Errorf("object name not in result: %q", result)
	}
}

// TestToolExecutor_GetNode_ConditionsAndCapacity verifies node formatting.
func TestToolExecutor_GetNode_ConditionsAndCapacity(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Message: "kubelet healthy"},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}
	sch := ctrlScheme(t)
	fc := runtimefake.NewClientBuilder().WithScheme(sch).WithObjects(node).Build()
	exec := &controller.KubeToolExecutor{
		Client: fc,
		Kube:   fakeLogServer(t, ""),
	}

	result, err := exec.Execute(context.Background(), "get_node", `{"node_name":"node-1"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Ready") {
		t.Errorf("Ready condition not in result: %q", result)
	}
	if !strings.Contains(result, "Capacity") {
		t.Errorf("Capacity section not in result: %q", result)
	}
}

// TestToolExecutor_UnknownTool verifies unknown tool name returns an error.
func TestToolExecutor_UnknownTool(t *testing.T) {
	exec := &controller.KubeToolExecutor{}
	_, err := exec.Execute(context.Background(), "do_something_weird", `{}`)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

// TestToolExecutor_GetPodLogs_MalformedArgs asserts bad JSON args return an error.
func TestToolExecutor_GetPodLogs_MalformedArgs(t *testing.T) {
	exec := &controller.KubeToolExecutor{}
	_, err := exec.Execute(context.Background(), "get_pod_logs", `{bad json`)
	if err == nil {
		t.Fatal("expected error for malformed args")
	}
}

// streamErrorServer returns a kubernetes.Interface whose log endpoint always returns 500.
func streamErrorServer(t *testing.T) kubernetes.Interface {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"internal error","code":500}`)
	}))
	t.Cleanup(srv.Close)
	kcs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("build error kube client: %v", err)
	}
	return kcs
}

// TestToolExecutor_GetPodLogs_StreamError asserts a stream failure returns an error (no panic).
func TestToolExecutor_GetPodLogs_StreamError(t *testing.T) {
	fc := runtimefake.NewClientBuilder().WithScheme(ctrlScheme(t)).Build()
	exec := &controller.KubeToolExecutor{Client: fc, Kube: streamErrorServer(t)}
	_, err := exec.Execute(context.Background(), "get_pod_logs",
		`{"namespace":"default","pod":"crash-pod"}`)
	if err == nil {
		t.Fatal("expected error when log stream fails")
	}
}

// TestToolExecutor_GetEvents_NoFilterAndCap seeds 35 events, calls get_events with no
// object_name filter, asserts output is capped at 30 lines and a secret is redacted.
func TestToolExecutor_GetEvents_NoFilterAndCap(t *testing.T) {
	sch := ctrlScheme(t)
	objs := make([]client.Object, 35)
	for i := range objs {
		msg := fmt.Sprintf("normal event %d", i)
		if i == 0 {
			msg = "token=supersecretvalue999" // secret in first event
		}
		objs[i] = &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("ev-%d", i),
				Namespace: "default",
			},
			InvolvedObject: corev1.ObjectReference{
				Name:      fmt.Sprintf("obj-%d", i%5), // mix of object names
				Namespace: "default",
			},
			Reason:  "SomeReason",
			Message: msg,
			Count:   1,
		}
	}
	fc := runtimefake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build()
	exec := &controller.KubeToolExecutor{Client: fc, Kube: fakeLogServer(t, "")}

	result, err := exec.Execute(context.Background(), "get_events", `{"namespace":"default"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Count(result, "\n")
	if lines > 30 {
		t.Errorf("expected <=30 event lines, got %d", lines)
	}
	if strings.Contains(result, "supersecretvalue999") {
		t.Errorf("secret still present in events output: %q", result)
	}
	// Multiple object names should appear (no object_name filter applied).
	hasMultiple := strings.Contains(result, "obj-0") && strings.Contains(result, "obj-1")
	if !hasMultiple {
		t.Errorf("expected events from multiple objects; got: %q", result)
	}
}

// TestToolExecutor_GetEvents_MalformedArgs asserts bad JSON returns an error.
func TestToolExecutor_GetEvents_MalformedArgs(t *testing.T) {
	exec := &controller.KubeToolExecutor{}
	_, err := exec.Execute(context.Background(), "get_events", `{bad json`)
	if err == nil {
		t.Fatal("expected error for malformed args")
	}
}

// TestToolExecutor_GetNode_NotFound asserts an error when the node is absent.
func TestToolExecutor_GetNode_NotFound(t *testing.T) {
	fc := runtimefake.NewClientBuilder().WithScheme(ctrlScheme(t)).Build()
	exec := &controller.KubeToolExecutor{Client: fc, Kube: fakeLogServer(t, "")}
	_, err := exec.Execute(context.Background(), "get_node", `{"node_name":"missing-node"}`)
	if err == nil {
		t.Fatal("expected error for missing node")
	}
}

// TestToolExecutor_GetNode_MalformedArgs asserts bad JSON returns an error.
func TestToolExecutor_GetNode_MalformedArgs(t *testing.T) {
	exec := &controller.KubeToolExecutor{}
	_, err := exec.Execute(context.Background(), "get_node", `{bad json`)
	if err == nil {
		t.Fatal("expected error for malformed args")
	}
}
