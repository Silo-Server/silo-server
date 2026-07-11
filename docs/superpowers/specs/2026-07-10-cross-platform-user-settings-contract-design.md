# Cross-platform user settings contract

**Date:** 2026-07-10

**Status:** Draft — coordinated breaking-release design for issue #376

**Scope:** `silo-server`, `silo-apple`, `silo-android`, and the Silo web client

**Tracking:** https://github.com/Silo-Server/silo-server/issues/376

> Commands and paths in this document are repository-relative; assume the relevant repository root
> is the cwd. Cross-repository references are prefixed with the repository name.

## Decision

The server repository owns the canonical contract for every **production, user-facing setting**.
That is true even when the value is intentionally stored only on one client. A client PR must not
invent a production setting key, type, default, range, or scope independently.

There is one narrow exception: a client may add a private implementation, diagnostics, or
experimental knob without a server PR when all of the following are true:

1. Its key is in `local.<client>.<domain>.<name>` (for example,
   `local.apple.player.decoder_logging`).
2. It is not shown as a normal production setting.
3. It is never sent to any Silo API.
4. It is not expected to roam, survive reinstall, appear in admin UI, or have shared semantics with
   another client.
5. Promoting it to a production feature requires adding it to the shared contract first.

This gives clients freedom for genuine local implementation details without allowing the public
settings model to drift again.

## User-visible behavior

The contract makes persistence visible and predictable:

| Setting scope | New browser/incognito session | Another signed-in client | Reinstall | Admin-visible |
|---|---:|---:|---:|---:|
| Account | Yes | Yes | Yes | Yes |
| Profile | Yes | Yes | Yes | Yes |
| Profile + device override | Profile default only | Profile default only | Profile default only unless the device identity is restored | Yes |
| Profile-device only | No; a new browser is a new device | No | No unless the device identity is restored | Yes |
| Client-local | No | No | No unless the client explicitly uses OS-backed backup | No |

Therefore, signing into an incognito window must carry profile language, subtitle behavior, and any
profile-level subtitle appearance. It must not copy ordinary-browser device overrides. The
incognito window gets a new device identity and resolves those settings from the profile fallback.

The UI must use these exact scope descriptions:

- **All devices for this profile** — profile value that roams after sign-in.
- **This device/browser** — override tied to the active profile and device identity.
- **Only this app/device** — client-local value that is never uploaded.

Avoid ambiguous labels such as “global,” “default,” or “remember this” without naming what the
value follows.

## Why this is needed

The current implementation has three partial contracts:

- `silo-server: internal/api/handlers/settings.go` owns validation, defaults, and a `user` versus
  `device` registry, but unknown user keys are accepted and values are strings.
- `silo-server: web/src/lib/settingsManifest.ts` independently owns labels, controls, defaults,
  enum options, and numeric ranges.
- Apple and Android independently own raw key constants, defaults, parsing, and local migration
  behavior.

That duplication has produced verified drift:

- Apple writes `playback.audio_language`, but playback selection reads the profile language; the
  device value currently has no effect.
- Android uses `player.next_up_prompt_seconds` while the server and Apple use
  `playback.next_up_prompt_seconds`.
- Android permits playback speed up to `4.0`; the server contract permits `3.0`.
- Android defaults `player.dv_profile7_hdr10_fallback` to `true`; the server and Apple default it to
  `false`.
- Android contains device-setting keys the server does not register.
- Apple queues failed writes only in memory and keys them only by setting key, so process death
  loses pending work and a profile/server switch can redirect a retry.
- Android removes pending writes before the server accepts them and only logs failures.
- Profile columns and device settings represent some of the same user intent but use separate API
  and resolution paths.

The web client also has useful precedent to preserve: owner-tagged cached date/time settings avoid
showing one account's cached values to another account. Theme and custom-style caches need the same
ownership rule.

### Verified baseline

This design was checked against these repository heads:

| Repository | Commit |
|---|---|
| `silo-server` | `3fd0912cb3fe15cc364f3dd04095c2e39db0bef0` |
| `silo-apple` | `120f493593119e71dfb1247dde0f89c55d46c1d0` |
| `silo-android` | `5c6439cebe753103c3a12cca7d1d152c5d6e35ab` |

