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

The contract lands as **one coordinated breaking release**: the manifest, typed API, canonical
storage, migration, and removal of the legacy settings surface ship together, and server, bundled
web, Apple, and Android update at the same time. Mixed-version operation is not supported.

That is a deliberate choice against a phased rollout. Phasing would mean building a compatibility
projection of the old API over the new resolver, plus bindings from the manifest to the tables the
migration is about to replace — both written only to be deleted, in a subsystem where the
transitional code would be a meaningful fraction of the permanent code. The project is pre-1.0,
`docs/architecture/v1-scope.md` is not locked, and the data volumes are small. One clean switchover
costs less than the scaffolding needed to avoid it.

**After this release, no future setting requires coordination.** The release is the only lockstep
event in this design; everything after it is governed by manifest revisions, which move
independently per repository. See **API delivery and compatibility**.

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
- **This device, for this profile** — override tied to the active profile *and* device identity.
- **Only this app/device** — client-local value that is never uploaded.
- **Everyone on this account** — account-scope value shared by every profile.

Avoid ambiguous labels such as “global,” “default,” or “remember this” without naming what the
value follows.

The device label names both halves of the identity deliberately. A bare “This device/browser”
implies the value applies to whoever is using the device, which is exactly backwards on the shared
screens where device overrides matter most: a living-room TV used by four household profiles. A
user who reads “This device” on a family TV will reasonably assume they are changing it for the
household, and the actual behavior — a private override for their profile alone — is the opposite.

## Why this is needed

The current implementation has three partial contracts:

- `silo-server: internal/api/handlers/settings.go` owns validation, defaults, and a `user` versus
  `device` registry, but unknown user keys are accepted and values are strings.
- `silo-server: web/src/lib/settingsManifest.ts` independently owns labels, controls, defaults,
  enum options, and numeric ranges. It registers no user-scope keys at all and omits several
  registered device keys, so the duplication is structurally incomplete, not just drift-prone.
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
- jellycompat's Jellyfin `DisplayPreferences` handler seeds its first-run state from the profile
  subtitle and auto-skip columns and persists its blobs through the legacy string settings store
  under `jellycompat:displayprefs:*` keys, coupling third-party client state to both surfaces this
  design retires.

There is also a fourth contract that #376 did not cover, and it is the one most likely to be
overlooked: **`internal/policy` already resolves restrictions over the same subject matter.**
`internal/policy/input.go` carries `account_max_playback_quality`, `profile_max_playback_quality`,
and `profile_preferred_metadata_language`, and `user_profiles` carries `max_playback_quality`,
`max_content_rating`, and `library_restrictions_enabled` alongside the preference columns
`quality_preference` and `preferred_metadata_language`. A settings contract that resolves
preferences without consulting that engine produces a second, disagreeing answer for the same
user-visible control. See **Preferences versus restrictions**.

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

The `silo-apple` commit sits on `feature/tvos-manual-up-next`, not `main`; its merge base with
`main` is `169e4917`. Every settings-relevant file cited by this design is identical at that
commit, at that merge base, and on the current development heads, so the findings hold on `main`
as well.

## Goals

1. One machine-readable definition for every production setting.
2. Native JSON value types instead of stringly typed values on the new API.
3. Explicit storage scopes and per-setting resolution order.
4. Compile-time key/type wrappers for Swift, Kotlin, and TypeScript.
5. Strict rejection of unknown remote keys and invalid values.
6. One coordinated cutover with a one-time data migration, and no lockstep releases after it.
7. Durable, profile-safe native synchronization.
8. Clear UX explaining what roams and what remains on a device.
9. A small, documented escape hatch for client-private knobs.
10. One explicit seam between user *preference* (this contract) and enforced *restriction*
    (`internal/policy`), so a client can never present a choice policy will refuse.

## Non-goals

- Replacing server-admin configuration in `server_settings`.
- Turning the settings manifest into a generic remote-form engine for every screen.
- Synchronizing secrets, credentials, tokens, or filesystem paths as user preferences.
- Giving an admin silent control over client-local values.
- Making every setting available on every platform.
- Preserving accidental key names, old string wire formats, or incorrect defaults as canonical
  behavior.
- Supporting old apps against the new server, or new apps against an old server. No shim,
  projection, fallback, or partial-operation mode is built for either direction.
- Replacing `internal/policy`. Settings express what a user wants; policy expresses what the
  account, profile, and access groups permit. Policy stays authoritative.

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
- **Restriction** — an enforced ceiling or lock owned by `internal/policy` (parental controls,
  access groups, account/profile `max_playback_quality`). A restriction is not a setting and is
  never stored in this contract; it constrains what an effective value is allowed to be.
- **Permitted value** — the effective value after policy constraint. Clients render and act on the
  permitted value, never on the raw effective value.

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
- The canonical JSON bytes are the RFC 8785 (JCS) canonicalization of the manifest: UTF-8,
  lexicographically sorted object keys, no insignificant whitespace. `ETag` is the SHA-256 digest
  of those bytes, and generated-code reproducibility is defined over the same bytes.

The API version and contract revision are separate:

```json
{
  "api_version": 1,
  "revision": 12,
  "definitions": []
}
```

- `api_version` identifies the settings protocol. It changes only for a change no revision rule
  below can express.
- `revision` is a monotonically increasing integer changed by every manifest PR.

Within one `api_version`, revisions are monotone-compatible in both directions. A client pinned to
an older revision remains valid; a client pinned to a newer revision hides what the connected
server does not know. That property depends on classifying every manifest change:

