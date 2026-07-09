# Plugin Hub and Approved Community Catalog Implementation Plan

> **For implementation:** Use the `executing-plans` skill and complete the
> phases in dependency order. Commands assume the repository root is the cwd.

**Goal:** Replace the current raw plugin cards with an operator-focused Plugin
Hub that explains what each plugin does, shows setup and release information,
links to its source, and lets a server administrator opt into Silo-approved
community plugins without manually managing repository URLs.

**Audience:** Server administrators and casual homelab operators. This is not an
end-user plugin surface. Developer identifiers and repository details remain
available, but they are secondary to plain-language purpose, setup, health, and
update information.

**Approval meaning:** An **Approved community** plugin has been reviewed by Silo
maintainers, validated to install and work as described, and considered safe for
its documented use at the time of approval. It remains community-maintained;
approval does not transfer maintenance or support ownership to the Silo core
team and is not a permanent guarantee against future vulnerabilities.

**Architecture:** Add typed presentation fields to `PluginManifest`; keep
version-specific GitHub release notes in catalog packages; create a separately
owned `silo-community/silo-plugins` catalog with an allowlisted approval gate;
and teach Silo to manage official, approved-community, and custom sources as
different provenance classes. The admin UI consumes additive `/api/v1` fields,
uses a default-off community-channel setting, and opens URL-addressable plugin
details from the Installed and Catalog views.

**Delivery shape:** This is one feature with coordinated PRs across
`silo-plugin-sdk`, `silo-plugins`, the new community catalog, `silo-server`, and
the affected plugin repositories. Do not combine all repositories into one
commit or PR.

---

## Product Decisions and Defaults

- The primary tabs remain **Installed** and **Catalog**.
- The catalog contains Silo-maintained plugins by default.
- **Include approved community plugins** is a server-wide setting and defaults
  to `false` for both fresh installs and upgrades.
- Enabling the setting manages approved community catalog sources on the
  administrator's behalf; it does not ask for a GitHub URL.
- Disabling the setting hides community catalog entries and pauses update
  discovery for community installations. It never disables or uninstalls an
  installed plugin.
- When community installations exist, disabling requires confirmation and
  states how many plugins will stop receiving update discovery.
- Manual catalog repositories and archive uploads remain available under
  **Manage sources → Advanced**.
- Cards use **Silo maintained**, **Approved community**, and **External source**
  as the provenance labels. Do not use one ambiguous `Verified` badge.
- The approval badge is catalog/host-derived. A plugin manifest cannot declare
  itself approved.
- In-app release content is labeled **What's new**. The first implementation
  stores the latest release notes and links to the complete external changelog;
  it does not retain a full in-app version history.
- The Plugin Hub is web-admin-only. No Android or Apple client changes are
  required.

## Explicitly Out of Scope

- An end-user plugin marketplace or plugin controls outside the admin UI.
- Selecting and transferring the first batch of repositories into
  `silo-community`; that migration should follow this infrastructure plan with
  an explicit repository list.
- Plugin rollback or installation of arbitrary historical versions.
- Cryptographic artifact signing or reproducible-build attestation beyond the
  existing release checksum contract.
- A remote kill switch. Removing a plugin from the approved catalog stops new
  installs and updates, but does not remotely stop already-installed code.
- Automated proof that a plugin is safe. Approval includes human review and
  runtime validation.

---

## Verified Baseline

- `PluginManifest` has `metadata` and `category` but no plugin-level display
  name, summary, description, setup guide, publisher, or source links.
  `CapabilityDescriptor.description` exists but describes individual
  capabilities rather than the plugin as a whole.
- `silo-plugins` already writes `repo_url` into each catalog package, but
  `silo-server/internal/plugins/catalog_service.go` does not decode or return
  it.
- The catalog updater fetches the GitHub release tag and assets but does not
  preserve the release page URL, publication time, or release body.
- The server seeds one official repository only when *no* repository rows
  exist. This is not sufficient for multiple managed channels or for adopting
  an existing installation that already has custom repositories.