## Goals

1. One machine-readable definition for every production setting.
2. Native JSON value types instead of stringly typed values on the new API.
3. Explicit storage scopes and per-setting resolution order.
4. Compile-time key/type wrappers for Swift, Kotlin, and TypeScript.
5. Strict rejection of unknown remote keys and invalid values.
6. One coordinated server/client cutover with a one-time data migration.
7. Durable, profile-safe native synchronization.
8. Clear UX explaining what roams and what remains on a device.
9. A small, documented escape hatch for client-private knobs.

## Non-goals

- Replacing server-admin configuration in `server_settings`.
- Turning the settings manifest into a generic remote-form engine for every screen.
- Synchronizing secrets, credentials, tokens, or filesystem paths as user preferences.
- Giving an admin silent control over client-local values.
- Making every setting available on every platform.
- Preserving accidental key names, old string wire formats, or incorrect defaults as canonical
  behavior.
- Supporting old apps against the new server or new apps against an old server.

## Terminology

- **Definition** — the canonical key, type, constraints, scopes, defaults, resolution, and UX
  metadata for one setting.
- **Stored value** — an explicit value at one allowed scope.
- **Unset** — no explicit value at that scope. This is distinct from `false`, `0`, `""`, and
  JSON `null`.
- **Effective value** — the first stored value found in the definition's resolution order, or the
  contract default.
- **Override** — a more specific stored value that wins over a broader fallback.
- **Contract-known local** — a production user-facing setting defined by the shared contract but
  persisted only by the client.
- **Private local** — a non-production implementation or diagnostics knob outside the shared
  contract.

## Ownership classes

Every setting definition declares one persistence class:

| Persistence | Contract PR required | Server stores value | Sent to API | Intended use |
|---|---:|---:|---:|---|
| `remote` | Yes | Yes | Yes | Roaming values and server-known device/profile overrides |
| `client_local` | Yes | No | No | Production OS/device behavior with shared, reviewed semantics |
| Private `local.*` | No | No | No | Diagnostics, implementation details, temporary experiments |

A setting that is visible in the production Settings UI is contract-owned. A setting implemented
by two or more clients is contract-owned. A setting expected to survive sign-in on a new client is
`remote`.

## Canonical contract artifact

The source of truth lives in `silo-server`:

```text
contracts/settings/v1/
├── manifest.schema.json
├── manifest.json
└── schemas/
    └── subtitle-appearance.json
```

- `manifest.schema.json` validates the contract format.
- `manifest.json` contains definitions and is embedded by the server.
- Object-valued settings use a named JSON Schema under `schemas/`.
- Server tests load the manifest and fail on duplicate keys, invalid defaults, invalid resolution
  chains, or missing schemas.
- `GET /api/v1/settings/manifest` serves this exact public artifact, excluding internal storage
  bindings.
- `ETag` is the SHA-256 digest of the canonical JSON bytes.

The API version and contract revision are separate:

```json
{
  "api_version": 1,
  "revision": 12,
  "definitions": []
}
```

- `api_version` identifies the settings protocol expected by this coordinated release.
- `revision` is a monotonically increasing integer changed by every manifest PR.
- Adding a key is additive.
- After this cutover, a key's value type, persistence class, meaning, or allowed scope is immutable.
  A later incompatible change requires another explicit contract-version cutover or a new key.
- Tightening a numeric range or enum requires a migration for every previously valid stored value.
- Changing a default requires explicit release notes because it can alter behavior without a stored
  value changing.

## Definition model

The public definition is a tagged, typed record:

```json
{
  "key": "playback.audio_language",
  "introduced_in": 1,
  "persistence": "remote",
  "allowed_scopes": ["profile", "profile_device", "profile_library", "profile_series"],
  "resolution_order": ["profile_series", "profile_library", "profile_device", "profile", "default"],
  "value_schema": {
    "type": "language_tag",
    "nullable": true
  },
  "default_value": null,
  "platforms": ["web", "ios", "tvos", "macos", "android", "android_tv"],
  "category": "playback",
  "label": "Preferred audio language",
  "description": "Choose which spoken language Silo should prefer first.",
  "deprecated": false
}
```

