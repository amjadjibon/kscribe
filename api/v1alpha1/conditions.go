package v1alpha1

import (
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SetCondition upserts cond into status.Conditions.
// Transition time is only updated when the Status value changes,
// delegating that logic to apimeta.SetStatusCondition.
func SetCondition(status *KscribeDiagnosisStatus, cond metav1.Condition) {
	apimeta.SetStatusCondition(&status.Conditions, cond)
}

// SetPhase sets the Phase field and returns the status for chaining.
func SetPhase(status *KscribeDiagnosisStatus, phase DiagnosisPhase) {
	status.Phase = phase
}
