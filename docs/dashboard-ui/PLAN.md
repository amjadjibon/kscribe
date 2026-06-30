---
goal: Dashboard UI refresh — themes, tabs, markdown RCA, better styling
version: 1.5
date_created: 2026-06-30
last_updated: 2026-06-30
owner: amjadjibon
status: 'Planned'
tags: [feature, frontend]
---

# Dashboard UI Refresh

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

Rework the kscribe web dashboard (`internal/web`, templ + HTMX) into a polished, dashboard-style UI: a design system with proper fonts and light/dark/system theming, summary stat cards on the incident list, a tabbed incident-detail view, and Markdown rendering of LLM-produced RCA fields (Summary, Root Cause, Remediation). The stack stays server-rendered templ + HTMX, with Alpine.js for client interactivity (theme, tabs, filter controls); front-end assets (JS/CSS/icons) are self-hosted from a repo-root `public/` directory embedded in the binary via `go:embed` and served at `/static/*` — no CDN for app assets, no JS build step — and untrusted LLM Markdown is sanitized server-side.

## 1. Requirements & Constraints

- **REQ-001**: The UI must support light, dark, and system themes, with a user toggle that persists across reloads (localStorage) and defaults to the OS preference.
- **REQ-002**: The incident list must read as a dashboard: a summary row of phase counts (Done / Diagnosing / Failed / Partial / Pending) plus a styled incident table.
- **REQ-003**: The incident detail page must use tabs (e.g. Overview, RCA, Event, Raw) so the page is scannable instead of one long column.
- **REQ-004**: RCA text fields (`Summary`, `RootCause`, `Remediation`) must render as Markdown (headings, lists, code, bold) rather than raw text.
- **REQ-007**: The incident list must be paginated (the store currently caps at 100 and there is no way to page further); the user can move between pages, and the summary stat counts must reflect all incidents, not just the visible page.
- **REQ-008**: The incident list must support filtering by phase, namespace, and reason, plus a free-text search across name/message/reason; filters compose with pagination (page links preserve the active filters) and with the stat counts.
- **REQ-005**: The UI must use a deliberate type system — a clean UI sans-serif (e.g. Inter) and a monospace (e.g. JetBrains Mono) for code/identifiers — loaded via CDN.
- **REQ-006**: The live SSE phase updates on the detail page must keep working after the markup/CSS changes.
- **SEC-001**: RCA Markdown originates from the LLM and is untrusted; it must be sanitized (HTML-escaped/allowlisted) before being injected into the page. No raw `@templ.Raw` of unsanitized model output.
- **SEC-002**: All filter/search inputs reach SQL; queries must be fully parameterized (placeholders + args), never string-concatenated values, and rendered filter values must be HTML-escaped in the form controls.
- **CON-001**: Front-end assets (JS, CSS, icons) are self-hosted from a repo-root `public/` directory served via `go:embed` — not from CDNs (fonts may remain CDN). Stay on templ + HTMX + Alpine, server-rendered, no npm/bundler/JS build step.
- **CON-005**: All client-side interactivity (theme toggle, tabs, any show/hide or stateful UI behaviour) is implemented with Alpine.js, vendored under `public/js/` and loaded as `<script defer src="/static/js/alpine.min.js">`. No other JS framework, no inline ad-hoc vanilla handlers beyond the pre-paint theme snippet. Alpine and HTMX must coexist (Alpine for local UI state, HTMX/SSE for server interaction).
- **REQ-009**: Static assets must be embedded in the operator binary (`go:embed`) and served from `/static/*`, so the container needs no extra files and works offline/air-gapped.
- **CON-002**: CON-003 (repo-wide): no `encoding/json` in application code; use `github.com/bytedance/sonic` if JSON is needed (unlikely here).
- **CON-003**: `make templ` output is committed and must be reproducible (rerun → no git diff).
- **CON-004**: Existing `internal/web` tests must stay green; update assertions only where markup legitimately changes, and add tests for new behaviour.

## 2. Implementation Steps

> After completing all tasks in a phase, `git add -u` and commit. No `Co-authored-by:`. Tick `[x]` as each task completes. Run `make templ` before committing so generated `*_templ.go` files are current.

### Phase 1: Embedded Static Assets, Design System, Theming & Layout Shell

**Goal**: Establish the asset-serving foundation and visual system everything else builds on — self-host JS/CSS/icons in a repo-root `public/` directory served via `go:embed`, plus fonts, a CSS design-token system, light/dark/system theming (Alpine), and a dashboard layout shell.

