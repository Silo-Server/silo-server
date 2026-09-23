# Notification inbox API

The v2 inbox uses the existing durable delivery rows and preference store. All
routes below `/api/v2/notifications` require an authenticated, verified profile
and retain the demo write guard. Websocket transport, email links, and
push-registration protocol retain their existing shapes.

| Method and suffix | Result |
| --- | --- |
| `GET /capabilities` | Existing in-app, Apple/Android push, web push, webhook, email, and Discord availability |
| `GET /` | Newest-first `items`, `page`, and signed `read_cutoff` |
| `GET /sync` | Ascending `items`, `page`, `sync_cursor`, `unread_count`, and `initial_snapshot` |
| `GET /{id}` | One delivery; another profile's delivery is 404 |
| `GET /unread-count` | `{ "count": 0 }` |
| `POST /{id}/read` | 204; already read succeeds, unknown delivery is 404 |
| `POST /read-all` | 204; body requires `{ "through": "<read_cutoff>" }` |
| `GET /preferences` | Profile ID and five preference booleans |
| `PUT /preferences` | Updated preferences; omitted fields retain their values |

The inbox list accepts `status=all|unread`, `limit` up to 200, and `cursor`.
A cursor binds the acting account/profile, filter, page size, last delivery tuple,
and original newest-delivery boundary. `page.has_more` is based on a lookahead
row. The final page omits `page.next_cursor`. An empty inbox still supplies a
signed cutoff describing an empty set.

Delivery IDs and linked library/item IDs are strings. Public timestamps use UTC
milliseconds; the signed cursors retain the database timestamp precision. The
`reason_flags` object retains event-specific data used by existing clients.

Delivery insertion serializes each profile's timestamp allocation through a
database row lock held until commit. A trigger assigns `created_at` strictly
above that profile's previous committed timestamp, even when a transaction
started earlier or the clock moves backwards. The boundary survives delivery
retention. A later delivery therefore cannot commit behind an observed cutoff.
Fanout transactions lock their complete recipient profile union in sorted order
before inserting; unrelated profiles can progress independently. Existing rows
retain their timestamps, and migration seeds the boundary from their maximum.

## Fixed read cutoff

Capture the displayed list's `read_cutoff` when the user chooses to mark the inbox
read. Reusing the same body only marks deliveries at or before that original
`(created_at, id)` boundary. It cannot expand to newer deliveries on retry. An
empty cutoff remains a no-op. Tokens from another account/profile or operation
are rejected; clients must not substitute a fresh cutoff after an uncertain
response without a new user intent.

The database update is authoritative. Best-effort `notification.read` events for
this operation contain `profile_id`, `through_created_at` at full precision, and
`through_id`; they do not claim `all: true`. Clients reread lists/counts for these
events rather than compare rounded public timestamps. Existing single-ID read
and legacy all-read events retain their shapes. No durable websocket delivery is
promised.

## Forward sync

Without a cursor, sync returns the existing bounded newest snapshot in ascending
order and sets `initial_snapshot: true`. It is not a complete historical inbox
export; use the list for older pages. Persist `sync_cursor` even when
`page.has_more` is false. An empty initial snapshot also supplies a checkpoint.

Subsequent calls pass that checkpoint as `cursor`, return newer deliveries in
ascending order, and set `initial_snapshot: false`. Continue immediately through
`page.next_cursor` while `page.has_more` is true. When caught up, retain
`sync_cursor` for the next wake or reconnect. The page size is part of the cursor
scope; legacy v1 cursors cannot be reused.

## Preference writes

`enabled`, `notify_favorites`, `notify_watchlist`, `notify_continue_watching`, and
`notify_next_up` are optional booleans. Explicit false is preserved; v2 rejects
null. Both API versions now apply partial fields in one database statement,
preventing independent concurrent toggles from overwriting each other. Missing
preference rows retain the existing all-enabled defaults.

The web captures account/profile authority for requests and mutations, validates
pagination, and surfaces initial and partial failures. It does not automatically
retry mutations or optimistically mark every cached delivery read.

### API v2 Apple push display

`GET /api/v2/notifications/push/apple/display/{delivery_id}`
(`getNotificationApplePushDisplay`) returns compact display metadata from the
same delivery row and renderer as the bridge endpoint. The response fields are
`delivery_id` (string), `title`, optional `body` and `thread_id`, `category`, and
`url`. Responses use `Cache-Control: no-store`; missing or other-profile
notifications return a 404 problem. Delivery IDs are opaque strings: the store
mints ULIDs, so they are not UUIDs and must not be validated as such.

