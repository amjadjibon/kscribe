---
date: 2026-06-30
branch: dashboard-ui-phase-6
reviewer: Claude
verdict: Approve
---

# Code Review: dashboard-ui

## Verdict

**Approve** — All security invariants (XSS sanitization, parameterized SQL), pagination math, SSE-tab persistence, embed safety, and CON-003 hold; only two minor non-blocking observations.

## Summary

Reviewed the full `git diff main...HEAD` for the web dashboard rework: the Markdown sanitizer (`internal/web/templates/markdown.go`), the dynamic SQL builder and pagination/count queries (`internal/store/sqlite.go`), the list handler and static-asset handler (`internal/web/server.go`), the templ sources, and the embed package. The high-risk areas the brief called out are all correctly implemented and well-tested (injection-literal test, javascript:/iframe sanitizer tests, filter+paging composition test, by-phase-ignores-phase test). `templ generate` reproduces the committed `_templ.go` with zero diff; `go test ./internal/web/... ./internal/store/...` passes. No Critical/High/Medium findings.

## Findings

### [LOW-001] Long-lived Cache-Control on unversioned static assets *(Low)*
**File**: `internal/web/server.go:49`
**Category**: Correctness
**Issue**: `/static/*` sets `Cache-Control: public, max-age=3600` on content-unversioned paths (e.g. `/static/js/alpine.min.js`, `/static/css/app.css`). After a deploy that changes an embedded asset, clients can serve a stale CSS/JS for up to an hour, producing visual or behavioral skew against freshly-rendered HTML. Acceptable for an internal tool, but worth noting.
**Fix**: Either lower `max-age` (e.g. 300) or append a build-hash/`?v=` query string to asset URLs in `layout.templ` and bump `max-age` to `immutable`. No change required if a 1h staleness window is acceptable.

### [INFO-001] Fonts still loaded from external CDN despite self-hosting goal *(Info)*
**File**: `internal/web/templates/layout.templ:43-44` (preconnect + stylesheet to `fonts.bunny.net`)
**Category**: Simplicity
**Issue**: The feature self-hosts CSS/JS/icons via go:embed, but typography still depends on `fonts.bunny.net`. This leaves a runtime network dependency (offline/air-gapped clusters lose the brand font and incur a layout shift) and a third-party request the self-hosting effort otherwise eliminates. The pre-paint theme script prevents color FOUC, but font swap can still flash.
**Fix**: Optional — embed the two woff2 font files into `public/` and serve from `/static/fonts/` with `font-display: swap` if full self-containment is desired.

## What's Good

- **SEC-001 fully closed**: `RenderMarkdown` (markdown.go:14-19) is the sole path RCA text reaches the page (confirmed: `DiagnosisBlock` is the only consumer, via `@RenderMarkdown` on Summary/RootCause/Remediation). goldmark is used at default config (no `html.WithUnsafe()`), and output is run through `bluemonday.UGCPolicy().SanitizeBytes` *before* `templ.Raw` — so even raw-HTML passthrough would be stripped. Tests cover `<script>`, `onerror=`, `javascript:` href, and `<iframe>`.
- **SEC-002 fully closed**: `buildWhere` (sqlite.go:60-83) emits only `?` placeholders into an `args []any` slice; LIKE wildcards are built into the bound arg (`"%" + f.Query + "%"`), never the SQL string. `TestInjectionLiteral` asserts `' OR 1=1 --` is treated as a literal.
- **Pagination correctness**: offset math `(page-1)*pageSize`, clamping for `page<1` and `page>lastPage`, and `lastPage` derived from `CountIncidents(filter)` on the *filtered* set (server.go:62-90). `TestFilterCombinedAndPaging` proves filter+paging compose.
- **Stat-card semantics**: `CountIncidentsByPhase` calls `buildWhere(filter, false)` to drop `filter.Phase` while honoring namespace/reason/query, keeping cards usable as phase toggles (`TestFilteredCounts`).
- **SSE survives tab switches**: detail tabs use `x-show` (display toggle), never `x-if`/`<template>`, so the `#live-status` `sse-connect` block stays mounted on all tabs (incidents.templ:135-166).
- **Embed safety**: `//go:embed all:css all:js all:icons` scopes the FS to three asset dirs (embed.go is not itself embedded); `http.FileServer(http.FS(...))` with `StripPrefix` handles path traversal. No source exposure.
- **CON-003**: no `encoding/json` in app code; `templ generate` is reproducible against the committed output.

## Pre-Merge Checklist

**Always:**
- [x] All Critical and High findings resolved (none found)
- [x] No secrets or credentials in committed files
- [x] `.gitignore` covers new artifact/config types (assets are embedded, intentional)
- [x] Tests cover changed behaviour and unhappy paths (injection, empty pages, sanitizer)
- [x] All async calls awaited or errors handled (list handler checks every store error)
- [x] Resources closed in all code paths (`defer rows.Close()` in new query funcs)

## Machine-Readable Verdict

```yaml
verdict: Approve
critical: 0
high: 0
medium: 0
low: 1
info: 1
blocking_ids: []
```
