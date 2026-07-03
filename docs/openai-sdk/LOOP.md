---
feature: openai-sdk
task: implement docs/openai-sdk (back the LLM client with the official openai-go SDK)
branch: openai-sdk
started: 2026-07-03
max_iterations: 3
max_phases: 5
max_agents: 3
current_iteration: 1
status: running
last_review_base: ''
---

# Dev Loop: openai-sdk

## Iterations

| Iter | Verdict | Crit | High | Med | Low | Mode | Action |
|------|---------|------|------|-----|-----|------|--------|
| 1    | Approve | 0    | 0    | 0   | 1   | inline | Phase 1 implemented inline; Phase 2 dropped (see note) |

## Stacked PRs

| Phase   | Branch               | PR URL | Base                 | Status  |
|---------|----------------------|--------|----------------------|---------|
| phase-1 | openai-sdk-phase-1   | —      | main                 | ready   |
| phase-2 | openai-sdk-phase-2   | —      | openai-sdk-phase-1   | dropped |

> **Phase 2 (SDK streaming) dropped.** The openai-go SDK's `ssestream.Stream.Next()`
> sets an error and aborts on any malformed JSON chunk — it cannot skip bad
> chunks. That conflicts with REQ-007 (malformed-chunk resilience), a robustness
> guard the codebase deliberately added for non-conforming providers (Groq /
> LM Studio). Migrating `CompleteStream` to the SDK would be a net regression
> (lost resilience, broken error-format tests) for no functional gain. The
> hand-rolled streaming path (sonic + bufio, resilient) is kept; only `Complete`
> — the RCA/tool-calling path — is SDK-backed.

## Active Worktrees

| Worktree path | Branch | Purpose | Status |
|---------------|--------|---------|--------|

## Log

### Iteration 1
- [x] implement-plan (Phase 1 inline; Phase 2 dropped — SDK streaming regresses REQ-007)
- [x] qa (existing agent/streaming tests + new SDK gate test cover the change)
- [x] code-review (Approve — 0 crit/high/med, 1 low, 1 info; see REVIEW.md)
- [x] decide (Clean Exit — awaiting push approval)