Required fields:

| Field | Rule |
|---|---|
| `key` | Lowercase dot-separated identifier. Canonical names do not encode a platform. |
| `persistence` | `remote` or `client_local`. |
| `allowed_scopes` | Non-empty and valid for the persistence class. |
| `resolution_order` | Contains every remote scope at most once and ends in `default`. |
| `value_schema` | One tagged schema from the type system below. |
| `default_value` | Valid against `value_schema`; may be JSON `null` only when nullable. |
| `platforms` | Platforms expected to expose or consume the setting. |
| `category` | Stable grouping for docs/admin UX; not authorization. |
| `label`, `description` | Canonical English copy. Clients may localize it. |

Optional fields include `unit`, `recommended_control`, and localized option identifiers. UI
metadata is advisory; validation, scope, and defaults are normative.

Internal server bindings map a definition to existing profile columns or preference stores. They
must not expose table or column names in the public manifest.

## Value type system

The v1 contract supports these tagged schemas:

| Type | Constraints | JSON value |
|---|---|---|
| `boolean` | none | `true` |
| `integer` | `minimum`, `maximum`, optional `step` | `30` |
| `number` | finite `minimum`, `maximum`, optional `step` | `1.25` |
| `string` | `min_length`, `max_length`, optional `pattern` | `"fit"` |
| `enum` | non-empty typed `values` array | `"always"` |
| `language_tag` | well-formed BCP 47 tag; optional null | `"en-US"` |
| `object` | required `schema_ref` | `{ "fontScale": 1.2 }` |

Rules:

- New APIs transport native JSON values. Booleans and numbers are not quoted.
- `NaN`, infinities, duplicate object keys, and values outside declared constraints are rejected.
- `unset` is an operation, not a value. JSON `null` is allowed only when the definition says it is
  meaningful.
- Enum wire values are stable identifiers, never localized labels.
- Language values are normalized to a canonical BCP 47 representation while preserving valid
  region/script specificity.
- Arbitrary untyped JSON is not allowed. Existing `subtitle_appearance` becomes an `object` with a
  versioned schema.

## Scopes and identity

The remote scopes are:

| Scope | Identity tuple | Meaning |
|---|---|---|
| `account` | `(user_id)` | Same for every profile and signed-in client on the account. |
| `profile` | `(user_id, profile_id)` | Roams with one profile. |
| `profile_device` | `(user_id, profile_id, device_id)` | Override for one profile on one device identity. |
| `profile_library` | `(user_id, profile_id, library_id)` | Content preference for one library. |
| `profile_series` | `(user_id, profile_id, series_id)` | Content preference for one series. |

`client_local` definitions use a single logical `client_local` scope and are never addressed by the
server values API.

All remote mutations carry their complete identity explicitly. The server authorizes that the
profile, library, series, and device belong to the authenticated user. A queued operation must not
derive its profile or server from whichever account happens to be active when the retry runs.

Device identity remains an installation/browser identity, not a person identity:

- A normal browser profile persists one random device ID.
- An incognito/private window receives a different, ephemeral device ID.
- Clients must not fingerprint hardware to reconstruct a deleted device ID.
- Merely reading effective settings may update `last_seen_at`, but empty device records with no
  settings, downloads, push registration, or other durable relationship are removed after 90 days.
- Users and admins can explicitly **Forget device**, which removes its settings and registrations
  through the existing device cleanup path.

## Resolution

There is no universal hard-coded precedence. Each definition declares its resolution order and the
server is the only canonical resolver.

Examples:

| Setting family | Resolution order |
|---|---|
| Audio/subtitle selection | series → library → device → profile → default |
| Playback behavior with device override | device → profile → default |
| Device playback capability | device → default |
| Account UI preference | account → default |
| Client-local OS behavior | local value → default |

Clients may cache effective values but must not reimplement a different precedence. Playback and
catalog code consume the server resolver or a server-produced effective preference snapshot.

The effective response identifies both value and source:

```json
{
  "key": "playback.audio_language",
  "value": "ja",
  "source": "profile_library",
  "source_context": { "profile_id": "p1", "library_id": "42" },
  "definition_revision": 12,
  "updated_at": "2026-07-10T15:03:04Z"
}
```

## Breaking API cutover

This is a coordinated breaking release, not an additive compatibility rollout. The release replaces
the current stringly `/api/v1/settings` contract and removes duplicated preference fields/routes in
the same server version that runs the one-time migration.

This is an explicit exception to the repository's normal additive-only `/api/v1` policy and must be
recorded in the v1 scope amendment that approves implementation. If that exception is not approved,
the exact same contract moves to `/api/v2/settings`; maintaining both protocols is not an option in
this design.

### Version handshake and hard upgrade gate

- Protocol version `1` is the only supported version after cutover, and the server requires an
  exact manifest revision match.
- Native and separately deployed web clients send `X-Silo-Settings-Contract-Version: 1` and
  `X-Silo-Settings-Contract-Revision: 12` on authenticated API requests.
- Authenticated middleware returns `426 Upgrade Required` with
  `required_settings_contract_version` and `required_settings_contract_revision` when either
  header is absent or mismatched.
- The server bootstrap response advertises both required values so a matching client can show a
  deliberate upgrade screen instead of a generic API error.
- The server-bundled web application is built from the same manifest revision and always sends the
  matching version.
- New clients do not support pre-cutover servers. Old clients do not support post-cutover servers.

The hard gate is intentional: partial operation would recreate split-brain settings behavior.
It also means every manifest revision—including a new contract-known client-local production
setting—ships as another coordinated server/web/Apple/Android version set. Merging the server
contract PR does not publish the definition until matching clients are ready. This release cost is
the explicit tradeoff for choosing no mixed-version compatibility.

### Manifest

`GET /api/v1/settings/manifest`

- Authenticated but not admin-only.
- Returns the public canonical manifest.
- Supports `If-None-Match` and `304 Not Modified`.
- Never includes current values, secrets, database bindings, or admin-only server configuration.

### Explicit stored values

`GET /api/v1/settings/values?keys=<comma-separated-keys>&scope=<scope>&<context>`

- Returns the explicit value and revision at exactly one requested scope; it does not resolve
  fallbacks.
- Context parameters are required by scope: `profile_id`, `device_id`, `library_id`, or `series_id`
  as defined by the identity table above.
- An unset value is represented as `is_set: false` with no `value` member, never as an empty string
  or JSON `null`.
- Settings screens use this endpoint to show profile defaults and device overrides independently.
- Unknown keys, disallowed scopes, and unauthorized contexts are rejected.

### Effective values

`GET /api/v1/settings/values/effective?keys=<comma-separated-keys>`

- Requires the active profile and device identity headers for definitions that can resolve those
  scopes.
- Rejects unknown keys rather than fabricating defaults.
- Returns native typed values, resolution source, source context, definition revision, and
  `updated_at`.
- A missing explicit value is not an error; resolution continues to the next declared scope.

### Mutations

`POST /api/v1/settings/mutations`

```json
{
  "mutations": [
    {
      "mutation_id": "8cc515ad-88c5-48f0-a6cc-44d0a870e32c",
      "operation": "set",
      "key": "playback.audio_language",
      "scope": "profile_device",
      "context": {
        "profile_id": "p1",
        "device_id": "apple-tv-living-room"
      },
      "value": "ja"
    },
    {
      "mutation_id": "5ae96ffc-1077-4da8-8f64-a1ca9c3c72b8",
      "operation": "unset",
      "key": "playback.auto_skip_intro",
      "scope": "profile_device",
      "context": {
        "profile_id": "p1",
        "device_id": "apple-tv-living-room"
      }
    }
  ]
}
```

Server rules:

1. Reject unknown keys with `unknown_setting`.
2. Reject a scope not listed by the definition with `invalid_setting_scope`.
3. Validate the context and value against the definition before writing.
4. Authorize every context object against the authenticated user.
5. Treat `mutation_id` as idempotent for at least 30 days. Repeating the same ID and body returns
   the prior result; reusing an ID with different content returns `mutation_id_conflict`.
