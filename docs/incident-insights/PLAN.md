---
goal: Persist and surface diagnosis context & reasoning, add an incident chatbot
version: 1.0
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

### Phase 3: Chat Backend (Streaming + Persisted)

**Goal**: A per-incident chat API that grounds answers in the stored context/RCA/history, streams the reply over SSE, and persists the conversation.

**Depends on**: Phase 2 complete

- [ ] TASK-011: Add `migrations/0003_chat.sql`: `chat_messages(id INTEGER PK, namespace TEXT, name TEXT, role TEXT, content TEXT, created_at TIMESTAMP)` with an index on `(namespace, name, id)`. Store methods `AppendChatMessage(ctx, ns, name, role, content)` and `ListChatMessages(ctx, ns, name)` (parameterized).
- [ ] TASK-012: Add a streaming call to the provider: `internal/agent/openai.go` `CompleteStream(ctx, req, func(delta string) error) (Response, error)` using the OpenAI-compatible `stream: true` SSE (parse `data:` chunks with sonic, invoke the callback per content delta, ignore `[DONE]`). Keep the existing non-streaming `Complete`.
- [ ] TASK-013: Add a chat service (`internal/web/chat.go` or `internal/agent`) that, for an incident, builds a prompt from: a system instruction, the stored **redacted** context + RCA summary/root cause, and prior chat history; calls `CompleteStream`; streams deltas to the incident's chat SSE topic via the `Broker` (topic key e.g. `ns/name/chat`); persists the user message before and the full assistant message after.
- [ ] TASK-014: Wire routes in `internal/web/server.go`: `POST /incidents/{namespace}/{name}/chat` (form/body `message`) → persist user msg, run the streaming chat, return quickly; `GET /incidents/{namespace}/{name}/chat/stream` → SSE for the chat topic (mirrors the existing `/stream` handler). Inject the provider + store into the `Server`.
- [ ] TASK-015: Tests: chat message append/list round-trip; `CompleteStream` parses SSE deltas (httptest server emitting `data:` chunks) and invokes the callback; the chat service persists user+assistant messages and publishes deltas (fake provider + fake broker); history is included in the built prompt.

**Completion criteria**: `go test ./internal/store ./internal/agent ./internal/web` passes (chat persist, stream-parse, service publishes+persists); `go build ./...`, `go vet ./...` pass; a POST chat message persists and streams assistant deltas over the chat SSE topic.

