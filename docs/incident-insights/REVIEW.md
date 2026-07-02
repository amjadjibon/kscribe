---
date: 2026-07-02
branch: incident-insights-phase-5
reviewer: Claude
verdict: Approve
---

# Code Review: incident-insights

## Verdict

**Approve** — All five security/correctness invariants (SEC-001, SEC-002, streaming error paths, broker coalescing, CON-007 budget, SQL parameterization, CON-003) hold; only Low/Info polish items remain, none blocking.

## Summary

Reviewed `git diff main...HEAD` (24 files) with focus on the two XSS invariants, streaming error paths, broker delta loss, context-budget enforcement, SQL safety, and concurrency. The escaping chokepoints are correct: `templ.Raw` appears only inside the bluemonday-sanitized `RenderMarkdown` helper, all other model-derived content renders through templ auto-escaping, and SSE chat deltas go on the wire as `html.EscapeString(accumulated)` plaintext. Streaming error paths surface non-2xx and scanner errors rather than returning silent empty replies. The migrations are additive and fail-closed, all queries are parameterized, and `templ generate` reproduces the committed output byte-for-byte. Findings are minor: UTF-8-unsafe context truncation, an unvalidated empty-message path, and a partial context-budget bound.

## Invariant Verification

- **SEC-001 (stored XSS): PASS.** `templ.Raw` exists only at `markdown.go:18`, wrapping `bluemonday.UGCPolicy().SanitizeBytes(...)`. RCA Summary/RootCause/Remediation and reasoning route through `RenderMarkdown` (`incidents.templ:412,444,448,452`); stored assistant chat via `RenderMarkdown` (`incidents.templ:238`); decoded `context_json` values, tool-trace `marshalJSON`, and user chat messages are plain templ text nodes (auto-escaped). Nothing un-redacts `context_json` — it is decoded (`viewmodel.go:38`) and re-rendered field-by-field as text.
- **SEC-002 (streamed XSS): PASS.** `RunChat` publishes `html.EscapeString(accumulated.String())` (`chat.go:84`); the UI swaps it via `sse-swap`/`innerHTML` with no un-escaping (`incidents.templ:243-251`).
- **Streaming correctness: PASS.** `CompleteStream` returns an error on non-2xx (`openai.go:120-123`), skips non-`data:`/`[DONE]`/malformed chunks (`openai.go:129-139`), and checks `scanner.Err()` (`openai.go:150`). `StreamOrComplete` propagates the error (`llm.go:27-29`); `RunChat` returns it; `chatPost` maps to HTTP 500 (`server.go:175-177`). A non-2xx stream therefore surfaces an error, not a silent empty reply.
- **Broker delta loss: PASS.** `RunChat` publishes the *coalesced accumulated* string on each delta (`chat.go:82-84`), not per-token, so a dropped 8-slot frame self-heals on the next delta; the persisted assistant message (`chat.go:92`) is authoritative.
- **CON-007 budget: PASS (with LOW-004 caveat).** The 4KB `context_json` slice (`chat.go:53-56`) and last-10 history cap (`chat.go:66-68`) are enforced in the built request.
- **SQL: PASS.** All new queries (`AppendChatMessage`, `ListChatMessages`, `InsertDiagnosis`, `GetIncident`) are fully parameterized; `buildWhere` binds every filter value.
- **Migrations: PASS.** 0002 is additive `ADD COLUMN ... NOT NULL DEFAULT`; 0003 is a new table + index; both run under the fail-closed `runMigrations` path (`sqlite.go:127-130`).
- **Concurrency: PASS.** `Broker.Publish` is mutex-guarded and non-blocking (drop-on-full); SSE handlers exit on `r.Context().Done()` or channel close with `defer cancel()` (`server.go:144-158, 200-214`); `accumulated` is request-local. No race, leak, or blocking publisher. Chat list uses `x-show` not `x-if` (`incidents.templ:227`) so the SSE region stays mounted.
- **CON-003: PASS.** No `encoding/json` imports anywhere (only comments/guards); `templ generate` produced no diff against the committed `incidents_templ.go`.

## Findings