- [ ] TASK-001: Create the repo-root `public/` tree: `public/css/app.css` (the design system — see TASK-004), `public/js/` with vendored `alpine.min.js`, `htmx.min.js`, and `htmx-sse.min.js` (download the pinned minified releases), and `public/icons/` with a `favicon.svg` (and any UI icons). Self-host these instead of CDN.
- [ ] TASK-002: Add `public/embed.go` (`package public`) with `//go:embed all:css all:js all:icons` exposing `var FS embed.FS`. Placing the embed file inside `public/` (its own package) embeds its sibling `css/`, `js/`, `icons/` directories directly and avoids a root-package name collision with the build-tagged `tools.go` (`package tools`). The FS root therefore contains `css/`, `js/`, `icons/` with no `public/` prefix.
- [ ] TASK-003: In `internal/web/server.go`, mount the embedded assets: serve `public.FS` at `/static/*` via `http.StripPrefix("/static/", http.FileServer(http.FS(public.FS)))` with a long `Cache-Control` header (no `fs.Sub` needed — the embed root is already the `public/` contents). Add the `github.com/amjadjibon/kscribe/public` import.
- [ ] TASK-004: Author `public/css/app.css` as the design system: CSS custom properties (colors, spacing, radius, shadow, fonts), a `[data-theme="dark"]` override set, a `@media (prefers-color-scheme: dark)` fallback, the dashboard shell styles, and token-based phase badge styles. Keep the existing badge class names (`badge`, `badge-pending`, `badge-diagnosing`, `badge-done`, `badge-partial`, `badge-failed`) so `PhaseBadge` and the SSE fragment keep working. Drop the Pico CDN and the inline `<style>`/`.badge-*` block from `layout.templ`.
- [ ] TASK-005: Rewrite `layout.templ` to reference local assets — `<link rel="stylesheet" href="/static/css/app.css">`, `<script defer src="/static/js/alpine.min.js">`, `htmx.min.js`, `htmx-sse.min.js`, and `<link rel="icon" href="/static/icons/favicon.svg">` — plus the font links (Inter + JetBrains Mono; CDN `@font-face` is acceptable, or self-host under `public/fonts/`). Restructure `Layout` into a dashboard shell: a top bar with the `kscribe` brand and an Alpine theme toggle (Light/Dark/System) and a styled `<main>`. Keep the signature `templ Layout(title string, content templ.Component)`.
- [ ] TASK-006: Implement theming with Alpine (CON-005): an `Alpine.store('theme')` persisting to `localStorage`, resolving `system` via `matchMedia`, setting `data-theme` on `<html>`. Keep ONE tiny inline pre-Alpine `<script>` in `<head>` that applies the stored `data-theme` before first paint (FOUC); Alpine owns the toggle after load.
- [ ] TASK-007: Run `make templ`; add an `internal/web` test asserting `GET /static/css/app.css` returns 200 with a CSS content-type; ensure `go build ./...` and `go test ./internal/web` pass (the binary now embeds `public/` — no Dockerfile change needed since assets compile into the binary).

**Completion criteria**: `make templ` reproducible; `go build ./...` and `go test ./internal/web` pass; `GET /static/css/app.css` and `GET /static/js/alpine.min.js` return 200 from the embedded FS; `/` renders the themed shell from `/static/css/app.css` and the toggle switches Light/Dark/System and persists across reload.

**git commit**: `git add -u && git add public && git commit -m "feat: serve embedded static assets and dashboard shell"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of dashboard-ui.

Context: kscribe is a Go Kubernetes operator with a templ + HTMX web dashboard in internal/web (module path github.com/amjadjibon/kscribe). This phase self-hosts the front-end assets (JS/CSS/icons) from a repo-root public/ directory served via go:embed, and builds the visual foundation: design-token CSS, light/dark/system theming with Alpine.js, and a dashboard layout shell. Server-rendered templ + HTMX + Alpine; NO bundler/build step. Assets are vendored locally, NOT loaded from CDNs (fonts may stay CDN).

Branch: dashboard-ui-phase-1  |  Base: main

Tasks:
- TASK-001: Create repo-root public/ : public/css/app.css; public/js/ with vendored minified alpine.min.js (Alpine v3), htmx.min.js (v2), htmx-sse.min.js — fetch the pinned upstream minified files and commit them; public/icons/favicon.svg.
- TASK-002: Create public/embed.go with `package public`, `import "embed"`, and `//go:embed all:css all:js all:icons` exposing `var FS embed.FS`. Put it INSIDE public/ (its own package) — this embeds the sibling css/js/icons dirs and avoids a root-package name clash with the existing build-tagged tools.go (package tools). The embedded FS has css/, js/, icons/ at its root (no public/ prefix). The css/js/icons subdirs must already contain files (from TASK-001) or the embed won't compile.
- TASK-003: In internal/web/server.go mount the assets: `r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(public.FS))))` with a Cache-Control header (e.g. max-age=3600). No fs.Sub is needed since the embed root is already the public/ contents. Import github.com/amjadjibon/kscribe/public.
- TASK-004: Author public/css/app.css as the design system: CSS custom properties (colors/spacing/radius/shadow/fonts), [data-theme="dark"] overrides, @media (prefers-color-scheme: dark) fallback, dashboard shell styles, and phase badge styles reusing class names badge/badge-pending/badge-diagnosing/badge-done/badge-partial/badge-failed. Remove the Pico CDN link and the inline <style> block from layout.templ.
- TASK-005: Rewrite internal/web/templates/layout.templ to reference local assets: <link rel=stylesheet href=/static/css/app.css>, <script defer src=/static/js/alpine.min.js>, htmx.min.js, htmx-sse.min.js, <link rel=icon href=/static/icons/favicon.svg>, plus Inter + JetBrains Mono font links (CDN @font-face acceptable). Restructure Layout into a dashboard shell (top bar with brand + Alpine theme toggle, styled <main>). Keep signature `templ Layout(title string, content templ.Component)` and render content with @content.
- TASK-006: Theming via Alpine (Alpine.store('theme') + localStorage + matchMedia for system, sets data-theme on <html>); keep ONE inline pre-paint <script> in <head> to apply stored data-theme before first paint (avoid FOUC). Alpine and HTMX must coexist.
- TASK-007: Run `make templ`. Add an internal/web test: GET /static/css/app.css returns 200 and a CSS content-type. Keep go build and existing internal/web tests green (they check 200/Content-Type text/html and phase strings in the body).

