---
goal: Dashboard UI refresh — themes, tabs, markdown RCA, better styling
version: 1.0
date_created: 2026-06-30
last_updated: 2026-06-30
owner: amjadjibon
status: 'Planned'
tags: [feature, frontend]
---

# Dashboard UI Refresh

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

Rework the kscribe web dashboard (`internal/web`, templ + HTMX) into a polished, dashboard-style UI: a design system with proper fonts and light/dark/system theming, summary stat cards on the incident list, a tabbed incident-detail view, and Markdown rendering of LLM-produced RCA fields (Summary, Root Cause, Remediation). The stack stays server-rendered templ + HTMX with CDN assets — no JS build step — and untrusted LLM Markdown is sanitized server-side.

## 1. Requirements & Constraints

- **REQ-001**: The UI must support light, dark, and system themes, with a user toggle that persists across reloads (localStorage) and defaults to the OS preference.
- **REQ-002**: The incident list must read as a dashboard: a summary row of phase counts (Done / Diagnosing / Failed / Partial / Pending) plus a styled incident table.
- **REQ-003**: The incident detail page must use tabs (e.g. Overview, RCA, Event, Raw) so the page is scannable instead of one long column.
- **REQ-004**: RCA text fields (`Summary`, `RootCause`, `Remediation`) must render as Markdown (headings, lists, code, bold) rather than raw text.
- **REQ-005**: The UI must use a deliberate type system — a clean UI sans-serif (e.g. Inter) and a monospace (e.g. JetBrains Mono) for code/identifiers — loaded via CDN.
- **REQ-006**: The live SSE phase updates on the detail page must keep working after the markup/CSS changes.
- **SEC-001**: RCA Markdown originates from the LLM and is untrusted; it must be sanitized (HTML-escaped/allowlisted) before being injected into the page. No raw `@templ.Raw` of unsanitized model output.
- **CON-001**: Stay on templ + HTMX, server-rendered, CDN assets only — no npm/bundler/JS build step.
- **CON-002**: CON-003 (repo-wide): no `encoding/json` in application code; use `github.com/bytedance/sonic` if JSON is needed (unlikely here).
- **CON-003**: `make templ` output is committed and must be reproducible (rerun → no git diff).
- **CON-004**: Existing `internal/web` tests must stay green; update assertions only where markup legitimately changes, and add tests for new behaviour.

## 2. Implementation Steps

> After completing all tasks in a phase, `git add -u` and commit. No `Co-authored-by:`. Tick `[x]` as each task completes. Run `make templ` before committing so generated `*_templ.go` files are current.

### Phase 1: Design System, Theming & Layout Shell

**Goal**: Establish the visual foundation everything else builds on — fonts, CSS design tokens, light/dark/system theming with a persistent toggle, and a dashboard layout shell (header + content) in `Layout`.

- [ ] TASK-001: In `internal/web/templates/layout.templ`, add CDN `<link>`s for Inter (UI) and JetBrains Mono (code) — use a privacy-friendly CDN (e.g. `https://fonts.bunny.net`) or Google Fonts; keep Pico as the base reset or replace its role with the new tokens.
- [ ] TASK-002: Add a CSS design-token block (CSS custom properties) for colors, spacing, radius, shadows, and fonts, with a `[data-theme="dark"]` override set and a `@media (prefers-color-scheme: dark)` fallback so "system" works with no JS.
- [ ] TASK-003: Restructure `Layout` into a dashboard shell: a top bar with the `kscribe` brand and a theme toggle control (Light / Dark / System), and a `<main>` content area styled with the tokens.
- [ ] TASK-004: Add a small inline `<script>` that reads/writes the theme choice in `localStorage`, applies `data-theme` on `<html>`, and honours `system` via `matchMedia('(prefers-color-scheme: dark)')`. No external JS dependency.
- [ ] TASK-005: Replace the ad-hoc `.badge-*` inline styles with token-based phase badge styles that read correctly in both themes; keep the existing class names (`badge`, `badge-done`, …) so `PhaseBadge` and the SSE fragment keep working.
- [ ] TASK-006: Run `make templ`; ensure `go build ./...` and `go test ./internal/web` pass (update only assertions broken by intentional layout changes).

