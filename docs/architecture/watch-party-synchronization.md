# Watch Party synchronization

Postgres owns the room anchor and its `runtime` JSONB value: connection identities,
leased presence, attached playback sessions, readiness, and the latest room
transport command. Each room operation locks its room row and commits the anchor
and runtime changes together. Commands and snapshots are delivered after commit.
Other rooms use independent locks. Playback ownership and catalog lookups happen
before acquiring the room transaction so they cannot exhaust the connection pool
while transactions wait for another pooled connection.

A transport command has one identity across the room. Seek readiness is tied to
that identity and destination, including an attachment during the seek. A new
selection clears previous attachments and readiness. Readiness considers all
attached, connected members across API servers. A 10-second waiting deadline
excludes stragglers so one client cannot hold everyone indefinitely. It applies
only once at least one member is ready; until then the room keeps waiting,
because resuming without an audience would only skip content. Past the
deadline, the first member to become ready resumes the room. The
shared coordinator checks the persisted command time during reconciliation; it
does not retain process timers that could outlive a remotely replaced barrier.

A connection owns a unique identity. Replacing it fences reports and disconnects
from the old socket. A transient disconnect retains its session for the host's
reconnect grace period; attaching that same session only synchronizes that viewer.
The durable playback-attempt store validates sessions started on another API
server. Legacy playback sessions still use the local session manager.

A failed disconnect immediately drops the closed local socket and queues its
connection identity for reconciliation. The room stays scheduled until that
disconnect commits. A replacement connection has a new identity, so an old retry
cannot remove it. Database failure cannot keep renewing a dead socket's lease.

Presence leases last 45 seconds and are refreshed at most every 15 seconds by the
server holding the socket. An expired lease stops contributing to membership and
readiness. Host disconnect checks use shared presence and the current disconnect
time, so an old server cannot end a room whose host reconnected elsewhere. The
host retains the existing two-minute grace period. Every API also scans the
database every 15 seconds for expired host deadlines, including rooms absent
from its local cache. The scan selects at most 100 rooms and rechecks each under
its room lock before closing it. Shared rooms use the persisted deadline instead
of local host-close timers, so failed reconnects or close transactions retain the
deadline for the next sweep. Losing the last socket-owning API does not require a
disconnect notification for cleanup.

PubSub notifications queue a refresh of the authoritative row. Each active room
coalesces pending notifications so room database waits never block the subscriber.
Every server with local viewers also reconciles rooms every two seconds, repairing missed
notifications and expiring lost connections. Unchanged reconciliation does not
broadcast another snapshot. SQL work has a five-second timeout; a failed mutation
rolls back and sends no command. This coordinates Watch Party state; it does not
restart a media worker or replace the player's transport recovery behavior.

The room generation orders transport changes, not every membership or readiness
update. The web client preserves a socket snapshot when an overlapping HTTP read
or mutation receipt returns the same generation. A higher-generation response
still advances the room. This prevents delayed responses from clearing a session
attachment that the socket has already confirmed.

Each WebSocket has one writer and a queue of 64 frames. A full queue or failed
write closes that socket, allowing the client's existing reconnect flow to
recover. Other viewers' broadcasts do not wait for its write deadline. Each
broadcast builds and sorts the common roster once, then adds each viewer's own
permissions and identity flags.

## Drift correction

Every transport command carries the room's position at its `execute_at`, and
the room anchor is taken at that moment too. While the room plays, its position
at server time `t` is `position_seconds + max(0, t - execute_at)` at 1x.
Commands used to carry the position at issue time while members started one
scheduling lead later, so every member trailed the server's model by that lead.
A slow member raised the lead for everyone, and in a large or long-distance room
the server then corrected members that were in sync, or let the host's reports
re-anchor the whole room.

The lead is the highest smoothed round trip among attached members, from 500
milliseconds up to 2 seconds. A member with a longer round trip runs the command
late and projects it forward to the room's position.

Web, Apple, and Android clients converge on the room the same way:

| Situation | Behavior |
|---|---|
| Clock offset | Ping on connect, every 2 seconds until 4 samples, then every 15 seconds. Use the offset of the fastest of the last 8 round trips. |
| Drift up to 0.4 s | No correction. |
| Drift 0.4–5 s, playing | Pitch-preserved playback rate of `1 ± clamp(drift / 25, 0.01, 0.04)` in 0.5 percent steps, until within 0.1 s. |
| Drift over 5 s, playing | Seek to buffered media, or a budgeted load (see below). |
| Not playing when a play or pause runs | Seek first only for drift over 5 s, or over 1 s when the target is reachable without rebuilding the stream. |

Each client keeps the last command it ran as its room reference and measures
drift against it about twice a second. That loop only changes the rate; it
never seeks. Rate and play state change when a command executes, never when it
arrives, and users cannot change playback speed in a room.

