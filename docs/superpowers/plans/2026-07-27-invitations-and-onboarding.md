# Implementation Plan: Emailed Invitations &amp; Server-Driven Onboarding

**Spec:** `docs/superpowers/specs/2026-07-27-invitations-and-onboarding-design.md`
**Mockups:** `docs/design/invite-onboarding.html`
**Date:** 2026-07-27

Commands assume the repository root is the cwd.

## Shape of the work

Two features that share a journey but not a dependency. **Part 1
(invitations) and Part 2 (onboarding) are independently shippable** — build
and merge them in that order, but neither blocks the other if priorities
shift.

Phases 1–4 are silo-server and ship as one PR each. Phases 5–6 are the client
repos and follow after the server contract is merged and stable.

Resolve the spec's three open questions before starting Phase 1 — expiry
configurability, replay entry point, and `tour_id` re-prompt semantics all
affect the schema.

---

# Part 1 — Invitations (silo-server)

## Phase 1 — Migration

`make migrate-create NAME=invitations` — one timestamped Goose migration, no
hand-numbering, no paired up/down files.

Up: the `invitations` table exactly as specced, plus the two indexes. The
partial unique index is the load-bearing one:

```sql
CREATE UNIQUE INDEX invitations_one_pending_idx ON public.invitations (email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
```

It is what makes "resend supersedes" true at the database level rather than
only in application code.

Down: `DROP TABLE public.invitations;`.

Verify with `make migrate-up && make migrate-status`.

## Phase 2 — Backend: model, repo, mail, service

New package `internal/invitations`. It owns the whole feature; nothing goes in
a catch-all helper.

**`internal/models/invitation.go`** — `Invitation`, `CreateInvitationInput`,
and a derived `Status()` method returning `pending|accepted|expired|revoked`
computed from the timestamps. No status column.

**`internal/invitations/repository.go`** — CRUD plus:
- `Create` — mints the token, stores only the SHA-256 hex. Returns the raw
  token to the caller once and never again.
- `GetByTokenHash` — the claim lookup.
- `Accept(ctx, tokenHash, userID)` — `UPDATE ... WHERE token_hash = $1 AND
  accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()`. Zero
  rows affected means already-used/expired/revoked; the caller distinguishes
  by re-reading. This is the concurrency guard for double-submit.
- `Revoke`, `List`.

Mirror the token helpers in `internal/notifications/email_address.go`
(`newEmailToken` / `hashEmailToken`) rather than reinventing them — consider
lifting them to a shared spot if the duplication grates, but do not
restructure `notifications` as a side effect of this work.

**`internal/invitations/email.go`** — `composeInvitationEmail`, built from
`mail.RenderLayout` / `EmailParagraph` / `EmailButton`
(`internal/mail/layout.go:88`). Escape the admin note. Link base resolution
reuses the `notifications.email.external_url` → public URL precedence.

**`internal/invitations/service.go`** — orchestration:
- `Send` — validates the address, supersedes any pending invite for it,
  creates, sends. On `mail.ErrNotConfigured`, returns the row plus the claim
  URL and `emailSent=false` rather than erroring. Rejects a `role: admin`
  invitation from a non-admin inviter (enforce at the service, not the UI).
- `Lookup` — claim-screen projection only: inviter display name, email,
  expiry. Nothing else leaves the server.
- `Accept` — one transaction: `auth.AccountProvisioner.CreateAccount`
  (`internal/auth/account_provisioner.go`, which already rolls back the user if
  profile creation fails) with the pre-bound role/group/libraries, then
  `repository.Accept`, then log in via the existing session path so the
  response is a normal `TokenPair`.

**Login fallback** — `internal/auth/provider.go:56`. `LocalProvider.Authenticate`
currently calls `GetByUsername` only. Add: on not-found, if the input parses as
an email address (`net/mail.ParseAddress`), retry `GetByEmail`. Keep the
password comparison path identical so timing does not diverge between the two
lookups.

Tests:
- Accept twice concurrently → exactly one account, one 4xx.
- Accept an expired invitation → refused, no user row.
- Resend invalidates the prior token.
- Email login fallback resolves; username login is unchanged.
- Non-admin inviter cannot mint an admin invitation.

## Phase 3 — Backend: HTTP

**`internal/api/handlers/admin_invitations.go`** — list/create/resend/revoke,
following `admin_invite_codes.go` for shape and error mapping.

**`internal/api/handlers/invitations.go`** — the two public endpoints.
`GET /invitations/{token}` returns an identical 404 for unknown, expired,
revoked, and accepted; do not leak which.