**Completion criteria**: `make templ` produces no diff on rerun; `go test ./internal/web` passes; `go build ./...` passes; viewing `/` shows the themed shell and the toggle switches Light/Dark/System and persists across reload.

**git commit**: `git add -u && git commit -m "feat: add dashboard theme, fonts and layout shell"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of dashboard-ui.

Context: kscribe is a Go Kubernetes operator with a templ + HTMX web dashboard in internal/web. This phase builds the visual foundation: fonts, a CSS design-token system, light/dark/system theming with a persistent toggle, and a dashboard layout shell. Server-rendered only, CDN assets, NO JS build step.

Branch: dashboard-ui-phase-1  |  Base: main

Tasks:
- TASK-001: In internal/web/templates/layout.templ add CDN font links — Inter (UI) and JetBrains Mono (code) — via fonts.bunny.net or Google Fonts. Keep Pico as base reset (CDN link already present) or supersede it with your tokens; do not add a JS build step.
- TASK-002: Add a CSS design-token block (CSS custom properties: colors, spacing, radius, shadow, font families) with a [data-theme="dark"] override and a @media (prefers-color-scheme: dark) fallback so "system" works without JS.
- TASK-003: Restructure the Layout templ into a dashboard shell: a top bar containing the "kscribe" brand link and a theme toggle (Light/Dark/System), plus a styled <main> content region. The Layout signature is `templ Layout(title string, content templ.Component)` — keep it compatible; the content component is rendered with @content.
- TASK-004: Add a small inline <script> that persists the theme choice in localStorage, applies data-theme on <html>, and resolves "system" via matchMedia('(prefers-color-scheme: dark)'). Apply the stored theme before first paint to avoid a flash. No external JS lib.
- TASK-005: Replace the inline .badge-* styles with token-based phase badge styles that work in both themes. Keep the existing class names (badge, badge-pending, badge-diagnosing, badge-done, badge-partial, badge-failed) so templates/incidents.templ PhaseBadge and the SSE fragment keep rendering correctly.
- TASK-006: Run `make templ` (regenerates *_templ.go). Keep go build and the internal/web tests green.

Key files:
- internal/web/templates/layout.templ — fonts, tokens, theme toggle + script, dashboard shell.
- internal/web/templates/layout_templ.go — generated by `make templ` (commit it).
- internal/web/server_test.go — update only assertions broken by intentional changes (it checks 200s, Content-Type text/html, and that phase strings appear in the body — keep those true).

Completion criteria: `make templ` produces no diff on rerun; `go test ./internal/web` passes; `go build ./...` passes; `/` renders the themed shell and the toggle switches Light/Dark/System and persists across reload.

When done: git add -u && git commit -m "feat: add dashboard theme, fonts and layout shell" — no Co-authored-by
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 2: Incident List Dashboard

**Goal**: Turn the incident list (`/`) into a dashboard view with a summary stat row (phase counts) and a styled, scannable incident table/cards, using the Phase 1 design tokens.

**Depends on**: Phase 1 complete

- [ ] TASK-007: In `internal/web/templates/incidents.templ`, add a `StatCards` (or inline) component that shows counts per phase (Done / Diagnosing / Failed / Partial / Pending) computed from the `[]store.Incident` slice; compute counts in a small helper func in the templ or a `internal/web` helper.
- [ ] TASK-008: Restyle `IncidentList` as a dashboard: a header row, the stat cards, and a styled table (sticky header, row hover, monospace for namespace/name, phase badge, relative or formatted timestamps). Provide a clear empty state when there are no incidents.
- [ ] TASK-009: Ensure the list page passes `incidents` to the stat computation without changing the `Server.list` handler contract (it calls `templates.Layout(..., templates.IncidentList(incidents))`); if a new wrapper component is cleaner, update `server.go` accordingly and keep the route behaviour identical.
- [ ] TASK-010: Run `make templ`; keep `go test ./internal/web` and `go build ./...` green; add/adjust a test asserting the stat counts render for a seeded multi-phase incident set.

**Completion criteria**: `go test ./internal/web` passes (including a stat-count assertion); `make templ` reproducible; `/` shows phase-count cards above a styled incident table, with a proper empty state when none exist.

**git commit**: `git add -u && git commit -m "feat: dashboard list view with phase stat cards"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 2 of dashboard-ui.