| Change | Allowed within `api_version` | Requires |
|---|---|---|
| Add a key | Yes | Revision bump |
| **Widen** `allowed_scopes` (add a more specific override scope) | Yes | Revision bump; new scope carries `introduced_in` |
| Add an enum member | Yes | Revision bump; member carries `introduced_in` |
| Widen a numeric range | Yes | Revision bump; bound carries `introduced_in` |
| Change a default | Yes | Revision bump plus explicit release notes — behavior changes with no stored value changing |
| Deprecate a key | Yes | Revision bump; `deprecated: true`, definition stays published |
| **Narrow** `allowed_scopes`, tighten a range, remove an enum member | No | New key, plus a migration for every previously valid stored value |
| Change value type, persistence class, or meaning | No | New key |

Widening is safe in a way narrowing is not, and the two must not share one rule. An older client
that does not know a newly added scope still receives a correctly resolved value and can read
`source`; it simply cannot author at that scope. An older client that has already stored a value
at a scope you remove has nowhere to put it.

Because defaults, enum members, ranges, and scopes can therefore all move within one
`api_version`, revision awareness has to be finer than whole definitions:

- `introduced_in` is a **manifest revision**, not an `api_version`.
- Every additively introduced sub-element — an enum member, a scope, a widened bound — carries its
  own `introduced_in`.
- A client filters options, scopes, and bounds against the server's advertised revision before
  rendering or sending them. This is what prevents a newer client from offering a choice an older
  server will reject with `invalid_value` for reasons the user cannot act on.

Published definitions are never unpublished. A deprecated definition stays in the manifest with
`deprecated: true` so older clients continue to resolve it.

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

A definition that policy can constrain declares that binding explicitly, and additively introduced
sub-elements carry their own revision:

```json
{
  "key": "playback.preferred_quality",
  "introduced_in": 1,
  "persistence": "remote",
  "allowed_scopes": ["profile", "profile_device"],
  "resolution_order": ["profile_device", "profile", "default"],
  "value_schema": {
    "type": "enum",
    "values": [
      { "value": "auto" },
      { "value": "1080p" },
      { "value": "2160p" },
      { "value": "1080p-high", "introduced_in": 14 }
    ],
    "ordered": true
  },
  "default_value": "auto",
  "constrained_by": {
    "policy_input": "max_playback_quality",
    "constraint": "ceiling"
  },
  "category": "playback",
  "label": "Preferred quality",
  "description": "Pick the quality Silo should prefer.",
  "deprecated": false
}
```

Required fields:

| Field | Rule |
|---|---|
| `key` | Lowercase dot-separated identifier. Canonical names do not encode a platform. |
| `introduced_in` | Manifest revision that first published this definition. |
| `persistence` | `remote` or `client_local`. |
| `allowed_scopes` | Non-empty and valid for the persistence class. Individual scopes added after `introduced_in` carry their own `introduced_in`. |
| `resolution_order` | Contains every remote scope at most once and ends in `default`. |
| `value_schema` | One tagged schema from the type system below. |
| `default_value` | Valid against `value_schema`; may be JSON `null` only when nullable. |
| `category` | Stable grouping for docs/admin UX; not authorization. |
| `label`, `description` | Canonical English copy. Clients may localize it. |

Optional fields include `unit`, `recommended_control`, `platforms`, `constrained_by`, and localized
option identifiers.

`platforms` is **advisory UI metadata only**. It tells a client whether a setting is expected to be
meaningful on that platform so unsupported entries can be hidden rather than shown disabled. The
server does not enforce it, because enforcement would mean every new platform, form factor, or
client needs a manifest PR before it can write a setting it already implements correctly. Omitting
`platforms` means "expected everywhere."

Validation, scope, resolution, defaults, and `constrained_by` are normative. Everything else is
advisory.

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
| `enum` | non-empty `values` array of member objects; optional `ordered` | `"always"` |
| `language_tag` | well-formed BCP 47 tag; optional null | `"en-US"` |
| `object` | required `schema_ref` | `{ "fontScale": 1.2 }` |

Rules:

- New APIs transport native JSON values. Booleans and numbers are not quoted.
- `NaN`, infinities, duplicate object keys, and values outside declared constraints are rejected.
- `unset` is an operation, not a value. JSON `null` is allowed only when the definition says it is
  meaningful.
- Enum wire values are stable identifiers, never localized labels.
- An enum member is an object — `{ "value": "always", "introduced_in": 14 }` — not a bare string,
  so members added after the definition can carry their own revision. `introduced_in` is omitted
  when the member shipped with the definition.
- `ordered: true` declares that members form a meaningful progression (quality ladders, size
  steps). A `ceiling` or `floor` policy constraint is only valid on an ordered enum or a numeric
  type, since otherwise "cap this value" has no meaning.
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

The effective response identifies value, source, and any policy constraint:

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

## Preferences versus restrictions

Silo already has a second resolver. `internal/policy` evaluates access groups, parental controls,
and the account/profile `max_playback_quality` ceiling, and it is authoritative for what a viewer
is permitted to do. This contract must not become a competing answer to the same question.

The seam is:

- **Settings answer "what does this user want?"** They are authored by the user and stored here.
- **Policy answers "what is this user allowed to have?"** It is authored by an admin or a household
  parent, evaluated by `internal/policy`, and never stored in `user_setting_values`.

Without an explicit seam the failure is concrete and immediate: a child profile capped by
`max_playback_quality` at `720p` opens the quality picker, the settings resolver reports an
effective value of `2160p`, the client renders 4K as selected and selectable, the user picks it,
and playback silently delivers something else. The same shape applies to
`catalog.metadata_language` against `profile_preferred_metadata_language` and to any future
restriction.

Therefore:

1. A definition that policy can constrain declares `constrained_by` with the policy input it reads
   and the constraint kind (`ceiling`, `floor`, `allowlist`, or `locked`).
2. The effective-values endpoint applies the constraint and reports both values:

```json
{
  "key": "playback.preferred_quality",
  "value": "720p",
  "requested_value": "2160p",
  "source": "profile_device",
  "constrained_by": { "policy_input": "max_playback_quality", "constraint": "ceiling" },
  "permitted_values": ["auto", "480p", "720p"],
  "definition_revision": 12,
  "updated_at": "2026-07-10T15:03:04Z"
}
```

3. `value` is the permitted value. Clients act on it. `requested_value` appears only when a
   constraint changed the outcome, so the UI can explain the difference instead of silently
   disagreeing with the user's stored choice.
4. `permitted_values` narrows the manifest's declared options for this viewer. Clients render from
   `permitted_values` when present, and from the manifest otherwise.
5. Mutations are **not** rejected for exceeding a restriction. Storing a preference the current
   policy forbids is legitimate: restrictions change, and a child's stored 4K preference should
   take effect on the day the cap is lifted rather than being destroyed by it. Validation rejects
   values invalid against the *definition*; policy constrains at resolution time.
6. Playback and catalog paths consume the permitted value. They must not re-resolve the raw stored
   value and re-apply policy independently.
7. A `locked` constraint means the user cannot author the setting at all under current policy. UI
   shows the value with a lock affordance and an explanation, not a disabled control with no reason.

Rule 5 is the one that is easy to get backwards. A restriction is a filter on what a preference
*does*, not a validator on what a preference *is*.

## API delivery and compatibility

**This is a coordinated breaking release.** One server version introduces the typed contract, runs
the migration, and removes the legacy string settings surface and the duplicated profile DTO
preference fields. Server, bundled web, Apple, and Android update together. There is no
compatibility shim, no projection of the old API over the new resolver, and no fallback path in
clients.

Supporting an old client against a new server, or the reverse, is an explicit non-goal. Every
mechanism that would make a mismatched pair partially work is code written to be deleted, and this
subsystem is not worth carrying that.

### Timing

`docs/architecture/v1-scope.md` currently reads **"Status: NOT LOCKED — proposal window open,"** and
the amendment process it describes only exists *after* lock. There is therefore no amendment to
write and no exception to request: before lock, removing the legacy settings surface is simply in
scope.

That argument does not live only here. Reasoning kept in a design doc is invisible to whoever reads
the policy later and sees a removal that appears to break it, so the removal is recorded in the
**pre-lock removals** table in `docs/architecture/v1-scope.md`, which is the file that governs it.
The table also carries the deadline: **this work must ship before the scope locks.** If it has not,
the justification lapses and the removal goes through Deprecation/Sunset like anything else.

**This is an argument for doing the work now rather than after lock.** After lock, the same removal
would need the Deprecation/Sunset flow the v1 policy mandates and the codebase already implements
(`internal/api/handlers/legacy_read_routes.go`), which reintroduces exactly the transitional
surface this design is avoiding.

Neither path needs `/api/v2/settings`. A `v2` namespace would imply a whole second API surface this
project does not want to own, for the sake of one subsystem.

### How a mismatch presents

Removing the old routes already produces the required outcome. Nothing further is added to enforce
it:

- An old client calls a removed route and receives `404`. Its settings screens fail. It is not
  supported, and the release notes say so.
- A new client detects a pre-contract server by the absence of `GET /api/v1/settings/manifest` and
  shows a server-upgrade-required message. This is an error message, not a compatibility path: no
  legacy fallback, no local defaults, no partial operation.
- The server-bundled web application is always built from the server's own manifest revision, so it
  is exact by construction.

**No settings version gate is added to the authenticated middleware, and no first-party route
returns `426`.** An earlier revision of this design did exactly that — an
`X-Silo-Settings-Contract-Version` header checked on every authenticated request. It is withdrawn
because it is strictly more code for the same user-visible outcome: header plumbing in four
repositories, a middleware check on every request, and a version constant to maintain, all to
enforce a break that deleting the routes already enforces.

It is also the wrong shape for a one-time event. A gate in the authenticated chain permanently
couples every endpoint in the product to the settings subsystem's versioning, and the next settings
protocol change inherits an installed base conditioned to expect a global block. Route removal has
no such tail: once the release ships, there is nothing left to maintain.

Two secondary points reinforce this. `docs/architecture/v1-scope.md` states the house rule as
capability endpoints for feature detection rather than version sniffing, citing
`GET /api/v1/libraries/provider-defaults` — and the manifest endpoint already *is* that capability
endpoint, carrying `api_version` and `revision`. And the header added no detection ability the
manifest endpoint did not already provide; it only added blocking.

### Post-release revision compatibility

The coordinated release is exact: every artifact ships against `api_version` 1 at the same manifest
revision. **After it, revisions move independently.** A new setting is one server PR plus *n*
client PRs on their own schedules, governed by the widening/narrowing rules and `introduced_in`
filtering above.

- `GET /api/v1/settings/capability` returns `api_version` and `revision` for clients that want to
  check compatibility without transferring the manifest body.
- Clients filter definitions, scopes, enum members, and bounds against the server's advertised
  revision.
- Clients may send `X-Silo-Settings-Contract-Revision` for telemetry about deployed revision
  spread. It is diagnostic only and never blocks a request.

This is the property that keeps the contract from becoming the thing people route around. One
coordinated release is a reasonable cost. A coordinated release for every future setting would not
be, and would push development straight back to unregistered `local.*` keys.

### Manifest

`GET /api/v1/settings/manifest`

- Authenticated but not admin-only.
- Returns the public canonical manifest.
- Supports `If-None-Match` and `304 Not Modified`.
- Never includes current values, secrets, database bindings, or admin-only server configuration.
- Doubles as the capability endpoint for this subsystem: its presence means the contract is
  available, and its `api_version`/`revision` fields are the only version negotiation clients need.

