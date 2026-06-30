---
date: 2026-06-30
plan: docs/dashboard-ui/PLAN.md
plan_version: 1.4
reviewer: Claude
verdict: Needs Revision
---

# Plan Review: dashboard-ui

## Verdict

**Needs Revision** — the plan is well-structured and the hard parts (SSE preservation, XSS sanitization, SQL-injection safety) are handled, but the root-level `go:embed` package will silently collide with the existing build-tagged `tools.go`, which must be resolved before Phase 1 ships.

## Findings

### [REVISE-001] Root `embed.go` package collides with `tools.go` under `-tags tools`
**Phase**: 1 (TASK-002)
**Issue**: The module root already contains `tools.go`, which declares `package tools` behind `//go:build tools`. TASK-002 adds a root `embed.go` as `package kscribe` with **no build constraint**. Under a normal build the two never coexist (tools.go is excluded), so `go build ./...`, `make templ`, and the current CI all pass — which is exactly why this is dangerous: the plan's completion criteria go green while the defect is latent. But any build that sets the `tools` tag over the root package (`go vet -tags tools ./...`, `go test -tags tools ./...`, and most editor/gopls analysis) compiles `embed.go` (always included) **and** `tools.go` (included under the tag) together, yielding `found packages kscribe (embed.go) and tools (tools.go) in <root>`. The agent will likely never notice because its CLI checks pass, and the breakage surfaces later as red IDE errors or a future tagged build.
**Fix**: Unify the root package name. Add a task to Phase 1: change `tools.go` to `package kscribe` (it only holds blank tool imports; the name is irrelevant to its function), so both files agree under every tag combination. Alternatively, keep the embed file out of the root package entirely by moving it (and `public/`) under a dedicated subpackage — but that conflicts with the stated "repo-root `public/`" requirement, so renaming `tools.go`'s package is the lower-friction fix. Either way, state explicitly in TASK-002 that the root directory must resolve to a single package name across `-tags tools`.

---

### [SUGGEST-001] Phase 1 bundles three goals and seven tasks
**Phase**: 1
**Issue**: Phase 1 covers (a) embedded static-asset serving infrastructure (`public/`, root `embed.go`, `/static/*` handler), (b) the CSS design system, and (c) theming + layout shell — seven tasks against the "~5 tasks / one goal per phase" guideline. It is the critical-path foundation, so a failure here is costly to debug across mixed concerns.
**Fix**: Optional split into "Phase 1a: embedded asset serving" (TASK-001..003, 007 — prove `/static/*` serves the embedded FS) and "Phase 1b: design system, theming, layout" (TASK-004..006). Acceptable as-is given the tight coupling, but splitting isolates the infra risk (REVISE-001 lives in 1a) from the visual work.

---

### [SUGGEST-002] `LIKE %?%` in TASK-021 is invalid SQL
**Phase**: 5 (TASK-021 checklist)
**Issue**: TASK-021 writes the free-text match as ``LIKE %?%``. That is not valid SQL — the wildcards cannot wrap a bound placeholder; they must be part of the argument value (`LIKE ?` bound to `"%" + q + "%"`). The Phase 5 Agent Prompt (the text the orchestrator actually hands the sub-agent) already states it correctly (`(name LIKE ? OR …)` with `%q%`), so impact is low, but the checklist line is misleading.
**Fix**: Change the checklist to `… matched via LIKE ? against name/message/reason, with the wildcards in the bound argument ("%"+q+"%")`.

---

### [SUGGEST-003] Requirement numbering is out of order and REQ-009 overstates "offline"
**Phase**: Frontmatter / §1
**Issue**: Requirements are interleaved oddly (REQ-007/REQ-008 appear before REQ-005/REQ-006; REQ-009 sits between CON entries), which makes the constraints harder to scan. Separately, REQ-009 claims the dashboard "works offline/air-gapped," but REQ-005 loads fonts from a CDN — so a truly air-gapped deploy would render without its fonts.
**Fix**: Renumber requirements in order and group CON-* together. Soften REQ-009 to "works offline except web fonts" or add a task to vendor the fonts under `public/fonts/` (ASSUMPTION-003 already anticipates this).

---

### [SUGGEST-004] Phase 1 and Phase 2 completion criteria omit `go vet ./...`
**Phase**: 1, 2
**Issue**: Phases 3–5 include `go vet ./...` in their completion criteria; Phases 1–2 do not. Phase 1 introduces a new root package and an `fs.Sub`/file-server wiring that vet can catch issues in.
**Fix**: Add `go vet ./...` to the Phase 1 and Phase 2 completion criteria for consistency.

---

## What's Good

The genuinely tricky failure modes are anticipated with explicit mitigations and tests: SSE survival across the tab refactor is pinned to `x-show` (not `x-if`) with a `sse-connect` assertion (RISK-002); untrusted LLM Markdown is sanitized at a single `RenderMarkdown` chokepoint with a `<script>`-stripping test (SEC-001/RISK-001); and the filter layer is parameterized with an injection-literal test (SEC-002/RISK-005).

The phases are correctly ordered and stacked — store changes land before the UI that consumes them (Phase 4 paging before Phase 5 filtering), and each phase has a self-contained Agent Prompt with specific file paths, runnable completion criteria, and the real types/fields from the codebase.

The architectural decisions carry rationale rather than assertion: server-side Markdown over client JS (ASSUMPTION-002), offset over cursor pagination with the trade-off named (ASSUMPTION-005), and the root-embed placement explained by the `go:embed` `../` limitation (ASSUMPTION-008) — the last of which is exactly right about *why* the file must be at the root, it just missed the `tools.go` package-name interaction (REVISE-001).

## Machine-Readable Verdict

```yaml
verdict: Needs Revision
block: 0
revise: 1
suggest: 4
blocking_ids: []
```
