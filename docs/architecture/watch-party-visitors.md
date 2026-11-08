# Watch Party visitors

A Watch Party host can invite people who have no Silo account. The host creates
an invite link for one room. Someone who opens it enters a display name and
joins that room as a **visitor**. A visitor cannot browse libraries or play
anything but the room's selection, and its streams count against the host's own
limits. The API is in [realtime-api.md](../realtime-api.md#watch-party-visitors).

## Identity

A visitor has no account. Accepting an invite opens a login session of the
**host's** account with purpose `watch_party_visitor` (`auth_sessions.purpose`).
The row binds it to the host's profile (`bound_profile_id`), the room
(`bound_room_id`) and the visitor (`bound_visitor_id`), and it has an absolute
end (`absolute_expires_at`, at most 12 hours) that refresh never extends. A
check constraint refuses a visitor session missing any of them.

The session acts as the host's profile with the role `user`, whatever the
account's role is. Playback therefore runs under the host profile's access
policy: library access, quality and bitrate caps, `max_streams` and
`max_transcodes` all apply unchanged, and a visitor's stream counts as one of
the host account's streams. There is no separate visitor stream category.

Inside the room a visitor is its own member. Its member profile id is
`visitor:<visitor_id>`, so it never shares the host's roster entry, votes,
suggestions or room proof. Its display name comes from `watch_together_visitors`.
Member-state reads skip visitors, which have no watch history or watchlist, and
are not open to them: the read answers for any content id the host can see.

## Liveness

A visitor session is active only while all of these hold, checked on every
request in the same query as the session itself (`SessionRepository.ActiveSession`)
and again on refresh:

- the session is not revoked or expired;
- its `watch_together_visitors` row names it and has no `removed_at`;
- its room is open (`closed_at IS NULL` and the phase is not `ended`).

Closing the room by any path (explicit close, host-disconnect expiry, idle
sweep) or removing the visitor therefore ends the session at its next check,
with no revocation write on those paths. Open room sockets re-validate on their
usual interval and close.

## What a visitor may call

`RequireAuth` narrows a visitor session to a route allowlist
(`internal/api/middleware/watch_party_visitor.go`), as it does for a
remote-playback handoff. Room routes must name the visitor's own room. The list
covers reading the room, its socket ticket, suggestions and votes,
source fallback, the room and playback capabilities, playback start, progress,
replan, stop and route events, stream and transcode media, and sign-out. Host
operations, room creation and joining, the catalog, profiles, account, settings,
admin and every `/api/v1` route are refused. A route added later stays refused
until it is listed; `TestWatchPartyVisitorRoutesAgainstContract` fails when a new
v2 operation matches the list.

Every other credential path refuses visitor sessions: public routes treat them
as anonymous (`AttachSession`), and download links, Apple push display tokens,
plugin access, theme-song grants, the v1 events socket and every v2 socket but
the room socket refuse them. The room socket admits a visitor only for its own
room and only as its visitor member.

A visitor's playback start must name the room's current `selected_file_id`, pin
it (`allow_alternate_versions: false`), and use `progress_persistence: client`,
so nothing is written to the host profile's resume state or history.

Playback ownership is per account and profile, so the playback routes cannot
tell a visitor's sessions from the host's own sessions on that profile. A
visitor that learned the id of such a session could stream, stop or replan it,
or report progress on it. Room snapshots show each member only its own
attached session id, and session ids are random, so a visitor has no way to
learn another one. Binding playback sessions to the login session that started
them would close this; it is not part of this change.

## Invites

`watch_together_visitor_invites` keeps only the SHA-256 digest of each token.
The token appears once, in the creation response. A link lasts 6 hours, can be
used by several visitors, and can be revoked; revoking stops new visitors but
keeps the ones already in. A room has at most 5 usable links and 10 visitors at
once. Unknown, revoked and expired links, and links to a closed room, all look
the same to the public lookup and acceptance routes, which use the `invitation`
rate-limit bucket.

Only the room's host account and profile may create, list or revoke links and
remove visitors, and only the host's own sign-in may create a link: not an API
key, an impersonating administrator or another bound session. Acceptance inserts the visitor and its login session in one
transaction; tokens, a profile token for a PIN-protected host profile, and the
visitor's room proof are minted after it commits.
