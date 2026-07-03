---
goal: Back the LLM provider with the official openai-go SDK
version: 1.1
date_created: 2026-07-03
last_updated: 2026-07-03
owner: amjadjibon
status: 'Planned'
tags: [refactor, backend]
---

# Back the LLM Client with the Official openai-go SDK

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

Replace kscribe's hand-rolled HTTP chat-completions client (`internal/agent/openai.go`) with the official `github.com/openai/openai-go/v3` SDK, as an **adapter** behind the existing `Provider`/`StreamingProvider` interfaces. The public shape — `NewOpenAIClient(baseURL, apiKey, model)`, the `agent.Request`/`Response`/`Message`/`ToolCall`/`ToolDefinition` DTOs, the diagnosis tool-call loop, the chat backend, and `cmd/kscribe/main.go` — stays unchanged, so multi-provider support (OpenAI / Gemini / Z.AI / Groq / LM Studio via base URL) and the redaction/tool pipeline are preserved. Only the HTTP/JSON guts change: the SDK gains us maintained request/response handling, robust SSE stream parsing, and typed params.

## 1. Requirements & Constraints

- **REQ-001**: `OpenAIClient` must internally use `github.com/openai/openai-go/v3` for both `Complete` (non-streaming, with tool calls) and `CompleteStream` (content-delta streaming), while keeping the `agent.Provider`/`agent.StreamingProvider` interfaces and the `Request`/`Response`/`Message`/`Choice`/`Usage`/`ToolCall`/`ToolDefinition` DTOs unchanged.
- **REQ-002**: `NewOpenAIClient(baseURL, apiKey, model string) *OpenAIClient` keeps its exact signature. `baseURL` maps to `option.WithBaseURL` (empty → SDK's OpenAI default); `apiKey` → `option.WithAPIKey`; `model` is used per request. Arbitrary model strings (e.g. `openai/gpt-oss-20b`, `glm-4.6`, `llama-3.3-70b-versatile`) must work via `openai.ChatModel(model)`.
- **REQ-003**: Multi-provider base URLs must keep working — a base like `https://api.groq.com/openai/v1` must resolve to `…/v1/chat/completions` (handle the SDK's trailing-slash/base-path expectation).
- **REQ-004**: Non-streaming `Complete` must round-trip tool definitions (request) and tool calls (response) so the diagnosis tool-call loop in `diagnosis_agent.go` still works: `Request.Tools` → SDK tool params; SDK response `message.tool_calls` → `Response.Choices[0].Message.ToolCalls` (id/type/function.name/arguments); `finish_reason` and `usage.total_tokens` preserved.
- **REQ-005**: Streaming `CompleteStream` must emit content deltas via `onDelta` and return the accumulated full `Response` (chat uses content only — no tool-call streaming needed). Request `StreamOptions.IncludeUsage=true` so the final chunk carries usage.
- **SEC-001**: Provider error bodies must be truncated to ~512 bytes before being returned (they flow into durable CR status + SQLite via `diagnosis_agent.go`). The SDK's error carries the full body — the adapter must truncate it, preserving the current `openai.go:76` behavior.
- **REQ-006**: Assistant turns that carry `tool_calls` (re-sent by the diagnosis loop at `diagnosis_agent.go:84`) must be mapped with the explicit assistant-message param struct including its `ToolCalls`, NOT `openai.AssistantMessage(content)` — otherwise the re-sent tool-call context is lost. Tool turns must set `tool_call_id`.
- **REQ-007**: Malformed/partial SSE chunks must not abort a chat stream — preserve the resilience proven by `TestCompleteStream_MalformedChunkSkips`. Verify openai-go's stream decoder behavior; if it errors on a bad `data:` line, the adapter must recover (return accumulated content, don't hard-fail) and a test must pin this.
- **CON-004**: Pin the openai-go SDK to a specific `v3.x.y` version in `go.mod` (no floating).
- **CON-001**: CON-003 (repo-wide) — no `encoding/json` in application code. The SDK does its own JSON internally (allowed dependency boundary); the adapter itself must not introduce `encoding/json` (use `sonic` if it ever needs to encode/decode, e.g. tool-argument passthrough).
- **CON-002**: `go test ./...` and `go vet ./...` stay green; existing agent tests (which point `NewOpenAIClient` at `httptest` servers emitting OpenAI-compatible JSON) are updated to work against the SDK, not deleted.
- **CON-003**: No behavior change for callers — `cmd/kscribe/main.go`, `internal/web/chat.go`, `internal/controller`, and `internal/agent/diagnosis_agent.go` compile and pass unchanged (except test wiring).

## 2. Implementation Steps

> After completing all tasks in a phase, `git add -u` and commit. No `Co-authored-by:`. Tick `[x]` as each task completes.

### Phase 1: Non-Streaming Complete via the SDK

**Goal**: Swap `OpenAIClient.Complete` to the openai-go SDK, including full tool-call translation, so the diagnosis loop runs on the SDK.

- [ ] TASK-001: `go get github.com/openai/openai-go/v3`. In `internal/agent/openai.go`, give `OpenAIClient` an SDK client field (built lazily or in `NewOpenAIClient`) via `openai.NewClient(opts...)` where opts include `option.WithAPIKey(apiKey)` (only if non-empty) and `option.WithBaseURL(normalizedBaseURL)` (only if `baseURL != ""`; normalize to end with `/`). Keep `BaseURL/APIKey/Model` fields for compatibility.
- [ ] TASK-002: Reimplement `Complete(ctx, req Request) (Response, error)` using `client.Chat.Completions.New(ctx, params)`: map `req.Messages` → `[]openai.ChatCompletionMessageParamUnion` (system/user/assistant/tool, carrying tool_calls on assistant turns and tool_call_id on tool turns), `req.Tools` → the SDK's function-tool params (name/description/parameters), `req.Model` → `openai.ChatModel(c.Model)`. Map the SDK response back to `Response`: `Choices[0].Message.Content`, `.ToolCalls` (→ `agent.ToolCall{ID,Type:"function",Function:{Name,Arguments}}`), `FinishReason`, `Usage.TotalTokens`. Map an SDK error to a `provider error %d: %s`-style message with the body TRUNCATED to ~512 bytes (SEC-001 — preserve `openai.go:76` behavior). Assistant turns carrying `ToolCalls` must use the explicit assistant param struct with those tool calls (REQ-006), not `openai.AssistantMessage(content)`; tool turns set `tool_call_id`.
- [ ] TASK-003: Delete the now-dead hand-rolled request/response HTTP code in `openai.go` that `Complete` used (keep `CompleteStream` working for now — Phase 2 migrates it; if they shared helpers, keep those until Phase 2).
- [ ] TASK-004 (Phase 1 GATE): The existing `agent_test.go` `Complete` tests use an in-memory `fakeProvider` and never touch HTTP/the SDK — so they do NOT exercise the new mapping. ADD a new httptest-backed test that constructs `NewOpenAIClient(srv.URL, "k", "m")` against a fake `/chat/completions` handler and asserts the SDK round-trip: (a) a response with `tool_calls` surfaces in `Response.Choices[0].Message.ToolCalls` (id/name/arguments intact), (b) a normal content response maps to `Choices[0].Message.Content` + `FinishReason` + `Usage.TotalTokens`, (c) a non-2xx returns a truncated (~512B) provider error, and (d) an assistant-with-tool_calls request message is serialized with its tool_calls (assert the request body the fake server received). This test — not the fakeProvider tests — is the Phase 1 completion gate. Add a base-URL normalization test (empty→SDK default; `…/v1`→request lands on `…/v1/chat/completions`).

**Completion criteria**: `go build ./...`, `go vet ./...` pass; `go test ./internal/agent ./internal/controller` pass INCLUDING the new httptest round-trip test (TASK-004) that exercises the SDK-backed `Complete` over the wire (tool-call round-trip, content, truncated provider error, assistant-tool_calls serialization, base-URL normalization); the diagnosis tool-call loop works end-to-end through the SDK.

**git commit**: `git add -u && git commit -m "refactor: back Complete with the openai-go SDK"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of openai-sdk.

Context: kscribe (Go 1.26, module github.com/amjadjibon/kscribe) has a hand-rolled OpenAI-compatible client in internal/agent/openai.go implementing agent.Provider (Complete) and agent.StreamingProvider (CompleteStream). This phase replaces the NON-STREAMING Complete with the official github.com/openai/openai-go/v3 SDK, as an adapter behind the SAME interfaces and DTOs. Keep NewOpenAIClient(baseURL, apiKey, model) *OpenAIClient signature. Do NOT change agent.Request/Response/Message/Choice/Usage/ToolCall/ToolDefinition/FunctionDef in schema.go, the diagnosis tool loop in diagnosis_agent.go, or cmd/kscribe/main.go.

Branch: openai-sdk-phase-1  |  Base: main

Read first: internal/agent/openai.go (current Complete + CompleteStream + streamChunk), internal/agent/schema.go (the DTOs), internal/agent/llm.go (Provider/StreamingProvider/StreamOrComplete), internal/agent/diagnosis_agent.go (how it calls Complete + reads Response.Choices[0].Message.ToolCalls in a loop), internal/agent/tools.go (ToolDefinition/FunctionDef shapes), internal/agent/agent_test.go (existing fake-server tests).

Tasks:
- TASK-001: `go get github.com/openai/openai-go/v3` (and its option subpackage, e.g. github.com/openai/openai-go/v3/option). Add an SDK client to OpenAIClient built in NewOpenAIClient: openai.NewClient(option.WithAPIKey(apiKey) if apiKey!="", option.WithBaseURL(base) if baseURL!="" — normalize base to end with "/"). Keep the existing BaseURL/APIKey/Model fields.
- TASK-002: Reimplement Complete(ctx, req) using client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{...}). Map req.Messages -> []openai.ChatCompletionMessageParamUnion: role "system"->openai.SystemMessage, "user"->openai.UserMessage, "assistant"->openai.AssistantMessage (include ToolCalls when present), "tool"->openai.ToolMessage(content, toolCallID). Map req.Tools (ToolDefinition{Function:{Name,Description,Parameters map[string]any}}) -> the SDK's function tool params (params.Tools). Model: openai.ChatModel(c.Model). Map the response back to agent.Response: Choices[0].Message.Content; ToolCalls -> []agent.ToolCall{ID, Type:"function", Function:agent.FunctionCall{Name, Arguments}}; FinishReason (string); Usage.TotalTokens (int). On error, return a `provider error %d: %s`-style message with the body TRUNCATED to ~512 bytes (SEC — this string is persisted to CR status + SQLite, don't leak the full body). CRITICAL (REQ-006): assistant turns that carry ToolCalls must be built with the explicit assistant param struct (e.g. openai.ChatCompletionAssistantMessageParam with ToolCalls set), NOT openai.AssistantMessage(content) — the diagnosis loop re-sends the assistant tool-call turn and it must round-trip; tool turns set tool_call_id. No encoding/json — sonic only if needed (tool arguments are already JSON strings; pass through verbatim).
- TASK-003: Remove the dead hand-rolled HTTP code that only Complete used. Leave CompleteStream + streamChunk intact (Phase 2 migrates streaming); if Complete and CompleteStream shared a request-building helper, keep it until Phase 2.
- TASK-004 (GATE): NOTE — agent_test.go's Complete tests use an in-memory fakeProvider and DO NOT touch HTTP/the SDK, so they won't exercise your mapping. You MUST add a NEW httptest-backed test (e.g. TestOpenAIClient_Complete_SDKRoundTrip) that builds NewOpenAIClient(srv.URL, "k", "m") against a fake /chat/completions handler and asserts, over the wire: (a) a response with tool_calls surfaces in Response.Choices[0].Message.ToolCalls (id/name/arguments intact); (b) a content response maps Content+FinishReason+Usage.TotalTokens; (c) a non-2xx yields a ~512B-truncated provider error; (d) send a Request whose messages include an assistant turn with ToolCalls + a following tool turn, and assert the request body the server RECEIVED contains those tool_calls + tool_call_id (proves REQ-006). Also add a base-URL normalization test: empty base → SDK default; base '<srv>/v1' → the request path is '/v1/chat/completions'. Keep TestDiagnosisAgent_* passing.

Constraints: CON-003 (no encoding/json in app code; SDK's internal JSON is a fine dependency boundary). go build/vet/test green. Do not touch schema.go DTOs, diagnosis_agent.go, main.go, web/chat.go.

Completion criteria: go build ./..., go vet ./..., go test ./internal/agent ./internal/controller pass.

When done: git add -u && git commit -m "refactor: back Complete with the openai-go SDK" — no Co-authored-by
Write a one-paragraph summary + commit SHA and note the SDK version pinned. Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 2: Streaming CompleteStream via the SDK

**Goal**: Migrate `CompleteStream` to the SDK's streaming API so the chat backend streams through the SDK, and remove the last hand-rolled SSE parsing.

**Depends on**: Phase 1 complete

- [ ] TASK-005: Reimplement `CompleteStream(ctx, req, onDelta)` using `client.Chat.Completions.NewStreaming(ctx, params)` (same param mapping as Phase 1, streaming variant). Set `StreamOptions.IncludeUsage=true` on the params so the final chunk carries usage. Iterate the stream, call `onDelta(chunk.Choices[0].Delta.Content)` for each non-empty content delta, accumulate the full content, and return a `Response` with the accumulated `Choices[0].Message.Content` + usage. Propagate `onDelta` errors. On the stream's terminal error (`stream.Err()`), do NOT hard-fail on a malformed/partial chunk — return the content accumulated so far (REQ-007, preserving the QA-fixed resilience); only return an error for a genuine transport failure.
- [ ] TASK-006: Remove the now-dead `streamChunk` struct and any remaining hand-rolled SSE/`bufio.Scanner` parsing in `openai.go`.
- [ ] TASK-007: Update `internal/agent/streaming_test.go`: the fake SSE servers emit OpenAI-compatible `data:` chunks — the SDK's stream reader consumes the same format, so point `NewOpenAIClient` at the test server and assert the same behavior (deltas collected, accumulated content, `[DONE]`/malformed-chunk resilience is now the SDK's job — keep an equivalent assertion or drop the malformed-chunk case if the SDK handles it, noting the change). Keep `StreamOrComplete` fallback tests passing.

**Completion criteria**: `go test ./internal/agent ./internal/web` pass (chat streaming works through the SDK); `go build ./...`, `go vet ./...` pass; `openai.go` no longer contains hand-rolled HTTP or SSE parsing.

**git commit**: `git add -u && git commit -m "refactor: back CompleteStream with the openai-go SDK"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 2 (final) of openai-sdk.

Context: kscribe's internal/agent/openai.go now backs Complete with the openai-go v3 SDK (Phase 1). CompleteStream is still hand-rolled SSE parsing. This phase migrates streaming to the SDK's streaming API. Keep the agent.StreamingProvider interface and DTOs unchanged; chat (internal/web/chat.go via agent.StreamOrComplete) must keep working.

Branch: openai-sdk-phase-2  |  Base: openai-sdk-phase-1

Read first: internal/agent/openai.go (the SDK client from Phase 1 + the old CompleteStream + streamChunk), internal/agent/llm.go (StreamingProvider/StreamOrComplete), internal/agent/streaming_test.go (fake SSE servers), internal/web/chat.go (RunChat uses StreamOrComplete for chat content streaming).

Tasks:
- TASK-005: Reimplement CompleteStream(ctx, req, onDelta func(string) error) (Response, error) using client.Chat.Completions.NewStreaming(ctx, params) with the same param mapping as Phase 1's Complete (reuse a shared param-builder helper). Set StreamOptions.IncludeUsage=true on the params. Iterate: for stream.Next() { chunk := stream.Current(); delta := chunk.Choices[0].Delta.Content; if delta != "" { if err := onDelta(delta); err != nil { return ..., err }; accumulate } }; after the loop check stream.Err(). Return a Response whose Choices[0].Message.Content is the accumulated text + usage. REQ-007: a malformed/partial chunk must NOT hard-fail the stream — first verify openai-go's decoder behavior; if it errors on a bad data: line, recover by returning the accumulated content instead of the error (preserve the QA-fixed TestCompleteStream_MalformedChunkSkips behavior). No encoding/json.
- TASK-006: Delete the streamChunk struct and any remaining hand-rolled SSE/bufio parsing in openai.go — the file should no longer do raw HTTP or SSE.
- TASK-007: Update internal/agent/streaming_test.go to drive CompleteStream through the SDK: point NewOpenAIClient at the fake SSE httptest server (it emits data: {...} chunks + data: [DONE]); assert deltas are delivered and the accumulated content is correct, and that StreamOrComplete still falls back to one delta for a non-streaming provider. REQ-007: the fake SSE server must include a malformed/partial `data:` line mid-stream; assert CompleteStream still returns the accumulated valid content (does not hard-fail) — verify openai-go's decoder behavior first and, if it errors, recover in CompleteStream by returning accumulated content. Do NOT silently drop this coverage. Keep internal/web chat tests green.

Constraints: CON-003 (no encoding/json). go build/vet/test green.

Completion criteria: go test ./internal/agent ./internal/web pass; go build ./..., go vet ./... pass; openai.go has no hand-rolled HTTP/SSE left.

When done: git add -u && git commit -m "refactor: back CompleteStream with the openai-go SDK" — no Co-authored-by
Write a one-paragraph summary + commit SHA. Do NOT push, open PRs, or modify PLAN.md.
```

---

## 3. Testing

- [ ] TEST-001: `go test ./internal/agent` — `Complete` tool-call round-trip, content, and provider-error paths pass against a fake server via the SDK (Phase 1); `CompleteStream` delta delivery + `StreamOrComplete` fallback pass (Phase 2).
- [ ] TEST-002: `go test ./internal/controller` — the diagnosis tool-call loop drives to Done/Failed through the SDK-backed provider (unchanged behavior).
- [ ] TEST-003: `go test ./internal/web` — chat streaming (`RunChat` → `StreamOrComplete`) still streams and persists.
- [ ] TEST-004: `go build ./...`, `go vet ./...`, `go test ./...` all green.
- [ ] TEST-005: Manual — point at Groq (`LLM_BASE_URL=https://api.groq.com/openai/v1`, `LLM_MODEL=openai/gpt-oss-20b`, a real key) and confirm a real diagnosis + chat exchange works end to end.

## 4. Risks & Assumptions

- **RISK-001**: The openai-go v3 param/response API (message unions, tool params, streaming iterator) differs across SDK minor versions — mitigation: the implementer pins a specific v3 version, reads the SDK's own types, and the fake-server tests validate the wire behavior; adapter is confined to `openai.go`.
- **RISK-002**: Base-URL/path handling — the SDK appends `chat/completions` to the configured base, and expects a trailing slash; a wrong base breaks every provider — mitigation: normalize the base to end with `/`, and a test points the SDK at an httptest server and asserts the request lands (REQ-003).
- **RISK-003**: The SDK is a heavier dependency (pulls in tidwall/gjson etc.) than the ~150-line hand-rolled client — accepted for maintained request/response + robust SSE handling; the adapter keeps blast radius to one file.
- **RISK-004**: Tool-call argument fidelity — the SDK exposes `tool_calls[].function.arguments` as a JSON string; kscribe passes it straight to the tool executor — mitigation: pass through verbatim (no re-encode), tested by the tool-call round-trip.
- **ASSUMPTION-001**: `github.com/openai/openai-go/v3` supports `option.WithBaseURL` for OpenAI-compatible providers (Groq/Gemini/Z.AI/LM Studio) — true; the SDK is explicitly usable against compatible endpoints.
- **ASSUMPTION-002**: Streaming is content-only for kscribe's use (chat); the diagnosis tool loop uses non-streaming `Complete`, so `CompleteStream` need not reconstruct streamed tool calls.
- **ASSUMPTION-003**: Phase branches use hyphenated `openai-sdk-phase-N` to avoid a git ref D/F conflict with the `openai-sdk` plan branch.
