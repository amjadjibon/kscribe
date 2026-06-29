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

// TestDecodeSnapshot_RoundTrip verifies that DecodeSnapshot is the inverse of
// EncodeSnapshot for non-sensitive fields (post-redaction values survive the
// encode/decode cycle intact).
func TestDecodeSnapshot_RoundTrip(t *testing.T) {
	original := &enricher.Snapshot{
		EventUID:   "uid-rt",
		Reason:     "BackOff",
		Message:    "container failed to start", // no secrets — survives redaction
		Namespace:  "default",
		ObjectKind: "Pod",
		ObjectName: "app-pod",
		Partial:    []string{"node-fetch-failed"},
		NodeConditions: []enricher.NodeCondition{
			{NodeName: "node-1", Type: "Ready", Status: "False", Message: "kubelet not ready"},
		},
	}

	encoded, err := enricher.EncodeSnapshot(original)
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	decoded, err := enricher.DecodeSnapshot(encoded)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}

	if decoded.EventUID != original.EventUID {
		t.Errorf("EventUID = %q, want %q", decoded.EventUID, original.EventUID)
	}
	if decoded.Reason != original.Reason {
		t.Errorf("Reason = %q, want %q", decoded.Reason, original.Reason)
	}
	if decoded.Namespace != original.Namespace {
		t.Errorf("Namespace = %q, want %q", decoded.Namespace, original.Namespace)
	}
	if len(decoded.Partial) != 1 || decoded.Partial[0] != "node-fetch-failed" {
		t.Errorf("Partial = %v, want [node-fetch-failed]", decoded.Partial)
	}
	if len(decoded.NodeConditions) != 1 || decoded.NodeConditions[0].NodeName != "node-1" {
		t.Errorf("NodeConditions = %v", decoded.NodeConditions)
	}
}

// TestDecodeSnapshot_InvalidJSON verifies DecodeSnapshot returns an error on garbage input.
func TestDecodeSnapshot_InvalidJSON(t *testing.T) {
	_, err := enricher.DecodeSnapshot([]byte("not-json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestRedact_AdditionalSecretPatterns covers secret types not in the original sample set:
// kubeconfig service-account token, GCP service-account PEM key embedded in JSON.
// These confirm the existing rules handle them — no new rules needed.
func TestRedact_AdditionalSecretPatterns(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{
			"kubeconfig token field",
			"token: eyJhbGciOiJSUzI1NiIsImtpZCI6ImFiYyJ9.eyJzdWIiOiJzeXN0ZW0ifQ.sig",
			"eyJhbGciOiJSUzI1NiIsImtpZCI6ImFiYyJ9",
		},
		{
			"GCP service-account PEM key in JSON value",
			`"private_key": "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEAverylongkeydata\n-----END RSA PRIVATE KEY-----\n"`,
			"MIIEpAIBAAKCAQEAverylongkeydata",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := enricher.Redact(tc.input)
			if strings.Contains(got, tc.secret) {
				t.Errorf("secret %q still present after redaction: %q", tc.secret, got)
			}
			if !strings.Contains(got, enricher.RedactedPlaceholder) {
				t.Errorf("placeholder not found in redacted output: %q", got)
			}
		})
	}
}

// TestRedactSnapshot_DeploymentAndReplicaSetConditions verifies that
// DeploymentStatus and ReplicaSetStatus condition strings are redacted when
// they contain sensitive values (covers previously-uncovered branches in
// RedactSnapshot).
func TestRedactSnapshot_DeploymentAndReplicaSetConditions(t *testing.T) {
	s := &enricher.Snapshot{
		DeploymentStatus: &enricher.DeploymentStatus{
			Name:       "app",
			Conditions: []string{"reason: token=supersecrettoken123"},
		},
		ReplicaSetStatus: &enricher.ReplicaSetStatus{
			Name:       "app-rs",
			Conditions: []string{"message: password=hunter2"},
		},
	}
	enricher.RedactSnapshot(s)
	for _, cond := range s.DeploymentStatus.Conditions {
		if strings.Contains(cond, "supersecrettoken123") {
			t.Errorf("DeploymentStatus condition still contains secret: %q", cond)
		}
	}
	for _, cond := range s.ReplicaSetStatus.Conditions {
		if strings.Contains(cond, "hunter2") {
			t.Errorf("ReplicaSetStatus condition still contains secret: %q", cond)
		}
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