Context: kscribe's web dashboard (internal/web, templ + HTMX) now has a themed layout shell and design tokens from Phase 1. This phase turns the incident list page (/) into a dashboard with phase-count stat cards and a styled incident table.

Branch: dashboard-ui-phase-2  |  Base: dashboard-ui-phase-1

Tasks:
- TASK-007: In internal/web/templates/incidents.templ add a stat-cards component showing counts per phase (Done/Diagnosing/Failed/Partial/Pending) derived from the []store.Incident passed to IncidentList. Compute counts in a small Go helper (in the templ file or a new internal/web helper) — Incident has a .Phase string field.
- TASK-008: Restyle IncidentList as a dashboard: page header, the stat cards, and a styled table (header, row hover, monospace namespace/name via the JetBrains Mono font from Phase 1, phase badge, formatted timestamp). Add a clear empty state.
- TASK-009: Keep Server.list behaviour identical (it renders templates.Layout("kscribe — Incidents", templates.IncidentList(incidents))). If you introduce a wrapper component, update internal/web/server.go to match and keep the route + Content-Type unchanged.
- TASK-010: Run `make templ`. Keep internal/web tests and `go build ./...` green; add a test that seeds incidents in multiple phases and asserts the rendered list page contains the expected phase counts.

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

- [ ] TASK-011: Add `github.com/yuin/goldmark` (Markdown → HTML) and `github.com/microcosm-cc/bluemonday` (HTML sanitizer) to `go.mod`; create `internal/web/markdown.go` exposing `RenderMarkdown(string) templ.Component` (or returning sanitized `template.HTML`) that converts Markdown and sanitizes the result with a strict allowlist policy.
- [ ] TASK-012: In `internal/web/templates/incidents.templ`, render `Diagnosis.Summary`, `Diagnosis.RootCause`, and `Diagnosis.Remediation` through the Markdown renderer (sanitized) instead of plain text.
- [ ] TASK-013: Add a tabbed layout to `IncidentDetail` — tabs such as Overview (event + LLM meta + live status), RCA (the Markdown diagnosis blocks), and Raw (key/value dump). Use a CSS-only tab mechanism (radio inputs / `:checked` or `:target`) so no JS is required; ensure the SSE `#live-status` hook and `sse-connect`/`sse-swap` attributes remain intact and on a tab visible by default.
- [ ] TASK-014: Style the diagnosis cards, confidence, and tabs with the Phase 1 tokens; ensure code blocks/inline code in rendered Markdown use the monospace font and a readable background in both themes.
- [ ] TASK-015: Run `make templ`; add tests in `internal/web` for: (a) Markdown rendering (e.g. `**bold**` → `<strong>`), (b) sanitization (a `<script>`/`onerror` payload in RCA is stripped), and (c) the detail page still contains the SSE `sse-connect` attribute and the phase string.

**Completion criteria**: `go test ./internal/web` passes including a sanitization test proving `<script>` in RCA is removed; `make templ` reproducible; `go build ./...` and `go vet ./...` pass; the detail page shows working tabs, Markdown-rendered RCA, and live SSE status still updates.

