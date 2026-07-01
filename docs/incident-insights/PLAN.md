---
goal: Persist and surface diagnosis context & reasoning, add an incident chatbot
version: 1.1
date_created: 2026-07-02
last_updated: 2026-07-02
owner: amjadjibon
status: 'Planned'
tags: [feature, frontend, backend]
---

# Incident Insights: Context, Reasoning & Chat

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

Extend kscribe so each diagnosis keeps the **original redacted context** it was built from (pod logs, related events, node conditions), records the agent's **reasoning** (both the tool-call trace and a natural-language rationale), surfaces all of it in the dashboard, and adds a **per-incident chatbot** that streams answers over SSE and persists the conversation. Builds directly on the RCA-enrichment work (the reconciler already assembles a redacted `enricher.Snapshot` and runs a tool-call agent loop).

## 1. Requirements & Constraints

- **REQ-001**: The redacted context snapshot sent to the LLM (logs, related events, node conditions, workload status) must be persisted per diagnosis in SQLite and shown in the incident detail UI.
- **REQ-002**: The agent's reasoning must be captured as (a) a natural-language `reasoning` field the model fills in, and (b) a structured tool-call trace (which tools were called, with what args, and a short result summary), both persisted and shown in the UI.
- **REQ-003**: The incident detail page must gain a **Chat** panel where a user asks follow-up questions; answers **stream** into the page over SSE, and the conversation is **persisted** in SQLite so it survives reload.
- **REQ-004**: The chatbot must ground its answers in the incident's stored context + RCA + prior chat history for that incident.
- **SEC-001**: Everything the model receives or the UI renders from model/cluster output stays redacted — the persisted context/trace is the already-redacted snapshot; chat context is the stored (redacted) data; rendered Markdown is sanitized (goldmark + bluemonday) exactly like RCA text.
- **CON-001**: Server-rendered templ + HTMX + Alpine, assets embedded (`public/`), no JS build step. Chat streaming reuses the existing SSE `Broker`.
- **CON-002**: No `encoding/json` in application code — use `github.com/bytedance/sonic` (repo CON-003).
- **CON-003**: SQLite migrations are additive and fail-closed (ADR-004); a new migration file is discovered and applied in order by the existing runner. All SQL is parameterized (SEC-002).
- **CON-004**: `make templ` output committed and reproducible; existing `internal/web` / `internal/store` tests stay green.
- **CON-005**: The chat LLM call uses the same configured OpenAI-compatible provider (works with the local LM Studio/gemma setup); streaming uses the provider's `stream: true` SSE.
- **CON-006**: Streaming is behind a `StreamingProvider` interface in `internal/agent`; a `StreamOrComplete` helper falls back to a single `Complete` (emitted as one delta) when the provider doesn't implement it. The chat service depends on the interface/helper, never the concrete client (REVISE-001).
- **SEC-002**: Streamed chat deltas are `html.EscapeString`-ed **plaintext** appended to the DOM — partial Markdown is never run through `RenderMarkdown`. Full sanitized Markdown rendering happens only on the **persisted** message (reload / history load) through the single `RenderMarkdown` chokepoint. A test must assert a `<script>` in a *streamed delta* is escaped (REVISE-003).
- **CON-007**: The chat prompt has a fixed context budget: system instruction + RCA `summary` + full `rootCause` + first ~4 KB of the stored `context_json` + the last 10 chat turns + the new user message. This bound is assertable in a test (SUGGEST-003).

## 2. Implementation Steps

> After completing all tasks in a phase, `git add -u` and commit. No `Co-authored-by:`. Tick `[x]` as each task completes. Run `make templ` before committing when templates change.

### Phase 1: Persist Context & Reasoning

**Goal**: Capture and store, per diagnosis, the redacted context snapshot, a narrative `reasoning` field, and the tool-call trace.

