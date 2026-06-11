# Notifications & Push — Test Checklist

A repeatable regression pass for the core notifications + push subsystems. Organized by layer: automated (fast, run anytime), server pipeline (API, no browser), real browser (human-in-the-loop), and not-yet-testable (future work). Run the automated layer on every change; run the browser layer before shipping a user-facing change.

Specs/plans this exercises live under `docs/superpowers/specs|plans/2026-06-10-core-notifications-*` and `*-push-*`.

---

## 1. Automated tests (fast, no live instance)

Run from the repo root. `export GOWORK=off` first (or run inside the worktree).

```bash
# Go — materializer, matchers, worker state machine, request events, presence, handlers, tasks
GOWORK=off go test ./internal/notifications/ ./internal/push/ ./internal/presence/ \
  ./internal/requests/ ./internal/api/handlers/ ./internal/taskmanager/tasks/

# Web — bell, inbox, settings, admin page, usePushDevice, SW logic
cd web && pnpm exec vitest run
```

Expected: all green. The Go suites cover the outcome state machine (sent/skipped/failed-with-backoff/dead + dead-token prune), child-suppression SQL, the `notifications.send` plugin contract, request lifecycle publishing, and presence refcounting. The web suites cover the UI and the `usePushDevice` lifecycle/race-guard.

What this does NOT cover: real WebSocket delivery, real push banners, real migrations against production-shaped data. Use layers 2–3 for those.

---

## 2. Server pipeline (API — proves source → materializer → enqueuer → worker, no browser)

Needs an admin API key and the instance reachable (on silo-new the API is `http://localhost:8090`; create a temp key, delete it after — see the snippet at the bottom). Observe results in the `notifications` and `push_deliveries` tables.

For each notification **source**, trigger it and confirm a row lands (and, if push is configured + a device registered, a `push_deliveries` row):

- [ ] **Announcement** — `POST /api/v1/admin/announcements {title, body, audience:{all:true}}` → confirm a `notifications` row (category `announcement`) for each targeted user. ⚠️ `audience:{all:true}` fans out to **every** user's inbox — use `{user_ids:[<your id>]}` when testing on a live instance.
- [ ] **Request lifecycle** — submit a media request, then `POST /api/v1/requests/{id}/approve` (admin) → confirm a `request.approved` notification for the requester. Decline/complete/fail similarly produce `request.declined` / `request.completed` / `request.failed`.
- [ ] **System** — admin user-update that changes a password → confirm a `system.password_changed` notification for that user.
- [ ] **Content** — add an item to a profile's watchlist/favorites, then add/scan a matching library item → confirm a `content.added` notification for that profile (respects the 48h recency gate + per-day burst dedup).
- [ ] **Admin alert** — force a failed job/scan → confirm an `admin`-category notification fans out to admin users only (throttled 1/hour per source).
- [ ] **Digest** — opt a user into `content_digest`, then run the `notifications_digest` task (or wait for 08:00) → one digest notification if there were additions.
- [ ] **Push test** — `POST /api/v1/admin/push/test` → 202; a `system.test_push` notification + a `push_deliveries` row per the caller's registered push devices; the worker transitions it within ~30–45s (grace + tick). With a real subscription it sends; with a bogus one it fails-with-backoff (still proves the worker path).

Push config endpoints to verify:
- [ ] `GET /api/v1/admin/push/status` → `{apns,fcm,webpush}` booleans (flip after saving creds).
- [ ] `POST /api/v1/admin/push/generate-vapid-keys` → `{vapid_public, vapid_private}` (admin only).
- [ ] `GET /api/v1/notifications/push/webpush-key` → the configured public key (empty when unconfigured).
- [ ] `PUT /api/v1/notifications/push/device {transport:"webpush", token:<subscription JSON>}` with `X-Profile-Id` → 204 (400 without the profile header — the route is profile-gated).
- [ ] `GET /api/v1/notifications/push/devices` → lists the user's push devices; `PUT .../devices/{id} {enabled}` toggles.

---

## 3. Real browser (the only way to validate the UI + actual push banner)

Open the web app over a secure context (`localhost` or HTTPS — service workers require it). On silo-new that's `http://localhost:8090` (tunnel if remote).

In-app notifications:
- [ ] **Bell + badge** — top of the app; unread count badge.
- [ ] **Dropdown** — latest notifications, "Mark all read", deep links.
- [ ] **Inbox page** (`/notifications`) — category tabs, dismiss, mark-on-view, "Load more".
- [ ] **Live toast** — with the app open, trigger a notification (announcement to yourself, or the push-test) → a toast appears in real time (via the WebSocket), unless that category is muted.
- [ ] **Preferences** — Settings → Notifications → per-category mute toggles persist; "Daily digest" defaults off; the admin category shows only for admins.

Push (the headline manual test):
- [ ] **Admin config** — Admin → Settings → Push Notifications → "Generate keys", save the VAPID public/private + a `mailto:` subject → the Web Push badge flips to "Ready".
- [ ] **Enable on this device** — Settings → Notifications → "Push on this device" → grant the browser permission prompt → a device row appears in the list.
- [ ] **Background banner** — **close the app tab**, then `POST /admin/push/test` (or trigger any notification) → within ~30–45s an OS notification banner appears.
- [ ] **Click-through** — click the banner → the app opens/focuses and navigates to the notification's deep link.
- [ ] **Presence gating** — with the app tab open and focused, the same notification should NOT push a redundant banner (you saw it in-app); after closing the tab for >30s it does.
- [ ] **Per-device toggle / revoke** — disable the device in the list → no more banners; re-enable restores.

---

## 4. Not yet testable (tracked future work)

- **APNs (Apple) / FCM (Android)** — no mobile clients exist to register tokens, so these transports are only unit-tested (pure result-mappers). End-to-end needs silo-apple / silo-android (Phase 3 mobile).
- **Plugin `notifications.send`** — the ebook/audiobook request plugins don't publish it yet. Until those one-line `PublishEvent("notifications.send", …)` calls are added, plugin-sourced notifications can't be exercised end to end (the contract itself is unit-tested).
- **iOS Safari web push** — only works for an installed PWA, not a normal tab; the client degrades to an "unsupported" state otherwise.

---

## Appendix — temp admin key on silo-new (create, use, delete)

```bash
# create
KEY="sa_$(openssl rand -hex 32)"
docker exec silo-server-postgres-1 psql -U silo -d silo -tAc \
  "INSERT INTO api_keys (user_id,label,api_key) VALUES (1,'manual-test','$KEY');"
echo "$KEY"   # use as: Authorization: Bearer $KEY against http://localhost:8090/api/v1/...

# observe
docker exec silo-server-postgres-1 psql -U silo -d silo -tAc \
  "SELECT id,category,type,title FROM notifications ORDER BY id DESC LIMIT 10;"
docker exec silo-server-postgres-1 psql -U silo -d silo -tAc \
  "SELECT status,transport,attempts,left(last_error,40) FROM push_deliveries ORDER BY id DESC LIMIT 10;"

# clean up (delete the key + any test artifacts you created)
docker exec silo-server-postgres-1 psql -U silo -d silo -tAc \
  "DELETE FROM api_keys WHERE label='manual-test';"
```

Always remove test notifications/devices you create on a real user's account; `push_deliveries` cascades when its `notifications` row is deleted.