`GET /api/v1/settings/capability` returns `api_version` and `revision` alone, for clients that want
to check compatibility without transferring the manifest body.

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
- Returns native typed values, resolution source, source context, definition revision,
  `updated_at`, and any policy constraint.
- A missing explicit value is not an error; resolution continues to the next declared scope.
- Applies `constrained_by` before responding, per **Preferences versus restrictions**.

`POST /api/v1/settings/values/effective` accepts a batched form for content-scoped resolution:

```json
{
  "keys": ["playback.audio_language", "playback.subtitle_mode"],
  "contexts": [
    { "context_id": "a", "library_id": "42", "series_id": "s-1001" },
    { "context_id": "b", "library_id": "42", "series_id": "s-1002" }
  ]
}
```

The batched form is not a convenience. `profile_series` and `profile_library` resolution is
per-item, so a season view, a continue-watching row, or any list that needs resolved track
preferences would otherwise issue one request per item. One round trip resolving *n* contexts
against a single prepared query is the required shape; per-item requests are a rejected design.
See **Read path** for the corresponding server-side rules.

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
   re-fetch effective values rather than trusting event payload values. Events ride the existing
   realtime event hub (`internal/events`) on a **new** `user_settings` channel with per-user and
   per-profile routing, following the personal-delivery pattern `allowsEventForClaims` already
   applies to notifications. The existing `settings` channel is reserved for admin server
   configuration: it is declared in `internal/events/types.go` and granted to admins only in
   `allowedChannelsForRole`, and although it currently has no publishers, overloading one channel
   name for both admin-wide and per-user payloads is a routing mistake waiting to leak.

HTTP `400` is used for malformed batches. A syntactically valid batch returns `200` with typed
per-mutation results such as `applied`, `already_applied`, `invalid_value`, `forbidden`, or
`transient_failure`.

Concurrent writes to the same identity are last-write-wins in server receipt order; each write
increments the stored row `revision`. There is no compare-and-set precondition in v1 — settings
are low-frequency user-intent values where the newest explicit choice should win.

### Removed surfaces

The release removes, rather than adapts, the old preference surfaces:

- String-valued `GET`, `PUT`, and `DELETE /api/v1/settings...` handlers.
- Preference fields on profile create/update/response DTOs, including language, subtitle behavior,
  skip behavior, quality, and next-up behavior.
- Separate library and series default-language/subtitle mutation routes. Track-selection history may
  remain specialized, but user preference defaults move to this contract.
- The open-ended unknown user-setting extension bag.
- The legacy `user_settings` string key/value table itself. Its only non-settings tenant —
  jellycompat display-preferences blobs — moves to a dedicated jellycompat store first (see below).
- Client-written raw remote keys and local copies of remote defaults/ranges.

The unknown-key extension bag deserves specific mention, because it is the mechanism that made all
of this possible. `keyUsesUserScope` in `internal/api/handlers/settings.go` currently returns true
for *any* unregistered key, so a client can invent a production setting unilaterally and the server
will store it. That behavior does not survive the release: after it, unknown keys are always
rejected, and every remaining stored key has a manifest entry or a migration disposition.

All production reads and writes use the typed manifest, effective-values endpoint, and mutation
endpoint immediately after the release.

## Jellyfin compatibility surface

`internal/jellycompat` serves third-party Jellyfin clients (Infuse, Findroid, JellyCon) that Silo
does not control and cannot ask to adopt anything:

- jellycompat runs on its own router and listener with its own auth middleware. No settings
  contract negotiation, header, or gate is ever added to jellycompat routes. Since this design no
  longer gates the first-party chain either, this is now a statement of scope rather than an
  exemption.
- The hardcoded Jellyfin user `Configuration` DTO and the disposition-based default audio/subtitle
  stream selection read none of the retired preference columns and are unaffected.
- `GET`/`POST /DisplayPreferences/{id}` (`internal/jellycompat/handlers_displayprefs.go`) is
  affected twice: it persists its blobs through the legacy `user_settings` string store under
  `jellycompat:displayprefs:*` keys, and `seedFromProfile` reads the profile `subtitle_language`,
  `subtitle_mode`, and `auto_skip_credits` columns this work removes. The release therefore (1)
  moves existing display-preferences blobs into a dedicated jellycompat storage table during the
  migration and (2) repoints the seed at the canonical resolver. Display-preferences blobs are
  Jellyfin client state, not production Silo settings; they do not join the manifest.
- **The seed resolves at profile scope only.** A Jellyfin client has no Silo device identity, so
  there is no correct `device_id` to resolve against. Resolving with a synthesized or borrowed
  device ID would silently import an unrelated device's overrides into a third-party client, and
  registering one would pollute the device registry with rows the user never created. The seed
  therefore walks the definition's resolution order with `profile_device` skipped.
- The phase-0 inventory covers jellycompat reads/writes alongside the first-party clients.

## Canonical storage

Remote values move to one typed `user_setting_values` table in the same release. The manifest
remains the schema; the database stores validated JSON and scope identity.

The public contract stays separated from physical storage regardless: internal bindings map a
definition onto its store, and the manifest never exposes table or column names. That indirection
is what lets storage change later without touching a client. It is not a reason to defer the
consolidation — doing so would mean writing bindings to `user_profiles` columns,
`user_device_settings`, `library_playback_prefs`, and `series_playback_prefs` that the migration
then makes obsolete.

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

Delete behavior is application-enforced, not FK-inherited. The per-user SQLite store deliberately
declares no foreign keys, and the existing PostgreSQL preference tables carry no references on
library, series, or device columns, so this table cannot inherit that behavior from constraints.
The PostgreSQL table keeps the cascades that do exist today (user ownership, and composite profile
ownership); everything else — a profile or user delete removing its values, library/series
deletion removing only values scoped to that entity, device forgetting removing `profile_device`
values — is performed by the owning delete paths and verified by the store conformance suite in
both backends.

