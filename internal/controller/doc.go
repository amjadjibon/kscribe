// Package controller implements kscribe operator controllers.
// RBAC markers here drive config/rbac/role.yaml generation.
//
// +kubebuilder:rbac:groups=kscribe.amjadjibon.dev,resources=kscribediagnoses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kscribe.amjadjibon.dev,resources=kscribediagnoses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kscribe.amjadjibon.dev,resources=diagnosispolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events;pods;nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=apps,resources=deployments;replicasets,verbs=get;list;watch
package controller