**git commit**: `git add -u && git add internal migrations && git commit -m "feat: streaming persisted incident chat backend"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 3 of incident-insights.

Context: kscribe's operator has an OpenAI-compatible provider (internal/agent/openai.go, OpenAIClient.Complete — non-streaming), a SQLite store (internal/store), and a web server (internal/web) with an SSE Broker (Subscribe/Publish per topic id) already used for live incident status. This phase adds a per-incident chatbot: streaming LLM answers grounded in the stored redacted context + RCA + chat history, persisted to SQLite.

Branch: incident-insights-phase-3  |  Base: incident-insights-phase-2

Tasks:
- TASK-011: Add internal/store/migrations/0003_chat.sql creating chat_messages(id INTEGER PRIMARY KEY AUTOINCREMENT, namespace TEXT NOT NULL, name TEXT NOT NULL, role TEXT NOT NULL, content TEXT NOT NULL, created_at TIMESTAMP NOT NULL) + index on (namespace,name,id). Add store methods AppendChatMessage(ctx, ns, name, role, content string) error and ListChatMessages(ctx, ns, name string) ([]ChatMessage, error), parameterized SQL only.
- TASK-012: In internal/agent/openai.go add CompleteStream(ctx, req Request, onDelta func(string) error) (Response, error): POST with "stream":true, read the response body line by line, parse each `data: {...}` chunk with sonic (skip "[DONE]"), call onDelta with choices[0].delta.content, accumulate the full message for the returned Response. Reuse the request building from Complete. No encoding/json.
- TASK-013: Add a chat service (internal/web/chat.go) with a func that takes (ctx, store, provider, broker, ns, name, userMsg): persists the user message; builds messages = [system prompt with the incident's stored redacted context_json + RCA summary/rootCause] + prior history (ListChatMessages) + the new user message; calls provider.CompleteStream, publishing each delta to broker topic ns+"/"+name+"/chat" as an SSE Event (HTML-escaped/rendered fragment); on completion persists the assistant message. Keep it context-bounded (truncate large context).
- TASK-014: In internal/web/server.go add routes: POST /incidents/{namespace}/{name}/chat (read "message" from the form/body; call the chat service; 200) and GET /incidents/{namespace}/{name}/chat/stream (SSE over the ns/name/chat topic — model it on the existing stream handler). Add provider + store fields to Server (New signature) and wire them in cmd/kscribe/main.go.
- TASK-015: Tests (internal/store: chat round-trip; internal/agent: CompleteStream parses httptest SSE chunks and calls onDelta; internal/web: the chat service persists user+assistant and publishes deltas using a fake provider + a capturing broker/publisher, and the built prompt includes prior history + context). Hermetic.

Key files: internal/store/migrations/0003_chat.sql, internal/store/sqlite.go (+_test), internal/agent/openai.go (+_test), internal/web/chat.go, internal/web/server.go (+_test), cmd/kscribe/main.go.

Completion criteria: go test ./internal/store ./internal/agent ./internal/web passes; go build ./..., go vet ./... pass; make manifests clean if RBAC unchanged.

When done: git add -u && git add internal && git commit -m "feat: streaming persisted incident chat backend" — no Co-authored-by
Write a one-paragraph summary + commit SHA. Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 4: Chat UI

**Goal**: A Chat tab in the incident detail with history, a message input, and streamed assistant replies.

**Depends on**: Phase 3 complete

- [ ] TASK-016: In `internal/web/templates/incidents.templ`, add a **Chat** tab to `IncidentDetail` (Alpine `x-show`): a scrollable message list rendered from `ListChatMessages` (assistant messages via sanitized `RenderMarkdown`), and a message input form (Alpine-managed) that POSTs to `/incidents/{ns}/{name}/chat`.
- [ ] TASK-017: Wire streaming: an HTMX/`hx-ext="sse"` region connected to `/incidents/{ns}/{name}/chat/stream` that appends incoming assistant deltas into the message list; the send form clears on submit (Alpine) and optimistically appends the user message. Keep the SSE region mounted (x-show).
- [ ] TASK-018: Pass the chat history into `IncidentDetail` (extend the detail handler/view-model to load `ListChatMessages`). Style the chat panel (bubbles, input, streaming cursor) in `public/css/app.css` with the design tokens, readable in both themes.
- [ ] TASK-019: Run `make templ`; tests: the detail page renders the Chat tab with seeded history; POST `/incidents/{ns}/{name}/chat` invokes the chat service (fake) and returns 200; the chat stream endpoint returns `text/event-stream`; an assistant message with a `<script>` payload is sanitized on render.

**Completion criteria**: `go test ./internal/web` passes (chat render + POST + SSE + sanitization); `make templ` reproducible; `go build ./...`, `go vet ./...` pass; the detail page has a working Chat tab that streams answers and persists them.

**git commit**: `git add -u && git add public && git commit -m "feat: incident chat UI with streaming"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 4 (final) of incident-insights.

Context: kscribe's web dashboard (internal/web, templ + HTMX + Alpine) has a tabbed incident detail. Phase 3 added the chat backend: POST /incidents/{ns}/{name}/chat (send a message), GET /incidents/{ns}/{name}/chat/stream (SSE of assistant deltas on topic ns/name/chat), store.ListChatMessages, and sanitized RenderMarkdown. This phase adds the Chat UI.

Branch: incident-insights-phase-4  |  Base: incident-insights-phase-3

Tasks:
- TASK-016: Add a "Chat" tab to IncidentDetail (Alpine x-show, keep SSE regions mounted): a scrollable message list from the incident's chat history (assistant content via sanitized RenderMarkdown, user content escaped), plus a form (Alpine-managed input) POSTing "message" to /incidents/{ns}/{name}/chat.
- TASK-017: Stream replies: an hx-ext="sse" sse-connect="/incidents/{ns}/{name}/chat/stream" region that appends assistant deltas into the list; the input clears on submit (Alpine) and optimistically shows the user's message. Do not use x-if for SSE regions.
- TASK-018: Load chat history into IncidentDetail (extend the detail handler/view-model with store.ListChatMessages). Style the chat panel in public/css/app.css (message bubbles, input row, streaming indicator) with the design tokens, both themes.
- TASK-019: make templ. Tests (internal/web): detail page renders the Chat tab with seeded history; POST /incidents/{ns}/{name}/chat calls the chat service (fake) → 200; GET .../chat/stream returns text/event-stream; a <script> in an assistant message is stripped on render.