Mutation idempotency uses a separate `user_setting_mutations` table keyed by
`(user_id, mutation_id)` with request hash, serialized result, and `expires_at`; rows expire after
30 days.

`expires_at` is not self-enforcing. A background sweeper deletes expired idempotency rows on the
same schedule and shape as `internal/policy/decisionlog_cleanup.go`, which already solves exactly
this problem for decision logs. Without it the table only grows. `user_setting_migration_rejects`
is bounded by the one-time migration rather than by traffic, so it is retained indefinitely and
removed by the operator, but it is reported in the completion summary so it cannot be forgotten.

The migration also creates `user_setting_migration_rejects`, an inactive audit table with source
table/key/identity/value and rejection reason. It has no runtime read/write API and is not an
extension bag. Its only purpose is to retain unrecognized or invalid historical rows for operator
inspection instead of silently deleting them.

### Read path

The repository's stated priority is performance and reliability first, and this design replaces
narrow purpose-built tables with a generic five-scope table. That trade has to be paid for
explicitly rather than assumed.

Normative rules:

1. **One query per resolution request, not one per scope.** Resolving a key with a four-scope chain
   issues a single query over the candidate identities, and the resolver ranks the returned rows by
   the definition's `resolution_order` in Go. Five sequential index lookups per key per item is a
   rejected implementation.
2. **Batched context resolution is the primary read shape** for anything content-scoped. See the
   `POST /values/effective` batch form above. A list view resolves *n* items in one round trip and
   one query.
3. **The covering index for the hot path is
   `(user_id, profile_id, key, scope)`**, in addition to the partial unique indexes, which exist for
   correctness rather than for reads. `profile_series` and `profile_library` resolution additionally
   needs `(user_id, profile_id, series_id)` and `(user_id, profile_id, library_id)`.
4. **Playback and catalog paths take a snapshot, not per-item resolution.** A session resolves its
   settings once at start and carries an effective-preference snapshot, which is what
   `internal/catalog/detail.go` and `internal/api/handlers/playback.go` effectively do today with
   `Profile.Language`. Re-resolving mid-stream is a correctness hazard as well as a cost.
5. **The release ships with a benchmark against the tables it replaces.** `series_playback_prefs` and
   `library_playback_prefs` reads are the baseline; a consolidated read that regresses a hot catalog
   or playback path against that baseline blocks the release. Consolidation is a tidiness win, and
   a tidiness win does not get to cost latency on a list endpoint.
6. **Account- and profile-scope values are cacheable per request** and should be resolved once per
   request rather than per consumer. Device-scope values are cacheable for the life of a session.

If rule 5 fails, the correct outcome is to keep the specialized tables as permanent bindings. That
is an acceptable end state, not a failure of the contract.

The one-time migration runs transactionally before the server accepts traffic:

1. Create and validate the canonical manifest and new tables.
2. Transform known values from account settings, profile columns, device settings, and
   library/series preference stores into typed JSON rows using checked-in migration rules.
3. Normalize aliases and values according to the migration table below.
4. Copy unrecognized ad hoc rows to `user_setting_migration_rejects` and include their counts/keys in
   the preflight and completion report. They do not become active settings.
5. Quarantine a recognized key whose stored value fails validation and has no normalization rule
   into `user_setting_migration_rejects`, reported the same way as unrecognized rows; the setting
   becomes unset and resolves to the contract default. Abort only on structural failures —
   duplicate identity, row-count/checksum mismatch, or schema errors. Nothing is silently dropped:
   every quarantined row appears in the preflight and completion report.
6. Record the completed contract version and manifest revision in the database.
7. Retain specialized track-history fields only when they represent a concrete selected track or
   signature rather than a default user setting.

One narrow exception to "do it all at once" is worth taking, because it costs no code: **the
migration does not `DROP` the columns and tables it supersedes.** It stops reading them and leaves
them in place, unread, to be dropped by a trivial follow-up migration one release later.

This is not a compatibility path — nothing reads those columns after the release, and no client can
reach them. It is an operator affordance. Omitting a `DROP` statement is free, and it converts
recovery from "restore the pre-upgrade backup and the prior binary together" into "revert the
binary." Given the migration touches two backends and fans out across per-user SQLite databases,
that is worth one deferred cleanup migration.

Migration atomicity is per database. The PostgreSQL store migrates in one transaction before the
server accepts traffic. Each per-user SQLite database migrates in its own transaction at startup
and records a per-database completion marker. One damaged user database must not prevent the
server from starting for everyone else.

A user database that fails structurally is quarantined, and the account then operates in
**degraded settings mode**: every definition resolves to its contract default, mutations are
rejected with a typed `settings_unavailable` result, and both the user and the operator see an
explicit error naming the condition. The account is **not** blocked. An earlier revision of this
design blocked "settings-dependent operation," which in practice means playback, browsing, and
resume — an account-wide outage caused by a corrupt preferences database. Falling back to defaults
degrades the experience; blocking removes it. Defaults are always a safe answer, which is the whole
point of having them.

There is no dual read, dual write, or fallback adapter between the old and new *storage* once a
database has migrated. Operators must take the normal pre-upgrade database backup.

## Initial canonical scope decisions

The first manifest must register every official key currently read or written by a supported
client. The following decisions resolve today's duplicate semantics:

| Canonical setting/family | Persistence and scopes | Migration disposition |
|---|---|---|
| `playback.audio_language` | remote: profile, profile_device, profile_library, profile_series | Migrate profile `language` as the roaming fallback; existing device values become real overrides. |
| `playback.subtitle_language` | remote: profile, profile_device, profile_library, profile_series | Migrate existing profile/library/series subtitle fields to this key. |
| `playback.subtitle_mode` | remote: profile, profile_device, profile_library, profile_series | Existing values are normalized to one enum. |
| `playback.show_forced_subtitles` | remote: profile, profile_device, profile_library, profile_series | Preserve explicit false separately from unset. |
| `catalog.metadata_language` | remote: profile | Migrate existing `preferred_metadata_language` values to this key. Constrained by `profile_preferred_metadata_language` policy input. |
| `playback.preferred_quality` | remote: profile, profile_device | Profile quality is fallback; device override wins. Constrained by account/profile `max_playback_quality` as a `ceiling`. |
| `playback.auto_skip_intro`, `credits`, `recap` | remote: profile, profile_device | Existing profile columns are fallback; explicit device values win. |
| `playback.auto_play_next`, `auto_play_next_preview`, `next_up_prompt_seconds` | remote: profile, profile_device | Use `playback.*`; Android's `player.next_up_prompt_seconds` is migrated and removed from production writes. |
| `subtitle_appearance` | remote: profile, profile_device | Profile value roams; device customization wins. Existing account fallback is copied to each profile. |
| `player.*` technical playback keys | remote: profile_device | HDR, DV, seek cache, speed, sync, gravity, and orientation remain device-specific and server-validated. |
| Theme, text scale/weight, contrast, custom theme variables/CSS | remote: **profile**, profile_device | Existing account rows are copied to every profile on the account; device override for per-screen contrast/scale. Owner-tag all local caches; never apply a cached value to a different authenticated user. |
| Date/time format | remote: **profile** | Existing account rows are copied to every profile. |
| Search media scope | remote: profile | Preserve strict enums. |
| `ui.library_page_state` | remote: profile_device | Keep navigation state tied to one profile/device. |
| OS caption mirroring, platform decoder diagnostics, temporary sleep timers | client_local or private `local.*` | Production caption-mirroring UI is contract-known local; diagnostics/timers remain private local. |

### Appearance belongs to the profile, not the account

Theme, text scale, contrast, custom CSS, and date/time format are stored today in `user_settings`
keyed by `user_id`, so they are account-wide. That is an artifact of the storage that predates
household profiles, and this contract should not canonize it — especially given the immutability
rules above, which would make it expensive to revisit.

Profiles are household members sharing one login. Appearance is the most personal category in the
product, and account scope produces two bad outcomes directly:

- Everyone in the household shares one theme, one text size, and one contrast setting. A parent who
  needs larger text imposes it on everyone, and a child who wants a different theme cannot have one.
- Combined with the account-scope authorization rule below, *any* non-child profile can restyle
  every other profile's UI, including the primary's. Nothing about that reads as intentional.

These keys therefore land at `profile` scope, with the existing account row copied to every profile
during migration — the same deterministic fan-out already specified for subtitle appearance. This
costs one migration rule now and avoids a new-key migration later.

`account` scope is kept in the model, because genuinely account-wide values exist (billing-style,
security, and account-identity preferences will want it). It simply should not be the default
landing place for anything that is merely stored per-user today. **The inventory in phase 0 must
justify every `account`-scope assignment rather than inheriting it from current storage.**

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
Every release in the coordinated cutover version set embeds the same protocol version and the exact
same manifest revision, and the pre-release conformance gate verifies that exact set.

**After the cutover, clients pin whatever revision they were built from** and adopt new revisions on
their own release cadence; revision-aware filtering keeps mixed-revision pairs safe. The cutover is
the only time a matching release is required in another repository.

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
10. Treat a pre-contract server (manifest endpoint absent) as a hold state, not a failure: keep
    entries queued, surface the server-upgrade-required message, and resume flushing once the
    server is upgraded. Do not drop entries, retry-spin, or attempt a legacy write.
11. Treat a `settings_unavailable` result as retryable, not terminal. It signals a degraded server
    store, not a bad mutation.

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
- Settings hidden by `platforms` are hidden, not displayed disabled without explanation.
- A setting constrained by policy shows the permitted value with an explanation of the limit, and
  offers only `permitted_values`. A `locked` constraint shows a lock affordance and states who set
  it — never a disabled control with no reason.
- When a stored preference exceeds a current restriction, the screen says so rather than silently
  rewriting the user's choice. The stored preference is still theirs; it is just capped today.
- Admin device views render controls from the canonical manifest and may clear remote overrides.
  They do not claim access to client-local values.
- Apple’s current subtitle copy — explicitly separating profile behavior from per-device appearance
  — is the UX baseline to retain and generalize.

## Validation and authorization

- Validation occurs in the server contract layer before any setting value is stored. Validation
  checks a value against its *definition*; it does not apply policy restrictions — see
  **Preferences versus restrictions**.
- Profile DTOs no longer contain preference fields, so profile identity/access updates cannot bypass
  settings validation.
- The authenticated user may mutate owned profiles according to existing profile permissions.
- Account-scope values affect every profile on the account, so account-scope mutations require the
  **primary** profile. Child profiles and ordinary non-primary profiles may read them but not
  write them. UX copy for account-scope settings states that they apply to the whole account.
  Restricting the write to the household parent matches what `is_primary` already means; allowing
  any non-child profile to change a value every other profile sees is an authorization gap, not a
  convenience.
- Device mutations require a non-empty bounded device ID and register/update device metadata.
- Library/series settings require access to the referenced content scope.
- Admin clear/reset operations are audited.
- Settings values must never contain secrets. A future secret-like preference requires a dedicated
  encrypted/credential API, not a new settings schema type.

## Coordinated release plan

Implementation is split across PRs, but none of the new clients or breaking server routes are
released independently. The deployable unit is one version set containing the server, bundled web,
Apple clients, and Android clients built against contract version `1` at the same manifest revision.