The route accepts ordinary bearer/API-key authentication with `X-Profile-Id`,
or the existing Apple display token in the Authorization header. A display
token binds its own profile, ignores a supplied profile header, and requires a
valid login session and a still-owned profile. It is rejected by other API v2
operations. Query-string display credentials are not accepted. Existing
pre-auth and post-auth rate limits apply. This read neither marks a notification
read nor sends a push. Apple registration and display-token issuance retain
the bridge contract until their separate migration is accepted.

The Apple notification extension is the consumer; Android does not call this
Apple display endpoint. Jellyfin compatibility has no equivalent display-token
flow and needs no route change.

### API v2 administrator test push

`POST /api/v2/admin/notifications/push/apple/test`
(`testAdminApplePushNotification`) and
`POST /api/v2/admin/notifications/push/fcm/test`
(`testAdminAndroidPushNotification`) use acting-administrator authorization.
The body requires `profile_id` and optionally selects `server_device_id`; both
identifiers are strings. An omitted device selector retains the existing
platform-specific device selection behavior.

Each request calls the existing test dispatcher once. It creates and claims a
new outbox attempt, invokes the configured sender, and returns HTTP 200 with
`attempt_id`, `push_device_id`, `server_device_id`, `outcome`, and any existing
relay/upstream diagnostic fields. A `retrying` or `failed` outcome is still a
successful HTTP report of that attempt, not proof that a push was delivered.
Background attempt recovery and sender retries retain their existing semantics.
These operations are `non_retryable`: clients must not repeat a lost request
automatically because another request creates another test attempt.

Invalid input returns a 422 problem, a missing target a 404, and unavailable
push delivery a 503. Demo restrictions and no-store responses apply. No current
web, Apple, or Android caller invokes these administrator test routes; this port
adds no test-send UI. Jellyfin compatibility has no corresponding operation.

### API v2 relay administration

`POST /api/v2/admin/notifications/push/relay/register`
(`registerAdminNotificationRelay`) and
`DELETE /api/v2/admin/notifications/push/relay`
(`clearAdminNotificationRelay`) require acting-administrator authorization.
Registration accepts an optional `relay_url` and otherwise uses the existing
default. It preserves relay origin allowlisting, initial registration versus
credential rotation, explicit re-registration after rejection, and atomic
credential persistence. Responses expose only relay/deployment identifiers,
key prefix, configured status, optional request/topics metadata, and an
`expires_at` UTC instant with millisecond precision. The reusable key is never
returned. Clearing removes the local credential, sets the re-registration
marker so first-use registration stays parked, and returns a bodyless 204.

Both operations are `non_retryable`. A repeated registration can rotate again;
a delayed clear can remove a newer credential. The existing web controls
capture administrator authority, permit one in-flight command, and disable
automatic authentication replay. A changed authority discards the response.
An administrator must explicitly decide whether to repeat an uncertain command.
This migration does not introduce generation guards or promise safe replay.

Mobile push delivery (`notifications.apple_push_delivery_enabled` and
`notifications.android_push_delivery_enabled`) defaults to on for new installs.
The setup wizard offers the relay privacy disclosure beside the toggle and lets
the administrator turn it off before finishing setup. When delivery is on and no relay credential
is stored, the push sender self-registers with the configured relay on the
first send; the explicit register endpoint remains for choosing a relay origin
or rotating the credential. Servers that already had an account before this
default changed are pinned to off by migration and keep their prior behavior
until an administrator turns delivery on. Native clients see the effective
state through `GET /api/v2/notifications/capabilities` and need no change.

Relay errors become v2 problems, with `Retry-After` retained when supplied.
Bridge 400 validation becomes 422 and bridge 502 upstream failures become the
shared 500 `internal_error`; other existing supported statuses retain their
meaning. V1 response statuses, error codes, timestamp format, and headers remain
unchanged. Native clients and Jellyfin compatibility do not manage relay
credentials and need no consumer change.

### API v2 email and Discord preferences

`GET` and `PUT /api/v2/notifications/email-preferences` read and set the acting
profile's email mode. The response retains `mode`, `custom_email`,
`pending_email`, and `can_edit_address`. Each profile uses its own verified
address; there is no login-account email fallback. Address verification and
removal remain separate bridge operations pending their durable dispatch and
callback migration.

`GET` and `PUT /api/v2/notifications/discord-preferences` read and set the login
account's Discord mode. A selected profile is optional; when supplied, the
existing viewer/PIN gate still validates it. Responses retain `linked`, optional
`discord_username` and `link_failure`, and `mode`. The underlying Discord user
identifier and credentials are not returned.