- Catalog fetches are live and unbounded, and a failing source is skipped. No
  last-known-good catalog payload is available to keep browsing and release
  notes usable during an outage.
- Auto-update selection currently chooses the highest version for a plugin ID
  across all enabled repositories. Updates must instead stay pinned to the
  installation's repository so a custom source cannot shadow an official or
  approved plugin.
- `AdminPlugins.tsx` is a single large page. Installed and available cards show
  raw plugin IDs, versions, capability chips, and operational controls but no
  plugin-level description, release notes, or source provenance.

---

## Public Contracts

### SDK manifest presentation

In `silo-plugin-sdk/proto/silo/plugin/v1/common.proto`, add a new additive
message and field:

```text
PluginPresentation presentation = 13;

PluginPresentation:
  display_name
  summary
  description_markdown
  setup_markdown
  homepage_url
  source_url
  support_url
  changelog_url
  publisher_name
  publisher_url
  license_spdx
```

Contract rules:

- Existing manifests without `presentation` remain valid.
- When `presentation` is present, validate lengths and accept only absolute
  `http` or `https` links. Reject control characters and unsafe schemes.
- Recommended limits: `display_name` 120 characters, `summary` 240 characters,
  and each Markdown field 32 KiB.
- SDK validation does not require presentation fields globally during the
  compatibility window. Official and approved-community catalog CI applies the
  stricter publishing requirement.
- Markdown is CommonMark-style text. Raw HTML is not part of the contract.
- `publisher_*` and source links are self-declared identity information; they
  never determine Silo approval.

Regenerate the Go protobuf output, update manifest fixtures and documentation,
and publish a new additive SDK minor release before downstream repositories
consume these fields.

### Catalog release and approval metadata

Extend the catalog package JSON shared by the official and community catalogs:

```text
repo_url
release:
  url
  published_at
  notes_markdown
approval:                 # community catalog only
  approved_at
  review_url
```

- `repo_url` remains the canonical source-code link generated from the GitHub
  repository that produced the release.
- `release` is generated from the GitHub Releases API. Bound
  `notes_markdown` to 64 KiB before writing the catalog.
- `approval` comes from the community catalog's reviewed allowlist, never from
  the plugin's manifest or release payload.
- `changelog_url` remains in `PluginPresentation`; when absent, the web UI may
  link to the repository's Releases page derived from `repo_url`.
- Preserve the complete protobuf manifest during catalog generation. Do not
  reduce it to a hand-built subset.

### Additive server API fields

Add `GET` and `PUT /api/v1/admin/plugins/catalog-settings`:

```text
include_approved_community_plugins: boolean
approved_community_plugin_count: number
installed_community_plugin_count: number
community_updates_paused: boolean
```

The `PUT` body accepts only
`include_approved_community_plugins`. It persists the preference, reconciles
managed repository rows, and returns the new state. A source refresh failure
does not roll the preference back; the repository status reports the failure
and the catalog falls back to cached data when available.

Add `GET /api/v1/admin/plugins/capabilities` for feature detection. It reports
support for typed presentation metadata, release notes, approved community
catalogs, and last-known catalog caching.

Extend existing responses additively:

- `PluginRepository`: `source_kind`, `managed`, `last_fetch_error`,
  `last_fetch_error_at`.
- `PluginCatalogEntry`: typed `presentation`, `source_kind`, repository display
  name, `repo_url`, `release`, optional `approval`, and `stale`.
- `PluginInstallation`: typed `presentation`, source/repository provenance,
  the matching current or available release metadata when known, and
  `updates_paused`.

Keep existing `metadata`, capability, route, asset, and configuration fields
unchanged for `/api/v1` compatibility.

---

## Phase 1 — SDK Presentation Contract

Repository: `silo-plugin-sdk`

- [ ] Add `PluginPresentation` and field 13 to the protobuf contract; regenerate
  `pkg/pluginproto` with `make proto`.
- [ ] Add reusable URL and length validation in
  `pkg/pluginsdk/manifest/manifest.go` without making presentation mandatory for
  older plugins.