**git commit**: `git add -u && git commit -m "feat: tabbed detail view with sanitized markdown RCA"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 3 of dashboard-ui.

Context: kscribe's web dashboard (internal/web, templ + HTMX) has a themed shell (Phase 1) and a dashboard list (Phase 2). This phase makes the incident DETAIL page tabbed and renders the LLM-produced RCA fields as sanitized Markdown. LLM output is untrusted — it MUST be sanitized before rendering.

Branch: dashboard-ui-phase-3  |  Base: dashboard-ui-phase-2

Tasks:
- TASK-011: Add deps github.com/yuin/goldmark and github.com/microcosm-cc/bluemonday (run `go get`). Create internal/web/markdown.go with a function that converts a Markdown string to HTML via goldmark, then sanitizes it with a bluemonday policy (start from UGCPolicy, allow code/pre/headings/lists/links with safe attributes), returning a value templ can render as raw HTML (e.g. templ.Component via templ.Raw of the sanitized string, or template.HTML). Do NOT use encoding/json (CON-003) — not needed here.
- TASK-012: In internal/web/templates/incidents.templ render Diagnosis.Summary, Diagnosis.RootCause, Diagnosis.Remediation through the Markdown renderer (sanitized) instead of plain { text }.
- TASK-013: Convert IncidentDetail into a tabbed view (e.g. Overview / RCA / Raw) using a CSS-only tab mechanism (radio inputs + :checked, or :target) — NO JavaScript. The existing SSE live-status block (div with hx-ext="sse", sse-connect="/incidents/{ns}/{name}/stream", sse-swap="message", id="live-status") MUST remain present, intact, and on a tab shown by default.
- TASK-014: Style diagnosis cards, confidence, and tabs with the Phase 1 design tokens; rendered Markdown code/pre must use the monospace font and a readable background in both light and dark themes.
- TASK-015: Run `make templ`. Add internal/web tests: (a) RenderMarkdown turns **bold** into <strong>; (b) a script/onerror payload embedded in an RCA field is stripped from the rendered detail page (sanitization); (c) the detail page still contains the sse-connect attribute and the phase string.

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

## 3. Testing

- [ ] TEST-001: `go test ./internal/web` — routes return 200/404 as before, Content-Type `text/html`, phase strings present (existing assertions stay green).
- [ ] TEST-002: `go test ./internal/web` — stat counts render correctly for a seeded multi-phase incident set (Phase 2).
- [ ] TEST-003: `go test ./internal/web` — `RenderMarkdown("**x**")` contains `<strong>`; a `<script>`/`onerror` RCA payload is stripped from the detail page (Phase 3, SEC-001).
- [ ] TEST-004: `go test ./internal/web` — detail page still contains the SSE `sse-connect` attribute (REQ-006).
- [ ] TEST-005: `make templ && git diff --exit-code` — generated templ output is reproducible.
- [ ] TEST-006: `make build` — operator binary still builds with the new web assets.
- [ ] TEST-007: Manual — run `scripts/local-test.sh` (or port-forward an existing install), open `/`, confirm: theme toggle (light/dark/system) persists; list shows stat cards; a `Done` incident's detail page shows tabs and Markdown-rendered RCA; SSE phase updates live.

## 4. Risks & Assumptions

- **RISK-001**: Unsanitized LLM Markdown could inject scripts (XSS) — mitigation: mandatory bluemonday sanitization at the single `RenderMarkdown` chokepoint, with a sanitization test (SEC-001).
- **RISK-002**: CSS-only tabs can be finicky for accessibility/anchor behaviour — mitigation: keep a simple radio-input pattern, ensure the SSE tab is default-selected so live updates are visible without interaction; acceptable for MVP.
- **RISK-003**: Theme flash-on-load (FOUC) if `data-theme` is applied after paint — mitigation: apply the stored theme in an inline head script before body render.
- **RISK-004**: Markup changes break existing `internal/web` assertions — mitigation: existing tests assert phase strings, status codes, Content-Type, and SSE framing, all preserved; update only where markup legitimately moves.
- **ASSUMPTION-001**: RCA fields (`Summary`, `RootCause`, `Remediation`) frequently contain Markdown-ish text from the LLM, so Markdown rendering is worthwhile (observed: models return fenced code blocks and lists).
- **ASSUMPTION-002**: Server-side Markdown (goldmark + bluemonday) is preferred over client-side JS rendering — it is testable, works without client JS, and keeps untrusted HTML sanitization on the server. Two small pure-Go deps are acceptable here.
- **ASSUMPTION-003**: CDN font/asset loading is acceptable (consistent with the existing Pico/HTMX CDN usage); no self-hosting/offline requirement for the dashboard in this iteration.
- **ASSUMPTION-004**: Phase branches use the hyphenated form `dashboard-ui-phase-N` (not `dashboard-ui/phase-N`) to avoid a git ref D/F conflict with the `dashboard-ui` plan branch.