- [ ] TASK-001: Add `migrations/0002_context_reasoning.sql` adding nullable columns to `diagnoses`: `context_json TEXT NOT NULL DEFAULT '{}'`, `reasoning TEXT NOT NULL DEFAULT ''`, `trace_json TEXT NOT NULL DEFAULT '[]'`. Additive; the existing runner applies it in order.
- [ ] TASK-002: In `internal/agent/schema.go`, add `Reasoning string \`json:"reasoning,omitempty"\`` to `RCAResult` and update the agent system prompt (`internal/agent/diagnosis_agent.go`) to instruct the model to include a concise `reasoning` explaining how it reached the conclusion.
- [ ] TASK-003: In `internal/agent/diagnosis_agent.go`, capture a tool-call trace during the loop — a `[]TraceStep{Tool, Args, ResultSummary}` (truncate result to ~200 chars) — and add `Reasoning string` and `Trace []TraceStep` (plus the final `ContextJSON []byte`) to `Outcome` so the reconciler can persist them.
- [ ] TASK-004: Extend `internal/store` `Diagnosis` (add `ContextJSON []byte`, `Reasoning string`, `TraceJSON []byte`) and `InsertDiagnosis` to write the three new columns (parameterized). Read them back in `GetIncident`.
- [ ] TASK-005: In `internal/controller/kscribediagnosis_controller.go`, pass the redacted `snapshotJSON` (context), `outcome.Reasoning`, and sonic-encoded `outcome.Trace` into `InsertDiagnosis`.
- [ ] TASK-006: Tests: migration adds the columns; `InsertDiagnosis`/`GetIncident` round-trip context/reasoning/trace; the agent returns a populated `Trace` for a fake provider that makes a tool call; the reconciler persists all three.

**Completion criteria**: `go test ./internal/store ./internal/agent ./internal/controller` passes (incl. round-trip + trace-capture tests); `go build ./...`, `go vet ./...` pass; a diagnosis row now has non-empty `context_json` and `trace_json`.

**git commit**: `git add -u && git add internal migrations && git commit -m "feat: persist diagnosis context, reasoning and tool trace"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of incident-insights.

Context: kscribe is a Go Kubernetes operator. The diagnosis reconciler (internal/controller/kscribediagnosis_controller.go) builds a redacted enricher.Snapshot, encodes it with enricher.EncodeSnapshot (redacts), and runs an agent tool-call loop (internal/agent/diagnosis_agent.go) that returns an Outcome. SQLite (internal/store) stores incidents + diagnoses. This phase persists the context snapshot, a narrative reasoning field, and the tool-call trace per diagnosis.

Branch: incident-insights-phase-1  |  Base: main

Tasks:
- TASK-001: Add internal/store/migrations/0002_context_reasoning.sql: ALTER TABLE diagnoses ADD COLUMN context_json TEXT NOT NULL DEFAULT '{}'; ADD COLUMN reasoning TEXT NOT NULL DEFAULT ''; ADD COLUMN trace_json TEXT NOT NULL DEFAULT '[]'. (SQLite: one ADD COLUMN per statement.) The runner (internal/store/migrations.go) applies migration files in order.
- TASK-002: In internal/agent/schema.go add `Reasoning string \`json:"reasoning,omitempty"\`` to RCAResult. Update the systemPrompt in diagnosis_agent.go so the JSON schema includes "reasoning" (a concise explanation of how the conclusion was reached).
- TASK-003: In diagnosis_agent.go define `type TraceStep struct { Tool, Args, Result string }` and record one per executed tool call (truncate Result to ~200 chars). Add to Outcome: `Reasoning string`, `Trace []TraceStep`, `ContextJSON []byte`. Populate them (Reasoning from the parsed RCA; ContextJSON from the snapshot bytes passed into Run — thread it through if needed).
- TASK-004: In internal/store/sqlite.go extend Diagnosis with ContextJSON []byte, Reasoning string, TraceJSON []byte; update InsertDiagnosis to INSERT the 3 columns (parameterized) and GetIncident's diagnoses SELECT to read them back. Use sonic for any JSON (CON-003).
- TASK-005: In the reconciler, after a successful diagnosis, pass the redacted snapshotJSON as context, outcome.Reasoning, and sonic.Marshal(outcome.Trace) into the Diagnosis written by InsertDiagnosis.
- TASK-006: Tests in internal/store (round-trip context/reasoning/trace), internal/agent (Outcome.Trace populated when a fake provider issues a tool call), internal/controller (reconciler persists the 3 fields). Hermetic.