- [ ] Add decode/round-trip/validation tests for complete, partial, absent, and
  unsafe presentation blocks.
- [ ] Document every presentation field and provide one complete example
  manifest for plugin authors.
- [ ] Publish an additive SDK minor release and verify the tag is consumable
  from a clean downstream module.

Verification: `GOWORK=off go test ./...`, `make proto`, and a clean-tree check
after regeneration.

## Phase 2 — Catalog Tooling and Community Approval Gate

Repositories: `Silo-Server/silo-plugins` and new
`silo-community/silo-plugins`

- [ ] Extend the shared catalog generator's GitHub `Release` DTO and
  `CatalogPackage` to preserve source, publication, release-note, and approval
  fields.
- [ ] Keep the update workflow's shared concurrency group so simultaneous
  plugin releases cannot race on `manifest.json`.
- [ ] Add tests proving the generator preserves the full manifest, bounds
  release notes, derives safe URLs, and produces deterministic JSON.
- [ ] Create `silo-community/silo-plugins` with the same catalog package and
  release-asset contract as the official catalog.
- [ ] Add `approved-plugins.json` to the community catalog. Each active entry
  contains `plugin_id`, exact GitHub repository, `approved_at`, and a URL to the
  approval review/evidence.
- [ ] Make community catalog updates reject releases whose repository or
  plugin ID is not active in `approved-plugins.json`.
- [ ] Protect the community catalog's main branch and approval registry with
  CODEOWNERS/required review. Release dispatch alone must not grant approval.
- [ ] Add a rebuild/check command that removes packages no longer present in
  the active allowlist so catalog membership cannot remain stale indefinitely.
- [ ] Publish an approval policy documenting the minimum review: successful
  builds/tests, checksum-bearing release artifacts, manifest/setup accuracy,
  runtime validation on a supported Silo version, source/release-workflow
  review, license, support path, and no known unsafe behavior.

Verification: `GOWORK=off go test ./...`; run the updater against fixtures for
an approved and rejected repository; run the catalog rebuild/check twice to
prove idempotence.

## Phase 3 — Server Repository Provenance, Preference, and Cache

Repository: `silo-server`

- [ ] Create a timestamped Goose migration with
  `make migrate-create NAME=plugin_catalog_provenance`.
- [ ] Extend `plugin_repositories` with a nullable unique `managed_key`, a
  constrained `source_kind` (`silo`, `approved_community`, `external`), and
  last-fetch error fields. Existing rows default to `external` and the known
  official URL is adopted as `silo` during reconciliation.
- [ ] Add a `plugin_catalog_cache` table keyed by repository, plugin ID, and
  version, storing the normalized catalog package JSON plus fetch time. Delete
  cache rows with the repository.
- [ ] Add a plugin-owned setting key
  `plugins.include_approved_community_plugins`; missing/invalid values resolve
  to `false`.
- [ ] Replace `seedDefaultRepository` with an idempotent managed-repository
  reconciler. It always adopts/upserts the official catalog and upserts
  `https://raw.githubusercontent.com/silo-community/silo-plugins/main/manifest.json`
  enabled according to the community setting. Structure the registry as a list
  so another approved community catalog can be added without a second toggle.
- [ ] Managed repository URLs, names, keys, and provenance are read-only through
  manual repository CRUD. Custom repositories retain existing create/edit/delete
  behavior.
- [ ] Bound repository-index downloads before JSON decoding. Validate and cache
  only accepted packages; on a successful refresh transactionally replace that
  repository's cache and clear its error state.
- [ ] On a fetch failure, record a concise error and serve last-known cached
  entries with `stale=true`. An enabled source with no cache returns no entries
  but remains enabled for retry.
- [ ] Resolve catalog duplicates deterministically by source precedence
  (`silo` → `approved_community` → `external`) before comparing versions within
  a source. A higher-version custom package must never shadow the same official
  or approved plugin ID.
- [ ] Make update discovery repository-pinned: installations with a
  `repository_id` compare only against that repository's entries. Direct uploads
  without a repository do not gain catalog updates accidentally.
