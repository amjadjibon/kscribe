package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/amjadjibon/kscribe/internal/agent"
	"github.com/amjadjibon/kscribe/internal/enricher"
	"github.com/amjadjibon/kscribe/internal/store"
	"github.com/amjadjibon/kscribe/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "dashboard listen address")
	flag.Parse()

	broker := web.NewBroker()
	srv := web.New(newDevStore(), broker, devProvider{})

	log.Printf("kscribe dashboard dev server listening on http://%s", *addr)
	log.Printf("open http://%s/incidents/default/crashloop-demo for chat UI testing", *addr)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

type devStore struct {
	mu           sync.Mutex
	incidents    map[string]*store.IncidentDetail
	orderedKeys  []string
	chatMessages []store.ChatMessage
	nextChatID   int64
}

func newDevStore() *devStore {
	now := time.Now().UTC()
	started := now.Add(-12 * time.Minute)
	completed := now.Add(-9 * time.Minute)

	crashSnap := &enricher.Snapshot{
		EventUID:   "event-crashloop-demo",
		Reason:     "BackOff",
		Message:    "Back-off restarting failed container api in pod api-7d9c",
		Namespace:  "default",
		ObjectKind: "Pod",
		ObjectName: "api-7d9c",
		PodContexts: []enricher.PodContext{{
			PodName:  "api-7d9c",
			NodeName: "kind-worker",
			Phase:    "Running",
			EnvVars: []enricher.EnvVar{{
				Name:  "DATABASE_URL",
				Value: enricher.RedactedPlaceholder,
			}},
			Logs: []enricher.PodLog{{
				ContainerName: "api",
				Lines:         "2026-07-03T07:42:11Z starting api\n2026-07-03T07:42:12Z failed to connect to DATABASE_URL\n2026-07-03T07:42:12Z exiting",
			}},
		}},
		RelatedEvents: []enricher.EventSummary{
			{Name: "api-backoff", Reason: "BackOff", Message: "Back-off restarting failed container", Count: 12},
			{Name: "api-pulled", Reason: "Pulled", Message: "Container image already present", Count: 3},
		},
		Partial: []string{"node conditions unavailable in dev fixture"},
	}
	crashContext, _ := enricher.EncodeSnapshot(crashSnap)

	oomSnap := &enricher.Snapshot{
		EventUID:   "event-oom-demo",
		Reason:     "OOMKilling",
		Message:    "Container worker exceeded memory limit",
		Namespace:  "payments",
		ObjectKind: "Pod",
		ObjectName: "worker-54b8",
		PodContexts: []enricher.PodContext{{
			PodName:  "worker-54b8",
			NodeName: "kind-worker2",
			Phase:    "Failed",
			EnvVars: []enricher.EnvVar{{
				Name:  "BATCH_SIZE",
				Value: "5000",
			}},
		}},
	}
	oomContext, _ := enricher.EncodeSnapshot(oomSnap)

	st := &devStore{
		incidents: map[string]*store.IncidentDetail{
			"default/crashloop-demo": {
				Incident: store.Incident{
					Namespace: "default", Name: "crashloop-demo", EventUID: "event-crashloop-demo",
					InvolvedObjectKind: "Pod", InvolvedObjectName: "api-7d9c", InvolvedObjectNamespace: "default",
					Reason: "BackOff", Message: "Back-off restarting failed container api in pod api-7d9c",
					Phase: "Done", StartedAt: &started, CompletedAt: &completed,
					LLMProvider: "dev", LLMModel: "fixture-stream", TokensUsed: 742,
					PromptRedacted: true, Persisted: true, CreatedAt: started, UpdatedAt: completed,
				},
				Diagnoses: []store.Diagnosis{{
					ID: 1, Namespace: "default", Name: "crashloop-demo", EventUID: "event-crashloop-demo",
					Summary:     "The API pod is restarting because it exits immediately after failing to read database configuration.",
					RootCause:   "Missing or invalid `DATABASE_URL` environment configuration.",
					Remediation: "Verify the Secret mounted into the deployment; restart the rollout after updating the value; add startup validation to fail with a clearer message.",
					Confidence:  0.91, CreatedAt: completed, ContextJSON: crashContext,
					Reasoning: "The recent pod logs show startup followed by a database configuration error. The Kubernetes event reason is `BackOff`, which matches repeated container restarts.",
					TraceJSON: []byte(`[{"tool":"get_pod_logs","args":{"pod":"api-7d9c"},"result":"failed to connect to DATABASE_URL"},{"tool":"list_events","args":{"object_name":"api-7d9c"},"result":"BackOff x12"}]`),
				}},
			},
			"payments/oom-demo": {
				Incident: store.Incident{
					Namespace: "payments", Name: "oom-demo", EventUID: "event-oom-demo",
					InvolvedObjectKind: "Pod", InvolvedObjectName: "worker-54b8", InvolvedObjectNamespace: "payments",
					Reason: "OOMKilling", Message: "Container worker exceeded memory limit",
					Phase: "Partial", StartedAt: ptrTime(now.Add(-28 * time.Minute)),
					LLMProvider: "dev", LLMModel: "fixture-stream", TokensUsed: 318,
					PromptRedacted: true, Persisted: true, CreatedAt: now.Add(-30 * time.Minute), UpdatedAt: now.Add(-22 * time.Minute),
				},
				Diagnoses: []store.Diagnosis{{
					ID: 2, Namespace: "payments", Name: "oom-demo", EventUID: "event-oom-demo",
					Summary:     "Worker memory limit is too low for the observed batch size.",
					RootCause:   "Memory pressure during payment batch processing.",
					Remediation: "Reduce batch size or raise the container memory limit after checking node capacity.",
					Confidence:  0.76, CreatedAt: now.Add(-22 * time.Minute), ContextJSON: oomContext,
					Reasoning: "The event reason is `OOMKilling` and the container state reports `OOMKilled`.",
					TraceJSON: []byte(`[]`),
				}},
			},
			"infra/pending-demo": {
				Incident: store.Incident{
					Namespace: "infra", Name: "pending-demo",
					InvolvedObjectKind: "Pod", InvolvedObjectName: "scheduler-test", InvolvedObjectNamespace: "infra",
					Reason: "FailedScheduling", Message: "0/3 nodes are available: insufficient cpu",
					Phase: "Pending", CreatedAt: now.Add(-7 * time.Minute), UpdatedAt: now.Add(-7 * time.Minute),
				},
			},
		},
		orderedKeys: []string{"default/crashloop-demo", "payments/oom-demo", "infra/pending-demo"},
		nextChatID:  3,
	}
	st.chatMessages = []store.ChatMessage{
		{ID: 1, Namespace: "default", Name: "crashloop-demo", Role: "user", Content: "What should I check first?", CreatedAt: now.Add(-6 * time.Minute)},
		{ID: 2, Namespace: "default", Name: "crashloop-demo", Role: "assistant", Content: "Start with the deployment's environment and Secret references. The logs point at missing database configuration before the container exits.", CreatedAt: now.Add(-5 * time.Minute)},
	}
	return st
}

