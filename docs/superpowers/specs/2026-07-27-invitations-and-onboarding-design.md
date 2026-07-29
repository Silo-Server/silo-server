# Emailed Invitations &amp; Server-Driven Onboarding — Design Spec

**Date:** 2026-07-27
**Status:** Draft — for review
**Mockups:** `docs/design/invite-onboarding.html`
**Surfaces:** silo-server, silo-server/web, silo-android, silo-apple

## Problem

Two gaps, one journey.

**Getting in.** Today an admin creates a multi-use `invite_codes` row and hands
the string to someone out-of-band. That person visits `/signup`, invents a
username, retypes their email, retypes the code, and lands on an empty home
screen. Nothing is scoped ahead of time — library access, group, and role are
whatever the defaults are, so the admin edits the user afterward. There is no
per-person revocation: disabling a code disables it for everyone who has it.

**Staying.** Silo has a set of features most self-hosted media servers do not
ship at all — watch together, media requests, watchlists with arrival
notifications, taste-based recommendations, a release calendar, per-library and
per-series playback overrides, subtitle appearance that follows the profile.
A new user discovers none of it. The only onboarding that exists is the
taste-seed picker (`web/src/pages/TasteSeed.tsx`), which is web-only and
covers exactly one feature.

This spec covers both: a personal, pre-provisioned invitation delivered by
email, and a first-run tour whose content the server owns so all three client
platforms stay in sync.

## Non-goals

- Replacing `invite_codes`. Shareable codes stay exactly as they are for the
  "drop a code in Discord" case. This adds a second, complementary mechanism.
- Self-service password reset. Adjacent and worth doing, but a separate
  concern with its own threat model. The token table here is deliberately
  shaped so a reset flow can reuse it later (see "Future work").
- SSO/OAuth invitations. Plugin auth providers own their own account
  provisioning; an invitation targets local credentials only.
- An admin-authorable tour editor. The manifest is server-owned but built in
  Go, not user-editable. Admins get on/off toggles, not a CMS.

---

# Part 1 — Invitations

## Model

One new table. An invitation is a **capability token bound to one email
address**, carrying the access decisions the admin already made.

```sql
CREATE TABLE public.invitations (
    id                bigserial PRIMARY KEY,
    email             citext NOT NULL,
    token_hash        text NOT NULL UNIQUE,
    -- Pre-bound access, applied verbatim at accept time.
    role              text NOT NULL DEFAULT 'user',
    access_group_id   bigint REFERENCES public.access_groups(id) ON DELETE SET NULL,
    library_ids       integer[],           -- NULL = all libraries
    create_profile    boolean NOT NULL DEFAULT true,
    show_tour         boolean NOT NULL DEFAULT true,
    note              text NOT NULL DEFAULT '',
    -- Lifecycle.
    invited_by        bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    expires_at        timestamptz NOT NULL,
    accepted_at       timestamptz,
    accepted_user_id  bigint REFERENCES public.users(id) ON DELETE SET NULL,
    revoked_at        timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX invitations_email_idx ON public.invitations (email);
-- At most one live invitation per address. Re-inviting supersedes rather
-- than accumulating parallel valid tokens for the same person.
CREATE UNIQUE INDEX invitations_one_pending_idx ON public.invitations (email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
```

Token handling mirrors the email-verification flow that already exists in
`internal/notifications/email_address.go`: 32 random bytes,
`base64.RawURLEncoding` for the URL, **SHA-256 hex stored at rest**. The raw
token exists in the sent email and nowhere else — a database dump does not
yield usable invitation links.

### Why no user row until accept

An unaccepted invitation creates nothing in `users`. A typo'd address cannot
squat a username, cannot appear in the user list as a permanent ghost, and
cannot be counted against any seat/limit logic added later. The
`invitations_one_pending_idx` partial unique index is what keeps "resend"
honest: it supersedes, so a forwarded copy of the old link stops working.

### Email as username

