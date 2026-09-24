# Realtime API

`GET /api/v2/events/capabilities` describes the shared event subscription
protocol. It requires an authenticated account; no active profile is required.
When the event service is unavailable, the capability response remains `200` with `state: not_configured` and `allowed: false`.

The response contains `schema_version`, `subscribe_frame`, `declared_channels`,
`subscribe_grace_period_seconds`, `max_requested_channels`, and `channels`.
These values come from the same implementation as the bridge capability route
and the event socket's enforced subscription limits. `channels` lists client
channels independently of the caller's role. The socket hello frame determines
which channels that connection may actually subscribe to. Internal plugin
channels are excluded.

This read creates no ticket or connection and does not grant access to a
channel. It describes the subscription protocol, not a socket authentication
credential or a guarantee that a particular API-version socket is available.
Socket authorization and ticket issuance are separate contracts. The bridge
capability response remains unchanged. Current first-party web, Apple, and
Android callers do not consume this capability read; Jellyfin has no matching
native realtime discovery operation.

## Session-bound socket handshake

`POST /api/v2/events/ws-ticket` delegates a current access-token login session
for one connection. API keys and credentials without a bounded access-token
expiry cannot mint this proof. A profile is optional; when present, its ownership
and PIN proof must validate. The ticket is opaque, expires within 30 seconds,
and binds the account, login session, account role, profile proof and resolved
access policy. The response includes `ticket`, `expires_in`,
`max_connection_seconds` (300), and `protocol` (`silo.events.v2`). It is not cached.
Minting is naturally idempotent in effect: extra credentials are harmless
orphans that expire, so shared session refresh may retry the mint. A consumed
credential is never reused for reconnects.