6. Return one result per mutation so a batch can retry only transient failures.
7. Apply each mutation atomically. The entire batch need not be transactional across unrelated
   keys.
8. Emit a settings-changed event carrying only affected keys/scopes and contract revision; clients
   re-fetch effective values rather than trusting event payload values.

HTTP `400` is used for malformed batches. A syntactically valid batch returns `200` with typed
per-mutation results such as `applied`, `already_applied`, `invalid_value`, `forbidden`, or
`transient_failure`.

### Removed surfaces

The cutover removes, rather than adapts, the old preference surfaces:

- String-valued `GET`, `PUT`, and `DELETE /api/v1/settings...` handlers.
- Preference fields on profile create/update/response DTOs, including language, subtitle behavior,
  skip behavior, quality, and next-up behavior.
- Separate library and series default-language/subtitle mutation routes. Track-selection history may
  remain specialized, but user preference defaults move to this contract.
- The open-ended unknown user-setting extension bag.
- Client-written raw remote keys and local copies of remote defaults/ranges.

All production reads and writes use the typed manifest, effective-values endpoint, and mutation
endpoint immediately after the release. Unknown keys are always rejected.

## Canonical storage

Remote values move to one typed `user_setting_values` table. The manifest remains the schema; the
database stores validated JSON and scope identity.

```sql
CREATE TABLE user_setting_values (
    id          bigserial PRIMARY KEY,
    user_id     integer NOT NULL,
    key         text NOT NULL,
    scope       text NOT NULL,
    profile_id  text,
    device_id   text,
    library_id  integer,
    series_id   text,
    value       jsonb NOT NULL,
    revision    bigint NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (scope IN ('account', 'profile', 'profile_device', 'profile_library', 'profile_series')),
    CHECK (
      (scope = 'account' AND profile_id IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_device' AND profile_id IS NOT NULL AND device_id IS NOT NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_library' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NOT NULL AND series_id IS NULL) OR
      (scope = 'profile_series' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NOT NULL)
    )
);
```

This is the PostgreSQL shape. The per-user SQLite store uses the same columns, checks, and partial
uniqueness but omits `user_id` because the database itself is already user-scoped, and uses the
equivalent SQLite integer/text/JSON-check representation. Both backends run the same store
conformance suite.

Partial unique indexes enforce one explicit value per identity:

```sql
CREATE UNIQUE INDEX user_setting_values_account_uq
  ON user_setting_values (user_id, key) WHERE scope = 'account';
CREATE UNIQUE INDEX user_setting_values_profile_uq
  ON user_setting_values (user_id, profile_id, key) WHERE scope = 'profile';
CREATE UNIQUE INDEX user_setting_values_profile_device_uq
  ON user_setting_values (user_id, profile_id, device_id, key) WHERE scope = 'profile_device';
CREATE UNIQUE INDEX user_setting_values_profile_library_uq
  ON user_setting_values (user_id, profile_id, library_id, key) WHERE scope = 'profile_library';
CREATE UNIQUE INDEX user_setting_values_profile_series_uq
  ON user_setting_values (user_id, profile_id, series_id, key) WHERE scope = 'profile_series';
```

Foreign keys and delete behavior follow existing user/profile/device/library/series ownership. A
profile or user delete cascades its values. Library/series deletion removes only values scoped to
that entity. Device forgetting removes `profile_device` values.

Mutation idempotency uses a separate `user_setting_mutations` table keyed by
`(user_id, mutation_id)` with request hash, serialized result, and `expires_at`; rows expire after
30 days.

The migration also creates `user_setting_migration_rejects`, an inactive audit table with source
table/key/identity/value and rejection reason. It has no runtime read/write API and is not an
extension bag. Its only purpose is to retain unrecognized historical rows for operator inspection
instead of silently deleting them.

The one-time migration runs transactionally before the server accepts traffic:

1. Create and validate the canonical manifest and new tables.
2. Transform known values from account settings, profile columns, device settings, and
   library/series preference stores into typed JSON rows using checked-in migration rules.
3. Normalize aliases and values according to the migration table below.
4. Copy unrecognized ad hoc rows to `user_setting_migration_rejects` and include their counts/keys in
   the preflight and completion report. They do not become active settings.
