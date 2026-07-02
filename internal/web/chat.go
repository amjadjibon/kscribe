package web

import (
	"context"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/amjadjibon/kscribe/internal/agent"
)

const (
	chatContextBudget     = 4096 // max bytes of context_json sent to LLM (CON-007)
	chatHistoryLimit      = 10   // last N chat turns included in the request (count cap)
	chatHistoryByteBudget = 8192 // max total content bytes of history sent to LLM (LOW-4)
)

// RunChat persists the user message, assembles an LLM request with bounded
// incident context and recent history, streams the response to the SSE broker,
// and persists the assistant reply.
//
// SEC-002: every SSE frame carries html.EscapeString(accumulated) so raw model
// output never reaches the wire as HTML. The persisted assistant message keeps
// the original text (authoritative record).
//
// Broker drop mitigation: we publish the *accumulated* string on every delta,
// not per-token deltas, so a slow subscriber catching up always gets the latest
// coalesced state; the persisted message is authoritative on reconnect.
func RunChat(
	ctx context.Context,
	st StoreReader,
	provider agent.Provider,
	broker *Broker,
	ns, name, userMsg string,
) error {
	// 1. Persist user turn.
	if err := st.AppendChatMessage(ctx, ns, name, "user", userMsg); err != nil {
		return err
	}

	// 2. Load incident context (latest diagnosis only).
	detail, err := st.GetIncident(ctx, ns, name)
	if err != nil {
		return err
	}

	var sys strings.Builder
	sys.WriteString("You are kscribe's SRE assistant. Answer questions about this incident using the provided context.")
	if len(detail.Diagnoses) > 0 {
		d := detail.Diagnoses[len(detail.Diagnoses)-1]
		sys.WriteString("\n\nSummary: ")
		sys.WriteString(d.Summary)
		sys.WriteString("\n\nRoot Cause: ")
		sys.WriteString(d.RootCause)
		ctxBytes := d.ContextJSON
		if len(ctxBytes) > chatContextBudget {
			ctxBytes = ctxBytes[:chatContextBudget]
			// LOW-1: back up to a valid UTF-8 boundary so the truncated slice is valid.
			for len(ctxBytes) > 0 && !utf8.Valid(ctxBytes) {
				ctxBytes = ctxBytes[:len(ctxBytes)-1]
			}
		}
		sys.WriteString("\n\nContext: ")
		sys.Write(ctxBytes)
	}

	// 3. Build message list: system + last N history (includes the user turn we just persisted).
	history, err := st.ListChatMessages(ctx, ns, name)
	if err != nil {
		return err
	}
	if len(history) > chatHistoryLimit {
		history = history[len(history)-chatHistoryLimit:]
	}
	// LOW-4: also cap total history content bytes, dropping oldest messages first.
	var histBytes int
	for _, h := range history {
		histBytes += len(h.Content)
	}
	for histBytes > chatHistoryByteBudget && len(history) > 0 {
		histBytes -= len(history[0].Content)
		history = history[1:]
	}

	msgs := make([]agent.Message, 0, 1+len(history))
	msgs = append(msgs, agent.Message{Role: "system", Content: sys.String()})
	for _, h := range history {
		msgs = append(msgs, agent.Message{Role: h.Role, Content: h.Content})
	}

	req := agent.Request{Messages: msgs}

	// 4. Stream; publish coalesced escaped text on each delta.
	topic := ns + "/" + name + "/chat"
	var accumulated strings.Builder
	_, err = agent.StreamOrComplete(ctx, provider, req, func(delta string) error {
		accumulated.WriteString(delta)
		// SEC-002: escape before putting on the wire; never raw model HTML.
		broker.Publish(topic, Event{HTML: html.EscapeString(accumulated.String())})
		return nil
	})
	if err != nil {
		return err
	}

	// 5. Persist assistant reply (authoritative; unescaped raw text).
	// LOW-3: if the stream produced no content, persist a visible fallback so the
	// conversation stays coherent rather than storing a silent empty message.
	reply := accumulated.String()
	if strings.TrimSpace(reply) == "" {
		reply = "(no response from model)"
	}
	return st.AppendChatMessage(ctx, ns, name, "assistant", reply)
}