### Phase 0 — freeze and inventory

- Stop adding ad hoc remote key literals in every repository.
- Inventory server, web, Apple, Android, and jellycompat reads/writes.
- Classify every production setting and record aliases, current defaults, ranges, and consumers.
- Justify every proposed `account`-scope assignment rather than inheriting it from current storage.
- Identify every definition that a policy input constrains.
- Define a migration disposition for every discovered stored key and profile preference column.

### Phase 1 — ship the #376 P1 fixes independently

These do not depend on the contract and should not wait for it:

- Fix Apple audio language so a stored value affects selection.
- Fix Android's `player.` → `playback.next_up_prompt_seconds` alias, the `4.0` → `3.0` speed clamp,
  and the `dv_profile7_hdr10_fallback` default.
- Remove Android's unregistered device-setting writes.
- Replace Apple and Android pending-write logic with durable scoped outboxes.
- Owner-tag web theme and custom-style caches.

Shipping these first keeps the contract release purely structural and stops user-visible bugs from
being held to the migration's schedule in either direction.

### Phase 2 — contract and storage

- Add `contracts/settings/v1` and manifest validation tests.
- Register all official current keys, including web theme/customization keys.
- Add canonical storage, mutation idempotency storage and its sweeper, and the one-time migration.
- Apply `constrained_by` in the resolver, wired to `internal/policy`.
- Add manifest, capability, values, effective-values (single and batched), and mutation routes.
- Add the `user_settings` event channel with per-user routing.
- Generate Go/TypeScript registry code from the manifest.
- Keep the new routes behind an unreleased build gate until the client work is ready.

### Phase 3 — canonical resolution

- Move profile, account, device, library, and series defaults to canonical values.
- Make playback/catalog paths consume the canonical resolver and its permitted values.
- Remove preference fields and mutation behavior from profile/library/series DTOs.
- Close the unknown-key extension bag.
- Repoint the jellycompat DisplayPreferences seed at the canonical resolver at profile scope, and
  move its blobs to dedicated jellycompat storage.

### Phase 4 — clients

- Generate and adopt Swift/Kotlin/TypeScript bindings.
- Replace raw key literals with generated types.
- Add the standardized scope/source/constraint UX.
- Add server-upgrade-required messaging keyed on the manifest endpoint being absent.

### Pre-release gate

- All four repositories pass the shared conformance fixture at the exact commits selected for the
  release.
- Migration is rehearsed against anonymized copies representing SQLite and PostgreSQL user stores,
  including invalid/unknown-value failure cases.
- The read-path benchmark shows no regression against the specialized tables being replaced.
- Store-distributed Apple/Android builds are approved and available before the server release is
  published.
- Release notes name the server build to pull alongside the client versions. `silo-android`
  publishes plain versions to Play Store and `silo-server` ships as Docker `latest` off the default
  branch, so the notes carry the pairing that image tags do not.
- Release notes state that server and apps must be upgraded together and that rollback requires
  reverting the binary, or restoring the pre-upgrade backup once the follow-up migration has
  dropped the superseded columns.
- Server startup reports a migration preflight summary.

### Cutover

1. Operator takes the required database backup.
2. Operator upgrades the server; startup runs the migration transaction and contract validation.
3. Server serves the matching bundled web client.
4. Users update Apple/Android clients. Mismatched clients receive `404` on removed routes; new
   clients against an old server show server-upgrade-required.
5. No old settings route or schema remains active after the migration commits.

### Rollback

Reverting the binary alone is **not** sufficient, and the reason is specific: the
DisplayPreferences move deletes the `jellycompat:*` rows from `user_settings` once it has
copied them, and the previous binary reads exactly those rows. An older server therefore
starts cleanly and silently serves defaults, so every Jellyfin client's saved view
preferences look reset. The settings backfill does not have this problem — it only derives
new rows and never touches the legacy tables.

Order matters:

1. Stop the server.
2. Roll the schema back before re-deploying the old binary:
   `make migrate-down-to VERSION=<last version to keep>`. This is a dedicated command rather
   than the `goose` CLI because the backfill and the DisplayPreferences move are Go
   migrations registered in-process, which the standalone CLI cannot see or reverse.
3. Deploy the previous binary.

**`down-to` is a range, not a list, and this release is not contiguous.** The settings work
is spread either side of migrations that belong to other features: `20260727010621`
(settings tables) and `20260728132327` (the DisplayPreferences move) sit around
`20260727212045_invitations` and `20260727220010_profile_onboarding`, both of which are
older-binary migrations. Goose walks down from the newest applied version and stops at the
one named, so it reverts everything in between — and `profile_onboarding`'s down is
`DROP TABLE user_profile_onboarding`, which discards every profile's onboarding-tour state.

So there is no version that undoes only this release:

- `VERSION=20260728132326` reverts just the DisplayPreferences move — the destructive half,
  and the one that matters for a binary rollback. Prefer this when the goal is simply
  "let the old binary find its jellycompat rows again."
- `VERSION=20260727212045` additionally reverts the settings tables, and takes
  `profile_onboarding` with it. Only use it if you accept losing tour state, or if you are
  restoring from backup anyway.

Two caveats an operator has to know before upgrading:

- **Take a database backup first.** The rollback path is exercised by a test
  (`internal/database/migrate_downto_test.go`), but a backup is the only recovery once the
  follow-up migration drops the superseded columns — and, given the interleaving above, the
  only way to undo this release without collateral.
- **Rolling back discards settings written while the new binary was live.** The canonical
  write path does not mirror into the legacy tables, so `rollbackSettingValues` drops those
  changes; users revert to their pre-upgrade preferences rather than to defaults.
