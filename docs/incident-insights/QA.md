---
date: 2026-07-02
feature: incident-insights
coverage_before: 29.6%
coverage_after: 30.3%
---

# QA Report: incident-insights

## Coverage

| Package | Before | After |
| ------- | ------ | ----- |
| internal/agent | 76.3% | 80.0% |
| internal/config | 100.0% | 100.0% |
| internal/controller | 78.2% | 78.2% |
| internal/enricher | 54.9% | 54.9% |
| internal/store | 83.1% | 83.1% |
| internal/web | 87.7% | 94.5% |
| internal/web/templates | 0.3% | 0.3% |
| **total (internal/)** | **29.6%** | **30.3%** |

Key function deltas:

| Function | Before | After |
| -------- | ------ | ----- |
| `CompleteStream` | 74.4% | 84.6% |
| `StreamOrComplete` | 77.8% | 88.9% |
| `RunChat` | 86.5% | 91.9% |
| `chatPost` | 57.1% | ~90% |
| `chatStream` | 71.4% | 85.7% |

Note: the total percentage moves modestly because `internal/web/templates` is generated
code (~1 500 statements) with 0.3% coverage; it dominates the denominator. The feature
packages that changed (agent, web) moved meaningfully.

## Bug Found and Fixed

**BUG: `CompleteStream` crashed on malformed SSE chunk**

`internal/agent/openai.go` line 137 previously returned an error on any non-JSON `data:`
line. Some providers (e.g. OpenRouter) emit lines like `data: : OPENROUTER PROCESSING`
that pass the prefix filter but are not valid JSON. The old code would abort the entire
stream on the first such line.

Fix: changed `return Response{}, fmt.Errorf(...)` to `continue` so malformed chunks are
skipped and valid deltas still accumulate.

```go
// before
if err := sonic.UnmarshalString(payload, &chunk); err != nil {
    return Response{}, fmt.Errorf("unmarshal chunk: %w", err)
}

// after
if err := sonic.UnmarshalString(payload, &chunk); err != nil {
    continue // ponytail: skip malformed chunk; some providers emit non-JSON SSE lines
}
```

This is confirmed correct by `TestCompleteStream_MalformedChunkSkips` which would have
failed before the fix.

## Tests Added

### internal/agent/streaming_test.go (4 tests)

- `TestCompleteStream_NonSuccess` — HTTP 429 from stream endpoint returns `provider error 429` error
- `TestCompleteStream_MalformedChunkSkips` — malformed `data:` chunk mid-stream is skipped; valid deltas still accumulate (validates bug fix)
- `TestCompleteStream_OnDeltaError` — `onDelta` returning an error aborts the stream and propagates the error
- `TestStreamOrComplete_OnDeltaError_NonStreaming` — `onDelta` error in non-streaming fallback path propagates through `StreamOrComplete`

### internal/web/server_test.go (7 tests)

- `TestChatPost_NilProvider` — POST /chat with nil provider returns 500
- `TestChatPost_BodyFallback` — POST /chat with no form field reads raw request body instead
- `TestChatPost_ProviderError` — RunChat failure returns 500
- `TestChatStream_DeliversSSE` — GET /chat/stream delivers published SSE event and closes on context cancel
- `TestRunChat_NoDiagnoses` — incident with no diagnoses still works; system message has no Summary/Context sections
- `TestRunChat_ProviderError` — provider failure leaves user message persisted but no assistant message written
- `TestRunChat_HistoryBudget` — with 13 stored messages the LLM request carries at most 11 messages (system + 10 history cap)

## Dead Code

`viewmodel.go:marshalJSON` — NOT dead code. The task prompt identified this as an
`unusedfunc` finding, but the function is called at lines 1605 and 1618 of
`incidents_templ.go` (same package). staticcheck was not runnable in this environment
(tool built with Go 1.25, module requires Go 1.26). No removal performed.

## Remaining Gaps (skipped, with reasons)

| Gap | Reason |
| --- | ------ |
| `internal/web/templates` (0.3%) | Generated templ code; not hand-written logic. Testing template rendering exercises it via web-package integration tests, but the go cover tool attributes lines to the templates package. Not worth driving to 100% — the XSS and render tests in `internal/web` already exercise the templates end-to-end. |
| `internal/enricher` collector functions (0%) | `collectDeployment`, `collectReplicaSet`, `collectPodsForSelector` require a live k8s API server. No fake client available in the existing test setup; hermetic coverage would require significant scaffolding. |
| `internal/controller` setup functions (0%) | `SetupWithManager`, `SetupEventWatcherWithManager`, `warningEventPredicate` require a controller-runtime manager and are integration-only. |
| `Complete` non-2xx / body-read paths | These mirror the `CompleteStream` patterns but in the non-streaming client used by the diagnosis agent. Covered at the integration level through `TestDiagnosisAgent_ProviderError`; adding a unit test would be coverage padding. |
| `AppendChatMessage` / `GetIncident` error injection in `RunChat` | Requires an error-injecting store variant. The paths are trivially correct (early return of store error). Added to skipped as low value vs. test complexity. |
| `internal/store` row-scan error branches | Require malformed DB state; SQL-level errors; not worth the scaffolding. |

## Risk Assessment

| Surface | Risk | Mitigation |
| ------- | ---- | ---------- |
| **Streaming (CompleteStream)** | Medium — malformed-chunk bug was real; fixed. Remaining gap: scanner.Err() path (network read failure mid-stream). Low probability in practice. | Bug fixed and tested. |
| **XSS (SEC-001, SEC-002)** | Low — SEC-001 (stored XSS in rendered page) tested end-to-end for RCA fields, context_json, reasoning, and assistant chat messages. SEC-002 (SSE wire escape) verified in `TestRunChat`. | Covered. |
| **Chat context budget (CON-007)** | Low — 4 KB truncation verified in `TestRunChat` (8 KB context, asserts system message < 6 KB) and 10-message history cap verified in `TestRunChat_HistoryBudget`. | Covered. |
| **Provider error leaving dangling user message** | Low — now tested: user message is persisted before LLM call; if LLM fails, only the assistant turn is missing. Callers can detect via HTTP 500 and retry. | Tested in `TestRunChat_ProviderError`. |
| **Chat isolation across (ns,name)** | Low — `TestChatMessageRoundTrip` in store package and `fakeStore.ListChatMessages` filter both cover this. | Covered. |