Key files:
- internal/store/migrations/0002_context_reasoning.sql, internal/store/sqlite.go (+_test), internal/agent/schema.go, internal/agent/diagnosis_agent.go (+_test), internal/controller/kscribediagnosis_controller.go (+_test).

Completion criteria: go test ./internal/store ./internal/agent ./internal/controller passes; go build ./..., go vet ./... pass; migration is additive + fail-closed.

When done: git add -u && git add internal && git commit -m "feat: persist diagnosis context, reasoning and tool trace" — no Co-authored-by
Write a one-paragraph summary + commit SHA. Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 2: Show Context & Reasoning in the UI

**Goal**: Surface the persisted context snapshot and reasoning (narrative + trace) in the incident detail page.

**Depends on**: Phase 1 complete

- [ ] TASK-007: In `internal/web/templates/incidents.templ`, add two tabs to `IncidentDetail` (Alpine `x-show`, keeping the SSE `#live-status` block mounted): **Context** — pretty-printed snapshot (logs, related events, node conditions) from the stored `context_json`; **Reasoning** — the narrative `reasoning` (Markdown, sanitized via the existing `RenderMarkdown`) plus the tool-call trace rendered as an ordered step list (tool, args, result summary).
- [ ] TASK-008: Decode the stored `context_json`/`trace_json` for rendering in the handler or a small view-model (sonic); format logs/events/nodes readably. Keep values HTML-escaped (templ default) except the sanitized Markdown reasoning.
- [ ] TASK-009: Style the context/reasoning/trace blocks in `public/css/app.css` with the design tokens (monospace for logs/args, readable in both themes).
- [ ] TASK-010: Run `make templ`; tests: detail page for a diagnosis with seeded context/reasoning/trace renders the Context and Reasoning tabs and contains a seeded log line + trace tool name; a `<script>` in a context/reasoning field is stripped (SEC-001).

**Completion criteria**: `go test ./internal/web` passes (incl. render + sanitization asserts); `make templ` reproducible; `go vet ./...` passes; the detail page shows Context and Reasoning tabs populated from the DB.

**git commit**: `git add -u && git add public && git commit -m "feat: show diagnosis context and reasoning in the UI"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 2 of incident-insights.

Context: kscribe's web dashboard (internal/web, templ + HTMX + Alpine) shows a tabbed incident detail (Overview/RCA/Raw) with x-show tabs and a sanitized-Markdown RenderMarkdown helper (internal/web/templates/markdown.go). Phase 1 persisted per-diagnosis context_json, reasoning, and trace_json (readable via store.GetIncident → IncidentDetail.Diagnoses[i].ContextJSON/Reasoning/TraceJSON). This phase surfaces them.

Branch: incident-insights-phase-2  |  Base: incident-insights-phase-1

Tasks:
- TASK-007: In internal/web/templates/incidents.templ add two tabs to IncidentDetail using Alpine x-show (NOT x-if — keep the SSE #live-status block mounted): "Context" (pretty-printed snapshot from context_json: pod logs, related events, node conditions) and "Reasoning" (the reasoning string via RenderMarkdown (sanitized), plus the trace_json rendered as an ordered list of tool/args/result-summary).
- TASK-008: Decode context_json/trace_json with sonic in the handler or a view-model; format readably. HTML-escape all values (templ default) except the sanitized Markdown reasoning.
- TASK-009: Style the blocks in public/css/app.css with existing tokens (monospace for logs/args; readable both themes).
- TASK-010: Run make templ. Tests (internal/web): a detail page for a seeded diagnosis (context_json with a log line, trace_json with a tool step, reasoning markdown) renders the Context+Reasoning tabs and contains the log line + tool name; a <script> payload in a context/reasoning field is stripped.

Key files: internal/web/templates/incidents.templ (+_templ.go), internal/web/server.go / handlers if a view-model is needed, public/css/app.css, internal/web/server_test.go, internal/store (read-only).

Completion criteria: go test ./internal/web passes; make templ reproducible; go vet ./... passes.

When done: git add -u && git add public && git commit -m "feat: show diagnosis context and reasoning in the UI" — no Co-authored-by
Write a one-paragraph summary + commit SHA. Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 3: Streaming LLM Provider

**Goal**: Add streaming to the LLM provider behind a clean interface, proven in isolation before anything consumes it.

**Depends on**: Phase 2 complete

- [ ] TASK-011: In `internal/agent/llm.go` define `type StreamingProvider interface { Provider; CompleteStream(ctx context.Context, req Request, onDelta func(string) error) (Response, error) }` and a package helper `func StreamOrComplete(ctx, p Provider, req Request, onDelta func(string) error) (Response, error)` that uses `CompleteStream` if `p` implements `StreamingProvider`, else calls `p.Complete` and invokes `onDelta` once with the full content (fallback home for ASSUMPTION-004).
- [ ] TASK-012: Implement `CompleteStream` on `*OpenAIClient` (`internal/agent/openai.go`): POST with `"stream": true`, read the body line-by-line, parse each `data: {...}` chunk with sonic, skip `[DONE]`, call `onDelta(choices[0].delta.content)` per chunk, and accumulate the full assistant message for the returned `Response`. Reuse the request/auth building from `Complete`. No `encoding/json`.
- [ ] TASK-013: Tests (`internal/agent`): an httptest server emitting `data:` chunks → `CompleteStream` invokes `onDelta` per delta and returns the accumulated content; `StreamOrComplete` with a non-streaming fake calls `Complete` and emits exactly one delta.

**Completion criteria**: `go test ./internal/agent` passes (stream-parse + fallback); `go build ./...`, `go vet ./...` pass.

**git commit**: `git add -u && git commit -m "feat: add streaming LLM provider interface"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 3 of incident-insights.

