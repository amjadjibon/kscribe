// Package metrics registers kscribe's custom Prometheus collectors on the
// controller-runtime registry, served by the manager's metrics endpoint.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// DiagnosesTotal counts diagnoses that reached a terminal phase.
	// outcome is the lower-cased DiagnosisPhase: done, partial, or failed.
	DiagnosesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kscribe_diagnoses_total",
		Help: "Diagnoses finished, by terminal outcome (done, partial, failed).",
	}, []string{"outcome"})

	// LLMTokensTotal counts total LLM tokens consumed, per provider/model.
	LLMTokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kscribe_llm_tokens_total",
		Help: "Total LLM tokens consumed.",
	}, []string{"provider", "model"})

	// DiagnosesThrottledTotal counts diagnosis starts denied by the global
	// hourly rate limit (the CR stays Pending and requeues).
	DiagnosesThrottledTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "kscribe_diagnoses_throttled_total",
		Help: "Diagnosis starts denied by the hourly rate limit.",
	})

	// LLMRequestSeconds observes the wall-clock duration of one diagnosis
	// agent run (all provider round-trips of the tool loop included).
	LLMRequestSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kscribe_llm_request_seconds",
		Help:    "Duration of a full LLM diagnosis run in seconds.",
		Buckets: prometheus.ExponentialBuckets(0.25, 2, 10), // 0.25s .. 128s
	}, []string{"provider"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(DiagnosesTotal, DiagnosesThrottledTotal, LLMTokensTotal, LLMRequestSeconds)
}
