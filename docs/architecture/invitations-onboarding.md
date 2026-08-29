# Invitations and server-driven onboarding

Two complementary entry paths coexist: shareable multi-use `invite_codes`
("drop a code in Discord") and personal, emailed invitations — a single-use
capability token bound to one email address, carrying the access decisions the
admin made at send time. Invitations live in `internal/invitations`; the
first-run tour lives in `internal/onboarding`.

## Invitation model

One row per invitation in the `invitations` table. The invitee chooses only a
password; the invitation's email address becomes their `username` (both
`users.username` and `users.email` are `citext` and globally unique, so the
address is a legal, unambiguous username). To make that natural on the apps,
`LocalProvider.Authenticate` falls back to `GetByEmail` when the username
lookup misses and the input parses as an email address
(`internal/auth/provider.go`).

**No user row exists until accept.** A typo'd address cannot squat a username
or linger as a ghost account. `Send` refuses addresses that already have an
account, checking both the email and username columns (the address is the
future username).

**Status is derived, not stored.** `pending | accepted | expired | revoked` is
computed from `accepted_at` / `revoked_at` / `expires_at` at read time
(`models.Invitation.Status`) — no status column to disagree with the
timestamps, no background expiry job. Revoked wins over accepted; accepted
wins over expired.

## Token model

Token handling mirrors the email-verification flow: 32 random bytes,
`base64.RawURLEncoding` in the claim URL (`/invite/<token>`), SHA-256 hex
stored at rest as `token_hash`. The raw token exists in the sent email (or in
the create response when mail is unconfigured) and nowhere else — a database
dump yields no usable links.

- **Enumeration resistance.** The public lookup returns the same not-found
  result for unknown, expired, revoked, and accepted tokens; the public
  endpoints sit behind the auth-endpoint rate limiter.
- **Privilege ceiling.** An invitation granting `admin` requires the inviter
  to be an admin, enforced in the service against the inviter's row in the
  database, not the request.
- **Invitation binding unchanged.** Pre-bound `library_ids` and
  `access_group_id` are applied verbatim at accept and then feed the existing
  inherit/override policy resolver: the group supplies every field the
  account leaves unset, and a pre-bound library list is stored as an explicit
  account override. An invitation sets initial values; it is never a bypass.
  Admin accounts are never grouped, so an `admin` invitation with an
  `access_group_id` is rejected at send (`422`) rather than stored and then
  silently dropped at accept.

## Lifecycle invariants

- **One live invitation per address.** A partial unique index
  (`invitations_one_pending_idx`, on `email` where neither accepted nor
  revoked) enforces it; `Repository.Create` revokes any live invitation for
  the address in the same transaction. Re-invite and resend therefore
  *supersede*: a forwarded copy of the old link stops working.
- **Accept is race-safe.** `Repository.Accept` claims the row with a single
  `UPDATE ... WHERE accepted_at IS NULL AND revoked_at IS NULL AND
  expires_at > now()`; of two concurrent accepts exactly one matches. The
  loser's account creation is independently blocked by the `users` unique
  constraints. Account-plus-default-profile creation goes through
  `auth.AccountProvisioner.CreateAccount` (rollback on profile failure), and
  a successful accept ends with a normal login, returning the same token pair
  shape as signup.
- **Revoke is idempotent**; deletion is a separate admin-only history-clearing
  operation.
- **Mail degrades loudly, not silently.** Claim links resolve their base from
  `notifications.email.external_url`, falling back to the server public URL;
  with neither set, creation fails. With no mail sender configured, the row is
  still created and the claim URL returned with `EmailSent` false so the admin
  copies the link instead of believing an email went out.

## Accounts vs household profiles

Invitations create **login accounts** (`users` rows), not profiles. Several
household profiles share one account's `user_id`; a profile's `is_primary`
marks the household parent, which is distinct from the server-wide `admin`
role on the account. The invitation's `create_profile` flag controls whether
accept provisions a default profile (named from the email's local part) for
clients that do not render a household-setup step; profiles added afterwards
use the ordinary profiles API. A spouse who needs their own password is a
second invitation — a second account — not a profile. A profile's rating
ceiling and library restrictions layer *under* the account's invitation-bound
access; a profile can never see more than the account received.

## Server-driven onboarding tour

The first-run tour is a server-owned manifest (`internal/onboarding`) so all
three client platforms stay in sync without app-store releases. The flow
endpoint returns an ordered step list already filtered for **this server,
this surface, this profile**:

- **Feature gating.** Each optional step names a gate (requests, watch
  together, recommendations, notifications, calendar, Jellyfin compat)
  checked at request time, so admin toggles apply without a restart and a
  disabled feature never produces a step. A nil gate check means off.
- **Surface filtering.** Steps needing text input are dropped for TV;
  web-only steps (e.g. "install the apps") are dropped elsewhere. Child
  profiles never receive steps they cannot act on (requests).
- **Forward compatibility.** The manifest carries a contract `Version`;
  clients silently skip unknown step kinds, so new kinds are additive and an
  old client gets a shorter tour, not a broken one. Illustrations are
  client-side asset keys — the server never sends image URLs.
- **Real writes.** `setting_choice` steps declare a write target
  (`profile_field`, `setting`, or `device_setting`) so the tour writes through
  the existing profile/settings APIs rather than a parallel persistence path.

Completion state is per-profile and server-side, stored through the
`userstore.UserStore` interface, keyed `(profile_id, tour_id)` with
`last_step`, `completed_at`, `skipped_at`. Both backends implement it:
Postgres (the default, `user_profile_onboarding`) and the per-user SQLite
store (`internal/userdb`, `profile_onboarding`).
Completion and skips are monotonic (upserts `COALESCE`-keep existing
timestamps) and sync across devices: finishing the tour on the web means the
phone does not ask again. The tour is not invite-only — any profile without a
completion record for the current `TourID` gets it; the invitation's
`show_tour` flag only suppresses it deliberately. A materially different
future tour mints a new `TourID` rather than resetting anyone's state.
