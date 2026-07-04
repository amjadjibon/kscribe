package controller_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/amjadjibon/kscribe/internal/controller"
)

// TestPruneDiagnosisCRs verifies only terminal CRs older than the cutoff are
// deleted: old Done goes, recent Done stays, old Pending stays.
func TestPruneDiagnosisCRs(t *testing.T) {
	old := metav1.Time{Time: time.Now().Add(-48 * time.Hour)}
	recent := metav1.Time{Time: time.Now()}

	oldDone := newKD("old-done", "default")
	oldDone.Status.Phase = kscribev1alpha1.DiagnosisPhaseDone
	oldDone.Status.CompletedAt = &old

	newDone := newKD("new-done", "default")
	newDone.Status.Phase = kscribev1alpha1.DiagnosisPhaseDone
	newDone.Status.CompletedAt = &recent

	oldPending := newKD("old-pending", "default")
	oldPending.CreationTimestamp = old
	oldPending.Status.Phase = kscribev1alpha1.DiagnosisPhasePending

	fc := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithStatusSubresource(&kscribev1alpha1.KscribeDiagnosis{}).
		WithObjects(oldDone, newDone, oldPending).
		Build()

	deleted, err := controller.PruneDiagnosisCRs(context.Background(), fc, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneDiagnosisCRs: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	var kd kscribev1alpha1.KscribeDiagnosis
	if err := fc.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "old-done"}, &kd); err == nil {
		t.Error("old-done should have been deleted")
	}
	for _, name := range []string{"new-done", "old-pending"} {
		if err := fc.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &kd); err != nil {
			t.Errorf("%s should survive pruning: %v", name, err)
		}
	}
}
