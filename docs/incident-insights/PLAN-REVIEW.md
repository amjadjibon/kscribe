---
date: 2026-07-02
plan: docs/incident-insights/PLAN.md
plan_version: 1.0
reviewer: Claude
verdict: Needs Revision
---

# Plan Review: incident-insights

## Verdict

**Needs Revision** — Phases are correctly ordered and the store/agent work lands before the UI that consumes it, but three streaming/chat details (the provider streaming interface, delta-drop on the Broker, and delta sanitization) are under-specified enough to cause divergence mid-Phase-3/4. No blockers.

## Findings

### [REVISE-001] Streaming provider interface is left undefined — chat service can't inject a fake
**Phase**: 3 (TASK-012, TASK-013, TASK-015)
**Issue**: `agent.Provider` (`internal/agent/llm.go`) is a one-method interface: `Complete(ctx, Request) (Response, error)`. TASK-012 adds `CompleteStream` to the concrete `*OpenAIClient` only. TASK-013 says the chat service "calls `provider.CompleteStream`" and TASK-015 wants a "fake provider" that streams — but neither the interface the service depends on nor the fake's shape is specified. The existing `fakeProvider` in `internal/agent/agent_test.go` implements only `Complete`. As written, an implementer must invent a type decision (extend `Provider` with `CompleteStream`, which forces updating every existing fake, vs. a new narrow `StreamingProvider` interface). ASSUMPTION-004's "falls back to a single `Complete`" also has no home — which type owns the fallback? This is a load-bearing type decision that is not documented.
**Fix**: Add a task/line specifying: define `type StreamingProvider interface { Complete(...); CompleteStream(ctx, Request, func(string) error) (Response, error) }` (or extend `Provider` and update `fakeProvider`), have the chat service depend on it, and state where the non-streaming fallback lives (default `CompleteStream` that calls `Complete` and emits one delta). Add it to Risks/Assumptions.

---

### [REVISE-002] Broker drops events when the buffer is full — will corrupt a streamed chat reply
**Phase**: 3 (TASK-013), 4 (TASK-017)
**Issue**: `Broker.Publish` (`internal/web/broker.go`) is deliberately non-blocking: it drops the event for any subscriber whose 8-slot buffer is full ("`// ponytail: non-blocking drop`"). That is correct for the live-status topic (last-write-wins). But chat streams *token deltas* where every delta is load-bearing — a dropped delta silently produces a garbled assistant message in the live view. The plan reuses the same Broker/publish path (TASK-013) without acknowledging this. RISK-003 covers Alpine/HTMX conflict but not delta loss.
**Fix**: Document that streamed deltas are best-effort and the *persisted* full assistant message (rendered via `RenderMarkdown` on reload, TASK-016) is authoritative; and/or coalesce deltas server-side (publish accumulated text, not per-token) so a dropped frame is self-healing, or raise the buffer for chat topics. State the chosen approach in TASK-013.

---

### [REVISE-003] Streamed-delta sanitization is ambiguous — the new XSS path isn't pinned down
**Phase**: 3 (TASK-013), 4 (TASK-017)
**Issue**: TASK-013 publishes each delta "as an SSE `Event` (HTML-escaped/rendered fragment)" — the slash hides a real fork. Deltas are *partial* Markdown (half a fence, a lone `<`); they cannot be run through `RenderMarkdown` (goldmark + bluemonday) meaningfully or safely per-fragment, and raw model text must never reach the SSE stream unescaped. SEC-001/TEST-005 assert stripping on the *persisted/rendered* field, but the streaming path is exactly where an implementer could inject raw model output.
**Fix**: State explicitly: streamed deltas are `html.EscapeString`-ed plaintext appended into the list; full Markdown rendering through the single `RenderMarkdown` chokepoint happens only on the persisted message (reload / TASK-016). Add a test asserting a `<script>` in a *streamed delta* is escaped, not just in the stored message.

---

### [SUGGEST-001] Phase 3 bundles the riskiest task with three others
**Phase**: 3
**Issue**: RISK-001 names SSE stream parsing as "the trickiest part," yet TASK-012 (streaming provider) ships in the same phase/commit as the chat table (011), the service (013), and routes + `Server`/`main.go` rewiring (014). The provider streaming is independently testable and should be proven before anything consumes it.
**Fix**: Optionally split TASK-012 into its own sub-phase (3a) with its httptest-chunk test, then build the service on a known-good `CompleteStream`. Cohesive to keep together, but the sequencing lowers risk.

---

### [SUGGEST-002] `StoreReader` interface must gain the chat methods — not stated
**Phase**: 3 (TASK-014), 4 (TASK-018)
**Issue**: The web `Server` depends on `StoreReader` (`internal/web/server.go`), which does not include `AppendChatMessage`/`ListChatMessages`. TASK-014/018 add chat persistence + history loading but only say "add store fields" — they don't mention extending `StoreReader` (or switching to the concrete `*store.Store`). An implementer will hit this at compile time; cheap to name up front.
**Fix**: Add a line to TASK-014: extend `StoreReader` with `AppendChatMessage` and `ListChatMessages`, and update the fake store in `internal/web/server_test.go`.

---

### [SUGGEST-003] Context-truncation budget is unspecified
**Phase**: 3 (TASK-013), RISK-002
**Issue**: RISK-002 and TASK-013 say "truncate large context" / "keep it context-bounded" but give no concrete cap, so two implementers (or the sub-agent) will pick different, untestable limits.
**Fix**: State a concrete budget (e.g. RCA summary + rootCause in full + first ~4 KB of `context_json` + last N history messages) so the bound is assertable in a test.

## What's Good

- **Phase ordering is correct**: store + agent + reconciler persistence (Phase 1) lands before the UI reads it (Phase 2), and the chat backend (Phase 3) lands before the chat UI (Phase 4). `Depends on` fields are consistent and reference real prior phases.
- **Faithful to the real code**: extension points match — `Run(ctx, snapshotJSON)` gives clean access to the already-redacted `enricher.EncodeSnapshot` bytes for TASK-005; additive `ADD COLUMN NOT NULL DEFAULT` migrations fit the fail-closed runner that Execs whole files; `x-show` (not `x-if`) and the single `RenderMarkdown` chokepoint are honored.
- **Completion criteria are runnable commands** (`go test ./... ./internal/web`, `go build`, `go vet`, `make templ && git diff --exit-code`) plus a concrete manual step, and the load-bearing assumptions (RCA merge, no-auth shared conversation, branch-naming D/F conflict, provider streaming support) are documented.

## Machine-Readable Verdict

```yaml
verdict: Needs Revision
block: 0
revise: 3
suggest: 3
blocking_ids: []
```