Connect to `GET /api/v2/events/ws` with exactly these offered subprotocols,
in order: `silo.events.v2`, `silo.ticket.<ticket>`. The server selects only
`silo.events.v2`, never the credential-bearing entry. Neither bearer tokens nor
tickets belong in the URL. Only the `channels` query selection is needed for
clients using declared subscriptions. Request bodies are refused. An Origin,
when present, must equal the configured public origin, the request's own scheme
and host, or a connected network access overlay origin (see
[native-raw-handshakes.md](architecture/native-raw-handshakes.md#websocket-origin-behind-a-reverse-proxy)).
Forwarded host headers grant no Origin exception. Native clients may omit Origin but need the same session proof.
Malformed upgrades and rejected origins do not consume valid tickets.

Consumption is atomic through Redis `GETDEL`. A Redis failure fails closed.
Without Redis, tickets are process-local and require affinity to the minting
node; restarting that node invalidates them. This store is distinct from the
bridge's user/profile-only ticket, which cannot authenticate a v2 connection.

Admission rechecks the current session, enabled account, account role and viewer
policy/PIN proof. Secondary profiles do not receive administrator channels.
Connections end at access-token expiry or after five minutes, whichever comes
first. Current session/account/profile policy is rechecked every 15 seconds;
a failed check closes the connection, with a two-second bound on authority
lookups. Clients must reconnect with a newly minted credential. This is bounded
revocation detection, not an instantaneous revocation guarantee.

The selected message protocol retains the existing event frames (`hello`,
`subscribe`, `subscribed`, `snapshot`, `event`, `error`) and per-channel payloads.
The shared event implementation still applies channel eligibility, the subscribe
grace period, inbound frame limits, snapshots, ping/pong and delivery filtering.
The handshake version does not rewrite another domain's event payload.
Web consumers capture account/profile authority before minting and discard
connection results and frames after that authority changes.

Native socket/ticket adoption and independent domain review are required before
these two migration rows can be ratified. No bridge socket or ticket was removed.

### Owner-bound playback control handshake (v2)

`POST /api/v2/playback/sessions/{session_id}/control/ws-ticket`
(`createPlaybackControlSocketTicket`) delegates the caller's current access-token
login session and verified profile proof to one control handshake for one
playback session. The body's `installation_id` is optional: send the value
`getPlaybackCapabilities` returned, and it must equal this server's installation;
omit it for a session the bridge started. Minting checks, in order: a bounded
login session; the current session, enabled account, role and viewer/PIN proof;
that the playback session exists in the session manager and belongs to the
caller's account and profile (`403 permission_denied` otherwise); and that a
presented `installation_id` matches the server's (`409 conflict`). A lane
already held by a different account, profile or installation is also `409`. The
response carries `ticket`, `expires_in` (at most 30 seconds),
`max_connection_seconds` (14400) and `protocol` (`silo.playback-control.v2`).
It is not cached. Minting is naturally idempotent in effect: extra credentials
expire unused.

Connect to `GET /api/v2/playback/sessions/{session_id}/control/ws`
(`connectPlaybackControlSocket`) offering exactly `silo.playback-control.v2`
then `silo.ticket.<ticket>`; the server selects only the protocol. Neither
bearer tokens nor tickets belong in the URL, request bodies are refused, and an
Origin, when present, must pass the same check as the events socket. Malformed
upgrades and rejected origins do not consume the credential. At upgrade the
credential is consumed atomically (Redis `GETDEL`; process-local without
Redis), login authority is re-validated, and ownership and installation are
re-admitted against the credential's captured binding: a binding that moved
since minting is `409` and the credential is spent. A credential presented for
another session is `403`; an ended session is `404`.

The connection is the session's single realtime lane. A reconnect resumes the
lane only for the same account, profile and installation: it takes the lane
over and the superseded connection is closed, so its later ack and result
frames are never routed. Ack and result frames are applied only while the
receiving registration still owns the lane, through the existing command
tracker and stop-completion paths. Login authority and session ownership are
re-checked every 15 seconds and the connection ends when either is lost, at
access-token expiry, or after four hours. The frames (`hello`, command, `ack`,
`result`, event) are unchanged from the bridge socket.

`GET /api/v2/playback/sessions/control/capabilities` reports `available`, plus
`protocol` only when this server serves the handshake. The web player mints
under captured profile authority for every connection, offers the protocol pair,
discards frames once that authority changes, and uses the bridge socket only
when the handshake is not served (`404`/`503`), never after an ownership
refusal. The bridge socket route is
unchanged.


### V2 suggestion reads and vote membership

`GET /api/v2/watch-together/rooms/{room_id}/suggestions` requires login/profile
credentials and the matching signed room access token in `X-Room-Token`. The room
proof binds the room, account and profile; it does not replace login authority.
The response is `{items, page}` with string identifiers and UTC timestamps.
`limit` and opaque `cursor` bound a live traversal ordered by creation time then
suggestion ID. Cursor scope includes account, profile, access policy, room and
page size. Vote changes do not move suggestions across the cursor. Concurrent
creation/deletion is not a snapshot; clients refresh to reconcile live changes.
Room lifecycle and the room websocket still use the bridge contract.

HTTP suggestion reads return the requesting profile's authoritative
`voted_by_me`. Both v1 and v2 socket `suggestions_update` frames carry common room
rows and tallies, with every `voted_by_me` set to `false`. That socket field is
non-authoritative: clients retain their own vote membership or refresh it through
the authenticated HTTP read. Local and cross-node broadcasts use the same shape;
each receiving API node with viewers reads the common list once, outside the
room mutex, and sends it to its current connections.

`POST` and `DELETE` on
`/api/v2/watch-together/rooms/{room_id}/suggestions/{suggestion_id}/vote` use the same
authority and return bodyless `204` for the requested vote membership, including
an already satisfied state. Repository membership and tally changes remain one
transaction. Existing no-op handling precedes list reads and broadcasts; actual
changes retain the existing room broadcast path. Opposing votes have no generation
ordering. A closed room returns `409`; a missing room or suggestion returns `404`.
An error after a database commit does not prove that the vote was unchanged.

The web adapter drains bounded pages under one captured authority and sorts the
completed list by votes for display. After a vote receipt it reloads under that
same authority; it does not replay or retarget a mutation after authentication or
profile changes. Suggestion creation, deletion, promotion, room policy and room
socket migration are separate operations. Native consumer closure remains
required before ratifying these mappings.

`DELETE /api/v2/watch-together/rooms/{room_id}/suggestions/{suggestion_id}`
requires the same login/profile and `X-Room-Token` proof. Only the room host or
original suggester, matched by both account and profile, may delete the entry.
Success returns bodyless `204`. Missing suggestions, including repeated deletion,
return `404` before list reads or broadcasts. Current creation never reuses IDs,
so repeating deletion cannot address a replacement entry. The existing domain
service retains its deletion and broadcast path. A failure after deletion does
not establish that the entry still exists.

The existing web delete action sends once, surfaces errors including `404`, and
reloads the bounded list only after success under the original captured authority.
It does not replay after a lost response or authentication error. Suggestion
creation and promotion remain separate bridge operations.

### End a Watch Together room

`DELETE /api/v2/watch-together/rooms/{room_id}` (`closeWatchTogetherRoom`) requires authenticated profile authority and the demo guard. The existing service checks both the host account and host profile. A guest room token does not authorize closing; this operation does not require room proof in addition to host identity.

Success returns bodyless `204` after the existing room-close service completes. Non-host authority returns `403`, a missing room `404`, an already-ended room `409`, and unavailable service `503`. Natural-idempotent classification describes convergence on ended state, not a promise that every repeated request returns `204`. The actual web action sends once without authentication replay and fences original authority before dispatch, after receipt and before completion feedback. It does not optimistically mark the room ended or automatically retry an uncertain close.

The owning service retains persistence, host/wait timer cleanup, local connected-member `room_closed` dispatch with the existing `host_left` reason, and live-room removal. This port changes no domain persistence or callback behavior. It does not cancel already-dispatched playback, establish cross-node socket broadcast, or complete the separate v2 room-socket contract. Existing room creation, joining and room credentials remain separate migration scopes; v1 wire behavior is unchanged.

### Read a Watch Together room

`GET /api/v2/watch-together/rooms/{room_id}` (`getWatchTogetherRoom`) requires authenticated profile authority and existing room proof in `X-Room-Token`. It fails closed when the room/token service is unavailable. Proof must match the exact room, account and profile. Invalid proof returns `403`, missing room `404`, closed room `409`, and unavailable service `503`.

A successful no-store `200` returns `room` and renewed `room_access_token`. The snapshot preserves selection, playback anchor, host/member roles and permission fields. IDs are strings on v2; the anchor time is a typed UTC instant. Renewal retains the existing room/account/profile token semantics. This token is not a session-bound socket credential, does not grant account authentication, and does not complete the separate v2 room-socket contract.

The existing web initial-room read sends proof only in the header, captures authority before dispatch, rejects stale success and failure, validates identities before converting to the existing UI model, and fences publication against room-effect cancellation and authority replacement. Closed-room conflict is terminal. Existing room-proof storage and socket transport remain separate; the read does not activate a new native surface or migrate the room socket. Domain snapshot and v1 wire behavior are unchanged.

### Set guest transport policy

`PATCH /api/v2/watch-together/rooms/{room_id}/policy` (`updateWatchTogetherRoomPolicy`) requires authenticated profile authority, the demo guard and both the host account and profile. Its body contains `guest_control_policy`: `host_only` or `guest_play_pause`. This host action does not require guest room proof. Invalid policy returns `422`, non-host authority `403`, missing room `404`, closed room `409`, and unavailable service/token configuration `503`.

Success returns a no-store `200` room snapshot and renewed existing room proof. The owning service retains generation-based persistence and local snapshot broadcasts. If a competing writer wins the generation check, it can return the refreshed winning snapshot without retrying or broadcasting the failed write. Clients must use that returned policy rather than assuming the requested value was stored. Natural-idempotent classification describes the policy value; it does not promise an unchanged generation or suppress successful-write broadcasts.

The existing web toggle captures policy and authority, sends once without authentication replay, and fences authority, replaced room and superseded run before publication or feedback. It retains a newer currently held room generation when a policy response is older. Stale room/run failures do not report into the replacement context. It does not automatically read, rebase or retry a conflict/uncertain result. Existing proof renewal is not a session-bound socket credential. V1 service behavior, guest transport enforcement and socket protocol remain unchanged; no cross-node broadcast or native activation is implied.

### Resolve a room invitation

`POST /api/v2/watch-together/join` (`joinWatchTogetherRoom`) requires authenticated account/profile authority and the demo guard. Send `code` or `join_token` in the JSON body. Values are trimmed; a nonempty invite token takes precedence when both are supplied. Missing input returns `422`, missing room `404`, closed room `409`, and unavailable room/token service `503` before proof issuance.

Success returns a no-store `200` with the existing room snapshot and room/account/profile-bound `room_access_token`. HTTP joining resolves the invitation and issues proof; it does not connect a member, attach playback, or cancel the host-disconnect timer. Those effects belong to the separate socket lifecycle. Repeated resolution can issue a different proof and observe a newer room snapshot; natural-idempotent classification does not promise a stable credential or durable admission receipt. Existing v1 behavior is unchanged.

The actual web code-entry and invite auto-join/retry actions copy input and capture authority synchronously, send once without authentication replay, and suppress navigation and errors after authority replacement, unmount, superseding request or invite replacement. The existing room route still receives room proof for the legacy socket flow. This operation does not make that proof session-bound, migrate the socket, or activate dormant native UI. Room creation remains a separate operation.

### Select room content as the host

`PUT /api/v2/watch-together/rooms/{room_id}/selection` (`selectWatchTogetherRoomItem`) requires authenticated profile authority, the demo guard and both the host account and profile. The body requires `content_id`; optional `file_id` and `library_id` are positive string IDs. Host selection does not require guest room proof. The existing resolver retains playable-content and access checks. Vote rooms reject direct selection with `409`; non-host authority returns `403`, missing room `404`, invalid selection `422`, closed room `409`, and unavailable service/token configuration `503`.

A changed selection retains the existing service behavior: reset playback anchor to zero and paused, enter waiting with resume-on-ready, advance selection revision and generation, clear prior member playback-session/readiness/buffering/ignore-wait state, and broadcast the room snapshot across connected API servers. The v2 writer locks the authoritative database row and compares resolved content, file and library identity before any reset. An identical current selection preserves anchor, revision, generation, member readiness, waiting timer and broadcasts. That no-op applies only while the room is playing: selecting the item a lobby has merely staged starts it, because staging is not playback. This is a current-state no-op, not a historical replay receipt: after a different intervening selection, the old request can select its content again. The operation remains non-retryable. A competing generation writer can cause the service to return its refreshed winning snapshot without replaying the failed selection. Clients must use the returned snapshot.

The actual web action captures input and authority, sends once without authentication replay, and fences replaced authority, room and request run before publication. A newer held generation is retained only for the same room. Stale receipts do not clear the candidate or display completion feedback. There is no automatic read, rebase or retry after an uncertain result. Success returns the existing room snapshot and renewed room/account/profile proof; that proof is not a session-bound socket credential. The v1 selection writer retains its frozen reset behavior; all nodes serving this v2 operation must use the guarded writer. A no-op refresh from another node adopts a newer selection revision locally and discards readiness belonging to the prior selection. Room coordination across API servers is described in [Watch Party synchronization](architecture/watch-party-synchronization.md). Native Watch Party UI remains inactive.

### Stage room content without starting it

`PUT /api/v2/watch-together/rooms/{room_id}/staged-selection` (`stageWatchTogetherRoomItem`) requires authenticated profile authority, the demo guard and both the host account and profile. The body requires `content_id`; optional `file_id` and `library_id` are positive string IDs, resolved by the same playable-content and access checks as a direct selection. Vote rooms refuse staging with `409` (suggest instead); a room that is already playing refuses with `409`; non-host authority returns `403`, missing room `404`, invalid selection `422`, closed room `409`, and unavailable service/token configuration `503`.

A staged item is a lobby room whose `selected_content_id` is set. A changed selection keeps the room in the lobby, advances the room generation, refreshes the idle timestamp so the janitor leaves an active lobby alone, and broadcasts the snapshot across API servers. It never advances the selection revision, because that revision is the playback epoch and nothing has started. Staging different content clears every member's lobby ready state; re-staging the same content keeps it. The row update is guarded in SQL on generation, lobby phase and host-pick mode, so a stale node cannot stage into a room another node has started or switched. The operation is naturally idempotent: repeating the same resolved content, file, and library preserves the generation, idle timestamp, readiness, and broadcasts.

The web sends once under captured authority and uses the returned snapshot. A member cannot attach a playback session to a staged lobby; attach, transport and buffering readiness remain playing-only. No v1 route stages: v1 selection always starts playback.

### Start the staged content

`POST /api/v2/watch-together/rooms/{room_id}/playback/start` (`startWatchTogetherRoomPlayback`) requires authenticated profile authority, the demo guard and both the host account and profile. It has no body: it starts whatever is staged. Nothing staged returns `409`; non-host authority `403`, missing room `404`, closed room `409`, and unavailable service `503`.

Start locks the authoritative row and performs the selection transition a direct selection performs: playing phase, waiting playback state, resume-on-ready, anchor reset to zero and paused, selection revision and generation advanced. Every member's attached session, buffering readiness, ignore-wait and lobby ready state is dropped as belonging to the previous epoch, and the waiting deadline is disarmed. A room that is already playing answers with its current snapshot unchanged, so a duplicate press cannot restart playback; a generation mismatch answers the same way. The operation is non-retryable: an uncertain result is read back, not replayed.

The lobby ready check is advisory. The server never gates start on it; the web counts connected guests and offers the same call as "start anyway" when some guests are not ready. Vote rooms start through suggestion promotion and do not show a lobby ready check.

### Stop playback without ending the room

`POST /api/v2/watch-together/rooms/{room_id}/playback/stop` (`stopWatchTogetherRoomPlayback`) requires authenticated profile authority, the demo guard and both the host account and profile. It has no body. Non-host authority returns `403`, missing room `404`, closed room `409`, and unavailable service `503`.

Stop locks the authoritative row and returns a playing room to the lobby: lobby phase, idle playback state, resume-on-ready cleared, anchor reset to zero and paused, selection revision and generation advanced. The selection columns are left alone, so the item that was playing is the lobby's staged item and the host can start it again or stage something else. Advancing the revision is what ends the playback epoch: every member's attached session, buffering readiness, ignore-wait and lobby ready state is dropped and the waiting deadline is disarmed, exactly as a start does. Members in the player see the room leave the playing phase and return to the room page. A room that is not playing answers with its current snapshot unchanged, so the call is naturally idempotent and a duplicate press cannot disturb the lobby it produced.

Ending the room (`DELETE .../rooms/{room_id}`) remains the way to dismiss everyone. Frozen v1 has no stop: v1 rooms either play or end.

### Switch a lobby's selection mode

`PATCH /api/v2/watch-together/rooms/{room_id}/selection-mode` (`updateWatchTogetherRoomSelectionMode`) requires authenticated profile authority, the demo guard and both the host account and profile. The body contains `selection_mode`: `host_pick` or `vote`. A playing room refuses with `409`; non-host authority returns `403`, missing room `404`, an invalid mode `422`, closed room `409`, and unavailable service `503`.

Switching drops the staged item and every member's lobby ready state, advances the generation, refreshes the idle timestamp and broadcasts the local snapshot. Suggestions and votes are rows that outlive the switch: a room switched from voting to host picks can still promote any of them, and a room switched to voting starts with whatever was already suggested. Repeating the current mode is a no-op with no persistence or broadcast. Frozen v1 fixes the mode at creation and has no switch.

### Lobby ready over the room socket

A connected member sends `{"type":"lobby_ready","ready":true|false}` on the v2 room socket. The server commits the flag in the shared runtime, broadcasts the snapshot, and answers `error bad_request` when the room is not in the lobby. V1 sockets reject this message, and v1 HTTP and socket snapshots retain the frozen six-field member representation. Every member listed in v2 `members[]` carries `lobby_ready`; it is always `false` once the room is playing. The flag is distinct from the buffering `ready` message, which still requires an attached playback session.

Lobby ready is persisted in the shared room runtime and included in snapshots across API servers. Staging different content, starting or stopping playback, and switching modes clear it in the same transaction as the room update. A stale node adopting that transaction preserves any ready flag subsequently saved for the current selection.

### Read member watch state for named content

`POST /api/v2/watch-together/rooms/{room_id}/member-state` (`queryWatchTogetherMemberState`) requires authenticated profile authority and room proof in `X-Room-Token` for the exact room, account and profile. The body carries `content_ids` (1 to 200). It is a POST-shaped read: the id set exceeds what a query string carries and the operation changes nothing, so it is classified naturally idempotent. Invalid proof returns `403`, missing room `404`, closed room `409`, an empty, blank or oversized id list `422`, and unavailable service `503`. The response is `no-store`.

The response lists `members` (connected across API servers, host first) and one `items` entry per distinct accessible requested id, in request order, with each member's `state` (`unseen`, `in_progress` with `position_seconds` and `duration_seconds`, or `watched`) and `on_watchlist`. Completed watch history is folded into `watched`. A series id is `in_progress` for a member when one of their in-progress episodes belongs to it. The caller's catalog access filter applies to movie, series, and episode IDs before any member state is read; inaccessible and missing IDs are omitted.

### Read the room picker rows

`GET /api/v2/watch-together/rooms/{room_id}/picker` (`getWatchTogetherRoomPicker`) requires the same authority and room proof. It returns `members`, `continue_together` (items two or more connected members are mid-way through, most shared first, then most recent) and `watchlist_union` (items on any connected member's watchlist, most shared first). Episodes collapse to their series, and a series row carries `next_up`: the episode most members would play next (their own in-progress episode, else their next unwatched one) with the count of members it applies to. Each row names the members it applies to and, for continue rows, their positions.

Rows are resolved to catalog cards through the caller's access filter; a row whose item the caller cannot see is dropped, so another member's viewing never names an item the caller cannot browse to. Resolution uses the batch card path: one base-row query, one access check, one localization lookup and one artwork signing for the whole page. It never takes the item-page build, whose per-item file, extra, folder-path and credit reads and separate image resolves made the picker cost seconds per row. Per-member reads are bounded pages (fifty in-progress rows, fifty watchlist rows); the rows are capped at twenty and thirty. The response is `no-store`. Invalid proof returns `403`, missing room `404`, closed room `409`, and unavailable service or catalog access `503`.

### Watch-together capabilities

`GET /api/v2/watch-together/capabilities` (`getWatchTogetherCapabilities`) requires an authenticated account and follows the shared capability document conventions (opaque `revision`, `state`, `allowed`, private revalidation with `ETag`). When rooms are served, each feature flag reflects its wired operation dependencies. `max_member_state_ids` is 200 when member-state is available and zero otherwise. `lobby_ready`, `connection_replaced` and `socket_protocol` (`silo.room.v2`) require the v2 socket; the protocol is an empty string when the socket is unavailable. When the room service is not wired the state is `not_configured` and every flag is `false`. Clients decide whether to show the staged lobby, the ready check, the mode switch and the shared picker from this document, not from the server version.

### Suggestion creation receipt storage

The v2 suggestion creation prerequisite stores a caller-selected suggestion identity with the original account, profile, room and a SHA-256 digest of the canonical content metadata. The receipt and suggestion insert commit in one transaction. The room row is locked before admission, so committed closure refuses creation. An identical retry returns the existing suggestion; a different owner, room or payload conflicts. The created flag distinguishes insertion from replay so the later caller can avoid repeat broadcasts.

Receipts survive suggestion and room deletion. Repeating a deleted identity conflicts instead of recreating it. Receipts retain no title, note or poster URL payload and are removed on login-account deletion. There is no age-based receipt expiry; expiring them would permit identity reuse. Legacy suggestion insertion/deletion and their wire behavior are unchanged. The receipt store does not dispatch broadcasts or provide durable broadcast delivery; the caller behavior is described below.

### Create a room suggestion

`POST /api/v2/watch-together/rooms/{room_id}/suggestions` (`createWatchTogetherSuggestion`) requires authenticated profile authority, the demo guard and original room proof in `X-Room-Token`. Supply a client-generated UUID `suggestion_id`, required `content_id`, `content_type` (`movie` or `episode`) and `title`, plus optional `subtitle`, `poster_url` and `note`. The receipt migration must be installed before enabling this writer on any v2-serving node.

A successful insertion or identical live replay returns `201` with `suggestion_id`. The original account/profile/room and metadata are bound by the receipt store. Changed or deleted identities return `409` without resurrection; closed room returns `409`, missing room `404`, invalid input `422`, invalid proof `403`, and unavailable service `503`. Current suggestion state is read separately. The service supplies creation time and attempts the existing local suggestion snapshot broadcast only for a new insertion. A failure after commit can leave the broadcast incomplete; retry returns the identity without attempting it again. This is not durable delivery or an outbox.

The actual web candidate holds an immutable ID, metadata, room proof and authority. Explicit retry of that unchanged draft reuses them; dismissing or replacing it creates a new identity. Authority replacement refuses the old draft instead of rebinding it. Each submission sends one POST without authentication replay and refreshes the existing suggestion list under the original authority after a matching receipt. Superseded room/run/authority cannot publish the list; superseded candidate cannot be cleared or receive success/error feedback. Drafts are not persisted across page reloads. Room proof expiry is surfaced without automatic renewal or rebasing. V1 creation remains unchanged; native caller inventory and adoption are separate gates.

### Promote a room suggestion

`POST /api/v2/watch-together/rooms/{room_id}/suggestions/promote` (`promoteWatchTogetherSuggestion`) requires profile/demo authority, original `X-Room-Token`, both host account and profile, and a suggestion belonging to the room. The body contains `suggestion_id`. The host may promote any suggestion in either mode: in a vote room the tally is advisory and the host's choice is broadcast as the selection, so an override is visible rather than silent. Clients read the leader from the suggestion list ordering rather than from a server-side gate.

The v2 path retains playable/access resolution, then uses the guarded selection transaction. Promotion chooses content rather than an explicit file variant: already-selected content preserves its selected file/library, playback anchor, revision/generation, attached readiness and local broadcast count even if the resolver now prefers another variant. A different eligible content selection resets playback/readiness once. Direct selection continues to compare all resolved identities. Host/closed/mode checks still precede the no-op. No historical replay receipt is promised after intervening different content, so promotion remains non-retryable. Frozen v1 promotion remains unchanged.

Success returns the authoritative room snapshot and renewed existing room proof. A competing writer may determine that snapshot; the web reports selection updated without asserting playback started. Missing room/suggestion returns `404`, non-host or invalid proof `403`, closed room `409`, unplayable selection `422`, and unavailable service `503`. V2 permits a host override of the tally. The frozen v1 route retains its `409` for a non-leader or a room with no votes. The actual panel sends once without auth replay, suppresses stale room/run/authority success and error feedback, and compares generations only within one room before publication. No new session-bound socket proof, cross-node broadcast, native activation or real playback dispatch is established by this port.

### Room creation identity storage

The v2 room creation prerequisite binds a caller-selected room ID to its original host account, host profile and selection mode. Room and receipt insert atomically. The caller supplies server-generated code, invite token and initial room state; regenerated values on retry do not replace stored credentials or state. Exact replay returns the current active room, including later playback changes. A changed original host or mode conflicts. A closed room stays closed, and a deleted room cannot be recreated under its retained ID.

Creation receipts retain no invitation credentials and survive room deletion. They are removed with login-account deletion and have no age-based expiry. Collision with an existing unreceipted legacy room refuses adoption and rolls back the new receipt. The v1 creation service and insert remain unchanged. This storage prerequisite does not register a creation endpoint or hydrate live members; the later v2 service must preserve existing live-room membership on replay. Stable web draft identity, native coordination and rollout remain separate work.

### API v2 room creation

`POST /api/v2/watch-together/rooms` (`createWatchTogetherRoom`) requires an authenticated account, selected profile, and non-demo authority. The JSON body requires a caller-selected UUID `room_id` and `selection_mode` (`host_pick` or `vote`). The server generates the initial room code, invite token, and timestamps. The retained room-creation receipt migration must precede enabling this writer.

A successful insert or exact active replay returns `201` with the current room snapshot and account/profile room proof. Original account/profile/mode binding cannot change. Closed rooms and conflicting or deleted creation identities return `409`; invalid input returns `422`; an unavailable dependency returns `503`. Replay retains stored invite credentials and does not overwrite connected members, readiness, or playback state. The response includes local member information without creating membership or broadcasting creation. This proof is not a session-bound socket credential.

Only a definite room-code or invite-token uniqueness violation allows up to three internal credential candidates with the same caller identity and original binding. An uncertain database outcome is not retried internally. The response is current state, not a durable admission receipt, historical snapshot, outbox, or cross-node membership guarantee. Creation does not hydrate a local room; the existing socket/read paths handle loading separately. Frozen v1 creation is unchanged.

The web create action captures one immutable room UUID, selection mode, and profile authority. An explicit retry after uncertainty retains that draft; mode replacement creates another draft. Replaced authority is never rebound to an old draft. Stale completion cannot navigate or clear a newer pending action. No automatic authentication replay or draft persistence across page reload is provided. Native creation adoption and exact caller inventories remain separate acceptance gates.

### Room socket credential storage prerequisite

Room socket tickets use a separate shared Redis namespace and atomic consumption. There is no in-memory fallback: Redis availability is required for both mint and consume. A ticket binds the room ID, captured delegated login session/account/profile/PIN/scope authority, and original room-proof expiry. Its lifetime is at most 30 seconds and cannot exceed either access-token or room-proof expiry. Consumption burns a ticket even if the requested room mismatches. Events tickets and profile-only room proofs cannot substitute for this credential.

This storage foundation does not expose a socket or validate current account/session/profile authority. The room socket handler below validates that authority and room proof before minting and enforces the connection obligations separately. Storage availability alone does not prove socket admission or native adoption.


### API v2 room socket admission and lifetime

`POST /api/v2/watch-together/rooms/{room_id}/ws-ticket` (`createWatchTogetherSocketTicket`) requires authenticated profile/demo authority, an expiring access login session, and the original `X-Room-Token` matching room/account/profile. API keys and profile-only room proof cannot delegate a socket. The room proof must have a valid HS256 signature and signed expiry; frozen v1 proof validation is unchanged. Current session validity, enabled account/role, profile PIN verification, viewer scope, and room existence are checked before minting. The response carries `ticket`, `expires_in`, `max_connection_seconds` (300), and `protocol` (`silo.room.v2`). Credential storage failure returns 503; invalid original proof or authority returns 403; closed rooms return 409. No automatic room-proof renewal occurs here.

Connect with `GET /api/v2/watch-together/rooms/{room_id}/ws` (`connectWatchTogetherSocket`), offering exactly `silo.room.v2` and `silo.ticket.<ticket>` in that order. Only `silo.room.v2` is echoed. Request bodies and all query strings are refused, including legacy bearer/profile/PIN/room credentials. A supplied browser Origin must pass the same check as the events socket: the configured public origin, the request's own scheme and host, or a connected overlay origin; absent Origin is permitted for independently authenticated native callers. Forwarded host headers do not authorize an Origin; only a trusted proxy's single `X-Forwarded-Proto` value can supply the scheme for the request-origin match. Malformed upgrades are refused before consuming the credential. After consumption, current session/account/profile/PIN/scope and room existence are checked again before 101.

The handler closes the underlying socket at the earlier of five minutes, access expiry, or original room-proof expiry. Every 15 seconds it rechecks current authority and room existence with a two-second validation timeout; errors close the connection rather than extending its deadline. Revocation is therefore bounded by that polling interval and timeout, not instantaneous. Missing Redis prevents runtime socket wiring; Redis errors never use an in-memory fallback.

The post-upgrade loop is shared with v1: Connect, Disconnect(false), initial snapshots, attach-session/transport/state-report/ready/buffering messages and ping/pong use the same service. The readiness validation below applies to both versions. There is no new leave message. Membership and readiness are shared through the room coordinator. V2 cancellation closes the transport so a blocked read releases and executes the existing disconnect callback. These raw frame shapes are not the typed HTTP room snapshot schema. No playback/provider operation is implied by obtaining a ticket.

The actual web room/player hook obtains a fresh credential for each reconnect. It captures original room proof and profile authority, sends no authentication retry, uses no URL credentials, checks the negotiated protocol, and suppresses old sockets' messages/results after authority or room replacement. It subscribes to the existing AuthProvider so same-profile PIN replacement rebinds even when room props do not change. A fresh reconnect uses an already-rotated access token only while the original logical authority remains current; it does not replay a refused ticket request. Terminal ticket refusals stop reconnecting; transient failures retain the existing bounded reconnect delay. Existing room-proof expiry ends access; it is not automatically renewed or rebound. Native adoption and exact caller inventories remain separate gates. Ticket consumption handles socket admission; the room coordinator handles membership and playback synchronization.

### Replaced room connections

A new socket for the same account and profile replaces that member's previous
socket. On v2, the displaced socket receives this terminal frame before closure:

```json
{"type":"connection_replaced","reason":"This profile joined the Watch Party on another device."}
```

Clients identify the terminal condition by `type`, stop automatic reconnect and
pending credential renewal, and explain that this profile joined on another
device. The room and the replacement connection remain active. Clients must not
interpret this frame as `room_closed`. An explicit user rejoin can claim the
membership again. The web room and player display a rejoin action; displaced
playback pauses until the user chooses to rejoin.

Local takeover and cross-node reconciliation use the same signal after the
membership transaction commits. The socket's single writer prioritizes the
terminal frame over queued updates and waits for the write before closing, with
a one-second bound for a blocked peer. Delivery cannot be guaranteed over a
broken connection. Delayed callbacks retain their original connection identity
and cannot disconnect the winner. Expired membership, ordinary network loss,
five-minute renewal and credential rotation do not send this replacement frame.

The `connection_replaced` capability advertises support. Deploy the correction
to every API node before relying on it across the cluster. V1 retains its frozen
close behavior. Older v2 clients that ignore the frame can still reconnect and
displace the winner; upgrading those clients is a release prerequisite.

Native adoption is tracked in
[Apple #344](https://github.com/Silo-Server/silo-apple/issues/344) and
[Android #355](https://github.com/Silo-Server/silo-android/issues/355). Before either
native surface ships, it must handle this terminal frame, preserve HTTP vote
membership when common suggestion rows arrive, and verify takeover with the web
client, explicit rejoin, credential renewal and transient network recovery.
Jellyfin compatibility does not use these room sockets.

### Room playback readiness

A `ready` frame acknowledges the waiting `transport_command` that the player has
applied. Clients send the existing `session_id`, actual media `position_seconds`,
and `is_paused`, plus the command's `command_id`. A new command requires a new
acknowledgement even if the room remains `waiting` and the playback session and
selection have not changed.

The server ignores an acknowledgement naming an older command without changing
member readiness or the room anchor. For an explicit seek, a guest's reported
media position must be within one second of the command's destination. The host
is the authority on position: a host acknowledgement within fifteen seconds of
the destination is accepted, and when it is more than one second away the
host's reported position becomes the room anchor before the room resumes. A
rebuilt stream that lands on a keyframe or segment boundary short of the
target therefore resumes from where the host really is instead of holding the
room until the waiting deadline. Position validation uses the same finite,
nonnegative range as other playback reports. Buffering pause commands can be
acknowledged at the member's actual position: they do not require rebuilding an
otherwise usable stream to reach an unbuffered anchor. Playback correction
still runs when the room resumes.

Readiness is level-triggered on the web player. It evaluates the same guards on
every media event that can indicate playable media (`canplay`,
`canplaythrough`, `loadeddata`, `seeked`, `playing`, `timeupdate`) after
command execution, the native seek has finished, and the element has future
media data. Old events on the stream being replaced cannot acknowledge a seek
whose destination has not arrived. It reports the actual media clock, including
the stream's timeline offset, rather than the optimistic timeline displayed
during a seek. When an acknowledgement is withheld, the player logs the reason
at debug level once per distinct reason.

While the room is `waiting`, `state_report` frames may carry `command_id` and
`is_ready: true`. The server treats such a report exactly like a `ready` frame
for that command, so a lost or rejected acknowledgement heals on the next tick
without a new media event. The web player retries readiness every 500 ms until
the room snapshot marks its own member `is_ready`. It then stops readiness
acknowledgements and returns to ordinary state reports every 1.5 s, even while
other members are still waiting. A readiness reset for a new command enables
retries again. Reports without `is_ready` are still ignored while waiting.

A viewer the room stopped waiting for sends `ready` while the room is `playing`
or `paused`. The v2 snapshot shows this state as `self_ignore_wait: true`, or as
the viewer's own member entry still marked `is_buffering`. The client sends the
acknowledgement once it has executed the latest transport command and its media
is playable, and retries every 500 ms until its own member entry is `is_ready`.
The server then clears the viewer's buffering status and, while the room plays,
sends it a transport command at the room's current position. When nobody else is
watching, for example after the other viewers left, the room's position moves to
the viewer's reported position first, so the viewer does not skip ahead. A `state_report` within one second of the room's
position, with a matching pause state, also marks the member ready and clears
the same status; this covers late joiners and clients that never send
`ready`. Seek-destination checks apply only while the room is
`waiting`.

`command_id` is optional for older clients on the shared v1/v2 message loop. Their
seek acknowledgements still need to reach the destination, but a client that
omits the ID cannot distinguish consecutive seeks to the same position. Older
servers ignore these additive request fields. Updated servers advertise
`watch_party_coordinator_v1` in playback capabilities. The waiting deadline,
after which members that never became ready stop blocking the room, is 10
seconds; it is a safety net rather than the expected path. It takes effect only
once at least one attached member is ready, so a room where nobody is ready
keeps waiting. Past the deadline, the first `ready` resumes the room.


### Room membership and buffering

Watch Party clients discover the shared coordinator through
`watch_party_coordinator_v1` in `GET /api/v2/playback/capabilities`. It guarantees
shared membership and readiness across API servers and support for the optional
`is_ready`, `is_buffering`, and `is_syncing` member fields. Omitted false fields
mean false when the capability is present. The initial upgrade requires the
stop/start procedure in [Watch Party synchronization](architecture/watch-party-synchronization.md);
old and new coordinators must not serve rooms together.

Room playback uses one selected source file for all viewers. Automatic selection
uses the catalogue's quality ordering within the selected edition, without a
room-specific resolution or dynamic range limit. Each viewer's playback plan can
direct play, remux, transcode, or tone map that file according to their device
capabilities and server settings. When that source cannot be adapted, a connected
viewer can request coordinated source fallback as described below. The web player
disables version switching and requires
`fixed_media_file_v1` from playback capabilities, starting with
`allow_alternate_versions: false` to prevent automatic file fallback during
recovery. Streaming quality remains adjustable on the same file.

`POST /api/v2/watch-together/rooms/{room_id}/source-fallback`
(`fallbackWatchTogetherSource`) requires authenticated profile authority, room
proof in `X-Room-Token`, and connected room membership. Discover support through
`watch_party_source_fallback_v1` in playback capabilities. The body contains
`selection_revision`, `failed_file_id` (a positive string ID), and `reason`:
`no_alternate_version`, `hdr_transcode_unsupported`,
`subtitle_conversion_unsupported`, or `transcoding_disabled`.

The server chooses a lower-ranked source in the same edition and presentation
part. `no_alternate_version` requires lower resolution; an HDR refusal requires
SDR. It preserves the shared position and resume intent, advances the selection
revision, clears attachments/readiness, and returns/broadcasts the new snapshot.
All clients must follow that selection and attach again, including the host.
This changes the file for the existing content in both host-pick and vote rooms.
A stale file or revision returns the current snapshot without a write, so replay
cannot cause a second fallback. No candidate returns `409`; disconnected or
unauthorized viewers receive `403`. Clients retain the playback refusal if the
fallback fails and must not independently substitute another file.

V2 room snapshots include members connected through all API servers. Each member can
include additive `is_ready`, `is_buffering`, and `is_syncing` booleans; absent fields
mean false. `is_syncing` identifies an attached member still blocking the current
readiness barrier. HTTP v2 snapshots and raw socket snapshots expose these fields.
The frozen v1 HTTP responses and room socket omit these status fields. The web
player lists viewer status and names the viewers it is waiting for.

The web player reports buffering after 2 seconds without playable media and
sends no `state_report` while its element is stalled, or while its media is
unplayable and a readiness acknowledgement is pending. Recovery, a changed
command/session, pause, a phase change, disconnect, and unmount cancel a pending
report. The server ignores late buffering reports for paused rooms. A buffering
report pauses a playing room only when the viewer has not stalled in the last 5
minutes, the room has not paused for buffering in the last minute, and the viewer
is not already catching up; a stall from a viewer with nobody else watching
always pauses the room. Otherwise the room keeps playing and the viewer's snapshot reports
`self_ignore_wait: true` until it acknowledges recovery. The waiting deadline
also sets `self_ignore_wait` for the members it skips.

A reconnected socket retains its validated playback-session attachment, and
reattaching that session does not pause a playing room. Attaching a new playback
session while the room plays does not pause it either: the member receives a
transport command at the room's position and catches up alone. A member attaching
during an explicit seek receives the seek command and must reach its destination
before acknowledging readiness. See
[Watch Party synchronization](architecture/watch-party-synchronization.md#buffering-policy)
for the full buffering policy.

See [Watch Party synchronization](architecture/watch-party-synchronization.md)
for transaction, lease, delivery, and deployment behavior.
