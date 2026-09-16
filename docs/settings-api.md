# Canonical Settings API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. The frozen alpha `/api/v1` settings routes are kept in
> [Bridge: the frozen `/api/v1` settings routes](#bridge-the-frozen-apiv1-settings-routes) at the
> end of this document and are retired after the pre-1.0 bridge window. See
> [the native API contract](architecture/api-contract.md).

The canonical settings API stores typed user preferences from the shared
settings contract. Client implementations should discover the server contract
before rendering controls or writing a value; do not keep a separate list of
keys, scopes, enum members, or defaults.

## The stable surface

Silo 1.0's stable settings surface is the `settings` section of the native `/api/v2` contract
(`contracts/api/v2/openapi.json`, tag `settings`). It serves the same settings contract, the same
stored rows and the same `internal/settingsresolve` semantics as the frozen v1 routes; what
changes is the request and response envelope. Everything in this section is taken from the operation
declarations in `internal/apiv2/settings.go` and the committed fixtures under
`contracts/api/v2/fixtures/`; the ledger row for each v1 route is in
`contracts/api/v2/migration.json` (`"section": "settings"`).

All paths below are absolute. Errors are RFC 9457 problem documents; a rejected request member is
`422` `validation_failed` with an `errors[]` entry whose `location` names the member
(`query.keys`, `path.key`, `body.value`, `header.X-Silo-Device-Id`, …).

### Operations

| Operation                                | Method and path                                      | Replaces (v1)                                                     |
| ---------------------------------------- | ---------------------------------------------------- | ----------------------------------------------------------------- |
| `getSettingsContract`                    | `GET /api/v2/settings/contract`                      | `GET /settings/contract`, `GET /settings/manifest`                |
| `getSettingsContractCapabilities`        | `GET /api/v2/settings/contract/capabilities`         | `GET /settings/capability`, `GET /settings/contract/capabilities` |
| `getOverlayConfig`                       | `GET /api/v2/settings/overlay-config`                | `GET /settings/overlay-config`                                    |
| `getEffectiveSubtitleAppearance`         | `GET /api/v2/settings/subtitle-appearance/effective` | `GET /settings/subtitle_appearance/effective`                     |
| `updateSubtitleAppearanceDeviceOverride` | `PUT /api/v2/settings/device/subtitle-appearance`    | `PUT /settings/device/subtitle_appearance`                        |
| `deleteSubtitleAppearanceDeviceOverride` | `DELETE /api/v2/settings/device/subtitle-appearance` | `DELETE /settings/device/subtitle_appearance`                     |
| `listPluginSettings`                     | `GET /api/v2/settings/plugins`                       | `GET /settings/plugins`                                           |
| `getPluginSettings`                      | `GET /api/v2/settings/plugins/{installation_id}`     | `GET /settings/plugins/{installation_id}`                         |
| `updatePluginSettings`                   | `PUT /api/v2/settings/plugins/{installation_id}`     | `PUT /settings/plugins/{installation_id}`                         |
| `listSettingValues`                      | `GET /api/v2/settings/values`                        | `GET /settings/values`                                            |
| `listEffectiveSettings`                  | `GET /api/v2/settings/values/effective`              | `GET /settings/values/effective`                                  |
| `resolveEffectiveSettings`               | `POST /api/v2/settings/values/effective`             | `POST /settings/values/effective`                                 |
| `updateNavigationShortcut`               | `PUT /api/v2/settings/values/nav.shortcuts/item`     | `PUT /settings/values/nav.shortcuts/item`                         |
| `getSettingValue`                        | `GET /api/v2/settings/values/{key}`                  | `GET /settings/values/{key}`                                      |
| `updateSettingValue`                     | `PUT /api/v2/settings/values/{key}`                  | `PUT /settings/values/{key}`                                      |
| `deleteSettingValue`                     | `DELETE /api/v2/settings/values/{key}`               | `DELETE /settings/values/{key}`                                   |

The untyped legacy routes (`/settings/`, `/settings/{key}`, `/settings/device/{key}`,
`/settings/effective`) have no v2 successor; they are removed at 1.0 and were already listed in
`docs/architecture/v1-scope.md`. The admin projection is not part of the settings section; it has its own v2
operations, `listAdminUserSettingValues` (`GET /api/v2/admin/users/{id}/settings/values`),
`setAdminUserSettingValue` (`PUT /api/v2/admin/users/{id}/settings/values/{key}`) and
`deleteAdminUserSettingValue` (`DELETE /api/v2/admin/users/{id}/settings/values/{key}`).

`X-Profile-Id` is required on every `/settings/values…` operation, on the subtitle-appearance
operations and on the device-override mutations; it is optional on the contract, capabilities,
overlay-config and plugin operations. `X-Profile-Token` is optional everywhere.

### Contract discovery and capabilities

`GET /api/v2/settings/contract` returns the same canonical public manifest bytes and the same
`ETag` the v1 routes send, with `Cache-Control: private, no-cache`. A conditional
`If-None-Match` GET is not carried yet; it lands with the foundation caching rules, so a v2
client compares the `ETag` (or the capability document's `contract_etag`) itself and re-fetches.

`GET /api/v2/settings/contract/capabilities` returns nine settings-specific members plus the
common `revision`, `state` and `allowed` fields. The previous numeric `revision` is now
`manifest_revision`; string `revision` identifies the complete authorized capability document.
All twelve fields are present, and `scopes` and `client_families` are never null. This capability
read supports `ETag` and `If-None-Match` revalidation with `Cache-Control: private, no-cache`;
a matching authorized representation returns a bodyless `304`.

Example configured response (`get_settings_contract_capabilities_ok.json`; the opaque revision
is illustrative):

<!-- prettier-ignore -->
```json
{
  "api_version": 1,
  "revision": "opaque-capability-revision",
  "manifest_revision": 12,
  "state": "available",
  "allowed": true,
  "contract_etag": "\"etag-12\"",
  "definition_count": 40,
  "scopes": [
    "account",
    "profile"
  ],
  "client_families": [
    "tv",
    "web"
  ],
  "supports_batched_effective": true,
  "supports_idempotent_writes": true,
  "supports_atomic_shortcuts": true
}
```

The web client reads `api_version`, `manifest_revision` and `contract_etag` from this document, gates
each vendored definition on `manifest_revision >= introduced_in` plus `supports_batched_effective`, and
gates the shortcut editor on `supports_atomic_shortcuts`. Read `supports_idempotent_writes` with
care: the server computes the flag for the v1 `X-Silo-Mutation-Id` replay, but no v2 write
declares that header (see "Writes have no mutation id" below), so a v2 client must not rely on
replay semantics while the flag is `true`. The web client masks the flag to `false` for exactly
this reason.

### Query parameters are repeated, not comma-separated

Every list-valued query parameter is sent once per value. `keys=<csv>` becomes
`keys=a&keys=b`; the same rule applies to `library_ids` and `series_ids` on
`listEffectiveSettings`. A comma inside a value is part of the key name and will be rejected as
an unknown key.

```http
GET /api/v2/settings/values?scope=profile&keys=ui.theme&keys=playback.preferred_quality
GET /api/v2/settings/values/effective?keys=ui.theme&library_ids=3&library_ids=7
```

`scope` is a required enum on `listSettingValues`, `getSettingValue`, `updateSettingValue` and
`deleteSettingValue`: `account`, `profile`, `profile_client`, `profile_device`, `profile_library`
or `profile_series`. The identity members keep their v1 names (`profile_id`, `device_id`,
`library_id`, `series_id` as query parameters; `X-Silo-Client-Family`, `X-Silo-Device-Id`,
`X-Silo-Device-Name`, `X-Silo-Device-Platform` as headers). Library ids and profile ids are
string `ID`s on both sides; `updated_at` is an instant.

### Collections use the `{items}` envelope

The v1 envelopes `{values}`, `{settings}`, `{contexts}` and `{installations}` all become the
common collection envelope `{items: [...]}`. The setting-value collections keep `revision` beside
`items`. These collections are unpaginated — they are bounded by the requested keys or the
installed plugins — so `page` is never present.

`GET /api/v2/settings/values/effective?keys=ui.theme&keys=playback.preferred_quality`
(`list_effective_settings_ok.json`):

```json
{
  "items": [
    {
      "key": "ui.theme",
      "value": "cinema-light",
      "source": "profile",
      "definition_revision": 3,
      "updated_at": "2026-01-02T03:04:05.678Z",
      "source_context": {
        "profile_id": "p-owner"
      },
      "scope": "profile",
      "profile_id": "p-owner"
    },
    {
      "key": "playback.preferred_quality",
      "value": "auto",
      "source": "default",
      "definition_revision": 3
    }
  ],
  "revision": 8
}
```

Each effective item carries the resolved `value`, its `source` scope, `definition_revision`, the
constraint members when a policy constrained it, and — when a stored row won — `source_context`
plus the winning row's own scope and identity members. A default winner has none of the row
members.

### Batch effective resolution

`POST /api/v2/settings/values/effective` resolves one bounded list of keys under several content
contexts in one store read; use it for a grid or list instead of one GET per item. The body is
`{keys, contexts}`; both are required and non-empty at the schema, unknown members are rejected,
and each context is `{context_id, library_id | series_id}` with `library_id` as a string `ID`.
The response preserves the requested key order, including repeated keys. Each `context_id` is an
opaque caller token: whitespace is retained, and uniqueness is checked on the exact token. Empty
or whitespace-only tokens are rejected. v1 retains its existing trimmed, deduplicated behavior.

The batch resolves for the acting profile and declared device, so the operation declares only the
device and client-family headers: the `profile_id`, `device_id`, `library_ids` and `series_ids`
query parameters of the single-shot GET are not accepted here (v1 parsed and then ignored them).

`contexts` is bounded at 100 entries (`maxItems`), not the 200 the seam's content-id budget might
suggest. The seam bounds the *ids* in one request at 200 — the same budget the single-shot GET
spends on `library_ids` plus `series_ids` — and a context may name both a `library_id` and a
`series_id`, so declaring 200 contexts would advertise a capacity the seam refuses as soon as
more than 100 contexts name both. Halving the declared bound keeps every valid context shape
inside one request and leaves v1 and the store query untouched; a 101st context is a 422 at
`body.contexts`.

```http
POST /api/v2/settings/values/effective
X-Profile-Id: <profile-id>
X-Silo-Client-Family: web
Content-Type: application/json

{"keys":["ui.theme"],"contexts":[{"context_id":"row-1","library_id":"3"},{"context_id":"row-2","series_id":"tv:12345"}]}
```

Response (`resolve_effective_settings_ok.json`); each item is `{context_id, settings}` in
request order:

```json
{
  "items": [
    {
      "context_id": "row-1",
      "settings": [
        {
          "key": "ui.theme",
          "value": "cinema-light",
          "source": "profile",
          "definition_revision": 3,
          "updated_at": "2026-01-02T03:04:05.678Z",
          "source_context": {
            "profile_id": "p-owner"
          },
          "scope": "profile",
          "profile_id": "p-owner"
        }
      ]
    },
    {
      "context_id": "row-2",
      "settings": [
        {
          "key": "ui.theme",
          "value": "cinema-light",
          "source": "profile",
          "definition_revision": 3,
          "updated_at": "2026-01-02T03:04:05.678Z",
          "source_context": {
            "profile_id": "p-owner"
          },
          "scope": "profile",
          "profile_id": "p-owner"
        }
      ]
    }
  ],
  "revision": 8
}
```

An empty `contexts` list is refused at the schema (`resolve_effective_settings_contexts_required.json`):

```json
{
  "type": "https://siloserver.org/docs/api/v2/problems/validation_failed",
  "title": "Validation failed",
  "status": 422,
  "detail": "The request did not pass validation; see errors.",
  "instance": "urn:silo:request:000000000000000000000038",
  "errors": [
    {
      "location": "body.contexts",
      "code": "out_of_range",
      "detail": "expected array length >= 1"
    }
  ]
}
```

### Unknown keys are `422`, not `404`

A key the settings contract does not define is request input, not a missing resource, so v2
answers `422` `validation_failed` at the key's declared location: `query.keys` on the list and
effective GETs, `body.keys` on the batch POST, `path.key` on the single-key operations. v1
answered `404` `unknown_setting`. The same rule covers the whole-document refusal of
`nav.shortcuts` on `updateSettingValue` and `deleteSettingValue` (v1: `400`
`atomic_update_required`) and every other rejected request member (v1: `400`).

`404` is kept for a real missing resource: `getSettingValue` and `deleteSettingValue` when nothing
is stored at the scope, a `profile_id`, `device_id` or `library_id` that names nothing, and a
plugin installation id that names nothing (numeric or not; identifiers are opaque, where v1
answered `400` for a non-numeric id).

`GET /api/v2/settings/values/effective?keys=no.such` (`list_effective_settings_unknown_key.json`):

```json
{
  "type": "https://siloserver.org/docs/api/v2/problems/validation_failed",
  "title": "Validation failed",
  "status": 422,
  "detail": "The request did not pass validation; see errors.",
  "instance": "urn:silo:request:000000000000000000000035",
  "errors": [
    {
      "location": "query.keys",
      "code": "invalid",
      "detail": "No setting named no.such exists in this server's contract"
    }
  ]
}
```

### Writes have no mutation id

`updateSettingValue` and `updateNavigationShortcut` declare no `X-Silo-Mutation-Id` header; a v2
client sends none, and the v1 idempotent replay (`X-Silo-Idempotent-Replay`,
`409 mutation_id_conflict`) does not exist on v2. The reason is sequencing, not a change of
design: idempotent replay is a foundation concern for every v2 mutation, and the foundation
mutation-retry work owns it. Until it lands, a v2 client retries a write as a plain desired-state
`PUT`: the same body converges on the same stored value, and the shortcut mutation is still
atomic on the server (an already-satisfied mutation is a successful no-op that does not advance
the revision). Once the header is declared on both writes, the web client's compile-time guard
flips and `supports_idempotent_writes` becomes meaningful on v2.

`updateSettingValue` accepts `{value}`, validated and normalized against the key's `value_schema`
(a refusal is `422` at `body.value`; v1: `400 invalid_value`), and answers `200` with the same
stored row `getSettingValue` serves. `deleteSettingValue` answers `204`.

`updateNavigationShortcut` accepts `{item, present}`; both are required at the schema (v1
answered `400` when `present` was missing). `item` is an extension bag whose members the
contract's `navigation-shortcuts.json` schema fixes. The response is the stored `nav.shortcuts`
row as a `SettingValue`, exactly as v1.

```http
PUT /api/v2/settings/values/nav.shortcuts/item
X-Profile-Id: <profile-id>
Content-Type: application/json

{"item":{"type":"section","library_id":7,"section_id":"recent","label":"Recently Added"},"present":true}
```

### Device override and plugin writes answer `200` with the document

`PUT /api/v2/settings/device/subtitle-appearance` takes the same `{value}` body as v1 (the
appearance as a JSON document in a string; only JSON validity is checked) and answers `200` with
the resolved `EffectiveSubtitleAppearance` — the same document
`GET /api/v2/settings/subtitle-appearance/effective` serves — where v1 answered `204`.
`X-Silo-Device-Id` is a required declared header on both device-override mutations (a missing id
is `422` at `header.X-Silo-Device-Id`; v1: `400`); `DELETE` still answers `204`, including when no
override existed. The device headers keep the v1 clamps (128/120/40) as declared bounds, so an
over-long value is a `422` where v1 truncated it.

`PUT /api/v2/settings/plugins/{installation_id}` takes `{values}`, an extension bag of strings
whose keys the plugin's `user_config_schema` defines; a refusal by the plugin is `422` at
`body.values`. It answers `200` with the same `{installation, values}` document
`getPluginSettings` serves (v1: `204`). `values` is `{}` rather than null when the account has
none. The v1 `admin_form` member of an installation is not carried: it is an administrator-form
descriptor no user-settings client renders.

```http
PUT /api/v2/settings/plugins/3
Content-Type: application/json

{"values":{"region":"eu"}}
```

Response (`update_plugin_settings_ok.json`):

```json
{
  "installation": {
    "id": "3",
    "plugin_id": "org.example.subtitles",
    "version": "1.2.0",
    "user_config_schema": [
      {
        "key": "region",
        "title": "Region",
        "description": "",
        "json_schema": "{\"type\":\"string\"}",
        "required": false
      }
    ],
    "routes": [
      {
        "id": "dashboard",
        "method": "GET",
        "path": "/dashboard",
        "access": "user",
        "navigable": true,
        "navigation_label": "Dashboard",
        "navigation_kind": "user",
        "static_asset": false
      }
    ],
    "assets": [],
    "category": "Tools"
  },
  "values": {
    "region": "eu"
  }
}
```

A missing `values` member (`update_plugin_settings_values_required.json`):

```json
{
  "type": "https://siloserver.org/docs/api/v2/problems/validation_failed",
  "title": "Validation failed",
  "status": 422,
  "detail": "The request did not pass validation; see errors.",
  "instance": "urn:silo:request:000000000000000000000029",
  "errors": [
    {
      "location": "body.values",
      "code": "required",
      "detail": "expected required property values to be present"
    }
  ]
}
```

`GET /api/v2/settings/plugins` returns the same installation objects in `{items}`
(`list_plugin_settings_ok.json`); an installation id that names nothing is `404`
(`get_plugin_settings_not_found.json`):

```json
{
  "type": "https://siloserver.org/docs/api/v2/problems/not_found",
  "title": "Not found",
  "status": 404,
  "detail": "Plugin installation not found",
  "instance": "urn:silo:request:000000000000000000000026"
}
```

### Migrating from v1

| v1                                                                               | v2                                                                              |
| -------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `GET /settings/manifest` or `/settings/contract`                                 | `GET /api/v2/settings/contract` (same bytes and `ETag`; no `If-None-Match` yet) |
| `GET /settings/capability`                                                       | `GET /api/v2/settings/contract/capabilities` (nine settings members plus the common capability fields)       |
| `?keys=a,b`, `?library_ids=1,2`, `?series_ids=x,y`                               | `?keys=a&keys=b`, `?library_ids=1&library_ids=2`, `?series_ids=x&series_ids=y`  |
| `{values: [...], revision}`, `{settings: [...], revision}`                       | `{items: [...], revision}`                                                      |
| `{contexts: [...], revision}` (batch POST)                                       | `{items: [{context_id, settings: [...]}], revision}`                            |
| `{installations: [...]}`                                                         | `{items: [...]}`                                                                |
| `404 unknown_setting`                                                            | `422 validation_failed` at `query.keys`, `body.keys` or `path.key`              |
| `400 bad_request` / `400 invalid_value` / `400 atomic_update_required`           | `422 validation_failed` at the member's declared location                       |
| `X-Silo-Mutation-Id`, `X-Silo-Idempotent-Replay`, `409 mutation_id_conflict`     | Not declared; retry the plain `PUT`                                             |
| `PUT /settings/device/subtitle_appearance` → `204`                               | `PUT /api/v2/settings/device/subtitle-appearance` → `200` effective appearance  |
| `PUT /settings/plugins/{id}` → `204`                                             | `PUT /api/v2/settings/plugins/{id}` → `200 {installation, values}`              |
| Numeric `library_id`, `profile_id`, installation `id`                            | String `ID`s                                                                    |
| `updated_at` as stored text                                                      | RFC 3339 instant                                                                |
| `admin_form` on a plugin installation                                            | Not carried                                                                     |
| `/settings/`, `/settings/{key}`, `/settings/device/{key}`, `/settings/effective` | Removed; use the typed operations above                                         |


## Public branding

Branding remains public so it can render before sign-in:

| Method | Path | Response |
|---|---|---|
| GET | `/api/v2/theme/capabilities` | Branding, CSS-override, and asset-storage availability |
| GET | `/api/v2/theme/branding` | Server name, login subtitle, optional accent/theme and asset URLs, storage availability |
| GET | `/api/v2/theme/admin-css` | JSON object with `vars` and `raw_css` strings |
| GET / HEAD | `/api/v2/branding/assets/{kind}` | Image bytes or matching headers without a body |

Discovery uses the existing branding service and theme settings. When dependencies
are absent, configuration returns default branding and empty overrides; capabilities
report availability explicitly. Discovery responses are not cached. The bundled web
uses these v2 reads. Native server-identity discovery must adopt the same public
endpoint through its negotiated API-version handling.

Asset URLs include `?v=<content-reference>`. Only an exact current reference receives
immutable caching; a stale reference returns `404`, so newer bytes cannot be cached
under an older content URL. An unversioned request uses `Cache-Control: public,
no-cache` and revalidates. ETags support `If-Match` and `If-None-Match`, including
weak read matches and lists. `304` retains the validator and cache headers. Images
preserve the branding content-security policy and `nosniff`, including SVG favicons.
A valid kind without a configured asset returns `404`; missing asset storage returns
`503`. Assets use the raw HTTP registry rather than JSON encoding.

The frozen v1 routes and administrator asset upload/delete operations are unchanged.
Theme catalog/download and catalog refresh are separate operations.


## Theme catalog and portable downloads

| Method | Path | Response |
|---|---|---|
| GET | `/api/v2/theme/catalog/capabilities` | Availability and accepted document byte limits |
| GET | `/api/v2/theme/catalog` | `{document, stale}` catalog envelope |
| POST | `/api/v2/theme/catalog/refresh` | The same envelope after clearing this node's cache and fetching synchronously |
| GET | `/api/v2/theme/download?url=...` | `{document}` portable theme file envelope |

Reads require account authentication and do not require a profile. Refresh requires
acting-admin authority. It invalidates the serving node's cache; it is neither a
cluster-wide invalidation nor a durable job. Repeated refresh converges, matching its
natural-idempotent retry declaration. The bundled web still disables automatic
mutation retries and authentication replay for the refresh button.

The `document` objects preserve the portable theme format's established property
names, including `updatedAt`, `downloadUrl`, `baseTheme`, `customCss`, and `createdAt`.
Catalog documents carry `version` and `themes`; each theme includes its identifier,
name, author/description, preview colors, tags, download URL, and version. Theme file
documents carry `version`, `name`, `baseTheme`, a string-valued `vars` object, and
`customCss`, with optional author, description, and creation time. Unsupported theme
versions/base themes remain subject to the installing client's parser. The web keeps
its existing portable-file validation and CSS sanitizer before applying a download.

Both transports call the same application methods for upstream access and caching.
Initial and redirected requests retain the existing approved-host HTTPS restriction
and timeout. The cache remains bound to the configured catalog URL; a fresh cache
from a previous URL cannot hide a configuration change. Upstream connection or
non-200 failures may return an expired catalog for the same URL, indicated by
`stale: true`. Invalid JSON and read failures do not use that fallback. Refresh clears
the cache before fetching and therefore cannot fall back to its former contents.

V2 responses use `no-store`; the frozen v1 transport keeps its original cache and
stale headers and byte-preserving JSON response. V1 reads remain capped at 1 MiB for
catalogs and 256 KiB for files. Because a capped read can be a valid JSON prefix of a
larger document, v2 refuses bodies exactly at those caps: its capability reports
1,048,575 and 262,143 accepted bytes respectively. Malformed portable documents and
upstream failures return `503 dependency_unavailable`; invalid requested URLs return
`422 validation_failed`, and disallowed download targets return
`403 permission_denied`. V1 status codes and error identifiers are unchanged.

There are no first-party Apple, Android, or Jellyfin callers of these theme catalog,
download, and refresh operations to migrate. The separate public branding discovery
consumer work remains tracked independently.

### Viewer library discovery

`GET /api/v2/user/libraries` returns an `items` collection of enabled libraries
visible to the account or selected household profile. Each item contains a string
`id`, `name`, `type`, `sort_order`, and optional `poster_url`. The projection
excludes storage paths and administrator scan metadata. The configuration list
retains complete enumeration in `sort_order`, then ID order, as library
administration does; it does not claim database-bounded paging.

The profile header is optional. When supplied it must belong to the account and
pass PIN verification, and the resolved viewer policy controls library access.
Without a profile, the existing account-policy fallback applies. Library rows
with `enabled=false` are excluded in both cases. An explicitly empty library
allowlist returns `items: []`. Optional poster signing uses the existing expiry
and omits the URL on signing failure.

`GET /api/v2/user/libraries/capabilities` exposes `available` under the same
authority rules. Both operations remain registered when the service is absent;
discovery returns `available: false`, and listing returns `dependency_unavailable`.
The bundled web keeps numeric library IDs at its UI boundary, rejects unsafe
numeric conversions, and fences responses/cache keys against account/profile
changes. Apple and Android have existing library-discovery callers and require
coordinated adoption before this migration row is ratified. The shared read
preserves frozen v1 response shape and errors; Jellyfin's separate library
projection is unchanged.

### Administrator device metadata

`GET /api/v2/admin/devices` returns a paginated `items` collection;
`GET /api/v2/admin/devices/{user_id}/{device_id}` returns one device's metadata.
Both require acting-administrator authority. Account IDs are strings; device IDs
remain opaque and are scoped to their account. Profile identities and counts stay
separate from the login account. Timestamps use UTC milliseconds, with absent or
unparseable historical metadata represented as null.

The list preserves descending activity order, followed by username, device name,
and device ID; account ID resolves ties. Signed cursors bind this position to the
actor, selected profile and page size. This is a live list: registration or
settings changes can reorder entries between pages. Each page still enumerates
all account stores using the existing concurrency limit of eight. Response paging
does not imply a database-bounded query or a snapshot. The web collects up to
100 pages of 100 records and rejects missing/repeated continuations or overflow
without publishing a partial list.

Device summaries merge registrations, legacy settings and canonical settings,
deduplicating mirrored keys. Detail's `settings` array retains legacy compatibility
rows; it is **not** a complete list of canonical overrides. Existing web editors
continue to read and write the administrator settings-values API for those
values. Metadata reads capture account/profile authority and discard stale
responses. No playback-session control, login revocation, push registration or
settings mutation is added by these reads.

`GET /api/v2/admin/devices/capabilities` returns `available` for the same
administrator authority. Missing account/store dependencies produce
`dependency_unavailable` from v2 metadata reads. The shared application read keeps
frozen v1 shapes, errors and aggregation semantics. No Apple, Android or Jellyfin
administrator-device metadata consumer was found.

### Browser OAuth login handshake

`POST /api/v2/auth/oauth/{install_id}/init` accepts a browser form submission
and redirects with 302 to the authentication plugin's authorization URL.
Optional `next` is normalized by the existing application service to a local
path. No request-body fields are consumed. This operation is non-retryable: it
creates a new provider authorization attempt and persisted state.

`GET /api/v2/auth/oauth/{install_id}/callback?state=...&code=...` completes the
provider redirect through the same application service. Signed state binds the
installation and expiry; the stored state is consumed before exchange. Exchange
uses its stored provider state and exact redirect URI. Success redirects to the
SPA with a one-time completion code, never bearer/refresh tokens in the URL.
The already-migrated `POST /api/v2/auth/oauth/complete` redeems that code. Failed
state, exchange, or login completion redirects to the existing local login-error
page. Missing code/state or invalid installation IDs return 400 plain text;
init's plugin/storage failures retain 502/500 plain text. Missing service returns
a 503 problem. V2 handshakes set `Cache-Control: no-store` and
`Referrer-Policy: no-referrer` and do not require an ambient login/profile.

`GET /api/v2/auth/oauth/capabilities` exposes `available`. The existing web login
form uses the v2 init route. **Providers must register the v2 callback URI**
(`/api/v2/auth/oauth/{install_id}/callback` under the configured host base URL)
before using that flow. V1 init continues to issue its v1 callback URI, and frozen
v1 transports are unchanged. This port adds no account-linking, PKCE, or new
browser-session-binding mechanism. No native in-app handshake or Jellyfin caller
was found; provider redirects and browser forms follow the emitted URLs.

### External watch-state webhook receiver

`POST /api/v2/webhook-sync/webhooks/{secret}` accepts existing Plex multipart,
Emby JSON/form/multipart, and Jellyfin JSON deliveries. The connection secret is
resolved before reading the body; an ambient bearer token or profile does not
replace this proof. Secret rotation immediately invalidates the previous URL.
No browser Origin is required for these server-to-server deliveries.

V2 limits the complete body to 10 MiB before provider parsing or watch-state
side effects, including bodies with unknown or understated lengths. Oversize
requests return a 413 `payload_too_large` problem. Multipart resources are cleaned
up after parsing. The shared application processor retains explicit profile
mapping, provider normalization, watch-state behavior and delivery logging with
bounded sanitized body excerpts. A processed delivery returns bodyless 204,
including ignored, skipped or unmatched events. Malformed payloads return 400,
unknown secrets 404, and internal failures a safe 500 problem. Known-connection
rejections retain delivery logs. This synchronous operation is non-retryable;
existing provider ordering and duplicate-handling behavior is unchanged and does
not provide an exactly-once guarantee.

Responses set `Cache-Control: no-store` and `Referrer-Policy: no-referrer`.
`GET /api/v2/webhook-sync/capabilities` exposes `available` and `max_body_bytes`.
V2 connection list/create/update and secret rotation emit v2 receiver URLs.
Existing external configurations continue to use their bridge URLs until updated;
frozen v1 URL generation, response shapes and provider parsing remain unchanged.
The web management screen consumes the emitted URL. No native receiver or
management caller was found; no Jellyfin-protocol endpoint needs migration.

### Revision-guarded onboarding

`GET /api/v2/onboarding/flow?surface=web|phone|tv` returns the current tour
manifest, filtered by live feature gates, surface and the active profile's child
status. Unknown step kinds remain skippable. `GET /api/v2/onboarding/state`
returns `tour_id`, optional `last_step`, optional UTC `completed_at`/`skipped_at`,
and `done`, with a strong opaque ETag scoped to the account, profile and tour.
Untouched state has its own validator. Conditional reads support 304.

`PUT /api/v2/onboarding/progress` requires the exact state `If-Match` validator.
Its body contains the current `tour_id`, optional `last_step`, and optional
`completed` or `skipped` flags. Supplying both terminal flags is invalid. Missing
If-Match returns 428, a stale validator returns 412 with the current ETag, and
`If-Match: *` returns 400 because existence alone cannot order progress. A stale
tour returns 409. Success returns the acknowledged state and its new ETag.
The three routes require a verified active profile; progress is demo-restricted.
`GET /api/v2/onboarding/capabilities` exposes `available` and `revision_guarded`.

Both SQLite and PostgreSQL atomically check revisions on insertion/update.
Database triggers advance the revision for bridge updates too, so a concurrent
bridge write invalidates a v2 validator. Completed/skipped timestamps remain
monotonic. The migration adds a revision column without changing existing state;
frozen v1 POST payloads, timestamps, response shapes and ordering behavior remain
unchanged. Bridge clients can still submit unordered progress during the bridge;
the ordering guarantee applies to guarded v2 writers.

Clients serialize intents for a captured account/profile, wait for each response
before advancing/dismissing, and use only the acknowledged next ETag. Progress is
`non_retryable`: on 412 or an uncertain response, stop and reload canonical state.
Never fetch a fresh validator merely to replay an old intent. The web follows
this sequence and requires reload after failure. Apple and Android adoption must
apply the same rule before these migration rows can be ratified. Jellyfin has no
onboarding counterpart.


## Bridge: the frozen `/api/v1` settings routes

The alpha surface below is frozen. Silo serves it through the pre-1.0 bridge window so
existing clients keep working; Silo 1.0 then answers the whole `/api/v1` namespace with
`410 Gone` and the `client_upgrade_required` problem code. No feature work lands here.
Use [Migrating from v1](#migrating-from-v1) to move a caller to the stable operations.

All paths in this section are relative to `/api/v1`.

### Contract discovery

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

### Request identity headers

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

#### App identity headers

Separately from the family, every first-party client should send its own app
identity on playback requests. These are server-wide contextual headers, not
settings-specific: the playback session stores them, the admin Activity page
renders them ("Silo Android TV 1.0.0 (build 5)"), and playback decision logs
carry them so a report can be tied to an exact build.

| Header                  | Clamp | Meaning                                                                                                          |
| ----------------------- | ----- | ---------------------------------------------------------------------------------------------------------------- |
| `X-Silo-Client`         | 128   | Product name, e.g. `Silo Android TV`, `Silo iOS`.                                                                |
| `X-Silo-Client-Version` | 64    | Marketing version, e.g. `1.0.0`. Sent verbatim and displayed verbatim — do not pre-shorten it.                   |
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

### Remote scopes

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

### Explicit values

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

#### Superseded keys and the write mirror

A definition may be marked `deprecated: true` in the manifest. It is still
served, still readable and still writable — old clients depend on it — but new
clients should read and write its replacement. The generated bindings carry the
flag (`deprecated` on the TypeScript `SettingDefinition`, a `Deprecated` /
`DEPRECATED` list in the Go, Kotlin and Swift bindings), so a client can hide a
superseded control without a hand-kept list of key names. A client that offers
both spellings as separate controls gives one preference two homes, and the
mirror below means editing either silently rewrites the other.

Revision 7 deprecates `playback.auto_skip_intro` in favor of
`playback.intro_skip_mode`, whose three members say what the boolean could not:
`never` (no prompt at all), `ask` (offer a Skip Intro button, what the boolean's
`false` always did) and `always` (skip it and offer an undo). The default is
`ask`, so an untouched profile behaves identically across the cutover.

For one release the server keeps the pair in step, so a preference set on any
client shows up correctly on the others:

| Request                                                                                                   | Server also does                                                                                                                                                                                        |
| --------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PUT` of `playback.auto_skip_intro`                                                                       | writes `playback.intro_skip_mode` at the same identity: `true → "always"`, `false → "ask"`                                                                                                              |
| `PUT` of `playback.intro_skip_mode`                                                                       | writes `playback.auto_skip_intro` at the same identity: `"always" → true`, otherwise `false`                                                                                                            |
| `DELETE` of either                                                                                        | removes the other at the same identity                                                                                                                                                                  |
| `PUT`/`POST /profiles` with `auto_skip_intro`                                                             | writes both keys at `profile` scope                                                                                                                                                                     |
| `PUT`/`DELETE` of the legacy `/settings/{key}` or `/settings/device/{key}` for `playback.auto_skip_intro` | writes or clears both keys at the scope that route owns                                                                                                                                                 |
| `PUT` or `DELETE` of either key at `profile` scope                                                        | sets `user_profiles.auto_skip_intro` to what the pair now resolves to — the written value, or the contract default once the row is cleared — so the profile DTO stays truthful for clients that read it |

Everything in that table commits as one transaction — both rows, the legacy
profile column, and the idempotency receipt when the request carries an
`X-Silo-Mutation-Id` — on the keyed and unkeyed paths alike, and on `DELETE` as
well as `PUT`. A request that fails changes nothing, and a replayed mutation id
re-serves its receipt without writing anything again. The response is always the
stored value of the key the request addressed; the companion row is not
reported. Only the addressed key raises a `user_settings.changed` event and an
audit record.

`DELETE` still answers `404 not_found` when the key it names has no value at
that scope. If a companion is nonetheless found there — a state only a partial
failure predating the transactional path could produce — it is cleared anyway,
because a stray companion goes on resolving as an explicit choice that no retry
can reach.

Surfaces that count stored overrides — the device list's `changed_count`, the
admin device summaries' `override_count` — count the pair once. It is one
preference held in two spellings, so counting rows would both overstate a
device's customization and drop by one, fleet-wide, on the day the mirror is
retired.

The boolean direction is lossy on purpose: a client that only understands the
switch sees `never` as `false` and shows the button. Such a client that then
flips the switch overwrites `never`, which is accepted for the overlap window
and is why the mirror is temporary. Once every client reads the enum, a
follow-up removes the mirror. Design: `docs/design/2026-08-16-intro-skip-mode.md`.

#### Atomic navigation shortcuts

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

### Effective values

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

### Admin projection

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

## Forward and rewind intervals

Revision 9 adds four profile-wide preferences. The backend stores and validates
these values; each client applies them to its own relative-seek actions.

| Key                                     | Default |
| --------------------------------------- | ------- |
| `player.video_skip_back_seconds`        | 10      |
| `player.video_skip_forward_seconds`     | 30      |
| `player.audiobook_skip_back_seconds`    | 10      |
| `player.audiobook_skip_forward_seconds` | 30      |

Each value is a numeric enum in seconds: `5`, `10`, `15`, `30`, `45`, `60`, or
`90`. Only `profile` scope is allowed; an unset preference resolves to its
contract default. The preference follows the active household profile across
client families and devices. It does not belong to the whole login account.

Discover support through the settings capability and manifest endpoints before
reading or writing these keys. Use the existing effective read, profile-scoped
PUT/DELETE, and `user_settings.changed` invalidation flow. No playback protocol
or database schema change is required. On an older server, or one whose
capabilities could not be read, a client uses the contract defaults for video
and any interval this browser or device stored locally for audiobooks; neither
state authorizes a write to the server.

Clients should use one resolved interval per media type and direction for
buttons, keyboard shortcuts, gestures, minimized players, and supported media
controls. Labels must show the interval the action will use. Settings changes
apply to subsequent actions without restarting playback. Explicit timestamp
seeks, chapter navigation, automatic marker skipping, and resume rewind are
separate operations and do not use these intervals.

Legacy browser-local audiobook intervals have no profile identity. Importing
must be an explicit user action that identifies the values and explains their
profile-wide effect. The two writes are independent: report each failure and
retain legacy values for retry instead of claiming an atomic import.

The standalone web player receives resolved intervals from its host rather
than calling the settings API itself. Media Session relative-seek handlers use
the configured interval even if an operating system supplies its own default
`seekOffset`. Browser-native fullscreen controls may perform seeks internally
without dispatching those handlers; there is no portable API to configure that
UI, and such controls are not guaranteed to honor these preferences.

Native adoption is tracked in [Apple #267](https://github.com/Silo-Server/silo-apple/issues/267)
and [Android #300](https://github.com/Silo-Server/silo-android/issues/300).
Jellyfin clients keep their own seek controls; this does not extend the
Jellyfin protocol.
