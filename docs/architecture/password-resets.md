# Password resets and temporary passwords

An administrator can help a locked-out account in two ways without handling its
password: issue a single-use reset link, or set a temporary password the account
must replace at its next sign-in. Reset links live in `internal/passwordreset`.
The temporary-password state lives in `internal/auth` and the auth middleware. The
wire contract is in [admin-users-api.md](../admin-users-api.md#passwords) and
[auth-api.md](../auth-api.md#temporary-passwords).

Both apply only to accounts that sign in with a local password
(`users.local_password_login_enabled`). An account an external authentication
provider manages has no password here to reset, so the server refuses the actions
and clients hide them.

## Reset links

**One live link per account.** `password_reset_tokens` is keyed by `user_id`.
Issuing a link upserts that row, so a newer link cancels the older one with no
separate revocation step. Completing a reset deletes the row, which makes the link
single-use. Expired rows stay until the next issue or the account's deletion,
bounded at one row per account.

**Stored as a digest.** Tokens use the invitation scheme (`auth.NewLinkToken`): 32
random bytes, base64url in the link, SHA-256 hex at rest. The raw token exists
only in the email or in the one response that discloses a shared link. The request
log redacts the `{token}` path parameter.

**Bound to the password it replaces.** Each row records a SHA-256 fingerprint of
`users.password_hash` at issue time, and a link only resolves while the account
still has that hash. A password changed any other way (self-service, an
administrator, another reset) retires outstanding links without other writers
knowing this table exists. Resolving also requires the account to be enabled and
to use local password sign-in.

**Uniform not-found.** Lookup and completion return the same `ErrNotFound` for
unknown, expired, used, replaced, and outdated links, so a probe learns nothing.
The public operations spend the `password_reset` per-IP rate-limit budget; with
256-bit tokens this is anti-probe hygiene, not the guard.

**One transaction.** Completion locks the link and account rows, re-checks
expiry against `clock_timestamp()`, deletes the link, replaces the password,
clears `password_change_required`, and revokes every login session the account
owns or impersonates from, then commits. The same transaction revokes the
account's Audiobookshelf-compatible sessions and denies any device sign-in it
approved that the device has not collected yet, which would otherwise mint a
fresh session afterwards. Personal API keys survive: the account created them on
purpose, and revoking them would break its integrations, so the account or an
administrator revokes them separately. Concurrent completions of one link have
exactly one winner. Sign-in afterwards is a separate effect: when it fails, the
response still reports the committed reset (`sign_in_required`) and the caller must
not replay it. The `OnUserSessionsRevoked` hook then drops Jellyfin-compatible
sessions held in memory, the same as for administrator account edits, on every
replica: it announces the revocation on the admin event channel.

**No link without an external URL.** Links are built on `server.public_url`
(`mail.AccountLinkBase`, shared with invitations). Without one, issuing fails with
`capability_not_configured`, and the capability document reports it up front. Email
delivery also refuses before minting when mail is unconfigured. A link nobody
receives would still replace the one the account may already hold.

## Temporary passwords

**Every password write decides the flag.** `users.password_change_required` is
written only together with a password. An administrator's create or update sets it
from `require_password_change`, false when omitted, and refuses it for an account
without local password sign-in, which could never run the change. A self-service change or a
completed reset clears it. A write without a password never touches it, so no path
leaves a stale flag behind.

**Carried in the token, recomputed on issue.** Login, device pairing, OAuth
completion, and refresh copy the flag from the account into the access-token claim
`password_change_required`. The auth middleware already validates the token and its
session on every request, so the restriction costs no extra read. An administrator
setting a temporary password signs the account out everywhere in the same
transaction, Audiobookshelf-compatible sessions included, so every session opened
afterwards carries the claim. Choosing the new
password revokes every other session of the account in the same transaction: each
was opened with the temporary password, possibly by someone else, and its next
refresh would otherwise lift the restriction. The session that chose the password
gets tokens without the claim on its next refresh. An impersonating
administrator's tokens never carry it.

**Allowlist, fail closed.** `RequireAuth` admits a restricted session only to the
exact routes in `passwordChangeRoutes`: reading the account, the password
capability, changing the password, and logout, on both API majors. Everything
else, including new routes, returns `403 password_change_required`. The
`plugin_access` bridge refuses restricted tokens too. The refusal is still
attributed to the account in the request log. A restricted session may change the
password without choosing a profile, but must still prove the temporary password,
and cannot reuse it as the new one.

**Surfaces that cannot run the change refuse the login.** Jellyfin and
Audiobookshelf compatible sign-ins call `auth.Service.CompatLogin`, which refuses an
account holding a temporary password before opening a session.
