// Package v1alpha1 contains API Schema definitions for the kscribe.amjadjibon.dev v1alpha1 API group.
//
// +kubebuilder:object:generate=true
// +groupName=kscribe.amjadjibon.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group/version for kscribe API types.
	GroupVersion = schema.GroupVersion{Group: "kscribe.amjadjibon.dev", Version: "v1alpha1"}

	// SchemeBuilder is used to register types with a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds kscribe types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
