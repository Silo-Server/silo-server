# Canonical Settings API

The canonical settings API stores typed user preferences from the shared
settings contract. Client implementations should discover the server contract
before rendering controls or writing a value; do not keep a separate list of
keys, scopes, enum members, or defaults.

All paths below are relative to `/api/v1`.

## Contract discovery

| Method and path            | Purpose                                                                                                 |
| -------------------------- | ------------------------------------------------------------------------------------------------------- |
| `GET /settings/manifest`   | Public client projection of the current manifest. Supports `If-None-Match`.                             |
| `GET /settings/capability` | Contract API version, revision, remote scopes, supported client families, and batch/write capabilities. |

`/settings/contract` and `/settings/contract/capabilities` are equivalent
aliases. A client whose vendored contract is newer than the advertised server
revision must hide definitions and features introduced after that revision.
Navigation shortcut mutation controls additionally require
`supports_atomic_shortcuts: true`. Revision-5 customization also requires
`supports_batched_effective: true` and `supports_idempotent_writes: true` so
batched resolution and replayed writes have the semantics the clients depend
on. Any missing flag fails closed.

## Request identity headers

Authenticated settings routes use the active account and profile from the
normal Silo session. The contextual headers below identify which client is
resolving or writing an override.

| Header                 | When required                                                                                               | Meaning                                                                                                                                     |
| ---------------------- | ----------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `X-Profile-Id`         | All `/settings/values` routes                                                                               | Active household profile.                                                                                                                   |
| `X-Silo-Device-Id`     | A `profile_device` explicit request, or an effective request containing a key that permits `profile_device` | Stable exact-device identity.                                                                                                               |
| `X-Silo-Client-Family` | Required for a `profile_client` explicit request; optional on effective reads                               | Closed like-client identity: `tv`, `mobile`, `tablet`, `desktop`, or `web`. A valid value includes the like-client layer; absence skips it. |
| `X-Silo-Mutation-Id`   | Optional on `PUT`                                                                                           | Idempotency key for safe retries. Reusing it for a different identity or value returns a conflict.                                          |

Client family is intentionally independent of the free-form
`X-Silo-Device-Platform` display metadata. The server never guesses one from
the other. Send the exact lower-case family:

| Client                  | Family    |
| ----------------------- | --------- |
| tvOS or Android TV      | `tv`      |
| iPhone or Android phone | `mobile`  |
| iPad or Android tablet  | `tablet`  |
| macOS                   | `desktop` |
| Browser                 | `web`     |

### App identity headers

Separately from the family, every first-party client should send its own app
identity on playback requests. These are server-wide contextual headers, not
settings-specific: the playback session stores them, the admin Activity page
renders them ("Silo Android TV 1.0.0 (build 5)"), and playback decision logs
carry them so a report can be tied to an exact build.

| Header                  | Clamp | Meaning                                                                                                       |
| ----------------------- | ----- | ------------------------------------------------------------------------------------------------------------- |
| `X-Silo-Client`         | 128   | Product name, e.g. `Silo Android TV`, `Silo iOS`.                                                             |
| `X-Silo-Client-Version` | 64    | Marketing version, e.g. `1.0.0`. Sent verbatim and displayed verbatim — do not pre-shorten it.                 |
| `X-Silo-Client-Build`   | 64    | Opaque per-platform build identifier (Android `versionCode`, Apple `CFBundleVersion`). Never parsed or compared. |
| `X-Silo-Client-Channel` | 32    | Opaque distribution channel: `release`, `beta`, `sideload`, `dev`. Stored verbatim; `release` is not displayed.  |

Values are trimmed and truncated to the clamp above — never rejected, on either
route, because an identity label must not be able to fail a playback start. The
clamp counts characters, not bytes, matching `maxLength` in the v3 request
schemas, and is applied where the request is read rather than where the session
is created so the decision logs and `playback_route_events` observe it too.
Nothing is validated against an enum either, so a client may introduce a new
channel without a server change.

Protocol-v3 `POST /playback/start` accepts `client_playback_context.app_version`,
`.app_build`, and `.app_channel` as a body-level fallback for clients that cannot
set the headers on every request. The headers win field by field when both are
present, and the fallback applies **only to a client that sent `X-Silo-Client`**:
`client_playback_context` carries no app name, so nothing in the body can
identify a client that did not name itself — such a session is labeled from its
user agent, and its `app_version` is a free-form platform string rather than the
marketing version `client_version` promises.

## Remote scopes

Every stored value has exactly one identity. Context fields not named by the
selected scope must be absent.

| Scope             | Identity after account        |
| ----------------- | ----------------------------- |
| `account`         | none                          |
| `profile`         | `profile_id`                  |
| `profile_client`  | `profile_id`, `client_family` |
| `profile_device`  | `profile_id`, `device_id`     |
| `profile_library` | `profile_id`, `library_id`    |
| `profile_series`  | `profile_id`, `series_id`     |

The manifest's `allowed_scopes` decides where each key may be written, and its
`resolution_order` decides precedence. `profile_client` values roam only among
like clients. For example, a TV value applies to tvOS and Android TV but not to
a phone or browser.

## Explicit values

Use explicit endpoints to edit or clear one scope, without resolving inherited
values:

- `GET /settings/values?keys=<csv>&scope=<scope>` returns every requested key
  with `is_set`; unset rows remain in the response.
- `GET /settings/values/{key}?scope=<scope>` returns one stored value or `404`.
- `PUT /settings/values/{key}?scope=<scope>` accepts `{"value": <typed JSON>}`.
- `DELETE /settings/values/{key}?scope=<scope>` removes the row so inheritance
  applies again.

`library_id` and `series_id` are query parameters for their matching scopes.
An explicitly managed device may be named with `device_id`, subject to the
profile/device authorization checks. The self-service `profile_client` scope
takes its family only from `X-Silo-Client-Family`.

Example: share a TV menu between tvOS and Android TV clients.

```http
PUT /api/v1/settings/values/nav.primary_menu?scope=profile_client
Authorization: Bearer <token>
X-Profile-Id: <profile-id>
X-Silo-Client-Family: tv
Content-Type: application/json

{"value":{"items":[{"type":"builtin","destination":"home"},{"type":"library","library_id":7,"label":"Movies"}]}}
```

Stored-value responses include `client_family` when the source or explicit row
is at `profile_client`.

### Atomic navigation shortcuts

`nav.shortcuts` is a profile-wide catalog shared by TV, mobile, desktop, and
web clients. Self-service clients must mutate one destination at a time instead
of replacing that shared document:

```http
PUT /api/v1/settings/values/nav.shortcuts/item
Authorization: Bearer <token>
X-Profile-Id: <profile-id>
X-Silo-Mutation-Id: <stable-uuid-for-this-intent>
Content-Type: application/json

{"item":{"type":"section","library_id":7,"section_id":"recent","label":"Recently Added"},"present":true}
```

The item is one member of `navigation-shortcuts.json`: a library, section, or
collection (whose `collection_id` is a string and whose `library_id` is
optional). Identity is exactly the schema's semantic identity:

- library: `type + library_id`
- section: `type + library_id + section_id`
- collection: `type + optional library_id + collection_id`

`present: true` appends an absent item or refreshes the label of an existing
identity in place without reordering it. `present: false` removes that identity;
the supplied label is validated but ignored for matching. The server atomically
rebases on concurrent edits, enforces the full schema and 256-item cap, and
returns the normal stored-value object containing the complete resulting
`value`, row `revision`, and `updated_at`. An already-satisfied operation is a
successful no-op and does not increment the revision. Removing from a catalog
that has never been stored returns `{"items":[]}` at revision `0` with no
`updated_at`.

Retry with the same `X-Silo-Mutation-Id`. A recorded retry returns the original
response with `X-Silo-Idempotent-Replay: true`; reusing the id for a different
semantic operation returns `409 mutation_id_conflict`. The mutation ID is
serialized before the setting write, and the setting plus its replay receipt
commit in one database transaction, so concurrent reuse cannot apply twice and
a crash cannot persist only one half. A rare exhausted
contention loop returns retryable `409 setting_update_conflict`. Malformed
envelopes or unknown envelope fields return `400 bad_request`, while item
schema failures (including unknown item fields) and cap failures return
`400 invalid_value`.

Ordinary session `PUT` and `DELETE` at
`/settings/values/nav.shortcuts?scope=profile` are rejected with
`400 atomic_update_required` so a whole-document mutation cannot erase
concurrent item edits or reset the row revision. The admin endpoint retains
whole-document `PUT` for explicit repair work, but rejects physical `DELETE`
for the same revision-history reason. An admin clears the catalog with
`{"value":{"items":[]}}`, which advances the row revision, or replaces it with
another validated document.

## Effective values

- `GET /settings/values/effective?keys=<csv>` resolves several keys for the
  request profile, device, and client family. Omitting `keys` resolves all
  remote definitions.
- `POST /settings/values/effective` resolves a bounded list of content contexts
  in one store read. Use it for grids or lists rather than issuing one request
  per item.

An effective request must include the device header when any requested
definition permits an exact-device override. The family header is optional for
backward compatibility: absence skips the `profile_client` layer, while a
non-empty invalid family is rejected. First-party clients should send their
family so like-device preferences participate in resolution. Each response
includes its source scope and source context; `client_family` is included for a
family-scoped winner.

## Admin projection

Admin routes are mounted behind the normal acting-admin authorization:

- `GET /admin/users/{id}/settings/values` lists every stored row across all
  scopes.
- `PUT /admin/users/{id}/settings/values/{key}?scope=<scope>` writes through the
  same contract validation and normalization as the self-service endpoint.
- `DELETE /admin/users/{id}/settings/values/{key}?scope=<scope>` clears the
  selected row.

Admin requests name profile/context identity with query parameters because the
target user is not the admin's active session. In particular,
`scope=profile_client` requires both `profile_id` and `client_family` query
parameters; it does not use `X-Silo-Client-Family`.

```http
PUT /api/v1/admin/users/42/settings/values/ui.card_presentation?scope=profile_client&profile_id=main&client_family=tv
Authorization: Bearer <admin-token>
Content-Type: application/json

{"value":{"poster_size":"large","caption":"artwork"}}
```

The five accepted `client_family` values are also returned by the capability
endpoint so admin tooling does not need to invent them.