Key files:
- public/css/app.css, public/js/*.min.js, public/icons/favicon.svg — new vendored assets.
- public/embed.go (package public) — //go:embed all:css all:js all:icons exposing var FS embed.FS.
- internal/web/server.go — /static/* file server from public.FS; import github.com/amjadjibon/kscribe/public.
- internal/web/templates/layout.templ (+ layout_templ.go via make templ) — local asset refs, Alpine theme toggle, dashboard shell.
- internal/web/server_test.go — add the /static asset 200 test; keep existing assertions green.

Completion criteria: `make templ` reproducible; `go build ./...` and `go test ./internal/web` pass; GET /static/css/app.css and /static/js/alpine.min.js return 200 from the embedded FS; / renders the themed shell from the embedded CSS and the toggle switches Light/Dark/System and persists.

When done: git add -u && git add public && git commit -m "feat: serve embedded static assets and dashboard shell" — no Co-authored-by
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 2: Incident List Dashboard

**Goal**: Turn the incident list (`/`) into a dashboard view with a summary stat row (phase counts) and a styled, scannable incident table/cards, using the Phase 1 design tokens.

**Depends on**: Phase 1 complete

- [ ] TASK-008: In `internal/web/templates/incidents.templ`, add a `StatCards` (or inline) component that shows counts per phase (Done / Diagnosing / Failed / Partial / Pending) computed from the `[]store.Incident` slice; compute counts in a small helper func in the templ or a `internal/web` helper.
- [ ] TASK-009: Restyle `IncidentList` as a dashboard: a header row, the stat cards, and a styled table (sticky header, row hover, monospace for namespace/name, phase badge, relative or formatted timestamps). Provide a clear empty state when there are no incidents.
- [ ] TASK-010: Ensure the list page passes `incidents` to the stat computation without changing the `Server.list` handler contract (it calls `templates.Layout(..., templates.IncidentList(incidents))`); if a new wrapper component is cleaner, update `server.go` accordingly and keep the route behaviour identical.
- [ ] TASK-011: Run `make templ`; keep `go test ./internal/web` and `go build ./...` green; add/adjust a test asserting the stat counts render for a seeded multi-phase incident set.

**Completion criteria**: `go test ./internal/web` passes (including a stat-count assertion); `make templ` reproducible; `/` shows phase-count cards above a styled incident table, with a proper empty state when none exist.

**git commit**: `git add -u && git commit -m "feat: dashboard list view with phase stat cards"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 2 of dashboard-ui.

Context: kscribe's web dashboard (internal/web, templ + HTMX) now has a themed layout shell and design tokens from Phase 1. This phase turns the incident list page (/) into a dashboard with phase-count stat cards and a styled incident table.

Branch: dashboard-ui-phase-2  |  Base: dashboard-ui-phase-1

Tasks:
- TASK-008: In internal/web/templates/incidents.templ add a stat-cards component showing counts per phase (Done/Diagnosing/Failed/Partial/Pending) derived from the []store.Incident passed to IncidentList. Compute counts in a small Go helper (in the templ file or a new internal/web helper) — Incident has a .Phase string field.
- TASK-009: Restyle IncidentList as a dashboard: page header, the stat cards, and a styled table (header, row hover, monospace namespace/name via the JetBrains Mono font from Phase 1, phase badge, formatted timestamp). Add a clear empty state.
- TASK-010: Keep Server.list behaviour identical (it renders templates.Layout("kscribe — Incidents", templates.IncidentList(incidents))). If you introduce a wrapper component, update internal/web/server.go to match and keep the route + Content-Type unchanged.
- TASK-011: Run `make templ`. Keep internal/web tests and `go build ./...` green; add a test that seeds incidents in multiple phases and asserts the rendered list page contains the expected phase counts.

Key files:
- internal/web/templates/incidents.templ — stat cards + restyled IncidentList (+ generated _templ.go via make templ).
- internal/web/server.go — only if you change the list component signature.
- internal/web/server_test.go — add the stat-count assertion; keep existing 200/Content-Type/phase-in-body checks passing.
- internal/store — read Incident fields (Namespace, Name, Phase, Reason, InvolvedObjectKind/Name, UpdatedAt) to know what is available; do not modify the store.

Completion criteria: `go test ./internal/web` passes incl. a stat-count assertion; `make templ` reproducible; `/` shows phase-count cards above a styled table with a proper empty state.

When done: git add -u && git commit -m "feat: dashboard list view with phase stat cards" — no Co-authored-by
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 3: Tabbed Detail View with Markdown RCA

**Goal**: Make the incident detail page a tabbed, dashboard-quality view, and render the LLM RCA fields as sanitized Markdown.

**Depends on**: Phase 2 complete

- [ ] TASK-012: Add `github.com/yuin/goldmark` (Markdown → HTML) and `github.com/microcosm-cc/bluemonday` (HTML sanitizer) to `go.mod`; create `internal/web/markdown.go` exposing `RenderMarkdown(string) templ.Component` (or returning sanitized `template.HTML`) that converts Markdown and sanitizes the result with a strict allowlist policy.
- [ ] TASK-013: In `internal/web/templates/incidents.templ`, render `Diagnosis.Summary`, `Diagnosis.RootCause`, and `Diagnosis.Remediation` through the Markdown renderer (sanitized) instead of plain text.
- [ ] TASK-014: Add a tabbed layout to `IncidentDetail` using Alpine (CON-005) — `x-data="{ tab: 'overview' }"`, tab buttons with `@click`/`:class`, and panels with `x-show`. Tabs such as Overview (event + LLM meta + live status), RCA (the Markdown diagnosis blocks), and Raw (key/value dump). Keep the SSE `#live-status` block and its `sse-connect`/`sse-swap` attributes in the DOM at all times (use `x-show`, which only toggles display, NOT `x-if`/`<template>`, which would remove it and break the live stream); the Overview/live tab is the default-selected one.
- [ ] TASK-015: Style the diagnosis cards, confidence, and tabs with the Phase 1 tokens; ensure code blocks/inline code in rendered Markdown use the monospace font and a readable background in both themes.
- [ ] TASK-016: Run `make templ`; add tests in `internal/web` for: (a) Markdown rendering (e.g. `**bold**` → `<strong>`), (b) sanitization (a `<script>`/`onerror` payload in RCA is stripped), and (c) the detail page still contains the SSE `sse-connect` attribute and the phase string.

**Completion criteria**: `go test ./internal/web` passes including a sanitization test proving `<script>` in RCA is removed; `make templ` reproducible; `go build ./...` and `go vet ./...` pass; the detail page shows working tabs, Markdown-rendered RCA, and live SSE status still updates.

**git commit**: `git add -u && git commit -m "feat: tabbed detail view with sanitized markdown RCA"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 3 of dashboard-ui.

Context: kscribe's web dashboard (internal/web, templ + HTMX) has a themed shell (Phase 1) and a dashboard list (Phase 2). This phase makes the incident DETAIL page tabbed and renders the LLM-produced RCA fields as sanitized Markdown. LLM output is untrusted — it MUST be sanitized before rendering.

Branch: dashboard-ui-phase-3  |  Base: dashboard-ui-phase-2

Tasks:
- TASK-012: Add deps github.com/yuin/goldmark and github.com/microcosm-cc/bluemonday (run `go get`). Create internal/web/markdown.go with a function that converts a Markdown string to HTML via goldmark, then sanitizes it with a bluemonday policy (start from UGCPolicy, allow code/pre/headings/lists/links with safe attributes), returning a value templ can render as raw HTML (e.g. templ.Component via templ.Raw of the sanitized string, or template.HTML). Do NOT use encoding/json (CON-003) — not needed here.
- TASK-013: In internal/web/templates/incidents.templ render Diagnosis.Summary, Diagnosis.RootCause, Diagnosis.Remediation through the Markdown renderer (sanitized) instead of plain { text }.
- TASK-014: Convert IncidentDetail into a tabbed view (e.g. Overview / RCA / Raw) using Alpine.js (CON-005): x-data="{ tab: 'overview' }", tab buttons using @click="tab='...'" and :class for the active state, and panels using x-show="tab==='...'". The existing SSE live-status block (div with hx-ext="sse", sse-connect="/incidents/{ns}/{name}/stream", sse-swap="message", id="live-status") MUST remain present and intact in the DOM at all times — use x-show (display toggle), NOT x-if/<template> (which removes it from the DOM and would kill the SSE connection). The Overview tab containing live status is selected by default.
- TASK-015: Style diagnosis cards, confidence, and tabs with the Phase 1 design tokens; rendered Markdown code/pre must use the monospace font and a readable background in both light and dark themes.
- TASK-016: Run `make templ`. Add internal/web tests: (a) RenderMarkdown turns **bold** into <strong>; (b) a script/onerror payload embedded in an RCA field is stripped from the rendered detail page (sanitization); (c) the detail page still contains the sse-connect attribute and the phase string.

Key files:
- internal/web/markdown.go — new: goldmark render + bluemonday sanitize.
- internal/web/templates/incidents.templ — markdown RCA + tabbed IncidentDetail (+ generated _templ.go via make templ).
- internal/web/server_test.go (or a new _test.go) — markdown + sanitization + SSE-attribute assertions.
- go.mod / go.sum — new deps.
- internal/store — Diagnosis has Summary, RootCause, Remediation (strings) and Confidence (float); IncidentDetail carries event + LLM meta fields. Do not modify the store.

Completion criteria: `go test ./internal/web` passes incl. a sanitization test proving <script> in RCA is removed; `make templ` reproducible; `go build ./...` and `go vet ./...` pass; the detail page shows working tabs, Markdown RCA, and live SSE status still updates.

When done: git add -u && git commit -m "feat: tabbed detail view with sanitized markdown RCA" — no Co-authored-by
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 4: Paginated Incident List

**Goal**: Page through incidents instead of capping at 100, and make the Phase 2 stat cards reflect totals across all incidents rather than only the visible page.

**Depends on**: Phase 3 complete

- [ ] TASK-017: In `internal/store/sqlite.go`, add offset support and totals: a `ListIncidentsPage(ctx, limit, offset int) ([]Incident, error)` (same `ORDER BY updated_at DESC` with `LIMIT ? OFFSET ?`, parameterized) and a `CountIncidentsByPhase(ctx) (map[string]int, error)` (one `GROUP BY phase` query) plus a total count. Keep the existing `ListIncidents` for compatibility or reimplement it via the new method.
- [ ] TASK-018: Extend the `internal/web` `StoreReader` interface and `Server.list` to read `?page=` (1-based) and a fixed page size (e.g. 25), compute `offset = (page-1)*size`, fetch that page and the phase-count totals, and clamp out-of-range pages.
- [ ] TASK-019: In `internal/web/templates/incidents.templ`, drive the stat cards from the DB phase-count totals (not the page slice) and add pagination controls below the table — Prev/Next links (`/?page=N`) and a "Page X of Y" indicator; disable Prev on page 1 and Next on the last page.
- [ ] TASK-020: Run `make templ`; add `internal/store` tests for paging (page 2 returns the next slice; offset past the end returns empty) and phase-count totals, and an `internal/web` test asserting the pager renders correct page numbers and the stat counts use totals.

**Completion criteria**: `go test ./internal/store ./internal/web` passes (paging + totals + pager assertions); `make templ` reproducible; `go build ./...` passes; with >25 incidents, `/?page=2` shows the next page and the stat cards show full totals.

**git commit**: `git add -u && git commit -m "feat: paginate incident list and total stat counts"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 4 of dashboard-ui.

Context: kscribe's web dashboard (internal/web, templ + HTMX) has a themed shell, a dashboard list with phase stat cards, and a tabbed Markdown detail view. This phase adds pagination to the incident list and makes the stat cards reflect totals across ALL incidents, not just the visible page. CR status is the source of truth; SQLite (internal/store) is the read model for the dashboard.

Branch: dashboard-ui-phase-4  |  Base: dashboard-ui-phase-3

Tasks:
- TASK-017: In internal/store/sqlite.go add `ListIncidentsPage(ctx context.Context, limit, offset int) ([]Incident, error)` — same query as ListIncidents (ORDER BY updated_at DESC) but `LIMIT ? OFFSET ?`, fully parameterized. Add `CountIncidentsByPhase(ctx context.Context) (map[string]int, error)` using a single `SELECT phase, COUNT(*) ... GROUP BY phase`. Keep ListIncidents working (or reimplement it as ListIncidentsPage(ctx, limit, 0)). No encoding/json (CON-003); use database/sql with parameters only (no string interpolation of values).
- TASK-018: In internal/web/server.go extend the StoreReader interface with the new methods and update Server.list: parse ?page= (1-based, default 1), use a fixed page size (25), compute offset, fetch the page + the phase-count totals, and clamp page to [1, lastPage]. Keep the route path, 200, and Content-Type text/html unchanged.
- TASK-019: In internal/web/templates/incidents.templ, compute the stat cards from the DB phase-count totals passed from the handler (NOT from the page slice), and add pagination controls under the table: Prev/Next anchors to /?page=N with a "Page X of Y" label; Prev disabled on page 1, Next disabled on the last page. Use the Phase 1 design tokens for styling.
- TASK-020: Run `make templ`. Add internal/store tests: seed >page-size incidents and assert page 2 returns the expected next slice and an offset past the end returns empty; assert CountIncidentsByPhase returns correct per-phase totals. Add an internal/web test asserting the rendered list shows the right page indicator and that stat counts come from totals (seed more incidents than one page, assert a count larger than the page size appears).

Key files:
- internal/store/sqlite.go — ListIncidentsPage + CountIncidentsByPhase (parameterized SQL).
- internal/store/sqlite_test.go — paging + totals tests.
- internal/web/server.go — StoreReader interface + list handler pagination (page parsing, offset, clamp).
- internal/web/templates/incidents.templ — totals-driven stat cards + pagination controls (+ generated _templ.go via make templ).
- internal/web/server_test.go — pager + totals assertions.

Completion criteria: `go test ./internal/store ./internal/web` passes (paging + totals + pager); `make templ` reproducible; `go build ./...` passes; with >25 incidents, /?page=2 shows the next page and the stat cards show full totals.

When done: git add -u && git commit -m "feat: paginate incident list and total stat counts" — no Co-authored-by
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 5: Search & Filtering

**Goal**: Let users narrow the incident list by phase, namespace, and reason, and free-text search across name/message/reason — composing cleanly with the Phase 4 pagination and stat counts.

**Depends on**: Phase 4 complete

- [ ] TASK-021: In `internal/store/sqlite.go` define an `IncidentFilter` struct (`Phase`, `Namespace`, `Reason`, `Query string`) and evolve the Phase 4 reads to honour it: `ListIncidentsPage(ctx, filter, limit, offset)`, `CountIncidents(ctx, filter) (int, error)` (total matching, for the pager), and `CountIncidentsByPhase(ctx, filter)` — build the `WHERE` clause dynamically from the non-empty filter fields using parameter placeholders and an args slice (SEC-002), with `Query` matched via `LIKE %?%` across `name`, `message`, and `reason`.
- [ ] TASK-022: For the stat cards, apply every filter field EXCEPT `Phase` when computing `CountIncidentsByPhase`, so the per-phase cards stay populated and act as phase toggles even while a phase is selected.
- [ ] TASK-023: In `internal/web/server.go`, parse `?phase=`, `?namespace=`, `?reason=`, `?q=` (plus the Phase 4 `?page=`), build the `store.IncidentFilter`, fetch the filtered page + filtered total + per-phase counts, and pass the active filter values to the template. Reset to page 1 when filters change.
- [ ] TASK-024: In `internal/web/templates/incidents.templ`, add a filter bar — a GET form with a search input (`q`), a phase `<select>`, and namespace/reason inputs (prefilled with current values, HTML-escaped) and a Clear link; make the stat cards clickable phase filters (`/?phase=<P>&...keep other filters`); and ensure the pagination links carry the current filter query string. Any client interactivity in the bar (auto-submit on phase change, a Clear button, toggling an advanced-filters panel) uses Alpine (CON-005); the actual filtering stays a server-side GET so the URL remains shareable.
- [ ] TASK-025: Run `make templ`; add `internal/store` tests (filter by phase, by namespace, by free-text `q`; combined filter + paging; filtered counts) and an `internal/web` test (applying `?phase=Failed` returns only Failed rows; pagination + stat-card links preserve filters).

**Completion criteria**: `go test ./internal/store ./internal/web` passes (filter, free-text, filtered counts, filter-preserving pager); `make templ` reproducible; `go build ./...` and `go vet ./...` pass; `/?phase=Failed&q=image` narrows the list and the pager/stat links keep the filter.

**git commit**: `git add -u && git commit -m "feat: search and filter incidents"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 5 of dashboard-ui.

Context: kscribe's web dashboard (internal/web, templ + HTMX) has a themed shell, a paginated incident list with DB-side phase stat counts (Phase 4), and a tabbed Markdown detail view. This phase adds filtering (phase/namespace/reason) and free-text search, composing with the existing pagination and counts. The read model is SQLite (internal/store); all inputs reach SQL so queries MUST be parameterized.

Branch: dashboard-ui-phase-5  |  Base: dashboard-ui-phase-4

Tasks:
- TASK-021: In internal/store/sqlite.go define `type IncidentFilter struct { Phase, Namespace, Reason, Query string }` and update the Phase 4 reads to take it: `ListIncidentsPage(ctx, filter IncidentFilter, limit, offset int)`, `CountIncidents(ctx, filter IncidentFilter) (int, error)`, `CountIncidentsByPhase(ctx, filter IncidentFilter) (map[string]int, error)`. Build the WHERE clause from non-empty fields using ? placeholders and an []any args slice — never interpolate values (SEC-002). Match Query with LIKE against name, message, and reason (e.g. `(name LIKE ? OR message LIKE ? OR reason LIKE ?)` with `%q%`). Keep filterable columns: phase, namespace, reason, message, name (all exist in the incidents table; phase and namespace/name are indexed).
- TASK-022: When computing CountIncidentsByPhase, apply all filter fields EXCEPT Phase, so the per-phase stat cards stay populated and usable as phase toggles while a phase is selected.
- TASK-023: In internal/web/server.go parse query params phase, namespace, reason, q (plus page from Phase 4), build store.IncidentFilter, fetch the filtered page + CountIncidents (for last-page math) + CountIncidentsByPhase, and pass current filter values to the template. Keep route/200/Content-Type unchanged.
- TASK-024: In internal/web/templates/incidents.templ add a filter bar: a GET <form> with a text search (name="q"), a phase <select> (options: all + each phase), and namespace/reason text inputs, all prefilled with the current (HTML-escaped) values, plus a Clear link back to /. Make the stat cards anchor to /?phase=<P> while preserving the other active filters. Make the Phase 4 pagination Prev/Next links include the current filter query string. Style with Phase 1 tokens. Any client interactivity (auto-submit the form when the phase select changes, a Clear button, an advanced-filters toggle) MUST use Alpine.js (CON-005), not ad-hoc vanilla JS; the form still submits as a normal GET so filtering stays server-side and URLs stay shareable.
- TASK-025: Run `make templ`. Add internal/store tests: filter by phase, by namespace, by free-text q (matches message and name), combined filter+paging, and filtered counts. Add an internal/web test: GET /?phase=Failed returns only Failed incidents, and the rendered pager + stat-card links carry the active filters.

Key files:
- internal/store/sqlite.go — IncidentFilter + filtered list/count queries (parameterized).
- internal/store/sqlite_test.go — filter + free-text + filtered-count tests.
- internal/web/server.go — parse filter params, build IncidentFilter, fetch filtered data.
- internal/web/templates/incidents.templ — filter bar, clickable stat cards, filter-preserving pagination (+ generated _templ.go via make templ).
- internal/web/server_test.go — filter + filter-preserving-pager assertions.

Completion criteria: `go test ./internal/store ./internal/web` passes (filter, free-text, filtered counts, filter-preserving pager); `make templ` reproducible; `go build ./...` and `go vet ./...` pass; /?phase=Failed&q=image narrows the list and the pager/stat links keep the filter.

When done: git add -u && git commit -m "feat: search and filter incidents" — no Co-authored-by
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

## 3. Testing

- [ ] TEST-001: `go test ./internal/web` — routes return 200/404 as before, Content-Type `text/html`, phase strings present (existing assertions stay green).
- [ ] TEST-012: `go test ./internal/web` — `GET /static/css/app.css` and `GET /static/js/alpine.min.js` return 200 from the embedded FS with sensible content-types (Phase 1, REQ-009).
- [ ] TEST-002: `go test ./internal/web` — stat counts render correctly for a seeded multi-phase incident set (Phase 2).
- [ ] TEST-003: `go test ./internal/web` — `RenderMarkdown("**x**")` contains `<strong>`; a `<script>`/`onerror` RCA payload is stripped from the detail page (Phase 3, SEC-001).
- [ ] TEST-004: `go test ./internal/web` — detail page still contains the SSE `sse-connect` attribute (REQ-006).
- [ ] TEST-008: `go test ./internal/store` — paging returns the correct slice for page 2 and empty past the end; `CountIncidentsByPhase` returns correct totals (Phase 4).
- [ ] TEST-009: `go test ./internal/web` — with more incidents than one page, the list renders pagination controls with the right "Page X of Y" and stat counts reflect totals, not the page (Phase 4, REQ-007).
- [ ] TEST-010: `go test ./internal/store` — filtering by phase/namespace and free-text `q` (matching name/message/reason) returns the expected rows; filtered counts are correct (Phase 5, SEC-002).
- [ ] TEST-011: `go test ./internal/web` — `GET /?phase=Failed` returns only Failed incidents, and the rendered pagination + stat-card links preserve the active filter query string (Phase 5, REQ-008).
- [ ] TEST-005: `make templ && git diff --exit-code` — generated templ output is reproducible.
- [ ] TEST-006: `make build` — operator binary still builds with the new web assets.
- [ ] TEST-007: Manual — run `scripts/local-test.sh` (or port-forward an existing install), open `/`, confirm: theme toggle (light/dark/system) persists; list shows stat cards; a `Done` incident's detail page shows tabs and Markdown-rendered RCA; SSE phase updates live.

## 4. Risks & Assumptions

- **RISK-001**: Unsanitized LLM Markdown could inject scripts (XSS) — mitigation: mandatory bluemonday sanitization at the single `RenderMarkdown` chokepoint, with a sanitization test (SEC-001).
- **RISK-002**: Alpine `x-if`/`<template>` for tab panels would remove the SSE block from the DOM and silently break live updates — mitigation: tabs use `x-show` (display toggle only); the live/Overview tab is default-selected; a test asserts the `sse-connect` attribute is present in the rendered detail page (REQ-006).
- **RISK-003**: Theme flash-on-load (FOUC) — Alpine initializes after first paint, so relying on it alone would flash the wrong theme — mitigation: one tiny inline head `<script>` applies `data-theme` from `localStorage` before paint; Alpine takes over the toggle after load.
- **RISK-006**: Alpine and HTMX can clash if both try to own the same DOM subtree (HTMX swaps can drop Alpine state) — mitigation: scope Alpine `x-data` to containers HTMX does not swap; HTMX/SSE only swaps the small `#live-status` fragment, which carries no Alpine state.
- **RISK-004**: Markup changes break existing `internal/web` assertions — mitigation: existing tests assert phase strings, status codes, Content-Type, and SSE framing, all preserved; update only where markup legitimately moves.
- **ASSUMPTION-001**: RCA fields (`Summary`, `RootCause`, `Remediation`) frequently contain Markdown-ish text from the LLM, so Markdown rendering is worthwhile (observed: models return fenced code blocks and lists).
- **ASSUMPTION-002**: Server-side Markdown (goldmark + bluemonday) is preferred over client-side JS rendering — it is testable, works without client JS, and keeps untrusted HTML sanitization on the server. Two small pure-Go deps are acceptable here. (Alpine handles UI interactivity; content rendering/sanitization stays server-side.)
- **ASSUMPTION-007**: Alpine.js v3, vendored under `public/js/` and loaded with `<script defer>`, is the client interactivity layer for all stateful UI (theme, tabs, filter-bar niceties) per CON-005; it adds no build step and coexists with HTMX. Actual data operations (filtering, pagination) remain server-side GET requests so URLs stay shareable and testable.
- **ASSUMPTION-003**: App assets (JS/CSS/icons) are self-hosted and embedded via `go:embed` for an offline/air-gapped-friendly, dependency-free binary; only web fonts may remain on a CDN (vendor them under `public/fonts/` later if full offline is required).
- **ASSUMPTION-008**: The `public/` directory lives at the module root, and the embed file is `public/embed.go` (`package public`) embedding its sibling `css/ js/ icons/` dirs — deliberately NOT a root-level file, to avoid a package-name collision with the build-tagged `tools.go` (`package tools`) at the module root. `internal/web` imports `github.com/amjadjibon/kscribe/public` for the FS; no `fs.Sub` is needed since the embed root is the `public/` contents. Vendored JS (Alpine/HTMX/htmx-sse) are committed minified files, pinned to specific upstream versions.
- **ASSUMPTION-004**: Phase branches use the hyphenated form `dashboard-ui-phase-N` (not `dashboard-ui/phase-N`) to avoid a git ref D/F conflict with the `dashboard-ui` plan branch.
- **ASSUMPTION-005**: Offset-based pagination with a fixed 25-row page size is sufficient for the dashboard; cursor/keyset pagination is deferred (incident counts are modest and `updated_at DESC` ordering tolerates offset paging for an MVP). Phase 2's stat cards are reworked in Phase 4 to use DB-side phase-count totals so they stay correct once only one page is loaded.
- **RISK-005**: Filter inputs are a SQL-injection surface — mitigation: dynamic `WHERE` built only from placeholders + an args slice, with a store test exercising a quote/`OR 1=1`-style value to prove it is treated as a literal (SEC-002).
- **ASSUMPTION-006**: Filtering is server-side via SQL with a plain GET form (shareable/bookmarkable URLs), not client-side JS; the per-phase stat cards double as one-click phase filters and therefore ignore the active `Phase` filter when counting.
