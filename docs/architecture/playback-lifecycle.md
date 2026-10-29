# Playback session lifecycle

How Silo decides when a playback session starts, pauses, resumes and ends, across API
servers, and what the admin sessions channel and history promise a monitor. The wire
contract is in [realtime-api.md](../realtime-api.md#playback-session-lifecycle-events).
Commands assume the repository root is the cwd.

## One session, one id

A logical session is one native session id: the key of the in-memory
`playback.Session`, the admin live row (`playback_sessions_sync`), the lifecycle row
(`playback_session_lifecycle`) and the admin history row (`admin_playback_history`).
It starts once and ends once; an id never starts again.

- Native (v2 and the v1 bridge): starts when `startPlannedPlaybackV3` commits the
  transport; ends when its attempt row is stopped. A stopped attempt never
  reconstructs, and reconstruction after a restart announces nothing.
- Jellyfin: one upstream native session is one logical session, and the PlaySessionId
  is not exposed. It starts when a new upstream id wins the attach CAS in
  `ensureUpstreamPlayback`. It ends on a Stopped report or ActiveEncodings teardown,
  on a claimed expiry (the compat play row becomes terminal and no report revives it),
  or on a method switch, where the new upstream id starts as a new session.
- Audiobookshelf (beta): starts at `startNativePlaybackSession`, ends on `/close` or
  expiry.

## State and order

`playback_session_lifecycle` holds each session's announced state and the number of
its last transition, taken from one global sequence in the statement that makes the
transition.

- A start inserts the row and announces only if none existed. Its state is the
  attempt's latest accepted pause state, read under a share lock on the attempt row,
  so progress accepted before the row existed is not lost. An end that finds no row
  inserts a stopped tombstone and announces nothing, so a start that arrives later stays
  silent. Tombstones are kept 25 hours, longer than an attempt lives.
- Native v2 pause and resume commit in `ApplyProgress`'s transaction, after the
  attempt's compare-and-set. The attempt row lock orders concurrent samples, every
  applied sample sets the state to its own, and a stale sample is never applied. The
  announced state is therefore always the latest accepted sample's, and a lost update
  is repaired by the next sample. The control socket's confirmed pause does not touch
  it; the player's next sample carries the pause.
- Jellyfin, Audiobookshelf and v1 bridge reports carry no sequence. Their pause state
  follows database arrival order and writes only when it changes.
- `stopped` is final.

## Ending a session

Every end goes through the admin history writer with a reason (`stopped`,
`terminated`, `expired`, `aborted`, `replaced`). For native sessions the end first
stops the attempt row and claims its finalization lease, so only one caller runs the
non-idempotent writers (watch history, provider events). The admin history row and the
lifecycle's stop commit in one transaction (`RecordEnd`): the first end sets the reason
and `lifecycle_seq`, later ends record that reason, and only the first is announced,
with the history entry as stored. A teardown the client recovers from (encoder exit,
copy-safety) is not an end: it records its stretch and announces nothing.

## Sessions no server holds

A purge of a live row is not an end: the client may continue on another server.
`StartLifecycleSweep` runs every 15 seconds on each API server. It selects only rows it
can act on now: live lifecycle rows no server has synced for 45 seconds, whose attempt
was stopped or whose last activity is past their grace, oldest activity first, so
sessions that are still active never fill its batch. For each one this server does not
hold:

- A native session with a live attempt ends once its client and its media have been
  idle for 5 minutes while playing or 30 minutes while paused. Node delivery counts as
  activity and is remembered on the lifecycle row. The claim stops the attempt only
  while no progress has landed since the cutoff (`ExpireIdleAttempt`, under the row
  lock `ApplyProgress` takes), and only a session it ended loses its stream tokens.
- A native session whose attempt is stopped but unfinalized is finalized once its
  lease is free; one whose attempt is finalized or gone is ended from the lifecycle row.
- A Jellyfin session ends through the shared compatibility expiry claim
  (`ClaimNativeSessionExpiry`), which re-checks the play row's activity under its lock.
- An Audiobookshelf session ends after 24 hours without a holder.

A Jellyfin copy that saw no progress records no end, because it cannot tell whether
another server holds the copy that took the play's reports; a Jellyfin play that ends
before reporting any progress is therefore ended by the sweep, as `expired`.

An end the sweep makes records history from the row the session stored when it
started, at the last position the attempt knows; a play whose media file left the
catalog is recorded without the item and duration only the file gave.

**Guarantee.** Every session announced by `session.started` (native or Jellyfin) gets
an admin history row with an `end_reason` and `lifecycle_seq` within its idle grace
plus one sweep interval after its last activity, while at least one API server runs.
Each sweep ends at most 100 sessions per server, oldest activity first, so a larger
backlog (a server dying with many sessions) drains over the following sweeps.
Lifecycle events themselves are at most once.

## Listing

The native session list (`ReadAdminPlaybackSessions`) joins the lifecycle row: `is_paused`
and `lifecycle_seq` come from it together, ended sessions are excluded, and a native
session whose attempt is live but that no server has a live row for is listed from its
start row at the attempt's position. The frozen v1 snapshot keeps its own query.

`worker.SessionSyncRowJSON` renders a session exactly as a sync stores its row, so a
start event and the start row read through `jsonb_populate_record` match the live list.
`TestSessionSyncRowJSONMatchesTheSyncedRowDB` pins that.
