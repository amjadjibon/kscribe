package enricher_test

import (
	"context"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	runtimefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/amjadjibon/kscribe/internal/enricher"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	return sch
}

// TestBuildSnapshot_PodNotFound verifies that a missing pod records the error in
// Partial and returns a non-nil snapshot without panicking (REQ-004).
func TestBuildSnapshot_PodNotFound(t *testing.T) {
	c := runtimefake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	kcs := k8sfake.NewClientset()

	s, err := enricher.BuildSnapshot(context.Background(), c, kcs, enricher.ObjectRef{
		Kind:      "Pod",
		Namespace: "default",
		Name:      "missing-pod",
		EventUID:  "uid-1",
		Reason:    "BackOff",
		Message:   "back-off restarting failed container",
	}, 0)

	if err != nil {
		t.Fatalf("BuildSnapshot returned error: %v", err)
	}
	if s == nil {
		t.Fatal("snapshot is nil")
	}
	if len(s.PodContexts) != 0 {
		t.Fatalf("expected no pod contexts, got %d", len(s.PodContexts))
	}
	if len(s.Partial) == 0 {
		t.Fatal("expected at least one Partial entry for missing pod")
	}
	if !strings.Contains(s.Partial[0], "missing-pod") {
		t.Fatalf("Partial[0] does not mention pod name: %q", s.Partial[0])
	}
}

// TestBuildSnapshot_PartialNodeMissing verifies that when the pod exists but
// its node does not, the pod context is still returned and the node collection
// failure is recorded in Partial (REQ-004).
func TestBuildSnapshot_PartialNodeMissing(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName:   "node-1", // node is NOT in the fake client
			Containers: []corev1.Container{{Name: "app"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	c := runtimefake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()
	kcs := k8sfake.NewClientset()

	s, err := enricher.BuildSnapshot(context.Background(), c, kcs, enricher.ObjectRef{
		Kind:      "Pod",
		Namespace: "default",
		Name:      "app-pod",
		EventUID:  "uid-2",
		Reason:    "OOMKilling",
		Message:   "OOM killed",
	}, 50)

	if err != nil {
		t.Fatalf("BuildSnapshot returned error: %v", err)
	}
	// Pod context must be present despite node fetch failing.
	if len(s.PodContexts) == 0 {
		t.Fatal("expected pod context to be present")
	}
	if s.PodContexts[0].PodName != "app-pod" {
		t.Fatalf("unexpected pod name: %q", s.PodContexts[0].PodName)
	}
	// Node fetch failure must be recorded in Partial.
	hasNodeErr := false
	for _, p := range s.Partial {
		if strings.Contains(p, "node-1") {
			hasNodeErr = true
			break
		}
	}
	if !hasNodeErr {
		t.Fatalf("expected a node error in Partial, got: %v", s.Partial)
	}
}

// TestRedact_SecretSamples asserts that each representative secret class is
// fully removed and replaced with RedactedPlaceholder (TASK-022 / SEC-001).
func TestRedact_SecretSamples(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string // substring that must be absent after redaction
	}{
		{
			"bearer token",
			"Authorization: Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.sig",
			"eyJhbGciOiJSUzI1NiJ9",
		},
		{
			"aws access key",
			"AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
			"AKIAIOSFODNN7EXAMPLE",
		},
		{
			"pem private key block",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Z3VS5JJcds3xHn/ygWep4\n-----END RSA PRIVATE KEY-----",
			"MIIEowIBAAKCAQEA0Z3VS5JJcds3xHn/ygWep4",
		},
		{
			"postgres connection string",
			"postgres://admin:s3cr3tpassword@db.example.com/mydb",
			"s3cr3tpassword",
		},
		{
			"basic-auth url",
			"http://proxyuser:hunter2@proxy.corp.local:3128",
			"hunter2",
		},
		{
			"password env var value",
			"DATABASE_PASSWORD=myS3cr3tValue",
			"myS3cr3tValue",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := enricher.Redact(tc.input)
			if strings.Contains(got, tc.secret) {
				t.Errorf("secret %q still present in output: %q", tc.secret, got)
			}
			if !strings.Contains(got, enricher.RedactedPlaceholder) {
				t.Errorf("placeholder %q not found in output: %q", enricher.RedactedPlaceholder, got)
			}
		})
	}
}

// TestEncodeSnapshot_RedactsBeforeSerialize verifies that secrets embedded in
// a Snapshot are absent from the serialized bytes (SEC-001 enforcement).
func TestEncodeSnapshot_RedactsBeforeSerialize(t *testing.T) {
	s := &enricher.Snapshot{
		Message: "postgres://admin:hunter2@db.local/prod",
		PodContexts: []enricher.PodContext{
			{
				PodName: "app",
				Logs: []enricher.PodLog{
					{
						ContainerName: "app",
						Lines:         "Authorization: Bearer supersecrettoken123",
					},
				},
				EnvVars: []enricher.EnvVar{
					{Name: "DB_PASSWORD", Value: "verysecret"},
				},
				Annotations: map[string]string{
					"example.io/config": "api_key=abcdef123456secret",
				},
			},
		},
	}

	b, err := enricher.EncodeSnapshot(s)
	if err != nil {
		t.Fatalf("EncodeSnapshot error: %v", err)
	}

	out := string(b)
	for _, secret := range []string{"hunter2", "supersecrettoken123", "verysecret", "abcdef123456secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q found in encoded snapshot output", secret)
		}
	}
	if !strings.Contains(out, enricher.RedactedPlaceholder) {
		t.Errorf("placeholder not found in encoded output: %s", out)
	}
}

// TestNoEncodingJSON asserts that no non-test enricher source file imports
// "encoding/json" (CON-003: sonic only).
func TestNoEncodingJSON(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		if strings.Contains(string(b), `"encoding/json"`) {
			t.Errorf("file %s imports encoding/json — use github.com/bytedance/sonic (CON-003)", e.Name())
		}
	}
}
