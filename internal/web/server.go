package web

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/amjadjibon/kscribe/internal/agent"
	"github.com/amjadjibon/kscribe/internal/store"
	"github.com/amjadjibon/kscribe/internal/web/templates"
	"github.com/amjadjibon/kscribe/public"
)

const pageSize = 25

// StoreReader is the subset of store.Store the web server needs.
type StoreReader interface {
	ListIncidents(ctx context.Context, limit int) ([]store.Incident, error)
	ListIncidentsPage(ctx context.Context, filter store.IncidentFilter, limit, offset int) ([]store.Incident, error)
	CountIncidents(ctx context.Context, filter store.IncidentFilter) (int, error)
	CountIncidentsByPhase(ctx context.Context, filter store.IncidentFilter) (map[string]int, error)
	GetIncident(ctx context.Context, namespace, name string) (*store.IncidentDetail, error)
	AppendChatMessage(ctx context.Context, namespace, name, role, content string) error
	ListChatMessages(ctx context.Context, namespace, name string) ([]store.ChatMessage, error)
}

// Server holds the web server dependencies.
type Server struct {
	store    StoreReader
	broker   *Broker
	provider agent.Provider
}

// New returns a Server.
func New(st StoreReader, br *Broker, provider agent.Provider) *Server {
	return &Server{store: st, broker: br, provider: provider}
}

// Handler returns the chi router as an http.Handler.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", s.healthz)
	r.Get("/", s.list)
	r.Get("/incidents/{namespace}/{name}", s.detail)
	r.Get("/incidents/{namespace}/{name}/stream", s.stream)
	r.Post("/incidents/{namespace}/{name}/chat", s.chatPost)
	r.Get("/incidents/{namespace}/{name}/chat/stream", s.chatStream)
	// ponytail: inline cache header wrapper — no middleware stack needed for a single route
	static := http.FileServer(http.FS(public.FS))
	r.Handle("/static/*", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		static.ServeHTTP(w, r)
	})))
	return r
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	filter := store.IncidentFilter{
		Phase:     q.Get("phase"),
		Namespace: q.Get("namespace"),
		Reason:    q.Get("reason"),
		Query:     q.Get("q"),
	}

	totals, err := s.store.CountIncidentsByPhase(r.Context(), filter)
	if err != nil {
		http.Error(w, "failed to count incidents", http.StatusInternalServerError)
		return
	}

	total, err := s.store.CountIncidents(r.Context(), filter)
	if err != nil {
		http.Error(w, "failed to count incidents", http.StatusInternalServerError)
		return
	}
	lastPage := (total + pageSize - 1) / pageSize
	if lastPage < 1 {
		lastPage = 1
	}
	if page > lastPage {
		page = lastPage
	}

	incidents, err := s.store.ListIncidentsPage(r.Context(), filter, pageSize, (page-1)*pageSize)
	if err != nil {
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Layout("kscribe — Incidents", templates.IncidentList(incidents, totals, page, lastPage, filter)).Render(r.Context(), w)
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	detail, err := s.store.GetIncident(r.Context(), ns, name)
	if err != nil {
		http.Error(w, "incident not found", http.StatusNotFound)
		return
	}
	msgs, _ := s.store.ListChatMessages(r.Context(), ns, name)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Layout("kscribe — "+name, templates.IncidentDetail(templates.BuildDetailView(detail, msgs))).Render(r.Context(), w)
}

// stream handles SSE for a single incident. It streams Event.HTML fragments to
// the client until the request context is cancelled.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	id := ns + "/" + name

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Flush headers immediately so the client unblocks its Do() call.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := s.broker.Subscribe(id)
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, ev.HTML)
			flusher.Flush()
		}
	}
}

// chatPost accepts a user message, runs the chat pipeline, and returns 200/500.
func (s *Server) chatPost(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	_ = r.ParseForm()
	msg := r.FormValue("message")
	if msg == "" {
		b, _ := io.ReadAll(r.Body)
		msg = string(b)
	}
	if s.provider == nil {
		http.Error(w, "chat provider not configured", http.StatusInternalServerError)
		return
	}
	if err := RunChat(r.Context(), s.store, s.provider, s.broker, ns, name, msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// chatStream streams SSE events for the per-incident chat topic.
func (s *Server) chatStream(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	topic := ns + "/" + name + "/chat"

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := s.broker.Subscribe(topic)
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, ev.HTML)
			flusher.Flush()
		}
	}
}

// writeSSE writes a single SSE data frame. Multi-line HTML is split per spec.
func writeSSE(w http.ResponseWriter, html string) {
	sc := bufio.NewScanner(strings.NewReader(html))
	for sc.Scan() {
		fmt.Fprintf(w, "data: %s\n", sc.Text())
	}
	fmt.Fprint(w, "\n")
}