The server corrects a member whose reports stay more than 1 second from the
room, or whose pause state differs, and repeats a correction at most every 10
seconds. A member of a playing room already closing its drift by rate, within
5 seconds and closer than its best report since its last command, is left to
finish; the first report after a command sets that best. A correction refreshes the
client's reference; a client with the drift loop still closes up to 5 seconds
by rate, and an older client without it is kept near the room. A command sent
to a member starts this count over.

Reports can trail the commands they follow. One sent within 1.5 seconds after
a command's `execute_at` (or after the command was sent, if it went out late)
is neither corrected nor taken as the host's decision, unless it names that
command as the last one its client applied, reports the play state the command
set, and the member was also sent the room's latest command. A report naming
an older command than the member was last sent is treated the same way for up
to 10 seconds. A host that has re-attached is corrected like a viewer until a
report names its sync, plays as the sync said, and is within the host band; from
then it may pause or resume the room again even while it closes the rest of its
drift by rate. Naming the sync alone is not enough: a player whose play was
refused (a browser blocking autoplay after a reload) or whose correction never
loaded names it too.

A play or pause request carries where the requester was, and the lead is added
when the requester was playing. Once the room's latest command has run, the
room's state says whether it was. While that command is still pending, the room
already shows its outcome, so the server uses the state the room had before the
pending change, which members keep until they run it. A second play inside the
lead does not start the room a lead later, a second pause still lands a lead
after its press, and a press that undoes the pending command neither moves a
paused room nor leaves a playing one behind. The request's `is_paused` is not
used for this, because clients send either their own state or the state they
ask for.

Every command executes one lead from when it is sent, and never before the
room's own latest command or anchor, so a member synced after the lead drops
never starts ahead of the room.

A seek request within 1 second of the seek the room is already waiting on is
the same seek: it does not restart the barrier. Clients compute a relative
skip from their own position, which stays on the old stream until the room's
seek runs there, so a burst of presses on an older client repeats the pending
target. Current clients hold their newest request on the timeline and build
the next skip on it. The room's seeks for earlier presses move the player but
do not release the request. The room's seek to it releases it, as does a newer
command that replaced that seek before it ran, or a 5 second timeout when the
room never runs it. A seek during an open barrier also keeps whether the room
resumes playing, since the requester is paused by the barrier rather than by
choice.
A paused member that stays where its last correction found it cannot apply
one in place (a transcode lands on segment boundaries) and is not corrected
again until it moves.

## Buffering policy

The room waits for a viewer once, then lets them catch up. One slow connection
must not pause everyone repeatedly, and one short hiccup must not make a viewer
miss the scene.

- Stalls shorter than 2 seconds stay local. Clients report buffering only
  after 2 seconds without playable media, and the viewer converges on the room
  by playback rate. It sends no position reports while its element is
  stalled, or while its media is unplayable and a readiness acknowledgement is
  pending, because a stalled position is not a decision.
- A longer stall pauses the room for at most the 10-second waiting deadline,
  and the overlay names who everyone is waiting for. When a guest stalls, the
  room pauses where it is; only that guest is sent a seek to that point, and
  the barrier waits for it to arrive within 5 seconds (1 second unless its
  `ready` names that seek). Other viewers do not rewind to the guest's
  position. When the host stalls, the room pauses at the host's position, as
  it does for a viewer with nobody else watching, because the host's `ready`
  re-anchors the room wherever its reloaded stream lands.
- A viewer who missed the deadline, or who stalls again within 5 minutes of
  their last stall, catches up on their own: the room keeps playing and their
  stalls no longer pause it. Buffering also cannot pause the room within 1
  minute of the last buffering pause, whoever stalls. A stall from a viewer with
  nobody else watching always pauses the room, and the room waits for them past
  the deadline. A viewer left alone while catching up resumes the room from
  wherever they recover instead of skipping ahead.
- Explicit shared actions stay coordinated: start, seek, resume, and a new
  selection. Viewers who are catching up receive every command but do not hold
  those barriers. A new selection clears every viewer's stall history.
- A late joiner, or a viewer whose playback session is replaced, syncs to the
  room alone. The room keeps playing, and the new stream's startup counts as
  that viewer's stall.
- A viewer who is catching up acknowledges recovery with `ready` once the latest
  command has executed and its media is playable again. The server clears its
  buffering status, keeps its stall history, and sends it the room's current
  position. A member reported as buffering receives no position corrections for
  up to 30 seconds; the acknowledgement ends that hold. A position report that
  matches the room also marks the member ready, which covers late joiners and
  clients that never send `ready`.
- The host follows the same rules. While the host is buffering or catching up,
  and whenever the host has drifted by 10 seconds or less, the host is corrected
  like any viewer instead of moving the room anchor. Every client sends its
  seeks as requests, so passive drift is the host falling behind, not a
  decision. A larger jump or a pause mismatch still moves the room. The frozen
  v1 room socket keeps its 2 second band, since its clients may report a host's
  scrub instead of requesting it. A stalled host's position bounds where the
  room pauses, measured from where the room is when the stall is reported.

