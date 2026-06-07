# Schema-Driven Plugin Config Form — Design Spec

**Date:** 2026-06-07
**Status:** Approved design, pre-implementation
**Goal:** Replace the bespoke, arr-shaped request-connection admin form with a single generic form engine driven entirely by a plugin's declared manifest schema, so any `request_router.v1` backend (Sonarr/Radarr, Seerr, third-party) renders its full configuration UI — including dynamic dropdowns, multi-select, conditional fields, sections, and validation — with **zero host code changes**.

**Architecture (one sentence):** Extend the SDK's `AdminFormDescriptor` into a complete form-description language, render it with one reusable `SchemaForm` component (driven by manifest data + a `ListConfigOptions` loader + a plugin `Validate` RPC), and retire the hand-coded arr form and the hardcoded `integrationOptionsFromRouter` translation.

**Tech stack:** Go (silo-server host + plugins), `silo-plugin-sdk` (protobuf over hashicorp/go-plugin), React/TypeScript + TanStack Query admin UI.

**Background:** This builds on the request-fulfillment pluginization (`docs/superpowers/specs/2026-06-07-requests-pluginization-design.md`, implemented on branch `feat/requests-pluginization`). That work made the *wire contract* agnostic (`RouterConnection.config` is an opaque `Struct`; `ListConfigOptions` returns a generic `options_by_field` map) but left the *admin UI* arr-specific — code-review finding #9. This spec closes that gap.

Commands assume the repository root is the cwd.

---

## 1. Motivation

The `request_router.v1` contract is agnostic, but two host-side layers still hardcode arr:

- `web/src/pages/AdminRequests.tsx` is a bespoke connection editor: a radarr/sonarr toggle, root-folder / quality-profile dropdowns, a tag button-grid, a collapsible anime panel, 4K role switches, and a fixed `IntegrationFormState`.
- `internal/requests/service.go`'s `integrationOptionsFromRouter` hardcodes the three field keys (`root_folder`, `quality_profile_id`, `tags`) when mapping the plugin's generic `options_by_field` into an arr-shaped `IntegrationOptions`.

A non-arr plugin (Seerr, or a third-party backend with a different config shape) therefore cannot be configured without editing host code — defeating the point of a plugin platform. A generic renderer already exists (`web/src/components/admin/plugins/PluginConfigForm.tsx`) and the manifest→API→frontend plumbing is in place, but the schema language can only express a flat list of the 6 basic controls with **static** options. The arr form is bespoke precisely because the schema can't express: dynamic options, multi-select, conditional visibility, sections, or validation.

## 2. Goals / non-goals

**Goals**
- A complete, backward-compatible form-description language in `AdminFormDescriptor`: dynamic options, multi-select, conditional visibility (`show_when`), sections/groups, and per-field validation.
- A plugin `Validate(config)` RPC for cross-field/business rules the schema can't express.
- One reusable `SchemaForm` renderer used for **all** plugin config (global settings + request connections).
- Retire the bespoke arr connection form, `integrationOptionsFromRouter`, and the arr-shaped `IntegrationOptions`.
- The arr plugin re-declares its config richly in its manifest and implements `Validate`. Seerr/third parties become pure consumers — no host changes.

