# Media naming and identity

The scanner and metadata worker turn file paths into movies, series, seasons, and episodes. This
page records the identity rules other code depends on. The user-facing catalog of supported
folder layouts and filename patterns is the manual's
https://siloserver.org/docs/media-folders; the regression tests in `internal/naming` hold the full
set of examples.

## Mixed classification

In a `mixed` library, `ResolvePathContext` (`internal/naming/filename.go`) and
`extractPathEvidence` (`internal/naming/root_inference.go`) classify each file in this order:

1. Season structure (`detectSeasonStructure`) makes it a series. Here a numeric-only folder
   such as `01` counts only when the file name has an episode token; series libraries accept it
   without one.
2. Movie-folder evidence (`detectInferMovieFolderEvidence`) makes it a movie: the parent folder
   carries a provider tag, or is a trusted title-and-year folder whose title the file name
   matches.
3. An episode token (`S01E02`, `1x02`, ...) makes it a series.
4. Anything else is a movie.

`movies` and `series` libraries skip this and take the library type. Step 2 comes before step 3
on purpose: a tagged movie folder must not become a series because its title looks like an
episode code (`TestResolvePathContext`, "mixed library obvious movie folder beats episode
token"). As a result, episodes in a provider-tagged show folder with no season folder classify as
movies, and an air-date name alone never makes a file a series. Metadata providers run after this
decision; an NFO never changes the type (see [local-nfo-metadata.md](local-nfo-metadata.md)).
`TestEpisodePatternAgreesAcrossClassifiers` keeps the two classifiers' episode detection in step.

## Episode numbers

- `parseEpisodeNumber` (`internal/naming/episode.go`) returns 0 for a digit run longer than
  `maxEpisodeNumberDigits` (5). A longer run is not an episode number and is never truncated into
  one; catalog episode columns are 32-bit. `TestEpisodeNamingRangeNumberBounds` and
  `TestEpisodePatternAgreesAcrossClassifiers` cover it.
- A season folder that disagrees with a three-digit compact code wins, and the whole number
  becomes the episode: `Season 21/301.mkv` is S21E301. A folder that agrees keeps the compact
  reading: `Season 3/301.mkv` is S3E1. A season in the file name beats the folder.
  `TestEpisodeNamingCompactCodeRespectsContainingSeason` holds these cases.

## Linking files that have no season

`linkSeriesFilesToEpisodesWithOptions` (`internal/metadata/service.go`) resolves a file with an
episode number but no known season through `unseasonedEpisodeIndex`
(`internal/metadata/unseasoned_episode.go`):

- The index leaves out season 0, episode 0, and rows whose `MetadataSource` is
  `scanner_fallback` or empty without a provider match. Fallback episodes can never establish
  a season.
- A distinctive title (`isDistinctiveSingleEpisodeTitle`: not generic, at least 10 characters
  and two words) links the file when exactly one indexed episode has that title. If some episode
  carries the file's number, the matched episode must have that number too.
- Otherwise the number must match exactly one indexed episode, in season 1, where absolute and
  per-season numbering read the same. A non-generic title in the file name must match that
  episode's title. Requiring season 1 also defers the link until season 1 metadata is stored.
- Nothing derives a cumulative absolute order from season lengths, because stored seasons may be
  incomplete. A file that fails these rules stays unlinked; it is not moved to season 0.

`TestUnseasonedEpisodeResolution`, `TestUnseasonedEpisodeLinksUseProviderEvidence`, and
`TestUnseasonedEpisodeRetriesWhenProviderMetadataArrives` cover these rules.

Air-date names link through `selectAirDateEpisodeCandidate` in the same file: one candidate
links; several are narrowed by the series' TVDB, then TMDB, then IMDb ID; a date that stays
ambiguous is skipped with a warning.

## A changed root waits for a rescan

Before matching a queued series root, `MatchWorker.processSeriesRoot`
(`internal/metadata/worker.go`) calls `seriesRootNeedsIdentityRescan`. If the root is file-rooted
and its files now parse to different series titles, or an anonymous sibling such as `E02.mkv`
sits beside a file-rooted show, the worker records a `candidate_rejected` failure ("filenames
identify different series within the queued root; rescan the library to update file grouping")
and does not relink anything. The one exception is `seriesRootHasManualGroupIdentity`: every
queued file shares one content group key and version, and that group carries an operator
override typed as a series (or untyped). Tests: `TestStaleFlatSeriesQueueDoesNotRelinkDifferentShows`,
`TestStaleFlatSeriesQueueWithAnonymousSiblingRequiresRescan`, and
`TestConsistentSeriesQueueDoesNotRequireRescan` (`internal/metadata/flat_series_queue_test.go`).

## Ambiguous roots and overrides

- `InferRootAssignments` (`internal/naming/root_inference.go`) marks a root `ambiguous` when its
  type votes conflict with both episode and movie evidence, when it has no provider tag and
  either low type confidence or an inferred series type with no title, or when a single-file
  root contradicts its folder. `InferGroupIdentity` also marks a group ambiguous when folder and
  file titles disagree and no provider ID settles them. `createOrFindSkeleton`
  (`internal/metadata/service.go`) gives an ambiguous group's item `ItemStatus` `ambiguous`; a
  movie can leave that state when `resolveMovieTitleAmbiguity` confirms its provider IDs.
  Tests: `TestCollectScannedRoots_ContradictorySingleMovieFileBecomesAmbiguous`,
  `TestInferGroupIdentity_TitleConflictWithoutIDsStaysAmbiguous`.
- Manual overrides win over inference:
  - A root override makes the deepest overridden ancestor the file's root
    (`deepestOverrideAncestor`) and replaces the inferred type, title, year, and provider IDs.
    The root becomes `resolved` with override source `manual`.
  - A group override, which the admin **Override** action on an ambiguous root writes
    (`LibraryHandler.SetRootOverride`), is applied by `applyGroupOverrides`
    (`internal/scanner/group_inference.go`) the same way, with high type confidence.
  - A file identity override beats a root override.
  - A location that identity overrides split across groups is not flagged ambiguous
    (`aggregateLocationState`).
- Saving an override does not start a scan; the scanner applies it on the next one, and later
  scans keep it. Tests: `TestApplyGroupOverrides_ForcesResolvedIdentity`,
  `TestInferGroupAssignments_FileOverrideBeatsRootOverride`, and
  `TestInferGroupAssignments_OverridesConvergeAcrossRescans` (`internal/scanner/group_inference_test.go`).
