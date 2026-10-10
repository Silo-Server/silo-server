# Collection templates

Collection templates are curated presets for synced library collections. The server owns the
catalog; the web offers its templates as ready-made picks in the collection editor's Synced list
step, and template bundles apply a group of templates in one pass. The admin web shows bundles as
Starter packs. This page covers what a contributor needs to add or change a template:
registration, validation, the rules the tests enforce, and the poster artwork. User-facing
behavior is documented in the manual at https://siloserver.org/docs/manage-collections.

## Catalog and registration

- `internal/collections/templates/builtin.go` holds `builtinTemplates` and `builtinBundles`.
  Its `init()` registers both on `templates.Default` and fills an empty `PosterPath` with
  `/images/collection-templates/{id}.jpg`.
- `Registry.Register` (`registry.go`) runs `validate` (`validate.go`) and panics on an invalid
  template or a duplicate ID. `RegisterBundle` panics on a duplicate bundle ID, an unknown or
  repeated template ID, or a template with `RequiresProfile`. A bad catalog entry therefore fails
  at startup and in the package tests, never at request time.
- There is no config-file path: the catalog is Go code. A new template needs no frontend change.
- `withAllDefaultsBundle` builds `all_defaults` as the de-duplicated union of every other bundle
  and lists it first.
- Template IDs are permanent once shipped. Bundles reference them, bundle apply writes them into
  each collection's management key (`templateBundleManagementKey` in
  `internal/api/handlers/library_collections.go`), and poster filenames use them.

## Adding a template

1. Append a `Template` to `builtinTemplates` with `ID`, `Title`, `Description`, `Icon`,
   `Category`, `Source`, `MediaKind`, exactly one source spec, `DefaultLimit`,
   `DefaultSortOrder`, and `DefaultSyncSchedule`. `validate` requires `ID`, `Title`, `Category`,
   and `MediaKind`, and rejects a template with more than one source spec.
2. Give it a title no other template shares after slugifying. Bundle apply adopts an existing
   collection with the same slugified title in a library, so the second of two same-slug
   templates is silently skipped. `TestBuiltinTemplateTitleSlugsAreUnique`
   (`internal/api/handlers/collection_templates_test.go`) enforces this.
3. Set `DefaultLimit` to `builtinDefaultLimit` (100). Sync reads up to four times the limit from
   the source (`collectionutil.SourceFetchLimit`), so a finite canonical list that must not be
   truncated gets an explicit override: the IMDb Top 250 templates use 250, and Criterion
   Collection and A24 use 0 (no limit). Add any new override to the table in
   `TestBuiltinTemplateDefaultLimits`.
4. Put `DefaultSortOrder` in its band. The band orders the collections a bundle creates, and the
   tests fail on a template outside its band:

   | Band | ID prefix | Also enforced |
   | --- | --- | --- |
   | 1000–1999 | `mdblist_charts_` | |
   | 2000–2999 | `mdblist_best_of_` | |
   | 3000–3999 | `mdblist_awards_` | |
   | 4000–4999 | `mdblist_streaming_` | |
   | 5000–5999 | `tmdb_discover_popular_` | `sort_by` `popularity.desc`, `vote_count_gte` 300 |
   | 6000–6999 | `tmdb_discover_top_rated_` | `sort_by` `vote_average.desc`, `vote_count_gte` 1000 |
   | 7000–7999 | `tmdb_franchise_` | source `tmdb_collection`, media kind `movie` |
   | 8000–8999 | `mdblist_seasonal_` | |
   | 9000–9999 | `mdblist_misc_`, `tmdb_discover_kids_` | kids: `certification_lte` `PG` |

   `TestPhase1MDBListTemplatesHaveSortOrderInExpectedBands`,
   `TestPhase2DiscoverTemplatesUseExpectedBands`, and
   `TestPhase3FranchiseTemplatesUseExpectedBands` hold these rules. The last two also pin the
   template counts (18 popular genres, 18 top-rated genres, 1 kids, 11 franchises including the
   placeholder); update the count when you add one.