Context: kscribe's agent package (internal/agent) has a Provider interface with one method Complete(ctx, Request) (Response, error), implemented by *OpenAIClient (internal/agent/openai.go, non-streaming). This phase adds streaming behind a new interface, proven in isolation before the chat service (Phase 4) uses it.

Branch: incident-insights-phase-3  |  Base: incident-insights-phase-2

Tasks:
- TASK-011: In internal/agent/llm.go add `type StreamingProvider interface { Provider; CompleteStream(ctx context.Context, req Request, onDelta func(string) error) (Response, error) }` and `func StreamOrComplete(ctx context.Context, p Provider, req Request, onDelta func(string) error) (Response, error)`: if p implements StreamingProvider, delegate; else call p.Complete and call onDelta once with the full choices[0].message.content. This is where the non-streaming fallback lives.
- TASK-012: Implement CompleteStream on *OpenAIClient in internal/agent/openai.go: build the same request/auth as Complete but with "stream": true; read resp.Body line by line (bufio.Scanner), for each line starting with "data: " take the payload, skip "[DONE]", sonic-Unmarshal into a chunk struct, call onDelta with choices[0].delta.content, and accumulate into a full content string returned in the Response. No encoding/json (CON-003).
- TASK-013: Tests (internal/agent): httptest server that writes several "data: {\"choices\":[{\"delta\":{\"content\":\"...\"}}]}" lines then "data: [DONE]" → assert onDelta called per delta and Response content is the concatenation; a fakeProvider (Complete only) via StreamOrComplete → onDelta called exactly once with full content.

Key files: internal/agent/llm.go, internal/agent/openai.go (+_test).

Completion criteria: go test ./internal/agent passes; go build ./..., go vet ./... pass.

When done: git add -u && git commit -m "feat: add streaming LLM provider interface" — no Co-authored-by
Write a one-paragraph summary + commit SHA. Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 4: Chat Backend (Persisted + Streamed)

**Goal**: Persist per-incident conversations and serve grounded, streamed answers via the streaming provider + Broker.

**Depends on**: Phase 3 complete

