# Apple Push Display Token (client integration guide)

The iOS Notification Service extension turns a generic APNs alert into the
real notification text by fetching compact display metadata from the server.
The extension runs in its own process and cannot refresh the app's short-lived
access token, so the server issues it a separate long-lived credential at
push registration.

> All endpoints are under `/api/v1`. Android is unaffected: it fetches
> `GET /notifications/{id}` through the app's normal auth path, which refreshes
> on `401`.

## Discovery

```
GET /notifications/capability
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
display token. It is omitted (false) on servers without JWT auth or older
servers. `android_push` never carries the field.

## Registration

```
POST /devices/push/apple
Authorization: Bearer <access token>
X-Profile-Id: <profile>
```

The request body is unchanged. When the server can mint one, the response
gains two additive fields:

```json
{
  "id": "01M...",
  "server_device_id": "...",
  "enabled": true,
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
GET /notifications/push/apple/display/{delivery_id}
Authorization: Bearer <display token or access token>
```

Returns the same compact `NotificationDisplay` payload as before:

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

`404 not_found` is returned when the delivery does not belong to the
authenticated profile. The route is rate-limited like other authenticated
routes.
