# Single default per service type — schema-driven exclusivity enforcement

Status: design • 2026-06-08
Scope: request-router connections (Admin → Requests). This is **Spec B** (Spec A —
collapsible Library + anime gate — shipped separately). Multi-repo:
`silo-plugin-sdk`, `silo-plugin-requests-arr`, `silo-server`.

## Context

Each arr request-router connection carries (in its opaque `plugin_config` blob)
`service_kind` (`radarr`/`sonarr`), `is_default` (the HD/1080p default for that
kind), `is_4k` (this is a 4K server), and `is_default_4k` (the 4K default for that
kind). Routing picks a connection per quality; when several are marked default the
plugin's `RouteTargets` just takes the *first* `is_default` — there is no guard
that at most one connection per kind is the default. The old per-kind
default-uniqueness DB indexes were removed when the legacy arr columns were
dropped, so nothing enforces "one HD default and one 4K default per service type"
today.

The host deliberately treats `plugin_config` as opaque (it never reads
`service_kind`/`is_default`) — that agnosticism is what let the Seerr plugin ship
with zero host changes. So the rule must live in the plugin, but enforcing it
needs all of a plugin's connections at once, which only the host has.

## Goals

- At most one connection per `service_kind` may have `is_default` true; likewise
  for `is_default_4k`. Enforced at save time, surfaced as an inline field error.
- The admin UI prevents conflicts proactively: toggling a default on one
  connection auto-clears it on sibling connections of the same kind.
- The mechanism is schema-driven and generic — the host adds no knowledge of
  arr-specific keys; any future plugin can declare the same kind of exclusivity.

## Non-goals

- No bulk migration / auto-resolution of pre-existing conflicts. Enforcement is
  forward-only (the live data is already conflict-free: each kind has exactly one
  HD default + one 4K default). A pre-existing conflict, if any operator has one,
  is caught the next time a conflicting connection is saved; until then
  `RouteTargets`' first-default fallback keeps routing deterministic.
- No "exactly one" requirement — zero defaults is allowed (first-connection
  fallback applies). The rule is "at most one".
- The server backstop **rejects** a second default; it does not "steal" it. A
  read-only `Validate` cannot mutate siblings, and the UI already does the steal
  smoothly, so rejection is the correct agnostic backstop for direct-API / racy
  saves.

## Architecture

A form field declares itself mutually exclusive within a group via a new
`exclusive_group_field` attribute (names another config field whose value defines
the group). Three parties consume one declaration:

- **Plugin** owns the rule: its `Validate` rejects a duplicate default, reading
  the flags off the sibling connections the host supplies.
- **Host** mechanically supplies siblings to `Validate` — never interpreting them.
- **Frontend** reads `exclusive_group_field` to auto-clear conflicts as the
  operator toggles, so the backstop is rarely hit.

## Components

### 1. SDK contract (`silo-plugin-sdk`, branch `feat/request-router-capability`)

Breaking proto changes (fine pre-publish):

- `request_router.proto` `ValidateRequest` gains:
  ```proto
  repeated RouterConnection siblings = 3; // other connections for this
  // installation+capability; carry id + config only (no base_url/api_key) —
  // validation reads flags, not credentials.
  ```
- `common.proto` `AdminFormField` gains:
  ```proto
  string exclusive_group_field = 15; // at most one connection per distinct value
  // of this field may have this field truthy; empty = no exclusivity.
  ```
- Regenerate Go (`pkg/pluginproto/...`). New getters: `ValidateRequest.GetSiblings()`,
  `AdminFormField.GetExclusiveGroupField()`. The SDK manifest loader/validator
  treats `exclusive_group_field` as an ordinary optional string (no new
  validation rule required).

### 2. Plugin — `Validate` enforces uniqueness (`silo-plugin-requests-arr`)

`internal/router/server.go` `Validate`: keep the existing single-connection checks,
then add cross-sibling checks. For the connection under validation, parse its
`service_kind`, `is_default`, `is_default_4k`. For each sibling, parse the same.
If this connection sets `is_default` true and any sibling with the *same*
`service_kind` also has `is_default` true, set
`field_errors["is_default"]` to a message naming the conflict (e.g.
`"radarr already has an HD default; unset it on the other connection first"`).
Same for `is_default_4k`. Siblings are matched by `service_kind` value; siblings
of a different kind are ignored. The sibling's id is available for a friendlier
message if a name is not in config (id is acceptable).

`manifest.json` `admin_form.fields`: add `"exclusive_group_field": "service_kind"`
to the `is_default` and `is_default_4k` field definitions.

### 3. Host — gather siblings (`silo-server`)