5. Add a `tmdb_discover` or `tmdb_collection` template to a bundle. No import route creates
   those two sources, so the Synced list step can't create them and the v2 admin and personal
   template lists leave them out. `TestBundleOnlyTemplatesAreReachableFromBundles` fails on one
   that no bundle references.
6. Add both poster files (see below).

## Starter packs

The admin web calls template bundles Starter packs (`web/src/components/collections/StarterPacksDialog.tsx`,
helpers in `web/src/lib/collections/starterPacks.ts`). The Synced list step offers single
templates only, never bundles. The rules the web keeps:

- **Template summaries only.** The dialog reads `GET /api/v2/admin/collections/template-bundles`
  and its `templates` summaries, never the admin template catalog, so Discover and Franchise
  templates keep their titles and media kinds even when the catalog leaves them out.
- **One pack at a time.** Each pack is a separate apply with its own dry run and its own result.
  The table comes from `POST .../template-bundles/{bundle_id}/apply` with `dry_run: true`; Add
  runs the same dry run again, then queues `POST .../apply-job`. While the job runs, the pack,
  its libraries and its heroes are locked, so the job matches the table. After the job ends the
  dialog stays open, tags the pack Added and checks it again. The Collections page, not the
  dialog, refreshes the collections and Home rows the job changed; the dry run's query key sits
  outside `["admin", "collections"]` so that refresh doesn't re-run it.
- **No deletes.** The web always sends `delete_existing: false`. The server option stays for
  other clients.
- **Heroes are opt-in.** The hero switch is off by default and then the request carries no
  `featured` member, so `ClearFeaturedForSurface` never runs and hand-set heroes stay. With the
  switch on, the dry run and the job carry the same `featured`; a page set to Keep current is left
  out of it. The line for each page names the hero it replaces, read from the admin sections list.
- **Pinned first is the template's.** A pack collection keeps the template's `Featured` flag
  (pinned first on its shelf). Collections made in the editor start unpinned.
- **Hidden templates.** A template whose summary has `needs_setup` (the franchise placeholder) is
  never shown or counted. The server still creates it when its bundle is applied.
- **Where lists land.** A pack creates no shelves: its collections land in each library's
  no-heading group.

## Source rules

`validate` checks each source spec:

- `tmdb`: `preset` is `trending` (`media_type` `movie`, `tv`, or `all`; `time_window` `day` or
  `week`), `popular` or `top_rated` (`movie` or `tv`, no `time_window`), `now_playing` or
  `upcoming` (`movie`), or `airing_today` or `on_the_air` (`tv`).
- `mdblist`: an empty `url` makes the form ask for one. Otherwise `collectionutil.CanonicalMDBListURL`
  requires `http` or `https`, host `mdblist.com` or `www.mdblist.com`, port 80 or 443 if any,
  no userinfo, and a path under `/lists/`.
- `tmdb_list`: an empty `url` makes the form ask for one. Otherwise
  `collectionutil.ParseTMDBListURL` accepts a `themoviedb.org` or `www.themoviedb.org`
  `/list/{id}` page (the slug after the ID is ignored) or a bare list number.
- `tmdb_discover`: `media_type` `movie` or `tv`; `sort_by` from `tmdbDiscoverSortByValues`;
  non-negative vote and runtime bounds with `with_runtime_gte <= with_runtime_lte`;
  `YYYY-MM-DD` release dates; a two-letter `original_language`. Other filters pass to TMDB's
  `/discover` endpoint unchanged.
- `tmdb_collection`: `collection_id >= 0`. Zero is the placeholder for an admin-chosen franchise;
  `validateTMDBFranchiseConfig` (`internal/catalog/library_collection_service.go`) fails its sync
  until a real ID is set. `Template.NeedsSetup` reports this case: bundle apply creates the
  collection without a first sync, and the `/api/v2` bundle list marks its summary `needs_setup`.
- `trakt`: the validator still accepts it, but the server rejects new Trakt collections with
  `unsupported_source`. Don't add Trakt templates.