5. Abort startup on an invalid recognized value without an explicit normalization rule, duplicate
   identity, or row-count/checksum mismatch. Do not silently drop data.
6. Record the completed contract version and manifest revision in the database.
7. Remove migrated preference columns and obsolete settings tables/routes in the same release.
8. Retain specialized track-history fields only when they represent a concrete selected track or
   signature rather than a default user setting.

There is no dual read, dual write, fallback adapter, or rollback to the old settings schema after a
successful migration. Operators must take the normal pre-upgrade database backup; rollback means
restoring that backup and the prior server binary together.

## Initial canonical scope decisions

The first manifest must register every official key currently read or written by a supported
client. The following decisions resolve today's duplicate semantics:

| Canonical setting/family | Persistence and scopes | Migration disposition |
|---|---|---|
| `playback.audio_language` | remote: profile, profile_device, profile_library, profile_series | Migrate profile `language` as the roaming fallback; existing device values become real overrides. |
| `playback.subtitle_language` | remote: profile, profile_device, profile_library, profile_series | Migrate existing profile/library/series subtitle fields to this key. |
| `playback.subtitle_mode` | remote: profile, profile_device, profile_library, profile_series | Existing values are normalized to one enum. |
| `playback.show_forced_subtitles` | remote: profile, profile_device, profile_library, profile_series | Preserve explicit false separately from unset. |
| `catalog.metadata_language` | remote: profile | Migrate existing `preferred_metadata_language` values to this key. |
| `playback.preferred_quality` | remote: profile, profile_device | Profile quality is fallback; device override wins. |
| `playback.auto_skip_intro`, `credits`, `recap` | remote: profile, profile_device | Existing profile columns are fallback; explicit device values win. |
| `playback.auto_play_next`, `auto_play_next_preview`, `next_up_prompt_seconds` | remote: profile, profile_device | Use `playback.*`; Android's `player.next_up_prompt_seconds` is migrated and removed from production writes. |
| `subtitle_appearance` | remote: profile, profile_device | Profile value roams; device customization wins. Existing account fallback is copied to each profile. |
| `player.*` technical playback keys | remote: profile_device | HDR, DV, seek cache, speed, sync, gravity, and orientation remain device-specific and server-validated. |
| Theme, text scale/weight, contrast, custom theme variables/CSS | remote: account | Owner-tag all local caches; never apply a cached value to a different authenticated user. |
| Date/time format and search media scope | remote: account | Preserve existing account behavior and strict enums. |
| `ui.library_page_state` | remote: profile_device | Keep navigation state tied to one profile/device. |
| OS caption mirroring, platform decoder diagnostics, temporary sleep timers | client_local or private `local.*` | Production caption-mirroring UI is contract-known local; diagnostics/timers remain private local. |

The manifest inventory PR must also locate and classify currently unregistered web theme/custom
keys and Android-only keys. An unregistered official key blocks the migration and release.

### Subtitle appearance migration

Current subtitle appearance has an account-level legacy fallback plus device overrides. Migration
is deterministic:

1. Copy the account fallback to every existing profile as that profile's initial value.
2. Keep existing profile-device overrides unchanged.
3. Resolve device → profile → default after migration.
4. Mark migration completion per account so newly created profiles use the contract default rather
   than repeatedly copying stale legacy data.

## Generated client bindings

Each client vendors a pinned copy of the canonical manifest and generates bindings from it:

- Go: registry, validators, codecs, public manifest types, and resolver descriptors.
- TypeScript: key union, `SettingValueByKey`, definitions, and validated UI metadata.
- Swift: `SettingKey<Value>` constants, Codable value types, scope enums, and default accessors.
- Kotlin: `SettingKey<T>` objects, serializers, scope enums, and default accessors.

Generated files carry the manifest revision and a “do not edit” header. Handwritten raw remote keys
are forbidden outside migration tests.

Client CI must fail when:

- A production remote key literal is not generated.
- A client-local production setting is absent from the shared manifest.
- A local default or range duplicates and disagrees with generated metadata.
- The vendored manifest is malformed or generated files are stale.