The invitee sets only a password. `username` is set to the invitation's email
address. This works today without a schema change: `users.username` and
`users.email` are both `citext` with unique constraints (migration
`165_case_insensitive_username_email.sql`), so `marco@example.com` is a legal
username and matches case-insensitively.

One behavior change is required for this to feel right:

> **`LocalProvider.Authenticate` must accept an email address in the username
> field.** `internal/auth/provider.go:56` calls `users.GetByUsername` only. It
> gains an email fallback: if the username lookup misses **and** the input
> parses as an email address, retry via `GetByEmail`.

This is strictly additive — existing username logins are unaffected, and there
is no ambiguity risk because both columns are globally unique across the same
identity space. Without it, someone invited by email who later types their
address on the phone app gets "invalid credentials" and has no way to discover
that their username happens to be identical to the thing they just typed.

Login rate limiting already covers this endpoint (`AuthEndpointHandler("login")`),
so the extra lookup does not open an enumeration channel that the single lookup
did not already have.

## Endpoints

Admin, under the existing `/api/v1/admin` tree (admin-gated, same as
`/admin/invite-codes`):

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/admin/invitations` | List, newest first, with derived status. |
| `POST` | `/admin/invitations` | Create + send. Returns the row and, when email is off, the raw claim URL. |
| `POST` | `/admin/invitations/{id}/resend` | Supersede: revoke, mint fresh token, resend. |
| `DELETE` | `/admin/invitations/{id}` | Revoke. Idempotent. |

Public, under `/api/v1`, unauthenticated, **rate-limited via the existing
`deps.RateLimitMW.AuthEndpointHandler`** alongside login/signup:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/invitations/{token}` | Claim-screen data only: inviter display name, email, expiry, server name. |
| `POST` | `/invitations/{token}/accept` | Create the account, redeem, return a session token pair. |

`accept` returns the **same `loginResponse` shape as signup**
(`buildLoginResponse`), so every client reuses its existing session plumbing
rather than growing a parallel auth path.

### Status is derived, not stored

`Pending | Accepted | Expired | Revoked` is computed from
`accepted_at`/`revoked_at`/`expires_at` at read time. No status column, no
background job to expire rows, no possibility of the column disagreeing with
the timestamps.

### Accept is one transaction

Create user → apply pre-bound access → create the default profile → mark the
invitation accepted. `auth.AccountProvisioner.CreateAccount` already handles
user-plus-profile with rollback-on-profile-failure
(`internal/auth/account_provisioner.go`), so this reuses it and adds the
invitation update. A concurrent double-submit loses on the
`accepted_at IS NULL` predicate and returns "already used".

## Email

Composed in a new `internal/invitations` package using `internal/mail`'s
`RenderLayout`, `EmailParagraph`, and `EmailButton`, exactly as
`composeVerificationEmail` does. Same SMTP settings, same diagnostics, same
branded shell.

Link base resolution reuses the established precedence:
`notifications.email.external_url` → server public URL. When neither is set,
`POST /admin/invitations` still creates the row and returns the claim URL with
a `email_sent: false` flag; the admin UI shows **"Copy invite link"** instead
of pretending mail went out. Failing loudly beats a silent drop.

The email states the sign-in address explicitly. It is the one fact the person
will need again when they install the phone app, and the one they would
otherwise guess wrong.

## Deep links

The claim URL is `https://<server>/invite/<token>`.

- **Web** — a new public route, sibling to `/signup` and `/activate` in
  `web/src/App.tsx`.
- **Android** — the `silo` scheme already routes `device`, `item`, `play`, and
  `downloads` hosts in `AndroidManifest.xml`. Add an `invite` host plus an
  `https` App Link on the server's domain, parsed by the existing
  `ContentDeepLinkRoutes` machinery.
- **Apple** — `onOpenURL` already posts to a notification the root view
  consumes and queues until auth settles (`iOSApp.swift`). Add an `invite`
  case; universal links need the server to serve
  `/.well-known/apple-app-site-association`, which is new work.

