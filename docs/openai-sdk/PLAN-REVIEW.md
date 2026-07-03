---
date: 2026-07-03
plan: docs/openai-sdk/PLAN.md
plan_version: 1.0
reviewer: Claude
verdict: Ready
---

# Plan Review: openai-sdk

## Verdict

**Blocked** — the plan's Phase 1 verification rests on a factual error about the existing tests: no test exercises `Complete` over HTTP today, so the SDK-backed `Complete` (the core deliverable) can ship with the completion criteria green and zero coverage. Three further Revise-level gaps drop deliberate safeguards.

## Findings

### [BLOCK-001] Phase 1 completion criteria pass without ever exercising the SDK-backed `Complete`
**Phase**: 1 (TASK-004, Completion criteria)
**Issue**: The plan states "the existing fake httptest server(s) should still drive `Complete` via the SDK — pass `srv.URL`" and "the existing tests point `NewOpenAIClient` at httptest servers." That is false for the non-streaming path. `internal/agent/agent_test.go` uses an in-memory `fakeProvider` struct (a hand-written `Provider`, lines 13-29) — it never constructs `NewOpenAIClient` and never touches HTTP. Only `streaming_test.go` drives `NewOpenAIClient` against an httptest server. Consequences of following the plan literally:
- There is no existing non-streaming httptest test to "update." An implementer will find nothing to point at `srv.URL`.
- `go test ./internal/agent ./internal/controller` (the Phase 1 completion criteria) passes with the entire SDK `Complete` mapping — message translation, tool-definition params, tool_call round-trip, error wrapping — completely unexercised, because the diagnosis tests bypass the client via `fakeProvider`.
- The single most error-prone piece of the change (assistant-message-with-tool_calls → SDK params → response → `agent.ToolCall`) has no test at all.
**Fix**: Rewrite TASK-004 to **add** a new httptest-based test that constructs `NewOpenAIClient(srv.URL, "", model)` and asserts, over the wire: (a) a normal content response maps to `Response.Choices[0].Message.Content` + `Usage.TotalTokens`; (b) a response containing `tool_calls` surfaces as `Response.Choices[0].Message.ToolCalls` (id/type/function.name/arguments); (c) a request whose `Messages` include an assistant turn carrying `ToolCalls` and a following `tool` turn with `ToolCallID` is accepted and serialized correctly (inspect the request body the server receives); (d) a non-2xx returns a wrapped error. Make this test — not the `fakeProvider` tests — the Phase 1 completion gate. Correct the plan's claim that these tests already exist.

---

### [REVISE-001] 512-byte provider-error truncation safeguard is silently dropped
**Phase**: 1 (TASK-002), 2 (TASK-005)
**Issue**: `openai.go:75-78` truncates the provider error body to 512 bytes with an explicit rationale: "so raw provider detail is not embedded in durable CR status / SQLite." `diagnosis_agent.go:66` writes `err.Error()` straight into `Outcome.RawError`, which is persisted to the Diagnosis CR status and SQLite. The SDK's error type (`openai.Error`) carries the full response body and the echoed request. The plan says only "preserve the 'provider error …' style" — it does not mention preserving the truncation. As written, full (potentially large, potentially sensitive echoed-request) provider error bodies will land in durable storage.
**Fix**: In the error-wrapping code, extract the status code from the SDK error and truncate its message to ~512 bytes before wrapping, e.g. `fmt.Errorf("provider error %d: %s", apiErr.StatusCode, truncate(apiErr.Error(), 512))`. Add an assertion to the BLOCK-001 test that a long error body is truncated. Note this safeguard explicitly in TASK-002/TASK-005.

---

### [REVISE-002] Malformed-SSE-chunk resilience (a QA-fixed behavior) is likely lost, and the plan hand-waves it
**Phase**: 2 (TASK-007)
**Issue**: `TestCompleteStream_MalformedChunkSkips` (streaming_test.go:117) pins a fixed bug: a `data: NOT VALID JSON` line must be skipped, not fatal. The current code does this deliberately (openai.go:137-138). The plan says "the SDK tolerates it — keep an equivalent or drop it with a comment." This is an undocumented, unverified assumption. openai-go's `ssestream` decoder JSON-unmarshals each data event into the chunk type; a malformed data line sets a terminal error and ends iteration. If so, a single junk line from a noisy OpenAI-compatible provider will abort the whole stream and discard accumulated content — a real behavior regression versus today, on the exact case QA previously fixed.
**Fix**: Before Phase 2, verify the SDK's actual behavior on a malformed `data:` line (write a throwaway test against the SDK). If it aborts (likely), the plan must decide explicitly: either wrap the stream to tolerate decode errors, or accept the regression and document it as a behavior change in Risks with reasoning. Do not leave "drop the test if the SDK handles it" as the instruction — that lets a regression through silently.