Stall times and the last buffering pause live in the room runtime, so every API
server applies the same limits and a reconnect through another server does not
reset them.

Catching up is bounded on every client. Drift up to 5 seconds converges by
playback rate, and a larger correction to media that is already buffered seeks
at once. A correction that has to load new media, by a range request or a stream rebuild,
lands late by its load time; chasing the advancing room with a fresh load on
every correction never converges. Only one such load runs at a time. The next
waits 10 seconds after the previous one started playing, doubling to 60 seconds
until the viewer converges, and aims ahead of the room by the load time the
previous one took, up to 10 seconds. After two sustained stalls within 5
minutes, the web player offers a lower quality for the same source file. Explicit
room seeks are never delayed by this budget.

Pausing cancels pending browser reports, and the server ignores delayed
buffering reports for a paused room. Viewer status identifies who is still
buffering or syncing. The timeline remains at the requested seek position while
the player waits for the replacement stream.

## Deployment and clients

Every viewer uses the room's selected source file. Automatic selection uses the
catalogue's quality ordering within the preferred edition and presentation part:
resolution, then HDR, then file size. Watch Party adds no resolution or dynamic
range ceiling. Existing access policies still apply. Explicit file selections
retain their existing meaning.

Each viewer receives an independent playback plan for that source. A capable
client can play 4K/HDR directly while another viewer receives a transcode or
tone-mapped stream from the same file, subject to the server's transcoding and
tone-mapping settings and available executors. Clients must report their actual
decode and output capabilities; a fixed file does not require identical delivery,
resolution, bitrate, or dynamic range across viewers.

Room selection starts with the best source and responds to actual playback
refusals. A connected viewer can request `source-fallback` with the failed file,
selection revision, and refusal reason. The server selects a lower-ranked file
within the same edition and presentation part. A 4K conversion refusal skips
same-resolution files; an HDR conversion refusal prefers an SDR source without
imposing a resolution ceiling. If no candidate remains, the original refusal
stays visible.

Fallback changes the room's shared source under the same Postgres lock as normal
selection. It preserves the current anchor and resume intent, advances the
selection revision, clears every attachment and readiness report, and broadcasts
the new selection. Viewers restart against that file and join the readiness
barrier. Stale or concurrent reports return the current snapshot without another
change. Candidate order strictly decreases, preventing clients with different
capabilities from cycling between previously refused sources. This works in
host-pick and vote rooms without changing the selected content.

Before mounting the room player, the web client requires both
`watch_party_coordinator_v1` and `fixed_media_file_v1`. Missing support shows an
update message; a failed capability request can be retried without opening the
playback socket or starting a playback session. The web player disables version
switching while in a room and starts with `allow_alternate_versions: false`.
Clients discover coordinated fallback through the playback
capability `watch_party_source_fallback_v1`. That file constraint survives every replan, so decoder recovery
cannot silently move one viewer to another timeline. Streaming quality and audio
or subtitle adaptations can still use the same source.

The initial coordinator upgrade requires a stop/start rollout. Old API servers
cannot safely write alongside the new coordinator. Before enabling the new fleet:

1. Block new Watch Party HTTP requests and WebSocket upgrades at ingress, including
   reconnects. Finish existing parties or notify viewers of the interruption.
2. Stop every old API instance and wait for its process to exit. Removing it from
   load-balancer discovery alone is insufficient: existing WebSockets and
   background timers still have database write access. Keep replacement instances
   stopped until all old processes and their database connections are gone.
3. Apply the runtime-column migration and start the updated API fleet. Verify every
   API instance advertises `watch_party_coordinator_v1` through
   `GET /api/v2/playback/capabilities` before reopening ingress.
4. Reconnect viewers through the updated fleet and verify membership, seek, and
   pause across API instances. Returning to the old coordinator also requires a
   complete stop/start; never mix coordinator generations during rollback.

A single API instance follows the same stop-before-start order. A rolling update
from the old coordinator is unsupported. Later deployments between versions of
this shared coordinator can retain the normal rolling strategy.

Existing clients may omit readiness command IDs, but their seek position must
still reach the destination. Updated clients send the command ID so the server
can also reject superseded acknowledgements.

Clients discover shared membership, command-aware readiness, and member status
through `watch_party_coordinator_v1` in playback capabilities. An absent flag
means these guarantees and status fields are unsupported; an omitted false status
on a supported server means false.

The additive member status fields are optional in v2 socket frames and HTTP v2
snapshots. The web, Apple, and Android clients implement Watch Party with the
drift correction above. Jellyfin compatibility does not use these room sockets.

The Postgres tests run independent service instances against the same room and
exercise readiness, notification loss, connection replacement, expiration, and
rollback. They do not establish deployed playback behavior; that requires testing
with actual viewers and media transports.
