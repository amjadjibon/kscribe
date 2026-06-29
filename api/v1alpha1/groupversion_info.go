// Package v1alpha1 contains API Schema definitions for the kscribe.amjadjibon.dev v1alpha1 API group.
//
// +kubebuilder:object:generate=true
// +groupName=kscribe.amjadjibon.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group/version for kscribe API types.
	GroupVersion = schema.GroupVersion{Group: "kscribe.amjadjibon.dev", Version: "v1alpha1"}

	// SchemeBuilder registers kscribe types into a scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds kscribe types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&KscribeDiagnosis{},
		&KscribeDiagnosisList{},
		&DiagnosisPolicy{},
		&DiagnosisPolicyList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