func (s *devStore) ListIncidents(ctx context.Context, limit int) ([]store.Incident, error) {
	return s.ListIncidentsPage(ctx, store.IncidentFilter{}, limit, 0)
}

func (s *devStore) ListIncidentsPage(_ context.Context, filter store.IncidentFilter, limit, offset int) ([]store.Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	all := s.filteredIncidents(filter)
	if offset >= len(all) {
		return nil, nil
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return append([]store.Incident(nil), all...), nil
}

func (s *devStore) CountIncidents(_ context.Context, filter store.IncidentFilter) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.filteredIncidents(filter)), nil
}

func (s *devStore) CountIncidentsByPhase(_ context.Context, filter store.IncidentFilter) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	noPhase := filter
	noPhase.Phase = ""
	counts := make(map[string]int)
	for _, inc := range s.filteredIncidents(noPhase) {
		counts[inc.Phase]++
	}
	return counts, nil
}

func (s *devStore) GetIncident(_ context.Context, namespace, name string) (*store.IncidentDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.incidents[namespace+"/"+name]
	if !ok {
		return nil, errors.New("incident not found")
	}
	cp := *d
	cp.Diagnoses = append([]store.Diagnosis(nil), d.Diagnoses...)
	return &cp, nil
}

func (s *devStore) AppendChatMessage(_ context.Context, namespace, name, role, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatMessages = append(s.chatMessages, store.ChatMessage{
		ID: s.nextChatID, Namespace: namespace, Name: name, Role: role, Content: content, CreatedAt: time.Now().UTC(),
	})
	s.nextChatID++
	return nil
}

func (s *devStore) ListChatMessages(_ context.Context, namespace, name string) ([]store.ChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.ChatMessage
	for _, msg := range s.chatMessages {
		if msg.Namespace == namespace && msg.Name == name {
			out = append(out, msg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *devStore) filteredIncidents(filter store.IncidentFilter) []store.Incident {
	out := make([]store.Incident, 0, len(s.orderedKeys))
	for _, key := range s.orderedKeys {
		d := s.incidents[key]
		if d == nil {
			continue
		}
		inc := d.Incident
		if filter.Phase != "" && inc.Phase != filter.Phase {
			continue
		}
		if filter.Namespace != "" && inc.Namespace != filter.Namespace {
			continue
		}
		if filter.Reason != "" && inc.Reason != filter.Reason {
			continue
		}
		if filter.Query != "" {
			q := strings.ToLower(filter.Query)
			if !strings.Contains(strings.ToLower(inc.Name), q) &&
				!strings.Contains(strings.ToLower(inc.Message), q) &&
				!strings.Contains(strings.ToLower(inc.Reason), q) {
				continue
			}
		}
		out = append(out, inc)
	}
	return out
}

type devProvider struct{}

func (devProvider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	reply := devReply(req)
	return agent.Response{
		Choices: []agent.Choice{{Message: agent.Message{Role: "assistant", Content: reply}, FinishReason: "stop"}},
		Usage:   agent.Usage{TotalTokens: 128},
	}, nil
}

func (devProvider) CompleteStream(ctx context.Context, req agent.Request, onDelta func(string) error) (agent.Response, error) {
	reply := devReply(req)
	for _, token := range strings.SplitAfter(reply, " ") {
		select {
		case <-ctx.Done():
			return agent.Response{}, ctx.Err()
		case <-time.After(45 * time.Millisecond):
		}
		if err := onDelta(token); err != nil {
			return agent.Response{}, err
		}
	}
	return agent.Response{
		Choices: []agent.Choice{{Message: agent.Message{Role: "assistant", Content: reply}, FinishReason: "stop"}},
		Usage:   agent.Usage{TotalTokens: 128},
	}, nil
}

func devReply(req agent.Request) string {
	last := "this incident"
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" && strings.TrimSpace(req.Messages[i].Content) != "" {
			last = req.Messages[i].Content
			break
		}
	}
	return fmt.Sprintf("For `%s`: check the latest event reason, compare it with pod logs, then verify the referenced Secret or resource limit. In this fixture the strongest signal is the diagnosis context already shown on the page.", last)
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