Both mode writes accept only `mode`: `off`, `per_episode`, `daily_digest`, or
`per_episode_and_digest`. Existing allowance, verified-address, and linked-account
checks still apply; rejected modes return a 422 problem. Writes call the existing
setter once and then reread state. They are `non_retryable`: these setters also
reset delivery backoff and can advance the delivery watermark. The v2 ports do
not change those effects or present repeated writes as harmless. Web queries use
captured-authority cache keys and mode writes disable automatic retries and
401 authentication replay. No current native email/Discord preference caller was
found; Jellyfin compatibility has no equivalent preference surface.

### API v2 destination lists

`GET /api/v2/notifications/web-push/subscriptions`,
`GET /api/v2/notifications/webhooks`, and
`GET /api/v2/admin/notifications/server-channels` return bounded
`items`/`page` collections. Personal lists require the acting profile; server
channels require an acting administrator. Signed cursors bind the operation,
account/profile authority, page size, and exact `(created_at, id)` boundary.
Records retain creation order, with ID resolving timestamp ties. Deleting a
previous boundary row does not invalidate continuation. Indexes support the
profile-filtered and administrator paging queries.

Web-push metadata retains the endpoint needed to identify the current browser,
but excludes subscription keys. Webhook and server-channel metadata exposes only
`url_host`, never destination URL ciphertext or stored signing secrets. Delivery
health fields remain readable, with nullable timestamps using the v2 UTC instant
format. Reads do not reset backoff, mutate destinations, or dispatch a send.

Existing web lists drain bounded pages under captured authority and use scoped
cache keys. Missing/repeated continuations and authority changes fail explicitly
without returning a partial list. The current web display limit is 100 pages of
100 records; exceeding it is an explicit error. Destination creation, updates,
deletes, tests, and secret rotation remain separate bridge operations until their
own guarded mutation migration. No native destination-management caller exists;
Jellyfin compatibility has no equivalent list surface.

### API v2 administrator Discord credential test

`POST /api/v2/admin/notifications/discord/test`
(`testAdminDiscordNotification`) verifies the stored bot token by fetching the
bot's own identity once. It sends no message and does not change Discord links.
Acting-administrator authorization, demo restrictions, and no-store responses
apply. There is no request body. HTTP 200 reports `ok`, nonnegative `duration_ms`,
and `message`; a failed verification still returns this result. A missing token
reports “Bot token is not configured”; provider failures use a generic message
instead of exposing upstream diagnostics. An unavailable service returns 503.

The operation is `non_retryable`. The existing administrator test button captures
authority, prevents overlapping tests, disables authentication replay, and refuses
stale results after an account/profile switch. Apple and Android have no caller;
Jellyfin compatibility has no corresponding operation. Discord link initiation,
OAuth callbacks, and unlinking retain their bridge routes pending their separate
migration.

### API v2 webhook and server-channel test delivery

`POST /api/v2/notifications/webhooks/{id}/test` (`testNotificationWebhook`)
requires the acting profile; `POST /api/v2/admin/notifications/server-channels/{id}/test`
(`testAdminNotificationServerChannel`) requires an acting administrator. Both
accept an opaque destination ID and no request body, enforce demo restrictions,
and call the existing synchronous sample sender once. Personal webhook tests
retain the administrator's webhooks-enabled gate and profile ownership check.
Server-channel tests retain the existing administrator diagnostic behavior.

HTTP 200 reports `ok`, optional `http_status`, nonnegative `duration_ms`, and an
optional sanitized sender `message`. A destination's 429/5xx is a failed delivery
result, not an API failure or an instruction to retry. Test sends neither enqueue
retries nor update failure counters, auto-disable state, or delivery watermarks.
Missing destinations return 404; disabled personal webhooks return 403; unavailable
services return 503. Responses are no-store. Both operations are `non_retryable`.

Existing web cards capture authority, prevent overlapping test calls, disable
authentication replay, and suppress stale results. Configuration changes and
secret rotation retain their bridge routes pending guarded mutation migration.
Apple and Android have no destination-test callers. Jellyfin compatibility has
no corresponding operation.

### API v2 tokenized notification email links

Notification verification emails now link to
`GET /api/v2/notifications/email/verify?token=...`
(`verifyNotificationEmailAddress`). Notification email footers and
List-Unsubscribe headers use `/api/v2/notifications/email/unsubscribe?token=...`:
GET is `unsubscribeNotificationEmail`; the RFC 8058 one-click POST is
`unsubscribeNotificationEmailOneClick`.

These public token-authorized routes render the existing standalone HTML pages.
They require no Silo login or profile header and accept mail-client HTML
negotiation and one-click form bodies. Verification consumes the single-use
proof and promotes the pending address; success is 200, expired/consumed/missing
proof is 400, an address ownership conflict is 409, and storage errors are 500.
Unsubscribe retains the existing profile capability token and switches email
mode off: 200 on success, 400 for invalid/missing proof, and 500 on storage error.
An unavailable service returns a 503 problem. API v2 adds no-store and
no-referrer headers; pages never reflect the proof or internal error text.