- [ ] TASK-014: Add `internal/store/migrations/0003_chat.sql`: `chat_messages(id INTEGER PRIMARY KEY AUTOINCREMENT, namespace TEXT NOT NULL, name TEXT NOT NULL, role TEXT NOT NULL, content TEXT NOT NULL, created_at TIMESTAMP NOT NULL)` + index `(namespace, name, id)`. Add `type ChatMessage`, `AppendChatMessage(ctx, ns, name, role, content string) error`, `ListChatMessages(ctx, ns, name string) ([]ChatMessage, error)` (parameterized). **Extend the web `StoreReader` interface** (`internal/web/server.go`) with these two methods and update the fake store in `internal/web/server_test.go` (SUGGEST-002).
- [ ] TASK-015: Add the chat service (`internal/web/chat.go`): given (ctx, store, provider, broker, ns, name, userMsg) — persist the user message; build messages within the **CON-007 budget** (system + incident RCA summary + full rootCause + first ~4 KB of `context_json` + last 10 history turns + user msg); call `agent.StreamOrComplete`; publish deltas as **`html.EscapeString`-ed plaintext, coalesced** (publish the accumulated text each delta so a dropped Broker frame self-heals — REVISE-002/003) to Broker topic `ns+"/"+name+"/chat"`; on completion persist the assistant message (the **persisted** message is authoritative).
- [ ] TASK-016: Routes in `internal/web/server.go`: `POST /incidents/{namespace}/{name}/chat` (read `message`; run the chat service; 200) and `GET /incidents/{namespace}/{name}/chat/stream` (SSE over the `ns/name/chat` topic — model on the existing `/stream` handler). Add provider + store to `Server` (New signature) and wire in `cmd/kscribe/main.go`.
- [ ] TASK-017: Tests: chat message append/list round-trip (`internal/store`); the chat service persists user+assistant and publishes **escaped** deltas — assert a `<script>` in a streamed delta is `html.EscapeString`-ed (REVISE-003) — using a fake streaming provider + a capturing broker; the built prompt honors the CON-007 budget (includes history + a bounded context slice).

**Completion criteria**: `go test ./internal/store ./internal/web` passes (chat persist, escaped-delta, budgeted prompt); `go build ./...`, `go vet ./...` pass.

**git commit**: `git add -u && git add internal migrations && git commit -m "feat: persisted streaming incident chat backend"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 4 of incident-insights.

Context: kscribe has a SQLite store (internal/store), an SSE Broker (internal/web/broker.go: Subscribe/Publish per topic id; NOTE Publish non-blocking-DROPS on a full 8-slot buffer), a streaming provider (Phase 3: agent.StreamOrComplete + StreamingProvider), and per-diagnosis stored context_json/RCA (Phase 1). This phase adds a persisted, streamed per-incident chatbot.

Branch: incident-insights-phase-4  |  Base: incident-insights-phase-3

Tasks:
- TASK-014: internal/store/migrations/0003_chat.sql creating chat_messages(id INTEGER PRIMARY KEY AUTOINCREMENT, namespace, name, role, content TEXT NOT NULL, created_at TIMESTAMP NOT NULL) + index (namespace,name,id). Add type ChatMessage, AppendChatMessage(ctx,ns,name,role,content) error, ListChatMessages(ctx,ns,name) ([]ChatMessage,error) — parameterized. Extend the web StoreReader interface (internal/web/server.go) with AppendChatMessage + ListChatMessages and update the fake store in internal/web/server_test.go.
- TASK-015: internal/web/chat.go — a func(ctx, store, provider agent.Provider, broker *Broker, ns, name, userMsg string) error: persist the user message; build the prompt within budget = system instruction + the incident's RCA summary + full rootCause + first ~4096 bytes of the latest diagnosis context_json + last 10 ListChatMessages + the user message; call agent.StreamOrComplete; the onDelta callback ACCUMULATES text and Publishes html.EscapeString(accumulated) to broker topic ns+"/"+name+"/chat" (coalesced so a dropped frame self-heals, and never raw model HTML); after completion, AppendChatMessage(role="assistant", full content). The persisted assistant message is authoritative.
- TASK-016: In internal/web/server.go add POST /incidents/{namespace}/{name}/chat (read "message" from the form; call the chat service; return 200) and GET /incidents/{namespace}/{name}/chat/stream (SSE over topic ns/name/chat; mirror the existing stream handler). Add provider (agent.Provider) + store to Server and wire in cmd/kscribe/main.go (New signature change).
- TASK-017: Tests: internal/store chat round-trip; internal/web — chat service with a fake streaming provider (emits deltas incl. "<script>alert(1)</script>") + a capturing broker asserts every published frame is html-escaped (no literal "<script>"), user+assistant persisted, and the prompt includes prior history + a <=4KB context slice.

Key files: internal/store/migrations/0003_chat.sql, internal/store/sqlite.go (+_test), internal/web/chat.go, internal/web/server.go (+_test), cmd/kscribe/main.go.

Completion criteria: go test ./internal/store ./internal/web passes; go build ./..., go vet ./... pass.

When done: git add -u && git add internal && git commit -m "feat: persisted streaming incident chat backend" — no Co-authored-by
Write a one-paragraph summary + commit SHA. Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 5: Chat UI

**Goal**: A Chat tab with history, input, and streamed replies in the incident detail.

**Depends on**: Phase 4 complete

- [ ] TASK-018: In `internal/web/templates/incidents.templ`, add a **Chat** tab to `IncidentDetail` (Alpine `x-show`, keep SSE regions mounted): a scrollable message list from `ListChatMessages` (assistant content via sanitized `RenderMarkdown`, user content escaped), and an Alpine-managed input form POSTing `message` to `/incidents/{ns}/{name}/chat`.
- [ ] TASK-019: Stream replies: an `hx-ext="sse" sse-connect="/incidents/{ns}/{name}/chat/stream"` region that renders the incoming (already-escaped, coalesced) assistant text into a live bubble; the input clears on submit (Alpine) and optimistically echoes the user message. Do not use `x-if` for SSE regions. On reload, history renders through `RenderMarkdown` (sanitized) — the authoritative view.
- [ ] TASK-020: Load chat history into `IncidentDetail` (extend the detail handler/view-model with `ListChatMessages`). Style the chat panel (bubbles, input row, streaming indicator) in `public/css/app.css` with the design tokens, both themes.
- [ ] TASK-021: Run `make templ`; tests: the detail page renders the Chat tab with seeded history; POST `/incidents/{ns}/{name}/chat` invokes the chat service (fake) → 200; the chat stream endpoint returns `text/event-stream`; a `<script>` in a stored assistant message is stripped on render (SEC-001).

**Completion criteria**: `go test ./internal/web` passes (chat render + POST + SSE + sanitization); `make templ` reproducible; `go build ./...`, `go vet ./...` pass.

**git commit**: `git add -u && git add public && git commit -m "feat: incident chat UI with streaming"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 5 (final) of incident-insights.

