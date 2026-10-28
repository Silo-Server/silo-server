# Playback control relay

An administrator's pause, resume, stop, message or terminate has to run on the
API replica that holds the session's realtime control lane. The receiving
replica runs it itself only when the relay cannot hand it to another replica
(see "When the request runs locally") or, for a terminate, when the owner did
not answer (see "When the owner does not answer"). The lane holder holds
the lane itself, the command tracker that sees the player's result and runs the
deadline fallback, and the ledger that applies each sequenced command once. A
load balancer can send the administrator's request to any replica.
`playback.ControlRelay` (`internal/playback/control_relay.go`) moves the request
to the lane holder. The administrator command code in
`internal/api/handlers/admin_playback_relay.go` uses it.

## How a request moves

- Lane ownership is one Redis key per session, `silo:playback:lane:<session_id>`,
  whose value is the time the lane's connection took the lane (Unix
  microseconds) and the holding process's relay id. A replica claims the key
  when a lane opens or a new connection takes over the lane, and claims it
  again every 10 seconds while the lane stays open. A claim never replaces one
  for a newer connection (equal times go to the greater relay id), so the
  newest connection wins whatever order the replicas write in, and a dropped
  lane notification is repaired by the next refresh. When the lane closes, the
  replica deletes the key only if the key still names it. The key expires after
  30 seconds. Connection times come from each replica's clock, so ordering two
  connections relies on replica clocks agreeing more closely than the gap
  between a player's reconnects.
- Every request reads the key, including one on a replica that also holds a
  lane: a player that reconnected elsewhere can leave an old lane open here
  until its socket closes. When another replica owns the key, the request is
  published on `silo:playback:control`, addressed to the owner. The owner checks that it
  still holds the lane, runs the request through the same code a local request
  uses, and publishes the reply addressed back. Errors cross the hop as codes
  and come back as the same errors, so the HTTP answer does not depend on which
  replica received the request.
- While the lane stays on one holder, every command for the session runs in
  that process, so the ledger's applied-once ordering holds no matter which
  replica received each request. A lane that moves to another replica starts
  with an empty ledger (see "Limits").

## When the request runs locally

The receiving replica serves the request itself, as a single replica would,
when no other replica ran it:

- the key names this replica;
- no replica claims the lane;
- the Redis ownership read fails;
- the claimed owner answers that it no longer holds the lane.

The ownership reads and writes use a Redis client whose sockets honor the
one-second deadline, so a stalled Redis sends the request down the local path
instead of holding it for the connection's read timeout. Requests and replies
are published through the same client, so a stalled publish ends with the
reply timeout.

## When the owner does not answer

A request that was published and got no reply within 8 seconds may still have
run on the owner, and so may one whose publish failed, because Redis can
deliver a `PUBLISH` whose answer the client never read, so the receiving replica does not simply repeat it. The wait
covers one lane write, which has a 5 second socket write deadline, and the
session stop that a terminate runs.

- A command answers `503`. A retry is safe only with the same command id and
  sequence: when it reaches the owner, it gets the recorded receipt back. A new
  identity is a new command. The web admin allocates a new identity for each
  click, so a second click after a `503` can apply the command twice.
- A terminate revokes the session from the receiving replica, as it does for a
  session with no lane. Revocation is idempotent, and it must not wait for a
  dead owner's claim to expire. The revocation runs under its own bounded
  context, so it still completes when the caller gave up during the wait. If
  the session is already gone, which is what an owner that ran the terminate
  leaves behind, it answers `503` rather than `404`.

A replica that dies leaves its claim behind until the claim expires or the
player reconnects to a surviving replica, which claims the key with a newer
connection. A replica that shuts down cleanly deletes its claims first.

Without Redis there is no relay, and every request runs locally. A single
replica needs nothing more.

## Invariants

- Only the replica that holds the lane runs a relayed request. The receiving
  replica falls back to the local path only when the owner never ran the
  request, apart from the terminate revocation described above. A claim that
  outlived its lane gets an immediate "not handled" reply, so the caller does
  not wait out the timeout.
- A relayed request is never relayed again. The owner calls the local command
  path directly.
- Relay traffic stays off `silo:playback`. Plugin event consumers and
  admin-stats cache invalidation subscribe to that channel, and must not see
  administrator commands.
- Mixed builds during a rolling upgrade keep the single-replica behavior. Older
  replicas neither claim lanes nor forward requests, so a request that touches
  an older replica runs locally.

## Limits

- The ledger stays in the lane holder's memory. If the player moves its lane to
  another replica, the new holder starts with an empty ledger. A delayed command
  with an old sequence can then be applied once more.
- Requests are routed by lane only. A session with no lane on any replica
  (a Jellyfin-protocol session, or a native player that is reconnecting)
  is still handled only by the replica that receives the request. If another
  replica holds that session in memory, stop answers `404`. Terminate still
  revokes the session durably through the stopped attempt row and the stream
  deny marker.