The one-click POST is `non_retryable`: the existing capability token can turn
email off again after the profile later re-enables it. This migration adds no
generation guard or delivery retry. Address request/clear transports remain on
the bridge until their separate migration. Previously sent bridge links keep
working through the bridge release. Mail clients follow the emitted URLs;
Apple and Android have no in-app callback consumer, and Jellyfin compatibility
has no corresponding operation.

### API v2 Discord account-link consent and callback

`POST /api/v2/notifications/discord/link/init` (`beginNotificationDiscordLink`)
starts one consent flow for the authenticated login account. A supplied profile
still passes viewer/PIN checks; no profile is required for this account-level
operation. Demo restrictions apply. The response contains `url`, using Discord's
consent endpoint, the stored client ID, `identify` scope, a random one-time state,
and the exact v2 callback URI. This operation is `non_retryable`; web captures
authority, prevents overlapping initiation, disables authentication replay, and
checks authority again before navigating.

`GET /api/v2/notifications/discord/link/callback`
(`completeNotificationDiscordLink`) is public and authenticates through the
stored state rather than browser login headers. It retains the existing channel
enabled check, consent-denied and malformed-callback handling, one-time state
consumption, account lookup, and code exchange. The exchange uses the same
`<public URL>/api/v2/notifications/discord/link/callback` redirect URI as consent.
The result is HTTP 302 with Location pointing to notification settings and the
existing success/error query fields, plus the usual HTML redirect body. It never
invents a JSON 200 response. API v2 applies no-store/no-referrer headers.

Register the exact v2 redirect URI in the Discord application's OAuth settings
before using v2 linking. Keep the bridge redirect registered while bridge flows
remain supported; their consent and exchange still use the v1 URI. This port
preserves the existing link-state and account-link write semantics. It adds no
generation guard for overlapping consent flows or unlinking; unlink migration
remains separate. Apple and Android have no notification Discord-link callers,
and Jellyfin compatibility has no equivalent operation.

### API v2 destination creation

`POST /api/v2/notifications/webhooks` creates a webhook for the active profile.
`POST /api/v2/admin/notifications/server-channels` creates a server channel for
an acting administrator. Both require `name` and `url`; optional type and event
flags retain the existing service defaults. Creation validates and stores the
destination through the same service as the bridge and does not send a message.

A successful response is `201` with `id`, `name`, `type`, `url_host`, and an
optional one-time `signing_secret`. It never returns the destination URL or
stored credential ciphertext. Responses use `Cache-Control: no-store`.
Disabled personal webhooks return `403`; administrators may prepare server
channels while delivery is disabled. Invalid configuration or a quota limit
returns `422`. Missing services return `503`.

Creation is `non_retryable`: a lost response can leave a created destination
whose signing secret was not received. Clients must inspect the destination
list and explicitly manage or rotate that destination instead of automatically
resubmitting creation. There is no durable creation receipt or secret recovery
promise. The web forms capture request authority, prevent overlapping submissions,
and suppress results after an account or profile change. Native clients have no
destination-management caller; Jellyfin compatibility has no matching operation.

### Unlink Discord

`DELETE /api/v2/notifications/discord-link` (`unlinkNotificationDiscord`) unlinks the authenticated login account's Discord identity and turns its Discord delivery mode off. A profile header is optional; a supplied profile remains subject to the normal access checks. The demo guard applies. The operation returns bodyless `204`, including when there is no linked identity. An unavailable API service returns `503`; storage failure returns `500`.

Unlink is **non-retryable**: a delayed repeat can clear an identity established by a subsequent OAuth relink. No generation precondition or cancellation of in-flight OAuth/provider work is provided. The existing identity, DM-channel and mode clearing behavior remains unchanged. The settings action captures authority, sends once without authentication replay, rejects stale receipts, and invalidates only its exact Discord preferences cache. A successful receipt does not prove that an already-dispatched Discord message was cancelled. Native caller closure is separate from this server and web operation.

### Clear the custom email address

`DELETE /api/v2/notifications/email-preferences/address` (`clearNotificationEmailAddress`) clears the acting profile's verified address and pending verification address/token/expiry and turns email delivery off in the existing atomic update. Authenticated profile authority and the demo guard apply; the existing service refuses child profiles with `403`. The response is `200` with the current email preferences, read after the update. An unavailable API service returns `503`; storage/read failure returns `500`. A failed response can follow a successful clear, and the returned preferences may reflect a concurrent later change.