- **The per-user SQLite backend cannot be rolled back at all.** Its migrations are
  version-numbered with no down path, and an older binary refuses to open a database newer
  than it knows (`internal/userdb/migrate.go`), so every per-user store fails to open and
  the rollback is an outage rather than a degradation. Installs on `userdb.backend: sqlite`
  must restore from backup. The default backend is PostgreSQL.

### Post-cutover cleanup

- Verify migrated counts/checksums and effective-value samples.
- Add stale empty-device cleanup and Forget device UX.
- Retain the one-time migration as an inert historical migration unless Silo's release policy
  permits skipping directly to newer versions.

## Testing

### Contract tests

- Manifest validates against its schema and has a stable digest.
- Every default validates against its type.
- Every resolution chain references allowed scopes exactly once and ends with `default`.
- Generated Go, TypeScript, Swift, and Kotlin outputs are reproducible.
- Every stored legacy source key/column has exactly one migration disposition.
- Every additively introduced enum member, scope, and widened bound carries an `introduced_in`
  revision, and no `introduced_in` exceeds the manifest revision.
- A manifest change that narrows a scope, tightens a range, removes an enum member, or changes a
  value type fails the compatibility check without a new key.
- Every `constrained_by.policy_input` names a field `internal/policy` actually produces, and a
  `ceiling`/`floor` constraint is declared only on an ordered enum or a numeric type.

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
- Revision tolerance in both directions: an older-revision client is accepted, and a newer-revision
  client's unknown definitions, enum members, and scopes are filtered rather than rejected.
- No route in the first-party chain returns `426`, and no settings version check exists in the
  authenticated middleware.
- Removed routes return `404`; no legacy settings handler or profile DTO preference field survives.
- Policy constraint: an effective value is capped to the permitted value, `requested_value` is
  reported, `permitted_values` narrows correctly, and a mutation exceeding a restriction is
  **stored** rather than rejected and takes effect when the restriction is lifted.
- Degraded settings mode returns contract defaults and `settings_unavailable` instead of blocking
  the account.
- Batched effective resolution returns the same results as *n* single-context calls, in one query.
- jellycompat DisplayPreferences seeding from the canonical resolver at profile scope with
  `profile_device` skipped, and blob survival across the store move.
- Incognito/new-device fallback without copying another device override.
- Empty stale-device retention cleanup and idempotency-row expiry sweeping.

### Client tests

- Generated key/type use, and revision-aware filtering of definitions, enum members, and scopes.
- A pre-contract server produces a server-upgrade-required message rather than an unhandled error,
  an empty settings screen, or a crash.
- New sign-in/incognito receives profile values but not another device override.
- Profile switch and server switch cannot redirect queued writes.
- Process death preserves outbox entries.
- Failed writes remain queued and visible.
- Cache ownership prevents cross-account flashes.
- UI copy accurately names scope and reset behavior.

### Cross-platform conformance fixture

The contract directory includes a fixture set of definitions, explicit values, contexts, and
expected effective results. Server, web, Apple, and Android run the same fixture cases. This is the
gate that catches key, default, type, and precedence drift.

It gates the coordinated release at the exact commits selected for it, and it stays afterwards as a
**per-repository CI gate**: each repository runs it against its pinned manifest revision on every
PR. The second role is the durable one. Checking four commits once at release time catches drift
that already exists; running it per PR catches drift as it is introduced, which is what keeps the
contract true once releases stop being coordinated.

## Acceptance criteria

- A production user-facing setting cannot land in a client without a canonical manifest entry.
- A private `local.*` knob cannot be sent to the server.
- The server rejects unknown keys and invalid typed values.
- Swift, Kotlin, TypeScript, and Go use generated key/type bindings.
- Profile language, subtitle, and appearance preferences roam into a new incognito session.
- Device overrides do not roam into a different device identity.
- Effective responses explain where values came from and whether policy constrained them.
- A client can never present a choice that policy will refuse, and a stored preference is never
  destroyed by a restriction.
- Apple and Android persist failed mutations with full server/profile/device identity.
- The verified Android key/default/range drift and Apple no-op audio preference are covered by
  conformance tests.
- Only the primary profile can mutate account-scope values.
- The one-time migration either completes and verifies atomically or leaves the database unchanged.
- A quarantined per-user database degrades that account to contract defaults; it does not block the
  account or the server.
- No hot catalog or playback read regresses against the specialized tables it replaces.
- jellycompat routes carry no contract negotiation, and its DisplayPreferences seed and storage no
  longer depend on removed profile columns or the legacy string settings store.
- No old string settings route, open-ended key bag, or duplicated profile preference field remains
  after cutover.
- No settings version check exists in the authenticated middleware, and no first-party route
  returns `426`. A mismatched client fails because the routes are gone, not because a gate refused
  it.
- **After the cutover, adding a setting requires no coordinated release.** A server manifest PR can
  ship alone, and each client adopts the new revision on its own cadence.

## Required PR workflow for a new setting

1. Open a `silo-server` PR that adds the manifest definition, default, scopes, resolution order,
   UX copy, persistence class (or `client_local` declaration), any `constrained_by` binding,
   `introduced_in` revision, and contract tests.
2. Merge the contract PR before merging a production client implementation.
3. Update the client’s pinned manifest and regenerate bindings.
4. Implement the UI/consumer using generated types, filtering against the server's advertised
   revision.
5. Add the cross-platform fixture when the setting has resolution, constraint, or coercion behavior.

Steps 3 and 4 happen on each client's own schedule. A new setting is one server PR plus *n*
independent client PRs, never a synchronized release. That property is the reason the contract can
be strict without becoming the thing people route around.

This server-first PR requirement is intentional governance, not a requirement that every value be
stored by the server. It keeps the vocabulary, types, defaults, and UX semantics consistent while
preserving a clearly bounded client-local storage option.
