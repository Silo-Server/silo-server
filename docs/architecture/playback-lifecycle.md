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

- Native: starts when `startPlannedPlaybackV3` commits the
  transport; ends when its attempt row is stopped. A stopped attempt never
  reconstructs, and reconstruction after a restart announces nothing. A
  reconstruction from a stream token, whose recipe card carries no client, takes
  the client and device from the session's live row, or from its start row once
  no live row is left.
- Jellyfin: one upstream native session is one logical session, and the PlaySessionId
  is not exposed. It starts when a new upstream id wins the attach CAS in
  `ensureUpstreamPlayback`. It ends on a Stopped report or ActiveEncodings teardown,
  on a claimed expiry (the compat play row becomes terminal and no report revives it),
  or on a method switch, where the new upstream id starts as a new session.
- Audiobookshelf (beta): starts at `startNativePlaybackSession`, ends on `/close` or
  expiry.
- Audiobookshelf reported play (beta): a play a client reports only through
  `/session/local` (Plappa and Prologue never call `/play`). No server holds it in
  memory: it is its `abs_playback_sessions` row (`client_session_id` set) and its
  lifecycle row, so it never counts toward stream limits and no media route can load
  it; the public track route refuses its id. A live report (client `updatedAt` within a
  minute) starts it, under a new id, when the client session has no open play;
  `ReportPlay` finds or admits that play under a per-account lock, so every server
  applies reports to the same play and the account's ten-play cap holds cluster-wide.
  A report also records the start again while the play has no lifecycle row. It ends
  only through the sweep, once reports stop; the client session's next report starts a
  new play with its own id and history row.

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
  announced state is therefore always the latest accepted sample's, or a
  confirmation received after it (below), and a lost update is repaired by the next
  sample.
- A pause or resume the player confirms over its control socket moves the row at
  once, before the sync that cues monitors to re-read the list
  (`ConfirmLifecyclePause`). It holds a share lock on the attempt row and applies only
  while the attempt is live and has accepted no progress since the server read the
  attempt on receipt, so progress accepted after that read is never overwritten: that
  sample set its own state. The receipt is ordered by the server, not the player: a
  sample the player sent before it applied the command, but that the server accepts
  after that read, still sets its older state, and the player's next sample sets it
  back. A moved row is synced even when this server's copy already had the state; the
  sync announces `sessions.replaced` when it changes the session's synced row.
- Jellyfin and Audiobookshelf reports carry no sequence. Their pause state
  follows database arrival order and writes only when it changes.
- `stopped` is final.

## Ending a session

Every end goes through the admin history writer with a reason (`stopped`,
`terminated`, `expired`, `aborted`, `replaced`). For native sessions the end first
stops the attempt row and claims its finalization lease, so only one caller runs the
non-idempotent writers (watch history, provider events). The admin history row and the
lifecycle's stop commit in one transaction (`RecordEnd`): the first end sets the reason
and `lifecycle_seq`, later ends record that reason, and only the first is announced,
with the history entry as stored. A teardown the client recovers from (an encoder
exit) is not an end: it records its stretch and announces nothing.

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
- An Audiobookshelf reported play ends 90 seconds after its last report. Closing its
  row is the claim, made only while no report has arrived since the cutoff, so a play
  whose report lands after the sweep looked goes on. The history row takes the row's
  position and the listening its reports credited. A row already closed (an end whose
  history write failed, a sign-out, the stale-row cleaner) ends from what it holds.

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
When the history write of an end the sweep makes from the start row fails, the
session stays live and the failure counts as activity: a session selected by its
idleness is tried again after another grace instead of every 15 seconds, and sessions
that keep failing move behind the others, so they never fill a batch. The row is
recorded once the cause is fixed.
Lifecycle events themselves are at most once.

## Paused timeout

A native session left paused longer than
`playback.paused_session_timeout_minutes` (default 15, `0` off, read live
through a per-server cache that keeps a value for a minute) is ended, so an
abandoned pause stops holding a stream slot and its transcode. A server that
has not read the setting successfully yet ends nothing, rather than apply the
default an administrator may have turned off. The same sweep
runs it on every API server (`playback_paused_timeout.go`). The attempt's
24-hour lifetime bounds it: a value near the 1440-minute maximum may not fire
before the attempt expires, and the existing expiry then ends the session.