The operation is **non-retryable**. A delayed repeat can clear an address requested or verified after the first clear; no generation or If-Match precondition is provided. The settings action captures authority, sends once without authentication replay, rejects stale receipts, and publishes only to the exact captured email preferences cache. Clearing does not send a verification email or cancel an email already dispatched to a provider. Address requests and token callbacks retain their separate lifecycle.

### Delete a personal webhook

`DELETE /api/v2/notifications/webhooks/{id}` (`deleteNotificationWebhook`) requires the original row's strong validator in `If-Match`. The bounded webhook list supplies that quoted validator in each item's `etag`. Authenticated profile authority and the demo guard apply. Missing/foreign resources return `404` before precondition evaluation; an existing resource without `If-Match` returns `428`, a stale validator returns `412` with the current `ETag`, and an exact match returns bodyless `204`. Standard conditional-header grammar applies. The UI never supplies a wildcard.

A database sequence and insert/update trigger allocate a fresh revision for every row change, including legacy configuration and provider outcome writers. Revision allocation is not based on timestamps and never reuses a deleted identity's validator. Deletion locks the profile-owned row, evaluates its current validator, and deletes within that same transaction; a concurrent editor that commits first is preserved by a stale-delete refusal. Stored delivery attempts cascade only on a successful deletion. Deploy the revision migration before code that reads the new column. This does not change provider delivery or recall an already-dispatched request.

The settings confirmation captures the list's original ID and validator when opened, and passes that immutable intent plus captured authority through the mutation's variables. It sends once without authentication replay. A stale/missing/failed response surfaces without a read, rebase, or automatic retry. The user must explicitly reload and reconsider the current row; returned conflict validators are never adopted automatically. Success invalidates only the captured authority's list. Administrator channel deletion remains a separate operation.

### Delete an administrator server channel

`DELETE /api/v2/admin/notifications/server-channels/{id}` (`deleteAdminNotificationServerChannel`) removes the exact global server channel under acting-admin authority and the demo guard. It returns bodyless `204`, including when absent; a non-admin receives `403`, an unavailable service `503`, and storage failure `500`. It does not use household-primary status as a substitute for administrator authority.

Deletion removes the channel and its row-local watermark, last-attempt and outcome bookkeeping; server-generated identities are not reused. This operation naturally converges on absence but cannot recall already-dispatched provider work. The administrator action captures its ID and authority into mutation variables at invocation, sends once without authentication replay, refuses stale dispatch or receipts, and invalidates only the captured administrator channel-list cache. Personal webhook conditional deletion has a separate recorded contract.

### Rotate a personal webhook signing secret

`POST /api/v2/notifications/webhooks/{id}/rotate-secret` (`rotateNotificationWebhookSecret`) requires acting-profile authority and the demo guard. Only generic webhooks have signing secrets; a Discord webhook returns `422`, missing/foreign IDs `404`, and unavailable service `503`. A successful `200` returns `signing_secret` once with `Cache-Control: no-store`. The secret is encrypted at rest with the webhook's existing associated-data binding.

The v2 storage path updates only the encrypted secret and timestamp; it does not write a stale configuration snapshot or reset provider failure/disabled state. The revision trigger still advances the row validator. Existing bridge rotation is unchanged. Rotation immediately replaces the signing secret, with no dual-acceptance window or recoverable receipt. Later concurrent rotations or legacy configuration writes can supersede that secret; the response is not a lease guaranteeing continued acceptance.

This operation is non-retryable. The existing settings action captures ID and authority into invocation variables, sends once without authentication replay, checks authority before revealing the secret, and refreshes only the captured webhook list. Failure or uncertainty never causes an automatic read, rebase, repeat rotation or compensating write. No webhook delivery is sent by rotation; in-flight provider work may still use a previously captured secret.

### Update personal webhook configuration

`PUT /api/v2/notifications/webhooks/{id}` (`updateNotificationWebhook`) requires acting-profile authority, the demo guard and the original strong `If-Match` validator supplied with the listed row. Name, URL, enabled state and notification reason flags are optional; omitted fields stay unchanged. Type and signing secret are not configuration fields. Missing/foreign rows return `404`, missing preconditions `428`, stale validators `412`, invalid configuration `422`, and an unavailable service `503`. Success returns `200` with the destination and its new validator in both `etag` and `ETag`.

The repository locks the profile's exact row, checks its current revision, applies existing validation and writes configuration in one transaction. URL replacement preserves type restrictions, destination validation and encrypted URL binding. URL changes and re-enabling retain the existing failure-reset behavior. The write does not copy secret, type or delivery outcome fields from an editor. The reviewed server-owned revision trigger remains required and covers provider bookkeeping and bridge writers; those changes can conservatively invalidate an editor. Legacy writers may still change the row after this operation commits. A successful response is an observation, not a lease.