### [LOW-001] Context truncation can split a multi-byte UTF-8 rune *(Low)*
**File**: `internal/web/chat.go:55`
**Category**: Correctness
**Issue**: `ctxBytes = ctxBytes[:chatContextBudget]` cuts at a fixed byte offset, which can land mid-rune and produce invalid UTF-8 (and an invalid JSON tail). The bytes are then placed into a string and `sonic.Marshal`ed into the request body. This is the same byte-boundary class the QA pass just fixed in `CompleteStream`. Since it is prompt text (not parsed), the practical impact is a garbled trailing rune (or a marshal edge), not a crash.
**Fix**: Trim back to a rune boundary, e.g. `for len(ctxBytes) > 0 && !utf8.RuneStart(ctxBytes[len(ctxBytes)-1]) { ctxBytes = ctxBytes[:len(ctxBytes)-1] }` after the slice, or `strings.ToValidUTF8`.

### [LOW-002] Empty chat message is accepted and persisted *(Low)*
**File**: `internal/web/server.go:162-170`, `internal/web/chat.go:35`
**Category**: Correctness
**Issue**: `chatPost` does not reject an empty `message`; an empty string is persisted as a user turn and sent to the LLM. The `io.ReadAll(r.Body)` fallback after `ParseForm()` is also effectively dead for urlencoded bodies (already consumed) — harmless but misleading.
**Fix**: `if strings.TrimSpace(msg) == "" { http.Error(w, "empty message", http.StatusBadRequest); return }` and drop the dead body-read fallback.

### [LOW-003] 200 stream with zero content yields a silent empty reply *(Low)*
**File**: `internal/agent/openai.go:154-159`, `internal/web/chat.go:81-92`
**Category**: Correctness
**Issue**: If a provider returns HTTP 200 but every chunk is empty/malformed/skipped, `accumulated` is empty, `CompleteStream` returns success, and `RunChat` persists an empty assistant message while publishing nothing — the UI shows no reply and no error.
**Fix**: In `RunChat`, treat empty accumulated content on a successful stream as an error (or persist a visible "no response" marker) so the failure is observable.

### [LOW-004] Context budget bounds context_json only, not history/summary byte size *(Low)*
**File**: `internal/web/chat.go:45-74`
**Category**: Correctness / Performance
**Issue**: CON-007's two caps are enforced, but the prompt still grows unbounded from (a) Summary + RootCause (model-generated, usually short) and (b) the *content size* of each of the last 10 history turns — a user pasting a very large message is echoed back through history with no per-message or total-byte cap, which can still approach the model window.
**Fix**: Add a total-prompt or per-history-message byte cap alongside the turn-count cap if untrusted large inputs are expected.

## Info

### [INFO-001] SSE parser requires a trailing space after `data:`
**File**: `internal/agent/openai.go:129,132`
`strings.HasPrefix(line, "data: ")` skips lines using the spec-legal `data:` form without a space. OpenAI/Gemini emit `data: `, so this is safe for supported providers; note it as a portability limit.

### [INFO-002] RunChat runs synchronously inside the POST handler
**File**: `internal/web/server.go:175`, `internal/web/chat.go:27`
The HTTP POST blocks for the full stream duration, bounded only by `r.Context()` (client-driven). There is no server-side provider timeout; a slow provider holds the request open. Acceptable for the MVP/replicas:1 model — consider a bounded context if provider latency becomes an issue.

## What's Good

- The escaping story is genuinely airtight: a single `templ.Raw` chokepoint behind bluemonday, plaintext-escaped SSE deltas, and text-node rendering everywhere else — the two XSS invariants are provably held, not just asserted in comments.
- Coalesced-accumulated broker publishing is the right design for a lossy fan-out buffer, and the persisted message being authoritative closes the reconnect gap.
- Streaming error handling is complete: non-2xx, malformed chunks, and `scanner.Err()` are all handled, and errors propagate cleanly to a 500 rather than a silent empty stream.

## Pre-Merge Checklist

**Always:**
- [x] All Critical and High findings resolved (none found)
- [x] No secrets or credentials in committed files
- [x] Tests cover changed behaviour and unhappy paths (streaming_test.go covers malformed chunks / non-2xx)
- [x] All async calls awaited or errors handled
- [x] Resources closed in all code paths (resp.Body, rows, SSE cancel)

**If auth, sessions, or user data:**
- [x] No new auth surface; chat/SSE are per-incident read/append with parameterized queries
- [ ] No CSRF token on POST /chat (state-changing; no auth layer in MVP — track if auth is added)

## Machine-Readable Verdict

```yaml
verdict: Approve
critical: 0
high: 0
medium: 0
low: 4
info: 2
blocking_ids: []
```
