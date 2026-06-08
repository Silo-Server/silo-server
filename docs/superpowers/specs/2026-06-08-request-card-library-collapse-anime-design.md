# Request connection card: collapsible Library + anime gate/nesting

Status: design • 2026-06-08
Scope: Admin → Requests connection cards (schema-driven `request_router.v1` form).
This is **Spec A** of two. Single-default-per-service-type enforcement is **Spec B**
(separate cycle) and is explicitly out of scope here.

## Context

The connection cards render any installed `request_router.v1` plugin's `admin_form`
via the generic `SchemaForm` (`web/src/components/admin/plugins/SchemaForm.tsx`).
For the arr plugin (`silo-plugin-requests-arr/manifest.json`) the form has two
sections:

- **Library** — `collapsible:false`, holds all 12 core fields (service_kind,
  root_folder, quality_profile_id, tags, is_default, is_4k, is_default_4k,
  search_on_add, minimum_availability, series_type, season_folder, and currently
  `anime_enabled` as its last field).
- **Anime overrides** — `collapsible:true, collapsed_default:true`, gated by a
  section-level `show_when: anime_enabled == true`, holds anime_root_folder,
  anime_quality_profile_id, anime_tags.

Two UX problems:

1. Library is always fully expanded — a long field wall even for a configured,
   rarely-touched connection.
2. `anime_enabled` lives inside Library, but enabling it reveals the *separate*
   Anime overrides section, which renders as a sibling card **outside** the Library
   grouping — the anime config "pops out" of its context.

## Goals

- Library section is collapsible and **collapsed by default**, but **auto-expands
  when it has unresolved validation errors** (incl. empty required fields) so a
  new/misconfigured connection never hides its setup.
- The anime override fields read as a cohesive unit directly below Library, gated
  by a single visible toggle — no section popping in elsewhere.
- All changes stay schema-driven and generic: **no arr-specific logic in
  `SchemaForm`.**

## Non-goals

- Single-default-per-service-type enforcement (Spec B).
- Any change to fulfillment, the options probe, or the `request_router` contract.

## Repos touched

- `silo-plugin-requests-arr` — `manifest.json` `admin_form` (section flags + field
  regrouping). Hand-maintained JSON, embedded via `//go:embed manifest.json`.
- `silo-server` — `web/src/components/admin/plugins/SchemaForm.tsx` (auto-expand
  effective-open logic + a small nested-field affordance).

## Design

### Part 1 — Collapsible Library that auto-expands on issues

**arr `manifest.json`:** Library section → `collapsible: true`,
`collapsed_default: true`.

**`SchemaForm` `SchemaFormSection`:** replace the current
`useState(!collapsed_default)` open state with an effective-open computation that
cannot hide errors:

```
forceOpen = section.field_keys.some(k => mergedErrors[k] != null)
open      = forceOpen || (userToggled ?? !collapsed_default)
```

- `mergedErrors` already merges `validateSchemaValues`, which flags empty required
  fields (`schemaForm.ts:38`). So a brand-new connection (empty root_folder /
  quality_profile) starts force-expanded; once valid it collapses (unless the
  operator explicitly opened it).
- The Hide/Show button sets `userToggled`. While `forceOpen` is true the section
  stays open regardless of the button (you can't collapse a section with errors);
  the button still renders so the operator can collapse once the section is valid.

This is generic and benefits any plugin with collapsible sections.

### Part 2 — Anime gate toggle + nested section

**arr `manifest.json` `admin_form`:**

- Remove `anime_enabled` from the **Library** section `field_keys`.
- **Anime overrides** section: set `collapsible:false`, drop the section-level
  `show_when`, and prepend `anime_enabled` to its `field_keys`. The section now
  always renders, showing just the gate toggle when off. The existing field-level
  `show_when: anime_enabled == true` on anime_root_folder / quality / tags keeps
  them hidden until enabled.

Resulting render order (sections render after any ungrouped fields, in declared
order; the arr form has no ungrouped fields):

```
[ Library section (collapsible) ]
[ Anime overrides section: anime_enabled toggle → nested fields when on ]
```

Everything stays in one cohesive Anime box directly below Library; nothing pops
out as a detached sibling.

**`SchemaForm` (small polish):** a field rendered because its `show_when` passed
gets a subtle left indent/accent so revealed fields read as nested under their gate
toggle. Generic — applies to any conditional field, not just anime.

## Testing

- `SchemaForm` interaction tests:
  - a section containing an errored/empty-required field renders **expanded** even
    with `collapsed_default:true`;
  - a section with no errors honors `collapsed_default:true` (starts collapsed);
  - Hide/Show toggles a clean (error-free) section.
- arr plugin: `manifest.json` still loads/validates (manifest load test);
  `anime_enabled` no longer in Library `field_keys`; Anime section has no
  section-level `show_when` and lists `anime_enabled` first.
- Manual on the live box: Library collapsed for a saved/valid connection, expanded
  for a new one; enabling anime reveals fields within the Anime box below Library.
- Gates: `tsc -b`, eslint, prettier (silo-server); arr plugin `go test`.

## Rollout

Two coordinated artifacts:

1. **silo-server**: rebuild `web/dist` (frontend is embedded in the Go binary) +
   image, redeploy the live container.
2. **silo-plugin-requests-arr**: the `admin_form` lives in `manifest.json`. The
   arr Go binary is unchanged (no code change), but the manifest's binary
   `checksum` must still match — rebuild the plugin and re-install it on the box so
   the new `admin_form` reaches both the on-disk manifest and `plugin_capabilities`
   (the host serves `admin_form` from the installed manifest, falling back to the
   DB). Existing connections keep their stored `plugin_config` unchanged; only the
   form's layout changes.

No migration; no data changes.
