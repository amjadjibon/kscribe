package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetCondition_new(t *testing.T) {
	status := &KscribeDiagnosisStatus{}
	SetCondition(status, metav1.Condition{
		Type:               ConditionDiagnosing,
		Status:             metav1.ConditionTrue,
		Reason:             "Started",
		Message:            "diagnosis loop started",
		ObservedGeneration: 1,
	})
	if len(status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(status.Conditions))
	}
	if status.Conditions[0].Type != ConditionDiagnosing {
		t.Errorf("unexpected condition type: %s", status.Conditions[0].Type)
	}
}

func TestSetCondition_transitionTimeOnlyOnStatusChange(t *testing.T) {
	status := &KscribeDiagnosisStatus{}
	first := metav1.Condition{
		Type:    ConditionDiagnosed,
		Status:  metav1.ConditionFalse,
		Reason:  "Pending",
		Message: "not yet done",
	}
	SetCondition(status, first)
	t1 := status.Conditions[0].LastTransitionTime

	// Same status — transition time must NOT change.
	SetCondition(status, metav1.Condition{
		Type:    ConditionDiagnosed,
		Status:  metav1.ConditionFalse,
		Reason:  "StillPending",
		Message: "still not done",
	})
	if !status.Conditions[0].LastTransitionTime.Equal(&t1) {
		t.Error("LastTransitionTime changed despite same Status value")
	}

	// Status changes — transition time MUST change (or at least not be before t1).
	SetCondition(status, metav1.Condition{
		Type:    ConditionDiagnosed,
		Status:  metav1.ConditionTrue,
		Reason:  "Done",
		Message: "diagnosis complete",
	})
	t2 := status.Conditions[0].LastTransitionTime
	if t2.Before(&t1) {
		t.Error("LastTransitionTime regressed on status change")
	}
}

func TestSetPhase(t *testing.T) {
	status := &KscribeDiagnosisStatus{}
	for _, phase := range []DiagnosisPhase{
		DiagnosisPhasePending,
		DiagnosisPhaseDiagnosing,
		DiagnosisPhaseDone,
		DiagnosisPhasePartial,
		DiagnosisPhaseFailed,
	} {
		SetPhase(status, phase)
		if status.Phase != phase {
			t.Errorf("SetPhase: got %q, want %q", status.Phase, phase)
		}
	}
}