Crucially, the deep link carries **both the server URL and the token**, so the
app skips its "which server?" step entirely — that is most of the friction in
onboarding someone onto a self-hosted service.

## Household setup ("Who's watching?")

Between the password and the tour sits a household step: profiles for
everyone on the couch — a spouse, kids, grandparents. The journey indicator
reads **Invite → Password → Household**.

**This step needs no new backend.** `POST /api/v1/profiles` already carries
everything the screen sets: name, preset avatar, PIN, `is_child`,
`max_content_rating`, and per-profile library restrictions
(`createProfileRequest`, `internal/api/handlers/profiles.go:50`). The work is
purely presentational — surfacing the existing profile model at the moment it
matters instead of leaving it buried in Settings → Profiles.

Design decisions:

- **Profiles are not logins, and the screen says so.** One account, one
  password, many viewers — several profiles per `user_id` is the existing
  model. The copy heads off the "do I need to invite my wife separately?"
  question. (If she *should* have her own login — her own password, her own
  email — that's a second invitation, and the admin decides which they want.)
- **The kids toggle is a preset, not a form.** Flipping `is_child` on reveals
  a rating ceiling (default PG) and the library picker. Off, they stay hidden.
  A grandparent profile is just a plain profile — no ceiling, no extra fields.
- **PIN guidance over PIN plumbing.** The common mistake is pinning the kid's
  profile; the useful move is pinning the adults' so the kids can't wander in.
  One line of copy under the toggle: "Usually for the parents' profiles, not
  the kids' — it keeps the kids out of yours."
- **"Just me for now" is a first-class exit.** It creates nothing, and the
  tour later includes a household `feature_card` reminding them profiles
  exist. No nagging.
- **Restriction composition is unchanged.** A profile's rating ceiling and
  library restrictions layer under the account's invitation-bound access via
  the existing strictest-layer-wins resolver. The invitee cannot grant a
  profile more than the account received.
- **Taste-seed stays per-profile.** Each created profile gets its own
  taste-seed state, and the picker already filters by the active profile's
  rating ceiling — Emma's picker shows kid-appropriate titles.

The invitation's `create_profile` flag becomes moot when this screen exists on
a surface: the first tile ("You") *is* the default profile, pre-named from the
email's local part and renameable inline. The flag stays in the schema for
headless accepts (a client too old to render the household step still gets a
usable account).

---

# Part 2 — Server-driven onboarding

## Why the server owns it

Three clients, three release cadences, and one of them has a multi-day review
queue. If the tour lives in client code:

- Adding a stop for a new feature means three PRs and waiting on Apple.
- A server with requests disabled still ships a requests stop, so the tour
  advertises a 404.
- Fixing an awkward sentence is a store release.

With a server manifest, the tour is filtered per-server and per-profile before
it reaches the client, and copy changes are a server deploy.

## Contract

```
GET /api/v1/onboarding/flow?surface=web|phone|tv
```

Returns an ordered step list for **this server** and **this profile**, with
disabled features already filtered out:

```json
{
  "version": 1,
  "tour_id": "core-2026-07",
  "steps": [
    {
      "id": "welcome",
      "kind": "welcome",
      "title": "Silo isn't quite like the others",
      "body": "You've probably used Plex or Jellyfin...",
      "illustration": "welcome"
    },
    {
      "id": "playback-quality",
      "kind": "setting_choice",
      "title": "Pick a quality ceiling now, change it anywhere",
      "body": "Silo won't burn your data plan guessing...",
      "setting": {
        "target": "profile_field",
        "key": "quality_preference",
        "control": "segmented",
        "options": [
          {"value": "auto",  "label": "Auto"},
          {"value": "1080p", "label": "1080p"},
          {"value": "4k",    "label": "4K"}
        ],
        "default": "auto"
      }
    },
    {
      "id": "watch-together",
      "kind": "feature_card",
      "title": "Same movie, different couches",
      "body": "Start a room, send the link...",
      "illustration": "watch-together",
      "action": {"label": "Show me", "route": "/rooms/join"}
    },
    {
      "id": "handoff-taste",
      "kind": "handoff",
      "title": "Pick a few favorites",
      "route": "/taste-seed"
    }
  ]
}
```

### Step kinds

| `kind` | Renders | Notes |
| --- | --- | --- |
| `welcome` | Framing card | Always first. |
| `feature_card` | Title, body, illustration key, optional route action | The bulk of the tour. |
| `setting_choice` | A control that writes a real setting | See below. |
| `spotlight` | Highlight of a named UI anchor | Web/tablet only; phone and TV skip. |
| `handoff` | Ends the tour by routing somewhere | Hands off to `/taste-seed`. |

**Unknown kinds are skipped silently.** This is the whole point of the
versioned contract: the server may add a kind, and an older client drops it
rather than crashing or rendering a blank card. Clients send their surface;
the server omits steps that surface cannot render (spotlights on TV, text
entry on TV).

**Illustrations ship with the client**, keyed by a string. The server never
sends image URLs — assets stay native, sized correctly per platform, and work
offline.

### `setting_choice` writes real settings

The tour is not a slideshow that ends with "now go configure it yourself" — by
the last step the account is genuinely set up. It writes through the *existing*
APIs, so there is no new persistence and no risk of the tour and the settings
screens disagreeing.

Silo has three distinct write targets, and the step must say which one it
means. This matters: playback quality is **not** a settings key — it is a
column on the profile, written via `PUT /profiles/{id}` (see
`saveProfileField` in `web/src/pages/settings/PlaybackSettings.tsx:114`).
Collapsing all three into one "key" field would produce a contract that cannot
express the most important step in the tour.

| `target` | Written via | Used for |
| --- | --- | --- |
| `profile_field` | `PUT /api/v1/profiles/{id}` | `quality_preference`, `subtitle_language`, `subtitle_mode`, `auto_skip_intro`, `auto_skip_credits`, `auto_skip_recap` |
| `setting` | `PUT /api/v1/settings/{key}` | `playback.auto_play_next`, `subtitle_appearance`, overlay and home keys |
| `device_setting` | `PUT /api/v1/settings/device/{key}` | Anything that should differ per device (a quality ceiling on cellular) |

The server only emits a `setting_choice` the current profile may actually
write, so a child profile never gets a step it would be refused on.

### Progress

```
GET  /api/v1/onboarding/state      → {"tour_id": "...", "completed_at": ..., "skipped_at": ..., "last_step": "..."}
POST /api/v1/onboarding/progress   → {"tour_id": "...", "last_step": "...", "completed": bool, "skipped": bool}
```

State is stored **per profile, server-side**, in the existing per-user SQLite
store (`internal/userdb`) — no new Postgres table, and no join from Postgres to
profile storage (profiles may live in per-user SQLite, which is why the
notification tables denormalize `user_id` rather than joining).

Note that `user_settings` is keyed by `key` alone and is therefore
**account-wide, not per-profile** (`internal/userdb/schema.go:185`). Storing
tour state there would mean one profile finishing the tour silences it for the
whole household. Two options, in preference order:

1. **A dedicated `profile_onboarding` table** in the userdb schema — keyed
   `(profile_id, tour_id)` with `last_step`, `completed_at`, `skipped_at`.
   Explicit, queryable, and the right shape if contextual follow-up tours
   (multiple `tour_id`s) happen later. Requires a userdb schema version bump
   (`internal/userdb/migrate.go`).
2. A profile-prefixed key in `user_settings` (`onboarding.<profile_id>.state`).
   No schema change, but it abuses a flat key space and makes "show me every
   profile that skipped" a `LIKE` scan.

**Recommendation: option 1.** The userdb schema already carries per-profile
tables (`subtitle_preferences`, `series_playback_preferences`), so this follows
the established pattern rather than working around it.

Two consequences worth stating plainly:

- Finishing the tour on the web means the phone does not ask again.
- Skipping is honored everywhere, permanently, until the user replays it from
  Settings → Personalize.

The current taste-seed dismissal is a **localStorage** flag
(`web/src/lib/tasteSeed.ts`), which is why it re-prompts on every new browser
and never syncs to the apps. The tour deliberately does not repeat that
mistake; taste-seed's own flag can migrate to the same mechanism later.

### Gating

Steps are filtered against what the server actually has on:

| Step | Gated on |
| --- | --- |
| Requests | `requests_enabled` (`internal/requests`) |
| Watch together | watch-together enabled |
| Notifications | `mail.Sender.Enabled()` or push configured |
| Calendar | any library with scheduled content |
| Recommendations | recommendations enabled |
| Subtitles / quality / overlays | always (core playback) |

A server with nothing but a movie library gets a short, honest tour rather
than a tour of things it does not do.

## Reach

The tour is **not invite-only**. Anyone whose profile has no completion record
gets it: existing accounts, code-signup accounts, and additional profiles on
an existing account. The invitation just makes the entrance nicer. The
`show_tour` flag on an invitation only suppresses it for the deliberate case
(re-inviting someone who already knows the product).

---

## Security notes

- **Tokens at rest are hashed.** A DB dump yields no usable links.
- **Enumeration.** `GET /invitations/{token}` returns identical 404s for
  unknown, expired, revoked, and accepted tokens. The claim screen distinguishes
  them only via the accept path's error codes, which are rate-limited. 32 bytes
  of entropy makes guessing irrelevant.
- **Privilege ceiling.** An admin cannot mint an invitation granting more than
  they hold. Role `admin` on an invitation requires the inviter be an admin —
  enforced at the handler, not just hidden in the UI.
- **Access composition is unchanged.** Pre-bound `library_ids` and
  `access_group_id` feed the existing restriction-only resolver
  (`docs/superpowers/specs/2026-07-02-access-groups-design.md`): the strictest
  layer still wins. An invitation is a convenience for setting the initial
  values, never a bypass.
- **Mail as an oracle.** Invitation sending is admin-only, so it is not a
  spray vector the way unauthenticated verification sends would be. Resend
  inherits the same admin gate.
- **Note field** is admin-authored and rendered escaped in HTML mail and raw in
  the text part — same handling as every other user-supplied value in
  `internal/mail`.

## Risks and tradeoffs

**Email-as-username changes the login lookup.** Mitigated by making it a
fallback (username first, email only on miss and only when the input parses as
an address), so existing behavior is untouched. Worth a test that asserts a
user whose *username* is `a@b.com` and a different user whose *email* is
`a@b.com` cannot both exist — the unique constraints already prevent it, but
the test documents why the fallback is unambiguous.

**A server-driven tour couples client releases to a contract.** Mitigated by
"unknown kinds are skipped" and an explicit `version`. The failure mode of a
client that is too old is a shorter tour, not a broken one.

**Apple universal links need server-side association hosting.** The `silo://`
custom scheme works immediately; universal links are a follow-up that improves
the experience but does not gate the feature.

**Scope.** This is a three-repo change. The server plus web is independently
shippable and useful on its own; the clients can follow without the server work
being wasted in the meantime.

## Future work

- Password reset reusing the same token shape and mail plumbing.
- Self-serve "invite a friend" for household primaries, bounded by an
  admin-set quota — the table already carries `invited_by`.
- Migrating taste-seed's localStorage dismissal to server-side onboarding
  state.
- Contextual follow-ups ("you've watched 5 things — did you know about
  watchlists?") reusing the same manifest with a different `tour_id`.

## Open questions

1. **Invitation expiry** — 7 days assumed. Admin-configurable server setting,
   or fixed?
2. **Tour replay entry point** — Settings → Personalize is assumed. Confirm
   that is the right home rather than a Help menu.
3. **`tour_id` versioning** — when the tour changes materially, should users
   who completed `core-2026-07` be re-prompted for `core-2027-01`, or does
   completion mean completion forever?