---

### [REVISE-003] Assistant-with-tool_calls reconstruction is the correctness-critical, untested step; the SDK convenience helper does not cover it
**Phase**: 1 (TASK-002)
**Issue**: The diagnosis loop appends the assistant message that requested tools back into `messages` (diagnosis_agent.go:84) and re-sends the whole history on the next `Complete` call. So the adapter must serialize an assistant turn *carrying `ToolCalls`* into SDK params, followed by `tool` turns with matching `tool_call_id`. `openai.AssistantMessage(content)` takes only content — attaching `ToolCalls` requires hand-building `openai.ChatCompletionAssistantMessageParam{ToolCalls: ...}`. If this is gotten wrong (tool_calls dropped, or ids not matching the following tool message), strict providers reject the follow-up request and the tool loop breaks after the first round. The plan mentions "include ToolCalls when present" but treats it as a one-liner; combined with BLOCK-001 there is no test covering a second `Complete` call with tool_calls in the history.
**Fix**: Call out in TASK-002 that assistant-with-tool_calls needs the explicit param struct (not the helper), and that `tool_call_id` on tool turns must match. The multi-turn case must be covered by the BLOCK-001 test (assert the second request body contains the assistant `tool_calls` and the matching `tool` message).

---

### [SUGGEST-001] Pin an explicit SDK version in the plan
**Phase**: Risks (RISK-001)
**Issue**: RISK-001 says "the implementer pins a specific v3 version" but the plan names none. Message unions and the streaming iterator API have shifted across openai-go v3 minors; leaving the version unpinned invites drift between what the implementer reads and what CI resolves.
**Fix**: Name the target version (e.g. `github.com/openai/openai-go/v3 v3.x.y`) in TASK-001 so the two sub-agents build against the same API shape.

---

### [SUGGEST-002] Spell out the SDK-error → "provider error %d" mapping
**Phase**: 1 (TASK-002), 2
**Issue**: `TestCompleteStream_NonSuccess` asserts `strings.Contains(err.Error(), "provider error 429")`. The SDK surfaces failures as `*openai.Error` with a `StatusCode` field, not the raw HTTP status the old code read. The adapter must type-assert/`errors.As` the SDK error to recover the status and reproduce the string, or that test (and the equivalent Complete assertion) fails.
**Fix**: Add a line to TASK-002 noting the error must be mapped via `errors.As(err, &apiErr)` → `fmt.Errorf("provider error %d: ...", apiErr.StatusCode, ...)` (pairs with REVISE-001).

---

### [SUGGEST-003] Streaming usage requires opting in
**Phase**: 2 (TASK-005)
**Issue**: TASK-005 says "return usage if the SDK exposes it on the final chunk." openai-go only emits a usage chunk when `StreamOptions{IncludeUsage: true}` is set on the request; otherwise usage is always zero. Not a blocker (chat ignores usage), but "if available" is misleading.
**Fix**: Either set `StreamOptions.IncludeUsage` explicitly, or state that streaming usage is intentionally left at zero (consistent with ASSUMPTION-002).

---

### [SUGGEST-004] No task creates the base-URL normalization unit test that RISK-002 promises
**Phase**: 1 (TASK-001), Risks (RISK-002)
**Issue**: RISK-002's mitigation promises "a test points the SDK at an httptest server and asserts the request lands," but no TASK or TEST line creates a dedicated normalization test (empty → SDK default; non-empty → trailing-slash appended so `…/v1` + `chat/completions` resolves to `…/v1/chat/completions`). The streaming tests exercise it only incidentally, and their handlers ignore the request path, so a path-join bug would not be caught.
**Fix**: Add a small unit test asserting the normalized base and that the SDK POSTs to the expected `…/chat/completions` path (inspect `r.URL.Path` in the handler).

## What's Good

- Correct adapter boundary: keeping `NewOpenAIClient` signature and the `Request`/`Response`/`Message`/`ToolCall`/`ToolDefinition` DTOs unchanged means `diagnosis_agent.go`, `chat.go`, `main.go`, and the controller stay untouched — the blast radius really is one file.
- Sound phase split: non-streaming `Complete` (with the tool loop) before streaming `CompleteStream`, each with runnable `go build`/`vet`/`test` completion criteria and a clean dependency declaration.
- CON-003 is handled correctly: `FunctionDef.Parameters` is already `map[string]any` and tool-call `Arguments` are already JSON strings, so the adapter can map both directions without introducing `encoding/json`, and the SDK's internal JSON is correctly treated as an allowed boundary.
- The dependency trade-off (RISK-003) and tool-argument-string fidelity (RISK-004) are explicitly acknowledged.

## Machine-Readable Verdict

```yaml
verdict: Ready
block: 0
revise: 0
suggest: 0  # all resolved in plan v1.1
blocking_ids: []
```