**`internal/api/router.go`**:
- Admin routes beside `/invite-codes` (~line 2891), same admin gate.
- Public routes beside `/auth`, wrapped in
  `deps.RateLimitMW.AuthEndpointHandler("invitation")` where the limiter is
  present, matching the `login`/`signup` treatment at lines 1708–1716.

`accept` returns `buildLoginResponse` — the same shape as signup — so no
client grows a parallel auth path.

## Phase 4 — Web: admin tab + claim page

**`web/src/pages/admin-settings/InvitationsTab.tsx`** — table plus composer
dialog, per the mockup. Follow `InviteCodesTab.tsx` for structure and the
`useAdminInviteCodes` hooks for the query layer. Register the tab in
`AdminUsers.tsx` beside the existing `invite-codes` tab.

When the create response carries `email_sent: false`, swap the success toast
for a copyable link and say plainly that email is not configured.

**`web/src/pages/InviteClaim.tsx`** — public route `/invite/:token`, added to
`App.tsx` beside `/signup` (line 373). Reuse `auth-shell`, `AuthBackground`,
and the `PasswordInput` component. Expired/used/revoked renders an explanatory
card with a sign-in link, never a bare 404.

On success, store the returned tokens through the existing auth hook and route
onward — do not bounce the user back to `/login`.

**`web/src/pages/HouseholdSetup.tsx`** — the "Who's watching?" step, routed to
directly after a successful accept. **No new backend**: each tile posts through
the existing `POST /profiles` mutation, whose request already carries name,
avatar, PIN, `is_child`, `max_content_rating`, and per-profile library
restrictions (`createProfileRequest`, `internal/api/handlers/profiles.go:50`).

- The first tile is the invitee's own profile, pre-named from the email local
  part, renameable inline.
- The add-profile dialog per the mockup: kids toggle reveals rating ceiling
  (default PG) + library picker; PIN toggle carries the "pin the adults, not
  the kids" copy.
- "Just me for now" exits without creating anything.
- Reuse the avatar preset picker and PIN dialog pieces from
  `ProfilesSettings.tsx` rather than duplicating them — extract shared
  components if needed.

Add `Invitation` types to `web/src/api/types.ts` and hooks under
`web/src/hooks/queries/admin/invitations.ts`.

---

# Part 2 — Onboarding (silo-server, then clients)

## Phase 5 — Backend: manifest + state

**userdb schema** — add `profile_onboarding` keyed `(profile_id, tour_id)`
with `last_step`, `completed_at`, `skipped_at`. This needs a
`schemaVersion` bump in `internal/userdb/migrate.go` (currently 13) plus the
matching migration step; follow how the existing per-profile tables
(`subtitle_preferences`, `series_playback_preferences`) were added. Do **not**
use `user_settings` — it is keyed by `key` alone and therefore account-wide,
so one profile finishing would silence the tour for the whole household.

**`internal/onboarding`** — new package:
- `steps.go` — the manifest as Go data. Copy lives here.
- `filter.go` — per-server and per-surface filtering. Query the real feature
  gates: `requests_enabled` via `internal/requests`, watch-together, whether
  `mail.Sender.Enabled()` or push is configured, recommendations. Drop
  `spotlight` and any text-entry step for `surface=tv`.
- `state.go` — read/write via the userdb store.

**Handlers + routes** — `GET /onboarding/flow`, `GET /onboarding/state`,
`POST /onboarding/progress`, all profile-scoped (`apimw.RequireProfile`, as the
`/settings/effective` group does at router.go:2242).

The manifest must only emit a `setting_choice` the current profile may write —
check child-profile restrictions before including a step, rather than letting
the client discover the refusal.

Tests: a server with requests disabled omits the requests step; `surface=tv`
omits spotlights; completing on one profile does not mark another complete.

## Phase 6 — Web: the tour

**`web/src/components/onboarding/`** — `TourHost` (the state machine plus
progress POSTs) and one renderer per step kind. Unknown `kind` → skip, no
error. This is the compatibility guarantee; test it explicitly with a
fabricated future kind.

`setting_choice` dispatches on `target`:
- `profile_field` → `PUT /profiles/{id}` via the existing update mutation
  (see `saveProfileField`, `web/src/pages/settings/PlaybackSettings.tsx:114`)
- `setting` → the existing `useSetSetting`
- `device_setting` → the existing device-setting mutation

Reusing those mutations means the tour writes the same rows the settings
screens write and inherits their cache invalidation for free.

