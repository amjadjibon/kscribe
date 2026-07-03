package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
)

// PruneDiagnosisCRs deletes KscribeDiagnosis CRs that reached a terminal
// phase (Done, Partial, Failed) before cutoff. Age is status.completedAt,
// falling back to the creation timestamp. Returns the number deleted plus
// any delete errors (joined) so the caller can log them; failed deletes are
// retried on the next sweep.
func PruneDiagnosisCRs(ctx context.Context, c client.Client, cutoff time.Time) (int, error) {
	var list kscribev1alpha1.KscribeDiagnosisList
	if err := c.List(ctx, &list); err != nil {
		return 0, err
	}
	deleted := 0
	var errs []error
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
			errs = append(errs, fmt.Errorf("delete %s/%s: %w", d.Namespace, d.Name, err))
			continue
		}
		deleted++
	}
	return deleted, errors.Join(errs...)
}
