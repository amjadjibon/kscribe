package controller

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/amjadjibon/kscribe/internal/config"
)

// ResolvedPolicy is the effective policy for a given event namespace.
type ResolvedPolicy struct {
	Enabled       bool
	EventReasons  []string // nil means accept any reason
	LLMProvider   string
	LLMModel      string
	LLMBaseURL    string
	MaxIterations *int32
	Redact        *bool
	PolicyRef     string // name of the DiagnosisPolicy that was applied, empty if config defaults
}

// ResolvePolicy resolves the effective DiagnosisPolicy for eventNamespace (ADR-002):
//  1. Any DiagnosisPolicy in eventNamespace
//  2. DiagnosisPolicy named "default" in operatorNamespace
//  3. Operator config defaults
func ResolvePolicy(ctx context.Context, c client.Client, eventNamespace, operatorNamespace string, cfg config.Config) (ResolvedPolicy, error) {
	policy, found, err := findAnyPolicy(ctx, c, eventNamespace)
	if err != nil {
		return ResolvedPolicy{}, fmt.Errorf("lookup policy in %s: %w", eventNamespace, err)
	}
	if !found && operatorNamespace != "" && operatorNamespace != eventNamespace {
		policy, found, err = findNamedPolicy(ctx, c, operatorNamespace, "default")
		if err != nil {
			return ResolvedPolicy{}, fmt.Errorf("lookup default policy in %s: %w", operatorNamespace, err)
		}
	}
	if !found {
		return fromConfig(cfg), nil
	}
	return mergeWithConfig(policy, cfg), nil
}

// findAnyPolicy returns the first DiagnosisPolicy found in the given namespace.
func findAnyPolicy(ctx context.Context, c client.Client, namespace string) (*kscribev1alpha1.DiagnosisPolicy, bool, error) {
	var list kscribev1alpha1.DiagnosisPolicyList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, false, err
	}
	if len(list.Items) == 0 {
		return nil, false, nil
	}
	return &list.Items[0], true, nil
}

// findNamedPolicy returns the DiagnosisPolicy with the given name in namespace.
func findNamedPolicy(ctx context.Context, c client.Client, namespace, name string) (*kscribev1alpha1.DiagnosisPolicy, bool, error) {
	var policy kscribev1alpha1.DiagnosisPolicy
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &policy); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &policy, true, nil
}

func fromConfig(cfg config.Config) ResolvedPolicy {
	maxIter := int32(cfg.MaxIterations)
	redact := cfg.RedactEnabled
	return ResolvedPolicy{
		Enabled:       true,
		EventReasons:  cfg.EventReasonAllowlist,
		LLMProvider:   cfg.LLMProvider,
		LLMModel:      cfg.LLMModel,
		LLMBaseURL:    cfg.LLMBaseURL,
		MaxIterations: &maxIter,
		Redact:        &redact,
	}
}

func mergeWithConfig(p *kscribev1alpha1.DiagnosisPolicy, cfg config.Config) ResolvedPolicy {
	r := fromConfig(cfg)
	r.PolicyRef = p.Name
	if p.Spec.Enabled != nil {
		r.Enabled = *p.Spec.Enabled
	}
	if len(p.Spec.EventReasons) > 0 {
		r.EventReasons = p.Spec.EventReasons
	}
	if p.Spec.LLMProvider != "" {
		r.LLMProvider = p.Spec.LLMProvider
	}
	if p.Spec.LLMModel != "" {
		r.LLMModel = p.Spec.LLMModel
	}
	if p.Spec.LLMBaseURL != "" {
		r.LLMBaseURL = p.Spec.LLMBaseURL
	}
	if p.Spec.MaxIterations != nil {
		r.MaxIterations = p.Spec.MaxIterations
	}
	if p.Spec.Redact != nil {
		r.Redact = p.Spec.Redact
	}
	return r
}