The settings editor retains the row selected when opened, including its original validator. The enabled toggle uses the displayed row's validator. Invocation captures a copy of the input and current authority before a possible offline pause. The client sends once without authentication replay, refuses stale dispatch/receipts and invalidates only the captured webhook list after success. It never reads a newer validator to rebase or automatically retries a conflict or uncertain result. The recorded natural-idempotent classification describes setting configuration values; it does not waive the required original precondition. No provider delivery is sent by this operation. The bridge update wire contract remains unchanged.

### Rotate an administrator server-channel signing secret

`POST /api/v2/admin/notifications/server-channels/{id}/rotate-secret` (`rotateAdminNotificationServerChannelSecret`) requires acting-admin authority and the demo guard. It targets the exact global channel ID. Only generic channels have signing secrets; Discord channels return `422`, missing IDs `404`, and unavailable service `503`. Success returns `200` with one-time `signing_secret` and `Cache-Control: no-store`, encrypted at rest with the existing server-channel-specific associated-data binding.

The v2 write updates only the encrypted secret and timestamp. It preserves channel configuration, disabled/failure state and row-local watermark/last-attempt/outcome bookkeeping. It introduces no attempt table, revision protocol or provider send. The bridge rotation remains unchanged. Replacement is immediate with no dual-acceptance window or recoverable receipt; later rotations or legacy full-row configuration writes can supersede the returned secret, and in-flight delivery may use a previously captured secret.

The operation is non-retryable. The existing administrator action captures ID and authority at invocation, refuses stale dispatch/receipt/disclosure, and sends once without authentication replay. It invalidates only the captured administrator channel-list cache after success. Uncertainty never triggers an automatic read, rebase, retry or compensating rotation. This operation does not establish a secret lease or fleet rollout readiness.

### Update administrator server-channel configuration

`PUT /api/v2/admin/notifications/server-channels/{id}` (`updateAdminNotificationServerChannel`) requires acting-admin authority and the demo guard. It updates the exact global channel ID. Name, URL, enabled state and event flags are optional; omitted values stay unchanged. Type and signing secret are not configuration fields. Success returns `200` with the stored channel; missing ID returns `404`, invalid configuration `422`, and unavailable service `503`.

The v2 repository reads the current row under a lock and commits configuration and any required dispatch reset together. It excludes signing secret and type from its write. Existing URL validation, type restrictions and channel-bound encryption remain. Replacing the URL, or moving a stopped/auto-disabled channel into enabled delivery, clears failure backoff and last-attempt state and moves the watermark to the present, avoiding replay of the stopped interval. As in the bridge, editing an enabled but auto-disabled channel also resumes it. A disable or an already-delivering enable leaves the watermark unchanged. Reset time is sampled after any row-lock wait, so an old transaction start cannot rewind the reset watermark. Provider outcome fields remain intact.

This is a non-retryable operation: resubmitting a URL replacement advances the watermark again and can skip intervening events. There is no conditional revision contract for this administrator row. The lock protects the transaction; it does not reject stale editor observations or prevent later bridge/configuration writes. The actual editor and enabled toggle capture a copy of input and authority at invocation, send once without authentication replay, refuse stale dispatch/receipt/cache/error publication and invalidate only the captured administrator list after success. No automatic read, rebase, retry or compensating write follows uncertainty.

The bridge update wire and behavior remain unchanged. No provider delivery is sent by the update, and already-dispatched work cannot be recalled. No attempt table, secret lease, fleet rollout or enrollment guarantee is introduced.

## Ordered Apple registration storage

Apple registration ordering uses a separate retained installation record. Its
positive generation, canonical intent digest and installation-key hash are
committed atomically with the APNs device row. The digest binds the account,
profile, token, environment, topic and push mode. Bootstrap cannot take over an
existing registration belonging to another account. Subsequent writes require
the original installation proof; older generations and changed intents at the
same generation fail without replacing the registration.

Exact replay reads the current referenced row. It preserves a provider-disabled
row and reports a missing row as removed, without restoring it or advancing the
generation. This is required because Apple clients renew display credentials
while registration intent remains unchanged. Storage alone does not issue or
validate a display credential; the HTTP caller must validate current login and
profile authority and refuse credential issuance for a stale or removed intent.

A newer generation creates a fresh device row identity. Late provider results
addressing the retired row cannot disable the replacement. Existing foreign-key
cascades retire its stored delivery attempts; a request already dispatched to a
provider cannot be recalled. Modes `off` and `in_app_only` retain existing row
and enabled-field semantics; delivery eligibility continues to check push mode.
Profile cleanup deletes device rows but retains installation ordering metadata.

