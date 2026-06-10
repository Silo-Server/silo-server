# PR #124 (ebooks) — Review & Hardening Pass

Date: 2026-06-10
Scope: full review of the ebook feature branch (`work/ebooks-reader-base`) followed by
fixes for every finding. Two review rounds were performed (a full-PR review, then an
adversarial edge-case/security pass over both the original code and the first round of
fixes), with all changes verified against the full Go test suite, the full web test
suite, production frontend build, ESLint/Prettier, and `make verify-local-paths`.
Commands assume the repository root is the cwd.

## Security fixes

- **Stored XSS via book content (high):** book sections render in same-origin blob
  iframes with `allow-scripts` (WebKit requirement). A Content-Security-Policy is now
  served with all SPA HTML responses (`internal/server/frontend.go`) — blob/srcdoc
  documents inherit it, so `script-src 'self' 'wasm-unsafe-eval'` blocks script
  execution from book content on all browsers. Threat model documented on the policy
  constant. `X-Content-Type-Options: nosniff` added to frontend, jellycompat, and
  ebook file responses.
- Ebook file serving can no longer fall through to `application/octet-stream` for an
  admitted file (extension/container whitelist and MIME resolution share one resolver),
  closing a browser-sniff escape combined with the `?token=` query-auth fallback.
- External links in books are intercepted app-side: http(s) only, opened with
  `noopener,noreferrer` (blocks reverse tabnabbing and `javascript:` URLs).
- Request size caps (`http.MaxBytesReader`, 413) on progress/config/annotation writes;
  `Content-Disposition` built with `mime.FormatMediaType` (RFC 5987, no header
  injection); annotation PATCH is atomic (`SELECT ... FOR UPDATE`) with
  presence-aware field semantics and invariant re-validation.
- Verified safe under adversarial review: no IDOR (all reader-state SQL scoped to
  user+profile+content), no path traversal, range serving via stdlib, progress input
  validation. Accepted residual: CSS-only loads inside book iframes can leak the
  book's own content but cannot reach tokens, DOM, or navigation.

## Reliability fixes

- **Scanner missing-file reconciliation (critical class):** ebook scans now mark
  missing files like video/audio, with real walk-failure tracking (failed roots are
  excluded from deletion), symlinked-root support via the shared logical walker, and
  the empty-root cleanup allowance — an unmounted share can no longer wipe a library.
- **Enrichment:** provider errors record failures (dedicated `ebook_enrichment_state`
  backoff table, capped retries) instead of permanently stamping items done;
  unconfigured chains and scan-window races skip without stamping. No longer shares
  `media_items.refresh_failures` with the metadata refresh-debt system.
- Ebook items are created `pending` and promoted to `matched` on enrichment (backfill
  migration included), making curated-metadata protection real: matched items keep
  provider titles/people/series on re-scan (fill-empty only).
- PDF metadata: head+tail window scan (non-linearized PDFs keep the Info dict at the
  end), delimiter-aware key matching, head-wins merge. `.md` dropped as an ebook
  format; plain `.fb2` reads capped like `.fbz`.
- FK-cascade indexes added for the reader-state tables.

## Consistency & correctness fixes

- Hidden-history (`user_history_hidden_items`) gating applied to every ebook progress
  surface (watched/in-progress filters, all sort plans, Continue Reading, sort
  metrics, `Played` state, recommendation signals) with the exact video semantics.
- `GetItemWatchers` counts distinct watchers (no binge inflation) and the
  `minWatchers` floor counts distinct accounts, not profiles; ebook reading now feeds
  implicit taste signals and taste-seed candidates; Continue Reading pages past
  dismissals and dedupes across pages.
- The 0.9 finished threshold is centralized (`models.EbookFinishedProgressThreshold`).
- Reader frontend: open-flow race teardown (no wrong-file progress saves), ordered
  progress writes with `pagehide`/`visibilitychange` flush, settings no longer
  clobbered by late server config, TTS stop actually stops, Media Session cleanup,
  512 MiB download guard, fraction bookmarks navigable.

## Feature completion

- Native read-state endpoints: `POST/DELETE /watched/{id}` and `/history/remove`
  accept ebook content IDs (mark read = progress 1.0 preserving location; mark unread
  mirrors video unwatch and clears position; history removal hides without losing
  position). Web UI exposes Mark Read/Unread on the item page and card menus, with
  Continue Reading dismiss copy and dismissal-path ID encoding fixed.

## Client follow-ups (Android / Apple)

- Ebook leaf user data encodes the reading ratio as `position_seconds` (0..1) with
  `duration_seconds = 1`; do not render absolute times for ebooks.
- `/watched/{id}` returns `{type: "ebook", affected_count: 1, played: bool}` and the
  existing watched SSE event fires.
- Reading progress is deliberately per-book (cross-format): one position per content
  ID.
