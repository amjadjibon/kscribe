package web

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/amjadjibon/kscribe/internal/store"
	"github.com/amjadjibon/kscribe/internal/web/templates"
)

// StoreReader is the subset of store.Store the web server needs.
type StoreReader interface {
	ListIncidents(ctx context.Context, limit int) ([]store.Incident, error)
	GetIncident(ctx context.Context, namespace, name string) (*store.IncidentDetail, error)
}

// Server holds the web server dependencies.
type Server struct {
	store  StoreReader
	broker *Broker
}

// New returns a Server.
func New(st StoreReader, br *Broker) *Server {
	return &Server{store: st, broker: br}
}

// Handler returns the chi router as an http.Handler.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", s.healthz)
	r.Get("/", s.list)
	r.Get("/incidents/{namespace}/{name}", s.detail)
	r.Get("/incidents/{namespace}/{name}/stream", s.stream)
	return r
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	incidents, err := s.store.ListIncidents(r.Context(), 100)
	if err != nil {
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Layout("kscribe — Incidents", templates.IncidentList(incidents)).Render(r.Context(), w)
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	detail, err := s.store.GetIncident(r.Context(), ns, name)
	if err != nil {
		http.Error(w, "incident not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Layout("kscribe — "+name, templates.IncidentDetail(detail)).Render(r.Context(), w)
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

// writeSSE writes a single SSE data frame. Multi-line HTML is split per spec.
func writeSSE(w http.ResponseWriter, html string) {
	sc := bufio.NewScanner(strings.NewReader(html))
	for sc.Scan() {
		fmt.Fprintf(w, "data: %s\n", sc.Text())
	}
	fmt.Fprint(w, "\n")
}