The migration must precede deployment of guarded writers. Every serving node
must run the guarded Apple bridge upsert and generic bridge deletion before
ordered Apple adoption; adopted installations reject those legacy writes.
Generic deletion locks Android ordering before Apple ordering. The migration's
Down operation is not an online rollback protocol. This storage prerequisite
does not introduce an Apple v2 endpoint, native adoption or rollout authorization.

## Apple registration and display renewal

`GET /api/v2/devices/push/apple/capabilities` reports `ordered_apple_v1` and local
`registration_available`. Availability does not assert provider delivery or that
all serving nodes have completed the guarded-writer rollout.

`POST /api/v2/devices/push/apple` requires a current expiring access login session,
a verified selected profile (including its PIN proof where required), and the
existing demo write guard. API keys, display credentials and sessionless access
are not registration authority. The server checks current session, account and
profile policy before registration and again after any database lock wait.

Send `X-Push-Installation-Key` as a private canonical base64url 32-byte credential
and `X-Push-Generation` as a positive canonical decimal int64. Persist both before
sending. Keep the installation credential across account/profile switches and
increment generation only for a new registration intent. Capture the account,
profile and body with that intent. After uncertainty, replay the exact original
packet; do not automatically read, rebase or advance its generation. Neither
credential belongs in logs or URLs.

The JSON body contains `device_id`, `apns_token`, `apns_environment` (`production`
or `sandbox`), `apns_topic` (`org.siloserver.silo`) and optional `push_mode`
(`off`, `in_app_only`, `private_push`; existing default applies). Success returns
string `generation`, `id`, `server_device_id`, `push_mode`, `enabled` and `removed`.
An existing disabled or deleted registration remains so on exact replay. A stale
or changed same-generation intent returns409; invalid installation proof returns403.

For a current enabled registration, success may include `display_token` and UTC
`display_token_expires_at`. An unchanged accepted intent can renew these fields
without advancing generation or rewriting registration state. Credential issuance
runs under the installation and device transaction locks, so a newer registration
cannot overtake it. No credential is returned on transaction failure. Disabled or
removed receipts omit credentials. A signing failure still returns the accepted
registration without credentials, preserving the existing extension access-token
fallback. Responses are `no-store`.

The display credential retains its existing account/login-session/profile scope
and lifetime; this operation does not introduce device-bound display authorization
or revoke previously issued display credentials on registration replacement.
Current registration is guaranteed at issuance, not for the credential's entire
lifetime. Native code must retain its authority fence before storing a response,
clear stale display credentials for disabled/removed state, and preserve the
original registration packet for explicit renewal/retry. Native adoption requires
its own reviewed persistence and lifecycle implementation; this server packet does
not enable it. The browser has no Apple-registration caller.

## Durable email verification storage prerequisite

The retained address-request route still uses the bridge flow. Its v2 port
requires durable dispatch: replacing a pending token and sending inline would
invalidate an earlier link and send again after a lost HTTP response.

The email repository now supports a client-created verification intent UUID,
bound to its original account, profile and normalized address. Admission locks
the profile preferences row (creating the default row transactionally when
absent), then commits both the pending token hash and an encrypted verification
message in `notification_email_verifications`. The message contains the original
recipient, rendered content and verification link; encryption is bound to the
intent UUID. Plaintext bearer links are not stored in the outbox.

Exact replay returns the retained intent receipt before applying rate limits. It
never replaces the pending hash, regenerates the message, extends expiry or
consumes another rate allowance. A changed address or owner under the same UUID
conflicts. The receipt's `Current` value is false after expiry, clear, successful
verification or replacement by another intent. It describes pending-state
membership, not whether SMTP accepted a message. Server name and link-base changes
do not rewrite an already-admitted message.

A new intent preserves the one-minute minimum interval and ten-admissions-per-UTC-day
limit. Address ownership is checked at admission and remains authoritative at
verification through the existing uniqueness check. Current verified destination
and notification mode stay unchanged while a new address is pending. Clear and
verification keep their existing effects; retained intent rows prevent their old
requests from recreating pending state.

The migration must precede guarded writers on every serving node. After a profile
has admitted a durable intent, the bridge's inline request path returns
`email_verification_upgrade_required` rather than overwriting its pending token.
Unadopted profiles retain the bridge behavior. This prerequisite does not wire a
v2 route, web caller or sender, and must not be activated without the dispatch and
caller work. The table's Down operation is not an online rollback protocol.

Future dispatch must check current pending hash, expiry, account/profile authority
and message ownership under its claim before sending. Queued work cannot authorize
a send after clear, replacement or profile deletion. Retained receipts must survive
payload cleanup; no retention or worker lease policy is introduced here. The outbox
currently has no dispatch state, automatic retries or recovery worker.