Context: kscribe's dashboard (internal/web, templ + HTMX + Alpine) has a tabbed incident detail. Phase 4 added the chat backend: POST /incidents/{ns}/{name}/chat, GET /incidents/{ns}/{name}/chat/stream (SSE of html-escaped, coalesced assistant text on topic ns/name/chat), store.ListChatMessages, and the sanitizing RenderMarkdown helper. This phase adds the Chat UI.

Branch: incident-insights-phase-5  |  Base: incident-insights-phase-4

Tasks:
- TASK-018: Add a "Chat" tab to IncidentDetail (Alpine x-show, keep SSE regions mounted): a scrollable message list from chat history (assistant via sanitized RenderMarkdown, user escaped), plus an Alpine-managed input form POSTing "message" to /incidents/{ns}/{name}/chat.
- TASK-019: Stream replies via hx-ext="sse" sse-connect="/incidents/{ns}/{name}/chat/stream" into a live assistant bubble (the stream is already html-escaped + coalesced — just swap it in). Input clears on submit (Alpine) + optimistic user echo. No x-if on SSE regions. Reloaded history renders through RenderMarkdown (authoritative).
- TASK-020: Load chat history into IncidentDetail (extend the detail handler/view-model with store.ListChatMessages). Style chat bubbles/input/streaming indicator in public/css/app.css with design tokens, both themes.
- TASK-021: make templ. Tests (internal/web): detail renders Chat tab with seeded history; POST .../chat calls the chat service (fake) → 200; GET .../chat/stream returns text/event-stream; a <script> in a stored assistant message is stripped on render.

Key files: internal/web/templates/incidents.templ (+_templ.go), internal/web/server.go / handlers (detail view-model w/ chat history), public/css/app.css, internal/web/server_test.go.

Completion criteria: go test ./internal/web passes; make templ reproducible; go build ./..., go vet ./... pass.

