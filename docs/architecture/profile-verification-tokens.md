# Profile verification tokens

A profile verification token (`X-Profile-Token`) proves that the caller entered a
PIN-locked profile's PIN in the current login session. `POST
/api/v2/profiles/{id}/verify-pin` and v1 `POST /profiles/{id}/verify-pin` mint it,
and a remote-playback device sign-in returns one for the approved profile. The
viewer gate (`access.VerifyProfileForRequest`) and the v1 household check
(`handlers.verifyProfileToken`) accept it through one function,
`access.CheckProfileToken`.

## What the token is bound to

The signed claims are `user_id`, `session_id`, `profile_id` and `pin_revision`.
A token is accepted only while all four still match: the request's account and
login session, the profile it names, and that profile's current
`pin_revision` (`user_profiles.pin_revision` in Postgres, `profiles.pin_revision`
in the per-user SQLite store).

`pin_revision` advances in the same statement that sets, changes or clears the
profile's PIN, and on no other write. A database trigger does it whenever
`pin_hash` changes, so every writer advances it exactly once, including code
that does not know the column (an older node, manual SQL); the profile stores
do not bump it themselves. Postgres has `user_profiles_pin_revision`. The
per-user SQLite store, which `cmd/silo` uses instead of Postgres when
`userdb.backend` is `sqlite`, has `profiles_pin_revision`; schema version 30
adds its column, starting at 0, when a user's file is next opened. So:

- Changing or clearing a profile's PIN ends that profile's outstanding tokens,
  and only that profile's.
- Revoking or expiring the login session ends every token minted in it.
- Deleting the profile ends its tokens: the profile no longer resolves.
- Editing the profile's limits, editing another profile (its PIN included), and
  changing the account's role, permissions, access group or quality do not end
  the token. Every request resolves the profile's and the account's limits
  afresh, so the token does not need to carry them; it proves only knowledge of
  the PIN.

The account-wide `users.access_policy_revision` still advances on the same
events as before and still drives scope caches, cursors, theme-song grants and
the events socket's `access_changed` signal. It no longer gates profile tokens.
Before this binding, a household parent whose PIN-locked profile saved a child's
parental controls advanced the account revision and lost its own token on the
next request.

## Upgrade and rolling deploys

Tokens minted before `pin_revision` existed carry only the account's
`policy_revision`. Validation refuses them (a missing `pin_revision` is not read
as 0, which would resurrect a token for a PIN changed before the upgrade), so
each PIN-locked profile enters its PIN once after the upgrade.

New tokens still write `policy_revision`, the account revision at mint time.
Current nodes ignore it. A node from an older release compares it with the
account revision, as it always has. During a rolling deploy:

- An older node accepts a new token until the account revision moves. The
  older profile store advances it on every PIN change it handles, and this
  release's store still does too, so an older node never accepts a proof of a
  PIN that has since changed.
- A new node accepts a new token only while `pin_revision` matches. A PIN
  change handled by an older node writes `pin_hash` without naming
  `pin_revision`, and the trigger advances it anyway, so a new node refuses
  tokens for the replaced PIN.
- A new node refuses an old token, so a client whose PIN entry lands on an older
  node may be asked for the PIN again until the rollout completes.

With the SQLite backend the same rules hold. The per-user file carries an
equivalent `profiles_pin_revision` trigger, so a process from an older release
that still holds a connection it opened before the v30 migration advances the
revision too. An older process cannot open a file that is already migrated: it
refuses a schema version newer than its own.

So a stale PIN proof is refused in every combination; the worst case is an
extra PIN prompt. The `policy_revision` claim can be dropped once no node from
before `pin_revision` can serve a request.
