package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
)

// PruneDiagnosisCRs deletes KscribeDiagnosis CRs that reached a terminal
// phase (Done, Partial, Failed) before cutoff. Age is status.completedAt,
// falling back to the creation timestamp. Returns the number deleted; a
// failed delete is skipped, not fatal (retried on the next sweep).
func PruneDiagnosisCRs(ctx context.Context, c client.Client, cutoff time.Time) (int, error) {
	var list kscribev1alpha1.KscribeDiagnosisList
	if err := c.List(ctx, &list); err != nil {
		return 0, err
	}
	deleted := 0
	for i := range list.Items {
		d := &list.Items[i]
		switch d.Status.Phase {
		case kscribev1alpha1.DiagnosisPhaseDone, kscribev1alpha1.DiagnosisPhasePartial, kscribev1alpha1.DiagnosisPhaseFailed:
		default:
			continue // only terminal phases are pruned
		}
		finished := d.CreationTimestamp.Time
		if d.Status.CompletedAt != nil {
			finished = d.Status.CompletedAt.Time
		}
		if finished.After(cutoff) {
			continue
		}
		if err := c.Delete(ctx, d); err != nil && !apierrors.IsNotFound(err) {
			continue
		}
		deleted++
	}
	return deleted, nil
}
