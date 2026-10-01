# Collection templates

Collection templates are curated presets for synced library collections. The server owns the
catalog; the admin and personal template galleries render whatever it returns, and template
bundles apply a group of templates in one pass. This page covers what a contributor needs to add
or change a template: registration, validation, the rules the tests enforce, and the poster
artwork. User-facing behavior is documented in the manual at
https://siloserver.org/docs/manage-collections.

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
5. Add a `tmdb_discover` or `tmdb_collection` template to a bundle. The gallery can't create
   those two sources directly, and `TestBundleOnlyTemplatesAreReachableFromBundles` fails on one
   that no bundle references.
6. Add both poster files (see below).

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
  until a real ID is set.
- `trakt`: the validator still accepts it, but the server rejects new Trakt collections with
  `unsupported_source`. Don't add Trakt templates.

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
go test ./internal/api/handlers/ -run 'TestBuiltinTemplateTitleSlugsAreUnique|TestCollectionTemplateHandler|TestLibraryCollectionHandlerListsTemplateBundles'
```

When you change the gallery, also run
`pnpm --dir web exec vitest run src/components/CollectionTemplateGallery src/lib/collectionTemplates.test.ts`.