The server manifest PR lands first. Client PRs then update the pinned artifact and generated code.
Every release in the coordinated version set embeds the same settings protocol version and exact
manifest revision. The hard handshake prevents a mismatched server/client pair from entering the
application.

## Native synchronization contract

Apple and Android use a durable outbox for remote mutations. Each entry includes:

```text
(server_id, user_id, profile_id, device_id, key, scope, operation, typed_value, mutation_id, created_at)
```

Required behavior:

1. Persist the outbox before updating optimistic UI state.
2. Coalesce pending operations only when the complete identity tuple, key, and scope match.
3. Preserve the newest local operation while an older operation is in flight.
4. Remove an entry only after `applied`, `already_applied`, or a deliberate user discard.
5. Retry network/5xx failures with bounded exponential backoff and on app foreground.
6. Keep terminal validation/auth failures visible as a sync error; do not silently log and drop.
7. Flush using the stored server/profile/device context, not the currently selected context.
8. Cancel or quarantine work after logout until the same account/server identity returns.
9. Process `unset` as a first-class operation.

Web mutations may remain request-immediate, but caches must be keyed by server, user, profile,
device, and setting scope as applicable. A cached value must never render before ownership matches
the authenticated context.

## UX requirements

- Settings screens group profile values separately from device overrides.
- If a definition allows both, the screen shows the effective value and its source.
- “Use profile setting” performs `unset` at `profile_device`; it does not copy the profile value
  into the device row.
- Reset actions state their target: **Reset this device**, **Reset this profile**, or **Reset all**.
- Offline edits show a subtle pending indicator. Terminal sync failures show a retry action and a
  readable validation message.
- Unsupported platform settings are hidden, not displayed disabled without explanation.
- Admin device views render controls from the canonical manifest and may clear remote overrides.
  They do not claim access to client-local values.
- Apple’s current subtitle copy — explicitly separating profile behavior from per-device appearance
  — is the UX baseline to retain and generalize.

## Validation and authorization

- Validation occurs in the server contract layer before any setting value is stored.
- Profile DTOs no longer contain preference fields, so profile identity/access updates cannot bypass
  settings validation.
- The authenticated user may mutate owned profiles according to existing profile permissions.
- Device mutations require a non-empty bounded device ID and register/update device metadata.
- Library/series settings require access to the referenced content scope.
- Admin clear/reset operations are audited.
- Settings values must never contain secrets. A future secret-like preference requires a dedicated
  encrypted/credential API, not a new settings schema type.

## Coordinated release plan

Implementation may be split across PRs, but none of the new clients or breaking server routes are
released independently. The deployable unit is one version set containing the server, bundled web,
Apple clients, and Android clients built against contract version `1`.

### Build phase 0 — freeze and inventory

- Stop adding ad hoc remote key literals.
- Inventory server, web, Apple, and Android reads/writes.
- Classify every production setting and record aliases, current defaults, ranges, and consumers.
- Define a migration disposition for every discovered stored key and profile preference column.

### Build phase 1 — contract and storage

- Add `contracts/settings/v1` and manifest validation tests.
- Register all official current keys, including web theme/customization keys.
- Add canonical storage, mutation idempotency storage, and the one-time migration.
- Generate Go/TypeScript registry code from the manifest.
- Add typed manifest, effective-values, and mutation routes behind an unreleased build gate.

### Build phase 2 — canonical resolution

- Move profile, account, device, library, and series defaults to canonical values.
- Make playback/catalog paths consume the canonical resolver.
- Fix Apple audio language so a stored value affects selection.
- Remove preference fields and mutation behavior from profile/library/series DTOs.

### Build phase 3 — clients

- Generate and adopt Swift/Kotlin bindings.
- Fix Android aliases/defaults/ranges and remove unregistered production writes.
- Replace Apple and Android pending-write logic with durable scoped outboxes.
- Apply owner-tagged cache keys throughout web settings.
- Add the standardized scope/source UX.
- Add the hard protocol-version/manifest-revision handshake and upgrade-required screen.

### Pre-release gate

- All four repositories pass the shared conformance fixture at the exact commits selected for the
  release.
