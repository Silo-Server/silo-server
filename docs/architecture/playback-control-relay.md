# Playback control relay

An administrator's pause, resume, stop, message or terminate through the
`/api/v2` admin session routes has to run on the API replica that holds the
session's realtime control lane. The frozen `/api/v1` bridge routes are not
relayed. The receiving
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
  again every 10 seconds while the lane stays open. The refresh sends eight
  claims at a time and applies lane open and close events between batches,
  reducing renewal delays and letting a new lane claim before the sweep ends.
  This does not guarantee renewal within the expiry at every load or Redis
  latency. A claim never replaces one
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
  published on `silo:playback:control`, addressed to the owner. The owner
  reads the key again and checks that it still names this owner and that the
  owner still holds the lane, so a request queued while a newer connection
  took the lane elsewhere is refused even though the old socket is still open.
  It then runs the request through the same code a local request uses and
  publishes the reply addressed back. The recheck is not a fence: ownership
  that moves between the check and the run is not detected. Errors cross the hop as codes
  and come back as the same errors, so the HTTP answer does not depend on which
  replica received the request.
- While the lane stays on one holder and the ownership read succeeds, every
  command for the session runs in that process, so the ledger's applied-once
  ordering holds no matter which replica received each request. A request
  served locally because the ownership read failed or no claim was found (see
  "When the request runs locally") is outside that guarantee: it meets the
  receiving replica's ledger, or no session at all. A lane that moves to
  another replica starts with an empty ledger (see "Limits").

## When the request runs locally

The receiving replica serves the request itself, as a single replica would,
when no other replica ran it:

- the key names this replica;
- no replica claims the lane;
- the Redis ownership read fails;
- the claimed owner declines and a retry at a changed remote owner also
  declines, or no changed remote owner is found.

An explicit decline means the owner did not run the command. The receiving
replica reads ownership again and retries once if a different remote owner
now holds the lane. This covers a player reconnecting between lookup and
delivery. The retry shares the original reply deadline. A failed publish or
missing reply never triggers this retry, since the command may have run.

The ownership reads and writes use a Redis client whose sockets honor the
one-second deadline, so a stalled Redis sends the request down the local path
instead of holding it for the connection's read timeout. Requests and replies
are published through the same client, so a stalled publish ends with the
reply timeout.

## When the owner does not answer

A request that was published and got no reply within 8 seconds may still have
run on the owner, and so may one whose publish failed, because Redis can
deliver a `PUBLISH` whose answer the client never read. The receiving replica
therefore does not simply repeat it. The wait covers one lane write, which has
a 5 second socket write deadline, and the session stop that a terminate runs.
The owner finishes a request even after the caller stops waiting, so a slow
terminate still writes its watch history and stop receipt.

- A command answers `503`. A retry is safe only with the same command id and
  sequence: when it reaches the owner, it gets the recorded receipt back. A new
  identity is a new command. The web admin keeps a command's identity after an
  answer that leaves it unknown (a `5xx` or no response) and reuses it when the
  administrator repeats the same command on the same session; any other answer,
  a different command, or a change of signed-in profile drops it.
- A terminate revokes the session from the receiving replica, as it does for a
  session with no lane. Revocation is idempotent, and it must not wait for a
  dead owner's claim to expire. The revocation runs under its own bounded
  context, so it still completes when the caller gave up during the wait. It
  then answers `503` unless the receiving replica delivered the dismissal on a
  lane of its own: the owner may still be telling the player, or may already
  have ended the session, so neither a receipt saying the player had no lane
  nor a `404` would be true. A retry is safe.

A replica that dies leaves its claim behind until the claim expires or the
player reconnects to a surviving replica, which claims the key with a newer
connection. A replica that shuts down cleanly deletes its claims first.

Without Redis there is no relay, and every request runs locally. A single
replica needs nothing more.

If the initial control-channel subscription fails, commands run locally while
the relay retries. Retries start after one second and double to a maximum
30-second delay, stopping when the server context ends. Once subscribed, the
relay claims existing lanes and tracks new ones without requiring a restart.

## Invariants

- Only the replica that holds the lane and still owns the key when the request
  arrives runs a relayed request. A failed ownership read refuses it. The receiving
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