SMTP has no provider idempotency key and a failed connection cannot always establish
whether the server accepted the message. Durable admission therefore does not imply
exactly-once delivery. HTTP success describes queued admission, not delivery.

## Email verification dispatch

Each outbox row carries a dispatch state (`queued`, `sending`, `delivered`,
`failed`), an attempt count, the claim time, completion time and last error. A
dispatcher runs on every serving node that has a mail sender and an at-rest
cipher. It wakes after admission and once a minute, first marking unclaimable
rows failed, then claiming rows one at a time with `FOR UPDATE SKIP LOCKED`.
A claim sets `sending`, stamps the claim time and increments the attempt count
before anything reaches the provider, so a crash after hand-off is counted.

Under the claim the dispatcher re-reads the profile row and refuses to send when
the profile is gone, its owner changed, the pending hash was cleared or replaced,
the link expired, or the payload was retired. Those rows fail without a hand-off.
Otherwise it decrypts the retained message, sets an RFC 5322 `Message-ID` derived
from the intent UUID (SMTP has no idempotency key; this is the closest equivalent
and is constant across attempts), hands the message to the sender and records
`delivered`. A provider error requeues the row with the error text.

Uncertain outcome policy (decided 2026-09-07): a row still `sending` after its
three-minute claim lease is an uncertain send, meaning the worker or node died
between hand-off and record. It is retried exactly once with the same message,
link and `Message-ID`, and the second attempt is recorded with its attempt count.
A duplicate email after a crash is accepted; a lost verification is not. There
is no hold-for-manual-resend path. Provider rejections follow the same bound: two
hand-offs total, one lease apart, then `failed`. A `failed` dispatch never changes
the admission receipt; the intent remains `current` until it expires, is cleared,
verified or replaced, and the client's only remedy is a new intent after the rate
window. Retired rows use `current=false`.

Retention runs with the notification step of the `database_maintenance` task: the encrypted payload is
dropped from `delivered`/`failed` rows once the link has expired, and the receipt
row itself is deleted thirty days after expiry. Receipts outlive payloads so an
exact replay of an old UUID still answers `current=false` rather than admitting
a new message.

`dispatch_available` in the capability is true when the dispatcher is wired and
the mail sender is configured. It describes hand-off capacity, not delivery.

## Queued email verification caller

`PUT /api/v2/notifications/email-preferences/address` accepts an `email` and
client-created `verification_id` UUID under the existing authenticated profile and
demo guards. The service requires a current non-child profile. A new intent needs
a configured HTTP(S) external link base without credentials, query or fragment;
an exact admitted replay keeps its original link even if configuration disappears.

The200 response is a durable admission receipt: `verification_id`, UTC
`expires_at`, and `current`. It is not SMTP acceptance or delivery. `current=false`
means the original pending verification has expired, been cleared or verified, or
been replaced; it does not initiate a resend. The response contains neither the
bearer token nor the email message. Conflict returns409, admission rate limits429,
invalid input422, denied profile403 and unavailable storage503.

`GET /api/v2/notifications/email-preferences/address/capabilities` reports
an opaque `revision`, support `state`, effective caller `allowed`, and separate
`queue_available` and `dispatch_available` flags. Demo mode sets `allowed=false`
for non-admin accounts while leaving configured support available; administrators
retain the demo exception and still need a non-child profile. The response uses
`Cache-Control: private, no-cache` and an `ETag`. Revalidation repeats permission
checks, so a demo-mode change that changes `allowed` returns a new revision and
ETag instead of 304.
`dispatch_available` follows the dispatcher and sender state described under
"Email verification dispatch"; the ledger row is ratified with the accepted
duplicate-after-crash retry policy recorded in its retry note. No native Apple or
Android caller for the address request exists; both lanes' earlier inventories
recorded exact absence and that closure is tracked separately from this server
and web packet.

The settings web caller captures the email, UUID and render authority synchronously
before a mutation can pause offline. An uncertain response retains that exact draft
for explicit retry while the hook remains mounted. Changing the email or authority
creates another intent; a known receipt completes a draft, and a later explicit
request creates a new one. There is no automatic authentication replay, rebasing or
resend. Drafts are not persisted across page reload or unmount; this packet makes
no browser-crash recovery guarantee.

Dispatch, cache invalidation, toast and component callbacks require the original
acting authority and current mounted draft. The receipt invalidates only that
profile's email-preferences query rather than asserting a complete preferences
snapshot. The form reports queued/pending verification and prevents editing the
submitted address while its request is pending. It does not claim an email was
sent. The dispatcher and retention policy above close the worker uncertainty and
receipt/payload retention items; the migration must precede dispatching nodes.