Key files: internal/web/templates/incidents.templ (+_templ.go), internal/web/server.go / handlers (detail view-model with chat history), public/css/app.css, internal/web/server_test.go.

Completion criteria: go test ./internal/web passes; make templ reproducible; go build ./..., go vet ./... pass.

When done: git add -u && git add public && git commit -m "feat: incident chat UI with streaming" — no Co-authored-by
Write a one-paragraph summary + commit SHA. Do NOT push, open PRs, or modify PLAN.md.
```

---

## 3. Testing

- [ ] TEST-001: `go test ./internal/store` — 0002 + 0003 migrations apply (fail-closed); context/reasoning/trace and chat messages round-trip; all SQL parameterized.
- [ ] TEST-002: `go test ./internal/agent` — Outcome carries a populated tool-call trace; `CompleteStream` parses SSE `data:` chunks and invokes the delta callback.
- [ ] TEST-003: `go test ./internal/controller` — the reconciler persists context_json/reasoning/trace_json for a successful diagnosis.
- [ ] TEST-004: `go test ./internal/web` — detail page renders Context, Reasoning, and Chat tabs from the DB; chat POST hits the service; chat stream returns `text/event-stream`.
- [ ] TEST-005: `go test ./internal/web ./internal/store` — sanitization: `<script>`/`onerror` in a context, reasoning, or assistant-chat field is stripped from the rendered page (SEC-001).
- [ ] TEST-006: `make templ && git diff --exit-code` — generated templ reproducible; `make build` — binary builds.
- [ ] TEST-007: Manual — redeploy to colima, open an incident: Context tab shows the logs/events sent to the model, Reasoning shows the narrative + tool steps, and the Chat tab streams an answer that references the incident and persists across reload.

## 4. Risks & Assumptions

- **RISK-001**: Streaming SSE parsing of OpenAI-compatible chunks is the trickiest part and varies slightly by provider — mitigation: parse defensively (skip non-`data:` lines and `[DONE]`), unit-test with an httptest chunk emitter, and verify against the local LM Studio endpoint in the manual step.
- **RISK-002**: Chat context can exceed the model's window (logs are large) — mitigation: send the RCA summary/root-cause + a truncated context slice + recent history, not the full raw snapshot; bound sizes.
- **RISK-003**: Alpine + HTMX both manipulating the chat list can conflict — mitigation: HTMX/SSE only appends into a dedicated list container; Alpine owns the input/optimistic echo; scope `x-data` away from the SSE-swapped node.
- **RISK-004**: XSS via model/chat output — mitigation: all model-derived Markdown rendered through the single sanitizing `RenderMarkdown` chokepoint, with a sanitization test covering the chat path (SEC-001).
- **RISK-005**: Persisted context can grow the DB — mitigation: it's the already-truncated/redacted snapshot; acceptable for MVP, revisit retention later.
- **ASSUMPTION-001**: The RCA-enrichment work (PR #21: `enricher.BuildSnapshot` wired into the reconciler + `KubeToolExecutor`) is merged to `main` first, so the reconciler already produces the redacted snapshot and tool-call loop this feature persists. Phase branches base on `main` after that merge.
- **ASSUMPTION-002**: Chat is stateless server-side beyond SQLite — no per-user auth/session; any viewer of the dashboard shares an incident's single conversation (matches the current no-auth dashboard). Multi-user/auth is out of scope.
- **ASSUMPTION-003**: Phase branches use the hyphenated form `incident-insights-phase-N` to avoid a git ref D/F conflict with the `incident-insights` plan branch.
- **ASSUMPTION-004**: The configured provider supports `stream: true` (OpenAI, Gemini/Z.AI OpenAI-compat, and LM Studio all do). If a provider doesn't, `CompleteStream` falls back to a single `Complete` emitted as one delta.