## Templates in the Synced list step

The web has no template gallery. Templates are suggestions inside the editor's Synced list step
(`web/src/components/collections/editor/SyncedListPanel.tsx`, rules in
`web/src/lib/collections/synced.ts`):

- **Creatable only.** A template shows when its source is one of the scope's `import_sources`
  (`mdblist`, `tmdb` or `tmdb_list`) and it needs no profile. The personal template list is
  already filtered that way on the server; the web applies the same filter to both scopes.
- **Named lists only.** An `mdblist` or `tmdb_list` template with an empty `url` is a "bring your
  own list" template; the step has its own link fields, so it isn't shown as a pick.
- **A pick is a starting point.** It fills the name, description, poster (`PosterPath`, sent as
  `poster_url`), `DefaultLimit` and `DefaultSyncSchedule` only where the person hasn't typed. A
  personal list maps the schedule to a named one. A TMDB chart chosen on the chart tab uses the
  template with the same preset, media type and window when there is one.
- **Not pinned.** A list made from a pick is created with `featured: false`; the template's
  `Featured` applies only through bundles.

## Poster artwork

Every built-in template ships two files named after its ID:

| File | Size | Existence checked by |
| --- | --- | --- |
| `web/assets-source/collection-templates/raw/{id}.png` | 1024×1536, generated plate without text | `TestBuiltinTemplateSourcePlatesStayOutOfPublicAssets` |
| `web/public/images/collection-templates/{id}.jpg` | 1000×1500, final poster with text | `TestBuiltinTemplatePosterAssetsExist` |

The raw plate keeps typography reproducible without generating the image again. Raw plates must
stay out of `web/public/`: the same test fails if
`web/public/images/collection-templates/raw` exists. `TestBuiltinCatalog` checks that every
poster path sits under `/images/collection-templates/`.
`TestBuiltinTemplateAssetsHaveTemplates` fails on any file in either directory without a
registered template. No test checks image dimensions, so size files by hand.

When you remove a template, delete its raw plate. Its final JPG may still be in use: a collection
created from the template keeps the template's poster path unless the poster was copied into
artwork storage. Keep the JPG and add the ID to `retiredTemplatePosterIDs` in `templates_test.go`
until no stored poster path can point at it.

Art rules:

- A 2:3, full-bleed, cinematic composition in the style of Kometa and Plex collection posters.
- Generic, original scenes only: no copyrighted posters, recognizable actors, franchise
  characters, provider logos, watermarks, or readable text in the generated plate.
- Art that fits the template. A horror template looks like horror, a documentary template looks
  investigative, and a streaming-service template suggests the service without its branding.
  Don't reuse one generic image across unrelated templates.

Workflow:

1. Generate a 2:3 plate. The prompt names the template's title and theme and asks for no text,
   logos, watermarks, real posters, recognizable actors, franchise characters, or provider
   branding.
2. Resize and center-crop it to 1024×1536 and save it as the raw PNG.
3. Resize and center-crop to 1000×1500 for the final JPG, and add typography locally, not in the
   generator: the media type in gold at top left, the collection title at bottom left, and
   optionally a short label under the title. Use a subtle dark vignette and a text shadow or
   stroke for contrast, not a solid black box.

## Tests

Commands assume the repository root is the cwd.

```sh
go test ./internal/collections/templates/...
go test ./internal/api/handlers/ -run 'TestBuiltinTemplateTitleSlugsAreUnique|TestCollectionTemplateHandler|TestLibraryCollectionHandlerListsTemplateBundles|TestV1Template'
go test ./internal/apiv2/ -run 'AdminTemplate|BuiltinBundleOnly|ImportableCollectionTemplates'
```

When you change how the web offers templates or Starter packs, also run
`pnpm --dir web exec vitest run src/lib/collections/synced.test.ts src/pages/CollectionEditorPage.synced.test.tsx src/components/collections/StarterPacksDialog.test.tsx src/lib/collections/starterPacks.test.ts src/lib/collectionTemplates.test.ts`.
