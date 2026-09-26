# Apple Push Display Token (client integration guide)

The iOS Notification Service extension turns a generic APNs alert into the
real notification text by fetching compact display metadata from the server.
The extension runs in its own process and cannot refresh the app's short-lived
access token, so the server issues it a separate long-lived credential at
push registration.

> **API lifecycle:** the operations below are the stable `/api/v2` native contract,
> which locks with Silo 1.0. The frozen alpha `/api/v1` surface serves the same
> Apple display-token flow at the matching `/api/v1` paths through the pre-1.0
> bridge window, after which Silo answers the whole `/api/v1` namespace with
> `410 Gone` and the `client_upgrade_required` problem code. See
> [the native API contract](architecture/api-contract.md).

Android is unaffected: it fetches `GET /api/v2/notifications/{id}` through the app's
normal auth path, which refreshes on `401`.

## Discovery

```
GET /api/v2/notifications/capabilities
```

```json
{
  "apple_push": {
    "available": true,
    "provider": "silo_relay",
    "supported_modes": ["private_push", "in_app_only"],
    "display_token": true
  }
}
```

`apple_push.display_token` is `true` when Apple push registration returns a
display token. The field is optional and absent means false. `android_push` never
carries it.

## Registration

```
POST /api/v2/devices/push/apple
Authorization: Bearer <access token>
X-Profile-Id: <profile>
X-Push-Installation-Key: <installation secret>
X-Push-Generation: <positive decimal int64>
```

`registerApplePushDevice` takes an `ApplePushRegistrationBody` (`device_id`,
`apns_token`, `apns_topic`, `apns_environment`, and optional `push_mode`) and the
ordered-installation headers `X-Push-Installation-Key` and `X-Push-Generation`,
whose format and ordering rules are described under
[ordered Android registration](#ordered-android-registration). It returns `200` with an
`ApplePushRegistrationReceipt`. When the server can mint one, the receipt carries
the display credential:

```json
{
  "id": "01M...",
  "generation": "3",
  "server_device_id": "...",
  "enabled": true,
  "removed": false,
  "push_mode": "private_push",
  "display_token": "<jwt>",
  "display_token_expires_at": "2026-10-03T00:00:00Z"
}
```

- `display_token` is a JWT with `token_type: "apple_push_display"`, bound to
  the registering user, login session, and profile.
- Its lifetime follows the server's refresh-token expiry (30 days by
  default). Re-register before `display_token_expires_at` to renew it; the
  Apple client re-registers a week ahead.
- Both fields are omitted when the server cannot mint a token. Clients must
  fall back to the access token in that case.
- Registration with an API key (`sa_` prefix) never returns a token: there is
  no login session to bind.

Store the token where the extension can read it (the Apple client uses the
shared Keychain access group) and clear it on sign-out, server removal, or
profile change. The server revokes it implicitly when the login session is
revoked or expires.

## Display fetch

```
GET /api/v2/notifications/push/apple/display/{delivery_id}
Authorization: Bearer <display token or access token>
```

`getNotificationApplePushDisplay` returns a compact `NotificationPushDisplay`:

```json
{
  "delivery_id": "01M...",
  "title": "The latest episode of Severance S02E01 just dropped!",
  "body": "Hello, Ms. Cobel",
  "thread_id": "series:series-1",
  "category": "episode_available",
  "url": "/item/episode-1"
}
```

Authentication rules for this route only:

| Credential | Behavior |
|---|---|
| Display token in `Authorization` header | Accepted. The profile comes from the token's claims; `X-Profile-Id` and `X-Profile-Token` are ignored. PIN verification is skipped because the token was issued to an already verified profile session. The login session must still be valid, and the profile must still exist and belong to the user (a deleted profile's token returns `404`). |
| Display token in `?token=` query | Rejected with `401`. Long-lived credentials must not appear in URLs. |
| Access token | Accepted through the normal chain: `X-Profile-Id` is required and PIN-protected profiles need `X-Profile-Token`, as before. |
| Refresh token or API key | `401` / normal API key handling; neither is a display credential. |

`404` is returned when the delivery does not belong to the
authenticated profile. The route is rate-limited like other authenticated
routes.

## Ordered Android registration

`GET /api/v2/notifications/push/devices/capabilities` describes the
`ordered_android_v1` registration contract and whether local registration storage
is configured. It requires the acting login/profile. `registration_available`
is independent of relay delivery availability; use notification capabilities
for delivery configuration. Apple registration and browser web push remain
separate contracts.

`POST /api/v2/notifications/push/devices` takes `device_id`, `platform: "android"`,
`token`, and optional `push_mode` (`private_push` by default, `in_app_only`, or
`off`). Both POST and `DELETE /api/v2/notifications/push/devices/{device_id}`
require these headers in addition to captured login/profile credentials:

- `X-Push-Installation-Key`: a cryptographically random 32-byte secret encoded as
  unpadded base64url (43 characters). Persist privately for this server and device
  installation. Keep it across account/profile switches; never put it in a URL,
  log, settings sync, or receipt.
