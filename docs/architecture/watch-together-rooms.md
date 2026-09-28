# Watch Together rooms: lobby, staging, and readiness

How a room moves from an empty lobby to synchronized playback, which counters
mean what, and what "ready" means in each phase. The code is the source of
truth; this records the invariants the code keeps.

Paths are repository-relative; assume the repository root is the cwd.

## Room phases

A room row (`watch_together_rooms`) has a `phase` of `lobby`, `playing` or
`ended`. Within the lobby there are two states the row does not name
separately:

| State | Row | Meaning |
|---|---|---|
| Lobby, empty | `phase='lobby'`, `selected_content_id IS NULL` | Nothing chosen. Host-pick rooms wait for the host to stage; vote rooms collect suggestions. |
| Lobby, staged | `phase='lobby'`, `selected_content_id` set | The host has staged an item. Nothing plays. Members may mark themselves ready. |
| Playing | `phase='playing'` | The selection has started. Members attach playback sessions and the buffering barrier runs. |
| Ended | `phase='ended'` | Closed by the host, the host-disconnect timer, or the idle janitor. |

There is no separate `staged` phase or column. Every consumer that must not
act on a merely staged item already keys on `phase`: session attach
(`validateSessionContent` in `internal/watchtogether/service.go`), transport
requests, anchor writes (`UpdateAnchor` is guarded by `phase='playing'` in
SQL), and the web auto-start effects, which check `phase === "playing"`.

Transitions:

- **Stage** (`Service.StageItem`, `PUT .../staged-selection`): lobby → lobby.
  Host only, host-pick only. Sets the selection columns. An identical resolved
  content, file, and library selection is a no-op.
- **Start** (`Service.StartStagedOnce`, `POST .../playback/start`): lobby
  (staged) → playing. Host only. Performs exactly the transition a direct
  selection performs.
- **Select** (`Service.SelectItemOnce`, `PUT .../selection`) and **promote**
  (`PromoteSuggestionOnce`): lobby or playing → playing in one step. These are
  the pre-existing paths; they still work, and "Play in room" from a detail
  page uses select.
- **Stop** (`Service.StopPlaybackOnce`, `POST .../playback/stop`): playing →
  lobby (staged). Host only. Keeps the selection columns so the item that was
  playing is now staged; advances the revision so every attached session is
  dropped. A room that is not playing is a no-op receipt.
- **Finish** (`Service.reconcileRoom`, `internal/watchtogether/item_end.go`):
  playing → lobby (staged). The reconciler performs the host's stop when the
  room's position reaches the last two seconds of the playing file, so every
  client returns to the lobby at the end of an item without calling stop.
- **Switch mode** (`Service.UpdateSelectionMode`, `PATCH .../selection-mode`):
  lobby → lobby. Drops the staged item. Refused while playing.

## Rejoining a playing room

A member who attaches a session to a room that is already playing is sent a
targeted transport command with the room's current position. The stream they
just opened starts at the file's beginning, so until that command lands their
periodic state reports describe the wrong place. For a guest that is harmless:
guest reports only ever trigger corrections toward the room. For the host it
is not: host reports move the anchor, and a report of "position 0" arriving
before the seek would rewind everyone.

`memberState.syncingToRoom` covers that window. It is set on attach when the
sync command is issued and cleared by a state report that already matches the room, or by any transport request from
that member (an explicit action is intent, not a stale position). While it is
set, a host's reports are treated like a guest's: corrected, never
authoritative. The flag is persisted with the shared room runtime.

## Two counters

`generation` is the optimistic-concurrency counter. Every persisted change
advances it (policy, stage, mode switch, start, anchor writes). Room operations
load the authoritative row and runtime under the same database lock; cluster
events prompt other servers to reconcile.

`selection_revision` is the playback epoch. It advances only when playback
starts, restarts with different content, or stops (start, select, promote, stop). It never
advances on stage or mode switch. Transport commands carry it, the web
auto-start effects fire on it, and cross-node adoption uses it to decide
whether members' attached sessions belong to a previous epoch and must be
dropped.

Keeping the two apart is what lets a lobby be edited freely (stage, restage,
switch mode) without any client thinking playback restarted.

## Three kinds of "ready"

| Name | Where | Set by | Cleared by |
|---|---|---|---|
| `memberState.lobbyReady` (`members[].lobby_ready`) | shared room runtime | socket `lobby_ready` message, lobby only | stage with different content, start, stop, mode switch |
| `memberState.isReady` | shared room runtime | socket `ready` message, or a `state_report` with `is_ready`, with an attached session, playing only | any selection change, buffering, disconnect |
| `memberState.ignoreWait` | shared room runtime | the waiting deadline (`waitingResumeDeadline`) skipping a straggler | attach, ready |

Lobby ready is advisory. The server never gates start on it; the web shows
the connected guest count on the start button and offers the same call as
"start anyway". Vote rooms start through promotion without this ready check.
The buffering barrier (`isReady`, `ignoreWait`) is the one that actually holds
playback until members have buffered. Guests must reach a seek destination
within one second; the host is accepted within fifteen and re-anchors the room
to their real position, since a rebuilt stream lands on a keyframe or segment
boundary. The waiting deadline (`waitingResumeDeadline`, 10 s) is a safety net
for a member that never reports, not the normal way out of the barrier.

## What crosses nodes

The room row and runtime commit together under a PostgreSQL row lock. The
runtime includes membership, connection leases, attached playback sessions,
and readiness. Local socket objects remain on the server that owns them.
Cluster events prompt reconciliation; reads and mutations also load the shared
runtime, so correctness does not depend on delivery of an event.

Snapshots, member-state reads, and picker reads use connected members across
API servers. Expired remote leases are excluded. Staging and mode changes clear
lobby readiness in their transaction; adoption preserves readiness already
committed for the current selection.

## Member watch state

The picker leads with what the room has in common: items two or more
connected members are mid-way through, and the union of their watchlists.
Both are computed server-side by `MemberStateReader` in
`internal/watchtogether/member_state.go` from the per-user stores, over
bounded pages (fifty in-progress rows and fifty watchlist rows per member),
and resolved to catalog cards through the caller's access filter, dropping
any item the caller cannot see. Clients never receive a member's lists; they
receive the rows that made the cut plus who each row applies to.

The `member-state` read classifies named content (up to 200 ids) per member
after omitting IDs outside the caller's catalog access. The states are `unseen`,
`in_progress` (with position), and `watched`, with completed history folded in
and series ids classified by their in-progress episodes.
That is all a room may learn about a member's viewing.

## Vote rooms

On v2 the tally is advisory. The host may promote any suggestion; the promotion
is broadcast as the selection, so an override is visible to everyone. Clients
show the leader from the list ordering (`vote_count DESC, created_at ASC`),
which `VoteWinner` still exposes for that purpose. Direct selection remains
refused in vote rooms; the host switches to host picks first.

The frozen v1 promotion route retains its winner-only gate and rejects
promotion before any votes are cast.
