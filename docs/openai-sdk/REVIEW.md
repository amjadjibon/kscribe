---
date: 2026-07-03
feature: openai-sdk
reviewer: Claude
base: main...HEAD (openai-sdk-phase-1)
verdict: Approve
---

# Code Review: openai-sdk (Phase 1)

## Scope

Back the non-streaming `OpenAIClient.Complete` with the official `openai-go/v3`
SDK, preserving the provider-neutral `schema.Request`/`schema.Response` types,
`NewOpenAIClient` signature, multi-provider base-URL behavior, and SEC-001 error
truncation. Streaming (`CompleteStream`) intentionally left on the resilient
hand-rolled path — see LOOP.md for why Phase 2 (SDK streaming) was dropped.

## Findings

### [LOW-001] Empty API key now defers to SDK env fallback
**File**: internal/agent/openai.go:60
`sdkClient()` only adds `option.WithAPIKey` when `c.APIKey != ""`. With an empty
key the SDK's default options may pick up `OPENAI_API_KEY` from the environment,
whereas the old client sent no `Authorization` header at all. In practice the key
is always set from config/secret, and local providers ignore auth, so impact is
nil. Left as-is to match the "only send auth when present" intent.

### [INFO-001] Provider error string is byte-truncated
**File**: internal/agent/openai.go:93
`s[:512]` truncates on a byte boundary, which could theoretically split a
multi-byte rune. The SDK's `Error()` output is ASCII (method, URL, status, raw
JSON), so this cannot occur in practice; matches the previous byte-limited
behavior. No change needed.

## Verification

- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./...` — all packages pass
- Gate test `TestOpenAIClient_Complete_SDKRoundTrip` drives the SDK-backed
  `Complete` against a real `httptest` server: asserts the POST path
  (`/v1beta/openai/chat/completions`, no `/v1` collapse), bearer auth, outgoing
  model/messages/tools, the REQ-006 assistant-`tool_calls` + tool-result
  serialization, and the mapped-back content/tool_calls/usage.
- `TestOpenAIClient_Complete_ErrorTruncation` — 401 body of 4 KB returns a
  capped error (SEC-001).
- Existing `agent_test.go` tool-loop tests and `streaming_test.go` unchanged and
  green.

## What's Good

- The trickiest mapping (REQ-006, assistant messages carrying `tool_calls` via
  the explicit param struct) is covered by a real round-trip, not just the
  in-memory fake provider.
- Base-URL path preservation verified against the SDK's own `WithBaseURL`
  slash-normalization, so Gemini/Groq/Z.AI endpoints keep working.
- Honest scope call: streaming stays on the resilient path rather than
  regressing REQ-007 to satisfy the plan literally.

## Machine-Readable Verdict

```yaml
verdict: Approve
critical: 0
high: 0
medium: 0
low: 1
blocking_ids: []
```