When done: git add -u && git add public && git commit -m "feat: incident chat UI with streaming" — no Co-authored-by
Write a one-paragraph summary + commit SHA. Do NOT push, open PRs, or modify PLAN.md.
```

## 3. Testing

- [ ] TEST-001: `go test ./internal/store` — 0002 + 0003 migrations apply (fail-closed); context/reasoning/trace and chat messages round-trip; all SQL parameterized.
- [ ] TEST-002: `go test ./internal/agent` — Outcome carries a populated tool-call trace (Phase 1); `CompleteStream` parses SSE `data:` chunks and invokes the delta callback, and `StreamOrComplete` falls back to one delta for a non-streaming provider (Phase 3).
- [ ] TEST-003: `go test ./internal/controller` — the reconciler persists context_json/reasoning/trace_json for a successful diagnosis.
- [ ] TEST-004: `go test ./internal/web` — detail page renders Context, Reasoning, and Chat tabs from the DB; chat POST hits the service; chat stream returns `text/event-stream`.
- [ ] TEST-005: `go test ./internal/web ./internal/store` — sanitization: `<script>`/`onerror` in a context, reasoning, or **stored** assistant-chat field is stripped on render (SEC-001); and a `<script>` in a **streamed delta** is `html.EscapeString`-ed on the wire (SEC-002, Phase 4).
- [ ] TEST-006: `make templ && git diff --exit-code` — generated templ reproducible; `make build` — binary builds.
- [ ] TEST-007: Manual — redeploy to colima, open an incident: Context tab shows the logs/events sent to the model, Reasoning shows the narrative + tool steps, and the Chat tab streams an answer that references the incident and persists across reload.

## 4. Risks & Assumptions

- **RISK-001**: Streaming SSE parsing of OpenAI-compatible chunks is the trickiest part and varies by provider — mitigation: it's isolated in its own phase (Phase 3) behind a `StreamingProvider` interface with a `StreamOrComplete` fallback, parsed defensively (skip non-`data:` lines and `[DONE]`), unit-tested with an httptest chunk emitter, and verified against the local LM Studio endpoint in the manual step.
- **RISK-006**: `Broker.Publish` non-blocking-drops on a full 8-slot buffer, which would corrupt a per-token chat stream — mitigation: the chat service publishes **coalesced accumulated text** (not per-token deltas), so a dropped frame self-heals on the next publish, and the **persisted** assistant message rendered via `RenderMarkdown` on reload is authoritative (REVISE-002).
- **RISK-007**: Streamed partial Markdown can't be safely run through `RenderMarkdown` per-fragment — mitigation: deltas are `html.EscapeString`-ed plaintext on the wire; full sanitized Markdown rendering happens only on the persisted message at load; a test asserts a `<script>` in a streamed delta is escaped (REVISE-003, SEC-002).
- **RISK-002**: Chat context can exceed the model's window (logs are large) — mitigation: send the RCA summary/root-cause + a truncated context slice + recent history, not the full raw snapshot; bound sizes.
- **RISK-003**: Alpine + HTMX both manipulating the chat list can conflict — mitigation: HTMX/SSE only appends into a dedicated list container; Alpine owns the input/optimistic echo; scope `x-data` away from the SSE-swapped node.
- **RISK-004**: XSS via model/chat output — mitigation: all model-derived Markdown rendered through the single sanitizing `RenderMarkdown` chokepoint, with a sanitization test covering the chat path (SEC-001).
- **RISK-005**: Persisted context can grow the DB — mitigation: it's the already-truncated/redacted snapshot; acceptable for MVP, revisit retention later.
- **ASSUMPTION-001**: The RCA-enrichment work (PR #21: `enricher.BuildSnapshot` wired into the reconciler + `KubeToolExecutor`) is merged to `main` first, so the reconciler already produces the redacted snapshot and tool-call loop this feature persists. Phase branches base on `main` after that merge.
- **ASSUMPTION-002**: Chat is stateless server-side beyond SQLite — no per-user auth/session; any viewer of the dashboard shares an incident's single conversation (matches the current no-auth dashboard). Multi-user/auth is out of scope.
- **ASSUMPTION-003**: Phase branches use the hyphenated form `incident-insights-phase-N` to avoid a git ref D/F conflict with the `incident-insights` plan branch.
- **ASSUMPTION-004**: The configured provider supports `stream: true` (OpenAI, Gemini/Z.AI OpenAI-compat, and LM Studio all do). If not, `agent.StreamOrComplete` (Phase 3) is the fallback home — it calls `Complete` and emits the full content as one delta.