**Non-goals (this design)**
- The Seerr plugin itself (a consumer of the finished form engine; its own spec).
- Dropping the legacy arr columns (`kind`, `root_folder`, …). This spec removes the *client* dual-write and centralizes server-side derivation of the few columns still used (`kind`, `is_default`, `is_default_4k`), making the column-drop a clean follow-on migration (code-review #8).
- Cross-connection invariants (e.g. "exactly one HD default per kind") — these are multi-row rules the host/DB enforces, not single-connection form rules.

## 3. Architecture overview

A request-router connection form has two zones:

```
┌──────────── Connection form (admin) ─────────────────────────┐
│  Host-owned chrome (every connection, host-rendered)          │
│    name · enabled · base_url · api_key_ref · plugin selector  │
│                                                               │
│  Plugin-owned schema body (plugin_config, SchemaForm-rendered)│
│    <SchemaForm descriptor={capability.admin_form}             │
│       values={connection.plugin_config}                       │
│       optionsLoader={loadRequestOptions(connectionDraft)}     │
│       validator={validateRequestConnection(connectionDraft)}/>│
└───────────────────────────────────────────────────────────────┘
        manifest admin_form (sections, show_when, validation, dynamic_options)
        ListConfigOptions(connection) -> options_by_field   (dynamic selects)
        Validate(connection) -> {field_errors, form_error}  (on save)
```

The host never interprets arr-specific keys; it renders whatever the descriptor declares, fetches options the plugin advertises, and asks the plugin to validate.

## 4. SDK contract additions (`silo-plugin-sdk`)

All additions to `proto/silo/plugin/v1/common.proto` are **additive and backward-compatible**: a plugin that omits `sections`/`show_when`/`dynamic_options`/`validation` renders exactly as today.

```proto
enum AdminFormControl {
  ADMIN_FORM_CONTROL_UNSPECIFIED = 0;
  ADMIN_FORM_CONTROL_TEXT = 1;
  ADMIN_FORM_CONTROL_TEXTAREA = 2;
  ADMIN_FORM_CONTROL_PASSWORD = 3;
  ADMIN_FORM_CONTROL_NUMBER = 4;
  ADMIN_FORM_CONTROL_SWITCH = 5;
  ADMIN_FORM_CONTROL_SELECT = 6;
  ADMIN_FORM_CONTROL_MULTI_SELECT = 7;   // NEW: array-of-values (e.g. tags)
}

// NEW: a field/section is shown only when, for every condition, the named
// field's stringified value is in `equals`.
message AdminFormCondition {
  string field = 1;
  repeated string equals = 2;
}

// NEW: per-field declarative constraints (enforced client-side and re-checked
// server-side). has_min/has_max distinguish "unset" from a real 0 bound.
message AdminFormValidation {
  bool   has_min = 1;
  double min = 2;
  bool   has_max = 3;
  double max = 4;
  string pattern = 5;       // RE2
  int32  min_length = 6;
  int32  max_length = 7;
}

message AdminFormField {
  string key = 1;
  string label = 2;
  string description = 3;
  AdminFormControl control = 4;
  string placeholder = 5;
  bool required = 6;
  bool secret = 7;
  bool multiline = 8;
  google.protobuf.Value default_value = 9;
  repeated AdminFormOption options = 10;        // static options
  int32 rows = 11;
  bool dynamic_options = 12;                    // NEW: populate from ListConfigOptions[key]
  repeated AdminFormCondition show_when = 13;   // NEW: conditional visibility
  AdminFormValidation validation = 14;          // NEW: per-field constraints
}

// NEW: groups fields by key into an ordered (optionally collapsible) section.
message AdminFormSection {
  string key = 1;
  string title = 2;
  string description = 3;
  bool   collapsible = 4;
  bool   collapsed_default = 5;
  repeated string field_keys = 6;
  repeated AdminFormCondition show_when = 7;
}

message AdminFormDescriptor {
  repeated AdminFormField fields = 1;
  string submit_label = 2;
  repeated AdminFormSection sections = 3;        // NEW: optional; absent -> flat render
}
```

`proto/silo/plugin/v1/request_router.proto` gains the validate RPC (reusing `RouterConnection` as the carrier of the proposed config):

```proto
service RequestRouter {
  rpc Fulfill(FulfillRequest) returns (FulfillResponse);
  rpc CheckStatus(CheckStatusRequest) returns (CheckStatusResponse);
  rpc ListConfigOptions(ListConfigOptionsRequest) returns (ListConfigOptionsResponse);
  rpc TestConnection(TestConnectionRequest) returns (TestConnectionResponse);
  rpc Validate(ValidateRequest) returns (ValidateResponse);   // NEW
}

message ValidateRequest  { string capability_id = 1; RouterConnection connection = 2; }
message ValidateResponse { map<string, string> field_errors = 1; string form_error = 2; } // empty = valid
```

**Structural change:** the arr capability currently declares 14 separate `ConfigSchema` entries (each a 1-field `admin_form`), which can't express sections or cross-field layout. The capability will instead declare **one `ConfigSchema`** whose `admin_form` holds all fields + sections, and whose `json_schema` is one object schema describing all properties. The manifest loader's existing "admin_form fields must reference json_schema properties" check still applies, now against the object schema.

Run `make proto` to regenerate. Add a manifest-load test accepting the enriched descriptor and the `Validate` stubs.

## 5. Host: the `SchemaForm` renderer (`silo-server` frontend)

Extract the rendering into `web/src/components/admin/plugins/SchemaForm.tsx`; refactor `PluginConfigForm` to use it so plugin global-config settings inherit the richer language. `SchemaForm` renders one `AdminFormDescriptor`:

- **Controls:** the 6 existing + `MULTI_SELECT` (chip/checkbox multi-value bound to a string/number array).
- **Sections:** fields grouped per `sections` (collapsible, honoring `collapsed_default`); fields in no section render first; a section hides when its `show_when` fails.
- **`show_when`:** per-field and per-section visibility, evaluated against current values (the field's value is stringified and tested for membership in `equals`; all conditions must hold).
- **Per-field validation:** enforce `required`/`min`/`max`/`pattern`/`min_length`/`max_length` client-side → inline errors that block submit.
- **Dynamic options:** an injected prop `optionsLoader?: (config: Record<string, unknown>) => Promise<Record<string, Option[]>>`. Fields with `dynamic_options` read `loader(currentConfig)[key]`. The loader is re-invoked (debounced) when values change, so options may depend on other fields (e.g. `service_kind`) or credentials. Omitted for contexts without dynamic options (plugin global config).
- **Validate-on-submit:** an injected prop `validator?: (config) => Promise<{field_errors: Record<string,string>; form_error: string}>`. After client validation passes, `SchemaForm` calls it; `field_errors` map to inline per-field errors, `form_error` to a form-level banner.

TS types in `web/src/api/types.ts` mirror the proto additions (`PluginAdminFormField` gains `dynamic_options?`, `show_when?`, `validation?`; `PluginAdminForm` gains `sections?`; the control union gains `MULTI_SELECT`).

## 6. Host: Validate plumbing, generic options, and retirements (`silo-server` backend)

**Validate (mirror the existing capability-client pattern):**
- `pluginhost.RequestRouterClient.Validate(ctx, *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error)`.
- `requests.RequestRouterProvider.Validate(ctx, installationID, capabilityID, conn ResolvedRouterConnection) (fieldErrors map[string]string, formError string, err error)`; the `RouterClient` interface gains `Validate`; `PluginRequestRouterAdapter` already forwards the concrete client.
- `Service.CreateIntegration`/`UpdateIntegration` call `s.router.Validate(...)` **before persisting**. Non-empty `field_errors`/`form_error` → a structured validation error; the API handler maps it to **HTTP 400** `{ "field_errors": {…}, "form_error": "…" }`. A nil router / unbound installation falls back to the existing host-side required-field checks.

**Generic options endpoint:** `HandleLoadIntegrationOptions` keeps its path but returns the generic `options_by_field` map (`Record<string, [{value,label}]>`) straight from `ListConfigOptions`. **Delete** `integrationOptionsFromRouter` and the arr-shaped `IntegrationOptions`/`IntegrationRootFolder`/`IntegrationQualityProfile`/`IntegrationTag`; the TS `RequestIntegrationOptions` becomes the generic map.

**Legacy-column derivation (closes the client dual-write):** the form now writes only `plugin_config` + generic chrome. To keep `ClearDefault` and any remaining enforcement working until the staged column-drop, the service derives the columns it still uses (`kind`, `is_default`, `is_default_4k`) from `plugin_config` on save — server-side, once — instead of the client sending them.

**Retirements (`web/src/pages/AdminRequests.tsx`):** delete `IntegrationEditor`, `IntegrationFormState`, `formToIntegration`/`integrationToForm`, the dropdown/tag-grid/anime-panel JSX, the 4K switches, and the `boolOption`/`stringOption`/`numberConfig` helpers — replaced by `<SchemaForm>` over the capability descriptor plus the host-owned chrome. The radarr/sonarr tabs become a **generic grouping** (by plugin/capability, or a flat list); `service_kind` is now just a schema field.

## 7. arr plugin: enriched manifest + Validate (`silo-plugin-requests-arr`)

The capability declares one `ConfigSchema` with a rich `admin_form`:

| Field | Control | Notes |
|---|---|---|
| `service_kind` | SELECT (static: Radarr/Sonarr) | required; drives `show_when` |
| `root_folder` | SELECT, `dynamic_options` | |
| `quality_profile_id` | SELECT, `dynamic_options` | `required` |
| `tags` | MULTI_SELECT, `dynamic_options` | |
| `is_default` / `is_4k` / `is_default_4k` | SWITCH | `is_default_4k` `show_when is_4k=true` |
| `search_on_add` | SWITCH | |
| `minimum_availability` | SELECT (static) | `show_when service_kind=radarr` |
| `series_type` / `season_folder` | SELECT / SWITCH | `show_when service_kind=sonarr` |
| `anime_enabled` | SWITCH | gates the anime section |
| `anime_root_folder` / `anime_quality_profile_id` / `anime_tags` | SELECT/SELECT/MULTI_SELECT, `dynamic_options` | in the "Anime overrides" section |

**Sections:** `"Library"` (core fields) and `"Anime overrides"` (collapsible, `collapsed_default=true`, `show_when anime_enabled=true`).

**`Validate` impl:** parses the config and returns errors for the cross-field rules — `is_default && is_4k` → "the HD default cannot be a 4K server"; `is_default_4k && !is_4k` → "the 4K default must be a 4K server". (Per-field `required`/etc. are declarative; `Validate` owns only cross-field logic.) The existing `ListConfigOptions` already returns `root_folder`/`quality_profile_id`/`tags` options keyed by field, which the `dynamic_options` fields consume directly.

## 8. Data flow

1. **Open form:** host serves the capability's `admin_form` (via `GET /admin/plugins/installations`); `SchemaForm` renders it over the connection's `plugin_config`. For `dynamic_options` fields it calls the generic options endpoint with the current config → `options_by_field` → populates selects/multi-selects. Re-fetches (debounced) when `service_kind`/`base_url`/`api_key` change.
2. **Edit:** `show_when`/sections evaluated client-side; per-field constraints validated inline.
3. **Save:** client validation → `POST/PUT` integration → host calls plugin `Validate(connection)`. On errors → 400 `{field_errors, form_error}` rendered inline. Else persist (server derives legacy `kind`/`is_default*` from `plugin_config`) → success.

## 9. Error handling

- **Dynamic-options load fails** (plugin/arr unreachable): the affected field shows an inline "couldn't load options — retry" and remains editable; the rest of the form works.
- **Validate transport error** (vs validation failures): surface "couldn't validate connection" and block save; `field_errors`/`form_error` are normal validation results shown inline.
- **No plugin installed:** the existing host "install a request-router plugin" empty state (chrome-level).
- **Back-compat:** existing connections already carry `plugin_config`, so they render in `SchemaForm` unchanged.

## 10. Testing

- **SDK:** manifest-load test accepts the enriched descriptor (sections, `MULTI_SELECT`, `show_when`, `dynamic_options`, `validation`); `Validate` stubs generate; `make proto` clean.
- **`SchemaForm` (vitest/RTL):** each control incl. `MULTI_SELECT`; section collapse; `show_when` show/hide; per-field validation errors; dynamic options via a mock loader; validate-on-submit surfacing `field_errors`/`form_error`.
- **Host (Go):** pluginhost `Validate` client; provider `Validate` translation; `Create/UpdateIntegration` calls `Validate` → 400 mapping; generic options endpoint returns `options_by_field`; legacy-column derivation on save. Host packages that touch libvips build/test only in the `golang:1.26 + libvips-dev` container.
- **arr plugin:** `Validate` unit tests (the two cross-field rules); manifest valid + declares the rich schema.
- **Back-compat:** an existing connection (with `plugin_config`) renders correctly in `SchemaForm`.

## 11. Sequencing (plan phases)

1. **SDK** — extend `AdminFormDescriptor` + add `Validate` RPC; regenerate; manifest test.
2. **`SchemaForm`** — the renderer engine (controls, sections, `show_when`, validation, injected options-loader + validator); refactor `PluginConfigForm` onto it; TS types.
3. **Host plumbing** — `Validate` (pluginhost → provider → service create/update → 400); generic options endpoint; server legacy-column derivation; delete `integrationOptionsFromRouter` + arr `IntegrationOptions`.
4. **Requests admin page** — replace the bespoke editor with `SchemaForm` + generic chrome + generic connection grouping; delete dead code.
5. **arr plugin** — enrich manifest + implement `Validate`.
6. **Verify** — container build, Go + renderer tests, frontend gates, back-compat check.

## 12. Out of scope / follow-on

- `silo-plugin-requests-seerr` — a consumer of the finished engine; its own spec.
- The #8 legacy-column **drop** migration — this spec removes the client dual-write and centralizes server derivation, leaving a single clean seam for the drop.
- Cross-connection default-uniqueness — stays host/DB-enforced.