- **Eligible.** A native lifecycle row in `paused` since at least the timeout
  ago, by the database's clock. Its live attempt last accepted a paused sample
  and accepted one within `PausedStreamSlotGrace` (2 minutes): the client
  still reports, so the idle expiry above is not the one to end it. Exempt:
  an attempt whose `output.current_sink` is `cast_receiver` (the sender keeps
  a Cast play going without the viewer), a session that is a member of an open
  Watch Together room (`runtime->'members'`, phase `lobby` or `playing`; a runtime
  whose `members` is not an object lists nobody), and an audio-only effective
  file (`MediaFile.IsAudioOnly`, resolved in Go because it depends on the
  probe). The pause starts when a sample or a confirmed pause
  turns the lifecycle row paused; later paused samples, and seeks while paused, do not restart it.
- **Ownership.** A session this server holds, or one no server has synced for
  45 seconds, is this server's to end. One another server synced recently is
  left to that server's sweep, which ends it from its own copy; the candidate
  query leaves it out by passing the ids this server holds.
- **Paging.** Candidates come oldest pause first in pages of 100, at most
  three pages a sweep, continuing on the next sweep from where the last one
  stopped (keyset on the lifecycle row's `updated_at` and session id) and
  starting over once a page comes back short. Rows the sweep reads and leaves,
  an audio-only file or one it cannot read, therefore never keep eligible
  sessions behind them.
- **Claim.** `ExpirePausedAttempt` locks the attempt row (the lock
  `ApplyProgress` takes), requiring a paused last sample, then reads the
  lifecycle row in a second statement, whose snapshot is taken after the lock
  is held, and requires it to have said paused for the whole timeout. Both
  statements check the selection again: the first that the client accepted a
  sample within `PausedStreamSlotGrace` and that the attempt's sink is not
  `cast_receiver`, the second that no open Watch Together room lists the
  session. Only then does it stop the attempt under `pausedTimeoutStopID`.
  A resume that commits first, or while the claim waits on the lock, keeps
  the session live. A single `UPDATE` that joined the lifecycle row would read that row as
  of the statement's start under `READ COMMITTED`, and could end a session
  that resumed and paused again while it waited.
- **End.** The claim's winner denies the session's stream tokens and finishes
  the end like any stopped attempt (`finishInterruptedNativeEnd`): it tears
  down the copy it holds, or records history from the attempt row. The end
  reason is `expired`. The provider event is a pause marked `SessionEnded`, or
  a stop when the play had already completed. When finishing leaves the copy
  held here (another caller holds the finalization lease, or the writers
  failed), the copy and its live row are dropped at once without history, so
  its transcode and slot go now; the sweep's stopped-attempt recovery above
  then finishes the end from the row under the same lease, once.
- **Stale copies.** Another server claims a session only once no server has
  synced it for 45 seconds, so a holder whose sync stalled can still hold a
  paused copy. Each sweep, a server reads the attempts of the native sessions
  it holds in one query and, for each one stopped under `pausedTimeoutStopID`,
  first finishes the end from its copy when nobody has
  (`finishInterruptedNativeEnd`, so the writers run only under the
  finalization lease), then drops the copy and its live row. This runs even
  while the timeout reads zero, since another server may have claimed before
  the setting changed. Copies stopped by other ends keep their own paths.
- **Resume race.** A resume reaches the server with the client's next report.
  The web player reports on its video element's `play` and `pause` events;
  the native clients report every 10 seconds, so a viewer who resumes within
  one report interval of the timeout can still be ended and sees the
  paused-timeout notice, with the position kept.
- **Answers.** Progress, replan and the control-ticket mint answer `410`:
  `playback_session_paused_timeout` when the attempt's durable client features
  list `paused_timeout_v1` (attempt-sticky), `playback_session_ended`
  otherwise, so every client leaves. Media keeps the deny marker's
  `410 playback_session_ended`. No socket frame announces the end.
  [Protocol v3 §6.3](playback-protocol-v3.md) is the client contract.

## Listing

The native session list (`ReadAdminPlaybackSessions`) joins the lifecycle row: `is_paused`
and `lifecycle_seq` come from it together, ended sessions are excluded, and a native
session whose attempt is live but that no server has a live row for is listed from its
start row at the attempt's position. An open Audiobookshelf reported play is listed the
same way, at its row's position. The events socket's `sessions` subscribe snapshot
and `sessions.replaced` frames carry no rows; monitors re-read this list, as
[the realtime API](../realtime-api.md) describes.

`worker.SessionSyncRowJSON` renders a session exactly as a sync stores its row, so a
start event and the start row read through `jsonb_populate_record` match the live list.
`TestSessionSyncRowJSONMatchesTheSyncedRowDB` pins that.
