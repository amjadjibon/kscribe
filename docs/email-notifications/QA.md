# QA Report: email-notifications

Date: 2026-07-04 · Branch: `email-notifications` · Iteration 1

## Coverage

| Package | Coverage | Notes |
|---------|----------|-------|
| notify | 87.5% | uncovered: default-BaseURL/default-HTTPClient branches (hit only against the real API) |
| metrics | 100% | NotificationsTotal added to the collector test during QA |
| controller | 81.9% | async notify path covered incl. error-swallow and single-fire |

## Feature-path checks

- `TestSend` — auth header, JSON body shape, 200 → nil.
- `TestSendErrorTruncated` — 422 with 2KB body → error carries status, ≤512B body.
- `TestHTMLEscapes` — `<script>`/`&`/`<it>` in LLM-sourced fields are escaped.
- `TestSubject` — subject format.
- `TestReconcile_NotifiesOnTerminal` — exactly one async notification with the RCA summary; notifier error never fails the reconcile; channel-synchronized (no sleeps for the happy path).
- Existing reconciler tests unchanged → nil Notifier no-op confirmed.
- `helm template` renders secret-backed `KSCRIBE_RESEND_API_KEY` + joined `KSCRIBE_NOTIFY_EMAIL_TO`.

## Accepted gaps

- TEST-004 (real Resend send against a verified sender) requires a live API key — manual.
- Storage-error retry double-send (PLAN-REVIEW SUGGEST-001) — accepted, documented in the reconciler comment.

## Verdict

All packages green (`go test ./... -count=1`, 10/10 incl. new notify package).