- `X-Push-Generation`: a positive canonical decimal int64 string. Persist a
  strictly increasing generation for each new registration/removal intent across
  account/profile switches. Serialize allocation before dispatch and retain the
  exact generation, payload and captured authority through uncertainty. Never
  allocate a new generation merely to retry a request.

POST returns `200` with string `generation`, `registration_id`, `server_device_id`
and the accepted `push_mode`. This acknowledges registration storage, not provider
acceptance or successful delivery. DELETE returns bodyless `204`. The latest
exact command may replay with the same receipt and no device rewrite, re-enable,
last-seen refresh, or provider request. A lower generation or same generation with
a different account/profile/token/mode/verb returns `409`. A newer registration
may transfer this installation to another authenticated account/profile using
its existing installation key. Removal must still belong to the current
account/profile; stale removal cannot delete a later registration. Invalid
installation proof returns `403`; malformed fields/generation return `422`.

Bootstrap may bind an unclaimed installation or one whose legacy Android rows
belong to the acting account. It cannot take over a legacy row belonging to a
different account. Initial removal additionally requires the existing profile.
Bind while the existing account is selected; do not resolve a conflict by
silently changing ownership or replaying under another account. Losing the
installation secret, resetting the generation counter, or exhausting int64 has
no automatic recovery/rebinding path in this contract. Native persistence must
handle that state explicitly without sending guessed replacement credentials.

The installation authority, last intent and generation survive device-row and
profile deletion. Removal keeps a tombstone. Changed registrations receive new
device-row identities and retire old pending/retry attempts transactionally.
Provider results update only their original device-row ID, so a delayed terminal
failure cannot disable its replacement. Existing sender retry/delivery behavior
for a current registration is unchanged. A provider call already in flight cannot
be unsent; this contract does not guarantee immediate remote-delivery revocation.

Once an installation adopts v2, bridge Android registration and generic device
removal for that installation return `409 push_registration_upgrade_required`.
Unadopted installations keep their existing bridge behavior. Deploy the migration
and guarded writer code to every API node before activating ordered native
registration; older binaries that do not acquire the installation lock are not
safe concurrent writers. No existing installation is enrolled by the migration.
Native consumer adoption and its independent review remain required before the
two existing registration/removal mappings can be ratified.

### Remove a browser subscription by row ID

`DELETE /api/v2/notifications/web-push/subscriptions/{id}` (`deleteNotificationWebPushSubscription`) requires authenticated profile authority and removes only the matching profile's server registration. It returns bodyless `204`, including for absent or foreign-profile IDs. Repeating deletion naturally converges; a concurrent subscription is an opposing write, with no generation-order guarantee. Missing web-push storage returns `503`.

The settings list action captures account/profile/PIN authority, sends once without authentication replay, and refreshes only its exact scoped cache after a successful current-authority response. It does not call browser unsubscribe, revoke permission, or cancel a provider call already in flight. Browser subscription creation and endpoint unsubscribe retain their separate lifecycle; Android installation credentials and Apple display credentials do not apply to this operation.

### Unsubscribe this browser by endpoint

`POST /api/v2/notifications/web-push/unsubscribe` (`unsubscribeNotificationWebPush`) takes `{ "endpoint": "<browser endpoint>" }` under authenticated profile authority and returns bodyless `204`. Removal uses the account and endpoint, including a subscription reassigned to a sibling profile; it cannot remove another account's registration. An absent endpoint row returns `204`, invalid input `422`, and unavailable storage `503`.

This operation is **non-retryable**: a delayed repeat can remove a newer registration for the same endpoint. It has no generation/tombstone protocol. The browser captures authority before discovery, sends once without authentication replay, and waits for successful server removal before invoking local unsubscribe. HTTP failure or uncertainty leaves the local subscription discoverable; no automatic retry or reconciliation is claimed. An explicit later disable is a new user decision against current state. Authority replacement prevents subsequent local unsubscribe and scoped UI publication. Browser-side failure after server success is surfaced; already-running browser/provider operations cannot be cancelled by this authority check. Server removal does not revoke browser permission or cancel an external provider send already in flight.

### Register this browser

`POST /api/v2/notifications/web-push/subscriptions` (`subscribeNotificationWebPush`) takes the browser's `endpoint`, `keys.p256dh`, `keys.auth`, and optional `device_name`. Authenticated profile authority is required. The existing service validates a public HTTPS endpoint and registers or reassigns it to the current profile. `201` returns the subscription view used by the list; encryption keys remain write-only. Invalid input returns `422`, unavailable storage `503`. The request body is bounded to 16 KiB.

Registration is **non-retryable**. The existing endpoint upsert may re-enable delivery or replace a newer profile/token intent; it is not an ordered installation protocol. Existing provider callbacks and registration identity behavior remain unchanged, including the absence of generation-based late-callback isolation. The response describes stored registration state, not successful delivery. Android installation keys/generations and Apple display-token renewal do not apply.

The browser captures authority before permission and checks it after each service-worker/subscription await and HTTP response. It sends once without authentication replay and publishes only under the captured authority. An already-issued browser or server operation cannot be undone by these checks. Failure or uncertainty does not trigger automatic retry, local unsubscribe, or compensating server removal; such compensation could remove a newer intent. Browser permission and local subscription may therefore remain after a failed registration.