- Migration is rehearsed against anonymized copies representing SQLite and PostgreSQL user stores,
  including invalid/unknown-value failure cases.
- Store-distributed Apple/Android builds are approved and publicly available before the breaking
  server release is published.
- Release notes state that server and apps must be upgraded together and that rollback requires a
  pre-upgrade database restore.
- Server startup reports a migration preflight summary before applying destructive schema cleanup.

### Cutover

1. Operator takes the required database backup.
2. Operator upgrades the server; startup runs the transaction and contract validation.
3. Server begins requiring contract version `1` and serves the matching bundled web client.
4. Users update Apple/Android clients; mismatched clients receive the upgrade-required gate.
5. No old settings route or schema remains active after the migration commits.

### Post-cutover cleanup

- Verify migrated counts/checksums and effective-value samples.
- Add stale empty-device cleanup and Forget device UX if they did not ship in the cutover build.
- Delete migration-only transform code after the supported upgrade window only if Silo's release
  policy permits skipping directly to newer versions; otherwise retain the one-time migration as an
  inert historical migration.

## Testing

### Contract tests

- Manifest validates against its schema and has a stable digest.
- Every default validates against its type.
- Every resolution chain references allowed scopes exactly once and ends with `default`.
- Generated Go, TypeScript, Swift, and Kotlin outputs are reproducible.
- Every stored legacy source key/column has exactly one migration disposition.

### Server tests

- Native boolean/number/object round trips.
- Unknown key, invalid type, invalid range, invalid enum, invalid scope, and unauthorized context
  rejection.
- Set versus unset distinction for false, zero, empty string, and nullable values.
- Effective resolution for every declared chain, especially series → library → device → profile.
- Mutation idempotency and ID/body conflict.
- Per-mutation partial retry behavior.
- One-time migration success, atomic failure, alias normalization, row-count/checksum verification,
  and restart after completed migration.
- Protocol-version mismatch, manifest-revision mismatch, and missing-header `426` behavior.
- Incognito/new-device fallback without copying another device override.
- Empty stale-device retention cleanup.

### Client tests

- Generated key/type use and hard protocol-version/manifest-revision gate.
- New sign-in/incognito receives account/profile values but not another device override.
- Profile switch and server switch cannot redirect queued writes.
- Process death preserves outbox entries.
- Failed writes remain queued and visible.
- Cache ownership prevents cross-account flashes.
- UI copy accurately names scope and reset behavior.

### Cross-platform conformance fixture

The contract directory includes a fixture set of definitions, explicit values, contexts, and
expected effective results. Server, web, Apple, and Android run the same fixture cases. This is the
release gate that catches key, default, type, and precedence drift.

## Acceptance criteria

- A production user-facing setting cannot land in a client without a canonical manifest entry.
- A private `local.*` knob cannot be sent to the server.
- The server rejects unknown keys and invalid typed values on the new API.
- Swift, Kotlin, TypeScript, and Go use generated key/type bindings.
- Profile language and subtitle preferences roam into a new incognito session.
- Device overrides do not roam into a different device identity.
- Effective responses explain where values came from.
- Apple and Android persist failed mutations with full server/profile/device identity.
- The verified Android key/default/range drift and Apple no-op audio preference are covered by
  conformance tests.
- The one-time migration either completes and verifies atomically or leaves the old database
  unchanged.
- Mismatched server/client settings protocol versions or manifest revisions are blocked with an
  explicit upgrade message.
- No old string settings route, open-ended key bag, or duplicated profile preference field remains
  after cutover.

## Required PR workflow for a new setting

1. Open a `silo-server` PR that adds the manifest definition, default, scopes, resolution order,
   UX copy, persistence/scopes (or `client_local` declaration), contract-version impact, and
   contract tests.
2. Merge the contract PR before merging a production client implementation.
3. Update the client’s pinned manifest and regenerate bindings.
4. Implement the UI/consumer using generated types and the required version/revision handshake.
5. Add the cross-platform fixture when the setting has resolution or coercion behavior.

This server-first PR requirement is intentional governance, not a requirement that every value be
stored by the server. It keeps the vocabulary, types, defaults, and UX semantics consistent while
preserving a clearly bounded client-local storage option.