**Entry** — extend the existing `TasteSeedGate` in `App.tsx` (line 234) into an
onboarding gate that checks server state first, runs the tour, then hands off
to `/taste-seed` via the `handoff` step. The tour precedes taste-seed rather
than replacing it.

**Replay** — an entry point in Settings → Personalize (pending the spec's open
question 2).

## Phase 7 — Android (silo-android)

Separate repo, separate PR, after the server contract is merged.

- Models + client method for the three onboarding endpoints in
  `shared/src/commonMain/kotlin/.../model`, alongside the existing auth models.
- Compose tour UI in `androidApp/.../ui/screens/onboarding/`: a full-screen
  `HorizontalPager`, one step per page, skip always reachable. Unknown kinds
  filtered out at parse time.
- Invite deep link: add an `invite` host to the existing `silo` scheme
  `intent-filter` in `AndroidManifest.xml` (lines 49–60) plus an `https` App
  Link, and a claim screen beside `SignupScreen.kt`. Parse via the existing
  `ContentDeepLinkRoutes` machinery.
- Household setup screen after claim: profile tiles + add-profile sheet,
  posting through the existing profiles endpoint. The kid preset mirrors the
  web dialog (rating ceiling + libraries).
- TV (`androidTvApp`) requests `surface=tv` and renders the focus-driven
  variant — no keyboard entry.

## Phase 8 — Apple (silo-apple)

Separate repo, separate PR. **Build and validate on `mac-builder` through the
`xcodebuildmcp` MCP server**, preserving the exact commit plus dirty and
untracked state — do not validate against a stale remote checkout.

- Networking models beside `DeviceLoginModels.swift`.
- `Screens/Onboarding/` — SwiftUI `TabView(.page)` tour reusing the Aurora
  design system (`AuroraScreen`, `AuroraPrimaryButtonStyle`). Unknown kinds
  filtered at decode.
- Claim screen in `Screens/Auth/` beside `SignupView.swift`, reusing
  `AuroraJourneyProgress` (`DesignSystem/Aurora/AuroraStyle.swift:61`) with the
  invite path as step 2. Note `AuroraJourneyProgress` hardcodes
  `["Server", "Account", "Profile"]` — it needs a labels parameter for the
  invite journey (`Server → Password → Household`).
- Household setup after claim, adjacent to the existing
  `Screens/Profiles/ProfileSelectionView.swift`: tiles + add-profile sheet
  posting through the existing profiles endpoint, kid preset matching web.
- Deep link: add an `invite` case to the `onOpenURL` handler in `iOSApp.swift`
  (line 51), which already queues links until auth settles. Universal links
  additionally need the server to serve
  `/.well-known/apple-app-site-association` — a small server-side follow-up,
  not a blocker for the custom scheme.
- tvOS requests `surface=tv`.

---

## Verification

Per server PR:

```bash
make lint
cd web && pnpm run lint && pnpm run format:check
make verify-local-paths
```

Plus `make migrate-up` / `make migrate-status` for Phases 1 and 5, and browser
verification of the web flows via the `web-ui-testing` skill with screenshots
for the PR.

## Commit sequence

One PR per phase, Conventional Commit subjects, one concern each:

| Phase | Repo | Subject |
| --- | --- | --- |
| 1–3 | silo-server | `feat(invitations): add emailed pre-provisioned invitations` |
| 4 | silo-server | `feat(web): add invitation admin tab and claim page` |
| 5 | silo-server | `feat(onboarding): add server-driven onboarding manifest` |
| 6 | silo-server | `feat(web): add first-run feature tour` |
| 7 | silo-android | `feat(onboarding): add invite claim and feature tour` |
| 8 | silo-apple | `feat(onboarding): add invite claim and feature tour` |

Phases 1–3 are one PR: the migration, repo, service, and handlers are a single
coherent unit and splitting them leaves a table nothing reads.

Each PR needs `Part of #NNN` against the capability epic, the AI-use disclosure
block per `docs/ai-contributions.md`, and screenshots for the UI phases.

## Risks

**Login lookup change (Phase 2)** is the highest-risk item — it touches the
path every existing user authenticates through. It is additive (fallback only
on miss), but it warrants explicit regression tests for username login and a
careful review.

**userdb schema bump (Phase 5)** changes per-user SQLite stores. Follow the
existing migration test pattern (`internal/userdb/migrate_v13_test.go`) and
verify an older store upgrades cleanly.

**Client contract drift** is bounded by "unknown kinds are skipped" — but that
only holds if each client actually implements the skip. Test it on all three
before the first post-launch manifest change, not after.