- `internal/requests/provider.go`: `RequestRouterProvider.Validate` gains a
  `siblings []ResolvedRouterConnection` parameter; `pluginRouterProvider.Validate`
  maps them into `ValidateRequest.Siblings` via the existing `routerProtoConn`
  helper (which already encodes `Config`; `BaseURL`/`APIKey` left empty for
  siblings).
- `internal/requests/service.go` `validateViaPlugin`: after resolving the
  connection under save, load the other `request_integrations` rows for the same
  `InstallationID` (excluding the row's own `ID`), build
  `ResolvedRouterConnection{ID, Config: plugin_config}` for each (no credential
  resolution), and pass them to `s.router.Validate(...)`.
- Add a store method if needed to list integrations by installation id
  (`ListIntegrationsByInstallation`) or filter the existing `ListIntegrations`
  result in the service. Prefer filtering the existing list in the service to
  avoid a new query, unless the list is unbounded (it is small — admin
  connections — so in-memory filter is fine).

### 4. Frontend — generic mutual-exclusion (`silo-server` `web/`)

`web/src/pages/AdminRequests.tsx` `RequestIntegrationsForm` owns all connection
cards (`cards: IntegrationCard[]`, each with its own `pluginConfig` and selected
installation/descriptor). Add a generic helper that, when a card's `pluginConfig`
changes:

1. For each field in that card's descriptor with a non-empty
   `exclusive_group_field` whose new value is truthy,
2. find sibling cards (same `installation_id`) whose `pluginConfig[groupField]`
   equals this card's `pluginConfig[groupField]`,
3. set that field to `false`/absent in each sibling's `pluginConfig`.

Wire it into the card-config update path (`updateCardConfig`). The
`PluginAdminFormField` TS type (`web/src/api/types.ts`) gains
`exclusive_group_field?: string` to match the proto. No arr-specific keys appear
in the code — the behavior is driven entirely by the descriptor.

`SchemaForm` itself is unchanged (it renders a single card's fields; cross-card
exclusivity is inherently a multi-card concern owned by the parent form).

## Data flow (save)

```
Admin toggles is_default on card B (radarr)
  └─ frontend: auto-clears is_default on sibling radarr cards (card A) [UX]
Admin saves card B
  └─ host validateViaPlugin: gathers sibling connections (config only)
       └─ plugin Validate(connection=B, siblings=[A,...])
            └─ no conflict (A was cleared) → ok; persist
   (direct-API or racy double-default → Validate returns field_errors → 400 inline)
```

## Error handling

- Conflicts → `ValidateResponse.field_errors[is_default|is_default_4k]`, rendered
  inline on the offending toggle (existing `ValidationError` → 400 path in
  `writeRequestServiceError`; no change needed).
- Sibling config parse issues (malformed blob) are treated as "flag not set" — a
  sibling that can't be parsed never blocks a save (defensive; a corrupt sibling
  shouldn't lock the admin out of editing a good one).

## Testing

- **SDK:** proto compiles; generated getters exist. (No behavior tests in SDK.)
- **Plugin (`silo-plugin-requests-arr`):** table tests on `Validate` — (a) lone
  default ok; (b) second `is_default` for same `service_kind` rejected; (c)
  second `is_default` for a *different* kind allowed; (d) `is_default_4k`
  uniqueness same three cases; (e) existing single-connection checks still hold;
  (f) malformed sibling config doesn't error/block.
- **Host (`silo-server`):** `service_test.go` — `validateViaPlugin`/`UpdateIntegration`
  passes the correct siblings (other installation connections, self excluded, no
  credentials) to a fake provider that records them; a fake provider returning a
  field error surfaces as `*ValidationError`.
- **Frontend:** a `RequestIntegrationsForm`-level test (or a focused helper unit
  test) — toggling an `exclusive_group_field` field true on one card clears it on
  a same-group sibling card and leaves a different-group card untouched.
- Gates: SDK `go build`/regen; plugin `go test`; host `go test ./internal/requests/`,
  `tsc -b`, eslint, prettier, frontend vitest.

## Rollout

No DB migration. Coordinated rebuild/redeploy (same loop as Spec A):
1. SDK: edit protos, regenerate, `go build` (the host/plugin consume it via the
   local `replace` / vendored copy).
2. Host: implement, rebuild `web/dist` + image (`Dockerfile.deploy`), redeploy.
3. arr plugin: implement + manifest, rebuild binary, reinstall via
   `cmd/plugininstall` against installation id 5, `docker compose up -d silo`.
4. Re-vendor the SDK into the host (`go mod vendor`) before the host image build so
   the new proto reaches the vendored build.

Manual verification: toggling the HD default on a second radarr connection clears
it on the first; saving two radarr HD defaults via a crafted request is rejected
with an inline `is_default` error.
