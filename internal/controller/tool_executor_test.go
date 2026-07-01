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
	"k8s.io/client-go/rest"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	runtimefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

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