- [ ] When a source is disabled, retain `available_version` but return
  `updates_paused=true`; applying the update is blocked until the source is
  enabled again.
- [ ] Enrich installed responses from the on-disk manifest first and matching
  cached catalog metadata second, so descriptions and source links survive a
  network outage and release notes remain available after the community channel
  is disabled.

Tests:

- managed-source reconciliation is idempotent and adopts an existing official
  row without duplication;
- the community preference defaults off and toggles all managed community
  sources without touching custom rows;
- stale cache is served after fetch failure and replaced after recovery;
- official/community/external duplicate precedence is deterministic;
- update discovery cannot switch an installation to another repository;
- disabling the community channel leaves installations enabled and marks their
  updates paused;
- migration Up/Down and repository constraints behave correctly against the
  test database.

## Phase 4 — Server HTTP API

Repository: `silo-server`

- [ ] Add typed catalog-settings and capability handlers to
  `internal/api/handlers/plugins.go` or focused files in the same handler
  package; mount them under the existing acting-admin plugin route group.
- [ ] Extend repository, catalog, and installation serializers with the
  additive presentation, release, provenance, approval, stale, and paused
  fields.
- [ ] Compute `installed_community_plugin_count` by repository provenance, not
  by parsing plugin IDs or GitHub URLs.
- [ ] Validate the `PUT` body strictly and make preference persistence plus
  managed-row reconciliation atomic from the caller's perspective.
- [ ] Keep catalog source failures per-repository: one broken community or
  custom source must not fail the entire catalog response.
- [ ] Add handler tests for default-off state, enable/disable, malformed input,
  community installation counts, additive response fields, stale-source
  reporting, and acting-admin authorization.

Verification:
`GOWORK=off go test ./internal/plugins/... ./internal/api/handlers/...`.

## Phase 5 — Plugin Hub Web UI

Repository: `silo-server`

- [ ] Split `AdminPlugins.tsx` into focused components under
  `web/src/components/admin/plugins/` or `web/src/pages/admin-plugins/`: page
  shell, Installed list, Catalog grid, catalog controls, detail sheet,
  configuration content, Markdown renderer, and advanced source manager.
- [ ] Add typed DTOs and TanStack Query hooks for plugin capabilities and
  catalog settings. Mutations invalidate settings, repositories, catalog, and
  installations together.
- [ ] Keep **Installed** as the default tab and rename **Available** to
  **Catalog**.
- [ ] Place **Include approved community plugins** in the Catalog header with
  this helper meaning: these plugins are validated by Silo to work as described
  and are considered safe, but are maintained and supported by community
  contributors.
- [ ] Enabling shows a refresh state and then community entries. Disabling with
  installed community plugins opens a confirmation that reports the count and
  explains that plugins keep running while update discovery pauses.
- [ ] Add search plus provenance/capability filters. Preserve the active tab,
  search, and filters in URL search parameters.
- [ ] Catalog cards show display name, publisher/provenance, two-line summary,
  capabilities, version, configuration requirement, and Install. Installed
  rows show operational status, summary, version, Configure/Open, and
  Review update; move update policy, disable, and uninstall into secondary
  controls.
- [ ] Use URL search parameters for addressable details:
  `installation=<id>` for installed plugins and
  `repository=<id>&plugin=<plugin_id>` for catalog entries. Browser Back closes
  the sheet and focus returns to the originating card. Use a full-width sheet
  on small screens.
- [ ] Detail sections are **About**, **Setup**, **What's new**, and
  **Technical details**. Technical details include plugin ID, API/platform
  compatibility, publisher, license, source catalog, source repository, support,
  and changelog links.
- [ ] Change update behavior from immediate `Update` to `Review update`; show
  the target release notes before the administrator confirms the update.
- [ ] Add a constrained Markdown renderer. Raw HTML and images are disabled;
  only safe `http`/`https` links are rendered, and external links use
  `rel="noopener noreferrer"`.
- [ ] Move repository CRUD and archive upload beneath **Manage sources →
  Advanced**. Managed official/community sources show status and fetch errors
  but cannot have their system URL edited or be deleted.
- [ ] Backward-compatible UI fallbacks: humanize `plugin_id` when display name
  is absent; use the first capability description when summary is absent; show
  “No setup guide provided” or “No release notes were published” rather than an
  empty panel.
- [ ] Add accessible labels, keyboard navigation, focus management, loading
  skeletons, empty states, and per-source stale/error messaging.

Frontend tests:

- default-off toggle and opt-in catalog refresh;
- disable confirmation with installed community plugins;
- provenance filters and badges;
- legacy-manifest fallback rendering;
- detail deep link and Back behavior;
- safe Markdown links with raw HTML/images suppressed;
- Review update requires confirmation and displays release notes;
- stale catalog and missing release-note states.

Verification: `cd web && pnpm run lint`, `cd web && pnpm run format:check`,
`cd web && pnpm test`, and `cd web && pnpm run build`. Capture desktop and
mobile screenshots for the UI PR.

## Phase 6 — Manifest Backfill and Community Migration Readiness

Repositories: current first-party plugins and future community plugin repos

- [ ] Update every cataloged plugin to the released SDK version and add complete
  presentation metadata. Use plain language for homelab operators; capability
  descriptions remain technical and specific.
- [ ] Require each repository to publish meaningful GitHub release notes so
  What's new is not an empty surface.
- [ ] Standardize README setup instructions, license, security/support policy,
  CODEOWNERS or named maintainers, and the existing checksum-bearing release
  workflow.
- [ ] Before transferring a selected plugin to `silo-community`, update its
  manifest publisher/source links and release dispatch target, add it to the
  community approval registry through review, and remove it from the official
  catalog in the same rollout window.
- [ ] Verify a transferred plugin appears only once, installs from the community
  repository, remains repository-pinned for updates, and is hidden for admins
  who have not enabled the community channel.

---

## Rollout Order

1. Merge and release the SDK contract.
2. Upgrade official catalog tooling and create the approved community catalog
   plus approval policy/registry.
3. Land the server migration, managed-source reconciliation, cache, update
   pinning, and additive APIs.
4. Land the Plugin Hub UI and keep the community switch default-off.
5. Backfill presentation data and release notes in existing official plugins.
6. Transfer selected plugins one at a time only after both catalogs and the
   deployed server understand provenance.

Older servers ignore new catalog/manifest fields; newer servers retain fallbacks
for older manifests. Do not transfer a plugin out of the official catalog before
the community catalog and server opt-in path are deployed.

## Final Acceptance Scenarios

- A fresh server shows Silo-maintained catalog entries with friendly names,
  descriptions, setup instructions, source links, and release notes; approved
  community plugins are absent.
- An admin enables **Include approved community plugins**, sees the approved
  entries without entering a URL, and can identify their community maintenance
  and Silo approval clearly.
- A community catalog outage leaves cached entries visible with a stale warning
  and does not break the official catalog.
- Disabling the community channel with installed community plugins requires
  confirmation, leaves those plugins running, and clearly reports paused update
  discovery.
- A custom repository publishing the same plugin ID or a higher version cannot
  replace or update an official/approved installation.
- Review update presents the exact target release notes before installation.
- An old plugin without presentation fields remains manageable with readable
  fallbacks.
- Removing a plugin from the approved catalog stops new discovery and updates
  without remotely disabling existing installations.

## Cross-Repository Verification Gate

- `silo-plugin-sdk`: `GOWORK=off go test ./...` and `make proto`.
- Official and community catalogs: `GOWORK=off go test ./...` plus catalog
  rebuild/check idempotence.
- `silo-server`: focused plugin/API Go tests, web lint/format/test/build,
  `make lint`, and `make verify-local-paths`.
- End-to-end dev smoke: toggle community off/on, install one approved plugin,
  configure it, simulate a catalog outage, review an update, disable the
  community channel, and confirm the installed plugin continues running while
  updates are paused.
