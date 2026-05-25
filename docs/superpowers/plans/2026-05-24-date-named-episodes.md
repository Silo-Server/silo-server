# Date-Named Episodes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make date-named TV episode files visible as episodes even when TVDB/TMDB child episode metadata has not caught up yet.

**Architecture:** Keep the fix server-side in the metadata layer. The naming parser already extracts `AirDate` and season-folder `SeasonNum`; metadata linking should use that season hint to disambiguate existing episode rows, synthesize conservative `scanner_fallback` episode rows when provider rows are missing, and reconcile those fallback rows by `air_date` when provider metadata later arrives with real numbering.

**Tech Stack:** Go, PostgreSQL via `pgx`, existing `catalog` repositories, `internal/metadata` fallback/linking flow, targeted Go tests.

---

## Validated Direction

Three subagents independently reviewed scanner/naming, metadata/linking, and provider/refresh implications.

- Parser support already exists. `naming.ParseFilename` returns both `AirDate` and a season-folder `SeasonNum` for date-named files in `Season NN` folders.
- Do not depend on plugin changes. TVDB/TMDB plugins already expose season episode lists; the issue is provider incompleteness/latency and server fallback policy.
- Do not make scanner persistence the core fix. The scanner currently drops date-only season hints from `media_files`, but metadata reparses file paths and can link/synthesize without a schema migration.
- Add provider reconciliation. Fallback episodes use provisional episode numbers, so provider refresh must adopt an existing `scanner_fallback` row by exact `air_date` instead of creating a duplicate if provider numbering differs.

## File Structure

- Modify `internal/naming/filename_test.go`
  - Add a regression for the exact Late Show date-name shape so parser behavior stays pinned.
- Modify `internal/catalog/episode_repo.go`
  - Add unfiltered episode listing for one season.
  - Add a repository method that upgrades one exact `scanner_fallback` episode by air date to provider metadata.
- Modify `internal/metadata/service.go`
  - Preserve season hints for date-named files.
  - Filter air-date matches by explicit season when present.
  - Synthesize date-named fallback episodes for explicit season folders.
  - Use provider-by-air-date reconciliation before inserting provider episode rows.
- Modify `internal/metadata/fallback_episode_test.go`
  - Extend fakes for the new repository methods.
  - Cover season-hinted air-date disambiguation, Late Show fallback synthesis, safe skips, and provider adoption.

No migration is planned. No plugin repo changes are planned.

---

### Task 1: Pin Date-Name Parser Behavior

**Files:**
- Modify: `internal/naming/filename_test.go`

- [ ] **Step 1: Add the Late Show parser regression**

Add this case to the existing table that covers date-named series files, or create `TestParseFilenameDateNamedLateShowSeasonFolder` if the surrounding table shape is awkward:

```go
func TestParseFilenameDateNamedLateShowSeasonFolder(t *testing.T) {
	hints := ParseFilename(
		"/mnt/sharedrives/zd-storage-ceph/television/10s/The Late Show with Stephen Colbert (2015) {tvdb-289574}/Season 11/The Late Show with Stephen Colbert (2015) - 2026-05-21 - A Goodbye Celebration [WEBDL-1080p 8-bit h264 AAC 2.0]-ILP.mkv",
		"series",
	)
	if hints == nil {
		t.Fatal("ParseFilename returned nil")
	}
	if hints.Type != "series" {
		t.Fatalf("Type = %q, want series", hints.Type)
	}
	if hints.SeasonNum != 11 {
		t.Fatalf("SeasonNum = %d, want 11", hints.SeasonNum)
	}
	if hints.EpisodeNum != 0 {
		t.Fatalf("EpisodeNum = %d, want 0 for date-named file", hints.EpisodeNum)
	}
	if hints.AirDate != "2026-05-21" {
		t.Fatalf("AirDate = %q, want 2026-05-21", hints.AirDate)
	}
	if hints.Title != "The Late Show with Stephen Colbert" {
		t.Fatalf("Title = %q, want The Late Show with Stephen Colbert", hints.Title)
	}
	if hints.Year != 2015 {
		t.Fatalf("Year = %d, want 2015", hints.Year)
	}
}
```

- [ ] **Step 2: Run the parser test**

Run:

```bash
go test ./internal/naming -run TestParseFilenameDateNamedLateShowSeasonFolder -count=1
```

Expected: pass. This is a guardrail test for existing parser behavior, not a failing implementation test.

- [ ] **Step 3: Commit**

```bash
git add internal/naming/filename_test.go
git commit -m "test(metadata): pin date-named season filename parsing"
```

---

### Task 2: Use Season Hints When Linking Existing Air-Date Episodes

**Files:**
- Modify: `internal/metadata/service.go`
- Modify: `internal/metadata/fallback_episode_test.go`

- [ ] **Step 1: Add a failing ambiguity regression**

Add this test near the existing air-date linking tests in `internal/metadata/fallback_episode_test.go`:

```go
func TestEnsureSeriesEpisodeLinks_UsesSeasonHintForAirDateAmbiguity(t *testing.T) {
	h := newFallbackTestHarness()
	ctx := context.Background()

	seriesID := "series-late-show-season-hint"
	h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: seriesID,
		Title:     "The Late Show with Stephen Colbert",
		Type:      "series",
		Status:    "matched",
		TvdbID:    "289574",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	})
	h.episodeRepo.Upsert(ctx, &models.Episode{
		ContentID:      "special-2026-02-16",
		SeriesID:       seriesID,
		SeasonID:       "season-0",
		SeasonNumber:   0,
		EpisodeNumber:  16,
		Title:          "Texas Legislature Special",
		AirDate:        mustDate(t, "2026-02-16"),
		MetadataSource: "provider",
	})
	h.episodeRepo.Upsert(ctx, &models.Episode{
		ContentID:      "season11-episode73",
		SeriesID:       seriesID,
		SeasonID:       "season-11",
		SeasonNumber:   11,
		EpisodeNumber:  73,
		Title:          "Jennifer Garner, Robert Duvall",
		AirDate:        mustDate(t, "2026-02-16"),
		MetadataSource: "provider",
	})

	file := &models.MediaFile{
		ID:            120,
		MediaFolderID: 10,
		FilePath:      "/media/tv/The Late Show with Stephen Colbert (2015) {tvdb-289574}/Season 11/The Late Show with Stephen Colbert (2015) - 2026-02-16 - Jennifer Garner - [ WEBDL-1080p h264 EAC3 2.0 ]-JOAN.mkv",
	}
	h.fileRepo.addFile(file)
	h.fileRepo.contentIDs[file.ID] = seriesID

	if err := h.service.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
		t.Fatalf("ensureSeriesEpisodeLinks failed: %v", err)
	}
	if got := h.fileRepo.episodeLinks[file.ID]; got != "season11-episode73" {
		t.Fatalf("episode link = %q, want season11-episode73", got)
	}
	linked := h.fileRepo.files[file.ID]
	if linked.SeasonNumber != 11 || linked.EpisodeNumber != 73 {
		t.Fatalf("linked season/episode = S%dE%d, want S11E73", linked.SeasonNumber, linked.EpisodeNumber)
	}
}
```

- [ ] **Step 2: Run the failing regression**

Run:

```bash
go test ./internal/metadata -run TestEnsureSeriesEpisodeLinks_UsesSeasonHintForAirDateAmbiguity -count=1
```

Expected before implementation: fail because the current air-date hint discards `SeasonNum`, leaving two candidates for `2026-02-16`.

- [ ] **Step 3: Preserve season hints for air-date files**

In `internal/metadata/service.go`, change the air-date branch in `parseEpisodeLinkHint` from:

```go
if fnh.AirDate != "" {
	return episodeLinkHint{airDate: fnh.AirDate, ok: true}
}
```

to:

```go
if fnh.AirDate != "" {
	return episodeLinkHint{seasonNum: fnh.SeasonNum, airDate: fnh.AirDate, ok: true}
}
```

- [ ] **Step 4: Filter candidates by explicit season before provider preference**

Change `selectAirDateEpisodeCandidate` to accept `seasonHint int` and filter when `seasonHint > 0`:

```go
func selectAirDateEpisodeCandidate(candidates []*models.Episode, seriesItem *models.MediaItem, seasonHint int) (*models.Episode, bool) {
	if seasonHint > 0 {
		filtered := make([]*models.Episode, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate != nil && candidate.SeasonNumber == seasonHint {
				filtered = append(filtered, candidate)
			}
		}
		if len(filtered) == 1 {
			return filtered[0], true
		}
		if len(filtered) > 1 {
			candidates = filtered
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	for _, provider := range preferredEpisodeProviders(seriesItem) {
		filtered := filterEpisodesByProviderID(candidates, provider)
		if len(filtered) == 1 {
			return filtered[0], true
		}
		if len(filtered) > 1 {
			return nil, false
		}
	}
	return nil, false
}
```

Update the call site in `linkSeriesFilesToEpisodesWithOptions`:

```go
selected, ok := selectAirDateEpisodeCandidate(candidates, seriesItem, hint.seasonNum)
```

- [ ] **Step 5: Run the targeted metadata test**

Run:

```bash
go test ./internal/metadata -run 'TestEnsureSeriesEpisodeLinks_(UsesSeasonHintForAirDateAmbiguity|SkipsAmbiguousAirDateMatch|PrefersSeriesProviderForAirDateMatch|LinksDateNamedFileByAirDate)' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add internal/metadata/service.go internal/metadata/fallback_episode_test.go
git commit -m "fix(metadata): use season hints for air-date episode links"
```

---

### Task 3: Add Episode Repository Primitives For Fallback Inference And Adoption

**Files:**
- Modify: `internal/catalog/episode_repo.go`
- Modify: `internal/metadata/service.go`
- Modify: `internal/metadata/fallback_episode_test.go`

- [ ] **Step 1: Extend the metadata episode repository interface**

In `internal/metadata/service.go`, extend `metadataEpisodeRepo` with:

```go
	ListBySeriesAndSeasonUnscoped(ctx context.Context, seriesID string, seasonNum int) ([]*models.Episode, error)
	AdoptScannerFallbackEpisode(ctx context.Context, ep *models.Episode) (bool, error)
```

- [ ] **Step 2: Add unfiltered season listing to `EpisodeRepository`**

Add this method in `internal/catalog/episode_repo.go` near `ListBySeason`:

```go
// ListBySeriesAndSeasonUnscoped returns all episode rows for a series season,
// including rows without episode_libraries memberships. Metadata fallback
// inference needs provider rows before local files exist for every episode.
func (r *EpisodeRepository) ListBySeriesAndSeasonUnscoped(ctx context.Context, seriesID string, seasonNum int) ([]*models.Episode, error) {
	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE series_id = $1 AND season_number = $2
		ORDER BY episode_number ASC`

	rows, err := r.pool.Query(ctx, query, seriesID, seasonNum)
	if err != nil {
		return nil, fmt.Errorf("listing unscoped episodes by series season: %w", err)
	}
	defer rows.Close()

	return scanEpisodes(rows)
}
```

- [ ] **Step 3: Add provider adoption to `EpisodeRepository`**

Add a method named `AdoptScannerFallbackEpisode`. The method must:

- return `false, nil` when `ep` is nil, `ep.AirDate` is nil, or required identity fields are empty
- run in a transaction
- clear stale external IDs for `imdb_id`, `tmdb_id`, and `tvdb_id` in the same series before updating
- find fallback rows with the exact same `series_id`, exact same `air_date`, and `metadata_source = 'scanner_fallback'`
- require the fallback row's current `season_number` to equal the provider episode's `SeasonNumber`
- adopt only when exactly one fallback row exists
- refuse adoption if another row already owns the target `(series_id, season_number, episode_number)`
- update the fallback row in place, preserving its `content_id`
- run `updateSeriesLastAirDateSQL` before committing

Use this SQL shape inside the transaction:

```go
rows, err := tx.Query(ctx, `
	SELECT content_id
	FROM episodes
	WHERE series_id = $1
	  AND air_date = $2
	  AND season_number = $3
	  AND metadata_source = 'scanner_fallback'
	ORDER BY content_id ASC
`, ep.SeriesID, ep.AirDate, ep.SeasonNumber)
```

Then verify the target key:

```go
var existingTarget string
targetErr := tx.QueryRow(ctx, `
	SELECT content_id
	FROM episodes
	WHERE series_id = $1 AND season_number = $2 AND episode_number = $3
`, ep.SeriesID, ep.SeasonNumber, ep.EpisodeNumber).Scan(&existingTarget)
if targetErr == nil && existingTarget != fallbackID {
	return false, nil
}
if targetErr != nil && !errors.Is(targetErr, pgx.ErrNoRows) {
	return false, fmt.Errorf("checking provider episode target before fallback adoption: %w", targetErr)
}
```

Update the fallback row:

```go
_, err = tx.Exec(ctx, `
	UPDATE episodes
	SET season_id = $2,
		season_number = $3,
		episode_number = $4,
		title = $5,
		default_metadata_language = $6,
		overview = $7,
		air_date = $8,
		runtime = $9,
		rating_imdb = $10,
		rating_tmdb = $11,
		imdb_id = COALESCE(NULLIF($12, ''), imdb_id),
		tmdb_id = COALESCE(NULLIF($13, ''), tmdb_id),
		tvdb_id = COALESCE(NULLIF($14, ''), tvdb_id),
		still_path = $15,
		still_thumbhash = $16,
		metadata_s3_path = $17,
		metadata_etag = $18,
		metadata_source = $19,
		updated_at = NOW()
	WHERE content_id = $1
`, fallbackID, nilIfEmpty(ep.SeasonID), ep.SeasonNumber, ep.EpisodeNumber,
	ep.Title, ep.DefaultMetadataLanguage, ep.Overview, ep.AirDate, ep.Runtime,
	ep.RatingIMDB, ep.RatingTMDB, ep.ImdbID, ep.TmdbID, ep.TvdbID,
	ep.StillPath, ep.StillThumbhash, ep.MetadataS3Path, ep.MetadataEtag,
	ep.MetadataSource)
```

Set `ep.ContentID = fallbackID` when adoption succeeds.

- [ ] **Step 4: Extend the fake episode repo**

In `internal/metadata/fallback_episode_test.go`, add fake methods matching the new interface. `ListBySeriesAndSeasonUnscoped` should return all episodes for the series/season without availability filtering. `AdoptScannerFallbackEpisode` should find exactly one fallback row by `SeriesID`, `SeasonNumber`, `AirDate`, and `MetadataSource == "scanner_fallback"`, move it from its old `episodeKey` to the provider key, copy provider fields, preserve `ContentID`, and return `true`.

- [ ] **Step 5: Add provider adoption regression**

Add this test:

```go
func TestPersistSeasonsAndEpisodes_AdoptsScannerFallbackByAirDate(t *testing.T) {
	h := newFallbackTestHarness()
	ctx := context.Background()

	seriesID := "series-provider-adopts-airdate"
	h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: seriesID,
		Title:     "Daily Show",
		Type:      "series",
		Status:    "matched",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	})
	h.seasonRepo.Upsert(ctx, &models.Season{
		ContentID:      "season-11",
		SeriesID:       seriesID,
		SeasonNumber:   11,
		Title:          "Season 11",
		MetadataSource: "provider",
	})
	h.episodeRepo.Upsert(ctx, &models.Episode{
		ContentID:      "fallback-episode",
		SeriesID:       seriesID,
		SeasonID:       "season-11",
		SeasonNumber:   11,
		EpisodeNumber:  103,
		Title:          "Episode 103",
		AirDate:        mustDate(t, "2026-05-21"),
		MetadataSource: "scanner_fallback",
	})

	h.service.persistSeasonsAndEpisodes(ctx, seriesID, "en", "en", []SeasonResult{{
		SeasonNumber: 11,
		Title:        "Season 11",
	}}, []EpisodeResult{{
		ProviderIDs:   map[string]string{"tvdb": "real-tvdb-episode"},
		SeasonNumber:  11,
		EpisodeNumber: 120,
		Title:         "A Goodbye Celebration",
		AirDate:       "2026-05-21",
	}}, MergeReplace)

	if _, err := h.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 11, 103); err == nil {
		t.Fatal("old fallback natural key still exists; expected provider adoption to move it")
	}
	adopted, err := h.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 11, 120)
	if err != nil {
		t.Fatalf("adopted provider episode not found: %v", err)
	}
	if adopted.ContentID != "fallback-episode" {
		t.Fatalf("ContentID = %q, want fallback-episode", adopted.ContentID)
	}
	if adopted.MetadataSource != "provider" {
		t.Fatalf("MetadataSource = %q, want provider", adopted.MetadataSource)
	}
	if adopted.TvdbID != "real-tvdb-episode" {
		t.Fatalf("TvdbID = %q, want real-tvdb-episode", adopted.TvdbID)
	}
}
```

- [ ] **Step 6: Add cross-season adoption safety regression**

Add a regression that creates one `scanner_fallback` row for `S11` with `air_date = 2026-02-16`, then persists a provider `EpisodeResult` for `S00E16` with the same air date. Assert that `AdoptScannerFallbackEpisode` returns false through `persistSeasonsAndEpisodes`, the `S11` fallback keeps its original `content_id` and natural key, and the provider special is inserted or updated as its own row. This protects the real Late Show ambiguity where specials and regular episodes can share an air date.

- [ ] **Step 7: Wire provider adoption into persistence**

In `persistSeasonsAndEpisodes`, after `dbEp` is built and before `s.episodeRepo.Upsert(ctx, dbEp)`, add:

```go
adopted := false
if existingEpisode == nil && dbEp.AirDate != nil {
	var adoptErr error
	adopted, adoptErr = s.episodeRepo.AdoptScannerFallbackEpisode(ctx, dbEp)
	if adoptErr != nil {
		slog.Warn("metadata: failed to adopt scanner fallback episode",
			"series_id", seriesID,
			"season", dbEp.SeasonNumber,
			"episode", dbEp.EpisodeNumber,
			"air_date", dbEp.AirDate.Format("2006-01-02"),
			"error", adoptErr)
		adopted = false
	}
}
if !adopted {
	if err := s.episodeRepo.Upsert(ctx, dbEp); err != nil {
		slog.Warn("metadata: failed to upsert episode",
			"series_id", seriesID,
			"season", ep.SeasonNumber,
			"episode", ep.EpisodeNumber,
			"error", err)
		continue
	}
}
```

Keep the existing localization block immediately after this upsert/adoption branch. Adopted episodes skip only the normal `Upsert` call; non-canonical localization still runs with `dbEp.ContentID`, which `AdoptScannerFallbackEpisode` has set to the preserved fallback `content_id`. Do not duplicate localization logic.

- [ ] **Step 8: Run adoption tests**

Run:

```bash
go test ./internal/metadata -run 'TestPersistSeasonsAndEpisodes_(AdoptsScannerFallbackByAirDate|DoesNotAdoptFallbackAcrossSeasonByAirDate)' -count=1
```

Expected: pass.

- [ ] **Step 9: Commit**

```bash
git add internal/catalog/episode_repo.go internal/metadata/service.go internal/metadata/fallback_episode_test.go
git commit -m "fix(metadata): adopt date fallback episodes during provider refresh"
```

---

### Task 4: Synthesize Date-Named Fallback Episodes

**Files:**
- Modify: `internal/metadata/service.go`
- Modify: `internal/metadata/fallback_episode_test.go`

- [ ] **Step 1: Add failing Late Show fallback regression**

Add this test:

```go
func TestEnsureSeriesEpisodeLinks_SynthesizesDateNamedEpisodesAfterLatestKnownEpisode(t *testing.T) {
	h := newFallbackTestHarness()
	ctx := context.Background()

	seriesID := "series-late-show-date-fallback"
	h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: seriesID,
		Title:     "The Late Show with Stephen Colbert",
		Type:      "series",
		Status:    "matched",
		TvdbID:    "289574",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	})
	h.seasonRepo.Upsert(ctx, &models.Season{
		ContentID:      "season-11",
		SeriesID:       seriesID,
		SeasonNumber:   11,
		Title:          "Season 11",
		MetadataSource: "provider",
	})
	h.episodeRepo.Upsert(ctx, &models.Episode{
		ContentID:      "season11-episode102",
		SeriesID:       seriesID,
		SeasonID:       "season-11",
		SeasonNumber:   11,
		EpisodeNumber:  102,
		Title:          "Anderson Cooper, Patton Oswalt",
		AirDate:        mustDate(t, "2026-04-16"),
		MetadataSource: "provider",
	})

	files := []*models.MediaFile{
		{
			ID:            201,
			MediaFolderID: 10,
			FilePath:      "/media/tv/The Late Show with Stephen Colbert (2015) {tvdb-289574}/Season 11/The Late Show with Stephen Colbert (2015) - 2026-04-20 - Don Cheadle Jake Tapper [WEBDL-1080p 8-bit h264 AAC 2.0]-JOAN.mkv",
		},
		{
			ID:            202,
			MediaFolderID: 10,
			FilePath:      "/media/tv/The Late Show with Stephen Colbert (2015) {tvdb-289574}/Season 11/The Late Show with Stephen Colbert (2015) - 2026-04-21 - Neil deGrasse Tyson RAYE John Kerry [WEBDL-1080p 8-bit h264 EAC3 2.0]-JOAN.mkv",
		},
	}
	for _, file := range files {
		h.fileRepo.addFile(file)
		h.fileRepo.contentIDs[file.ID] = seriesID
	}

	if err := h.service.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
		t.Fatalf("ensureSeriesEpisodeLinks failed: %v", err)
	}

	ep103, err := h.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 11, 103)
	if err != nil {
		t.Fatalf("S11E103 fallback not created: %v", err)
	}
	if ep103.MetadataSource != "scanner_fallback" {
		t.Fatalf("S11E103 MetadataSource = %q, want scanner_fallback", ep103.MetadataSource)
	}
	if ep103.AirDate == nil || ep103.AirDate.Format("2006-01-02") != "2026-04-20" {
		t.Fatalf("S11E103 AirDate = %v, want 2026-04-20", ep103.AirDate)
	}
	if got := h.fileRepo.episodeLinks[201]; got != ep103.ContentID {
		t.Fatalf("file 201 episode link = %q, want %q", got, ep103.ContentID)
	}

	ep104, err := h.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 11, 104)
	if err != nil {
		t.Fatalf("S11E104 fallback not created: %v", err)
	}
	if ep104.AirDate == nil || ep104.AirDate.Format("2006-01-02") != "2026-04-21" {
		t.Fatalf("S11E104 AirDate = %v, want 2026-04-21", ep104.AirDate)
	}
	if got := h.fileRepo.episodeLinks[202]; got != ep104.ContentID {
		t.Fatalf("file 202 episode link = %q, want %q", got, ep104.ContentID)
	}

	item, err := h.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		t.Fatalf("series item not found: %v", err)
	}
	if !item.EpisodeMetadataIncomplete {
		t.Fatal("expected EpisodeMetadataIncomplete=true after scanner fallback synthesis")
	}
}
```

- [ ] **Step 2: Add a safety regression for seasonless date files**

Add:

```go
func TestEnsureSeriesEpisodeLinks_DoesNotSynthesizeDateFallbackWithoutSeasonHint(t *testing.T) {
	h := newFallbackTestHarness()
	ctx := context.Background()

	seriesID := "series-date-no-season"
	h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: seriesID,
		Title:     "Daily Show",
		Type:      "series",
		Status:    "matched",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	})
	file := &models.MediaFile{
		ID:            220,
		MediaFolderID: 10,
		FilePath:      "/media/tv/Daily Show/Daily Show - 2026-04-24.mkv",
	}
	h.fileRepo.addFile(file)
	h.fileRepo.contentIDs[file.ID] = seriesID

	if err := h.service.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
		t.Fatalf("ensureSeriesEpisodeLinks failed: %v", err)
	}
	if got := h.fileRepo.episodeLinks[file.ID]; got != "" {
		t.Fatalf("unexpected episode link = %q", got)
	}
	if episodes := h.episodeRepo.listBySeries(seriesID); len(episodes) != 0 {
		t.Fatalf("synthesized %d episodes without explicit season hint", len(episodes))
	}
}
```

- [ ] **Step 3: Run the failing fallback tests**

Run:

```bash
go test ./internal/metadata -run 'TestEnsureSeriesEpisodeLinks_(SynthesizesDateNamedEpisodesAfterLatestKnownEpisode|DoesNotSynthesizeDateFallbackWithoutSeasonHint)' -count=1
```

Expected before implementation: the synthesis test fails because date-only files are skipped.

- [ ] **Step 4: Add date fallback helper types**

In `internal/metadata/service.go`, add helper structs near `episodeLinkHint`:

```go
type dateFallbackCandidate struct {
	file      *models.MediaFile
	hint      episodeLinkHint
	airDate   time.Time
	airDateID string
}

type dateFallbackPlan struct {
	file       *models.MediaFile
	seasonNum  int
	episodeNum int
	airDate    time.Time
}
```

- [ ] **Step 5: Add date fallback synthesis entrypoint**

Add `synthesizeDateFallbackEpisodes` to `internal/metadata/service.go`. The method should:

- list unlinked series files
- parse `episodeLinkHint`
- keep only hints where `airDate != ""`, `seasonNum > 0`, and `episodeNum == 0`
- group by season
- load all existing episodes for each season using `ListBySeriesAndSeasonUnscoped`
- skip any date that already has an episode in that same season
- infer provisional episode numbers with append/gap-safe logic
- create missing season rows if needed using the same season creation pattern as numeric fallback
- create `scanner_fallback` episode rows with `AirDate`
- call `linkSeriesFilesToEpisodesWithOptions(ctx, seriesID, true)` after inserts

Use this inference policy:

```text
For each explicit season:
1. Sort existing episodes by air_date, then episode_number.
2. Sort missing local dates ascending.
3. If the season has no existing episodes, assign missing dates episode numbers 1..N.
4. If a missing date is after the latest existing dated episode, assign latest episode_number + ordinal_after_latest.
5. If a missing date is between two existing dated provider episodes, assign only when every local missing date in that date range exactly fills the numeric gap between those provider episode numbers.
6. Skip leading dates before the first existing dated episode.
7. Skip season 0.
8. Never overwrite an existing episode row.
```

- [ ] **Step 6: Call date fallback from ensure flow**

In `ensureSeriesEpisodeLinksCore`, after the existing `linkSeriesFilesToEpisodesWithOptions` call for the non-numeric path, call date fallback synthesis and then refresh metadata state:

```go
if !needsSynthesis {
	if err := s.linkSeriesFilesToEpisodesWithOptions(ctx, seriesID, item.EpisodeMetadataIncomplete); err != nil {
		return err
	}
	if err := s.synthesizeDateFallbackEpisodes(ctx, seriesID); err != nil {
		return err
	}
	s.refreshSeriesEpisodeMetadataState(ctx, seriesID, time.Now())
	return nil
}
```

Also call `s.synthesizeDateFallbackEpisodes(ctx, seriesID)` inside `synthesizeFallbackSeriesStructure` after the existing numeric fallback loop and before the final link call. This keeps explicit numeric and date fallback paths consistent.

- [ ] **Step 7: Keep fallback titles simple and stable**

Use `fallbackEpisodeTitle(episodeNum)` for synthesized date fallback rows in this task. Do not parse guest names from filenames in this implementation. Provider refresh will replace fallback titles, and avoiding title parsing keeps the matching fix focused and deterministic.

- [ ] **Step 8: Run fallback tests**

Run:

```bash
go test ./internal/metadata -run 'TestEnsureSeriesEpisodeLinks_(SynthesizesDateNamedEpisodesAfterLatestKnownEpisode|DoesNotSynthesizeDateFallbackWithoutSeasonHint|SkipsMissingAirDateMatch|UsesSeasonHintForAirDateAmbiguity)' -count=1
```

Expected: pass.

- [ ] **Step 9: Commit**

```bash
git add internal/metadata/service.go internal/metadata/fallback_episode_test.go
git commit -m "fix(metadata): synthesize fallback episodes for date-named files"
```

---

### Task 5: Full Regression Sweep And Static Checks

**Files:**
- No new files.
- Verify changes from prior tasks.

- [ ] **Step 1: Run focused packages**

Run:

```bash
go test ./internal/naming ./internal/catalog ./internal/metadata -count=1
```

Expected: pass.

- [ ] **Step 2: Run scanner package to confirm no scanner regression**

Run:

```bash
go test ./internal/scanner -count=1
```

Expected: pass. This plan intentionally avoids scanner persistence changes; this command guards the shared naming/parser usage.

- [ ] **Step 3: Run formatting**

Run:

```bash
gofmt -w internal/naming/filename_test.go internal/catalog/episode_repo.go internal/metadata/service.go internal/metadata/fallback_episode_test.go
```

Expected: no output.

- [ ] **Step 4: Re-run focused packages after formatting**

Run:

```bash
go test ./internal/naming ./internal/catalog ./internal/metadata ./internal/scanner -count=1
```

Expected: pass.

- [ ] **Step 5: Optional full backend test**

Run when time allows:

```bash
go test ./internal/... -count=1
```

Expected: pass. If unrelated packages fail, capture the failing package and error in the final handoff instead of hiding it.

- [ ] **Step 6: Commit verification-only cleanup if formatting changed files**

If `gofmt` changed files not already committed:

```bash
git add internal/naming/filename_test.go internal/catalog/episode_repo.go internal/metadata/service.go internal/metadata/fallback_episode_test.go
git commit -m "chore(metadata): format date episode fallback changes"
```

If no files changed, do not create an empty commit.

---

### Task 6: Dev Runtime Verification

**Files:**
- No code files.
- Use the deployed dev server and database after code is merged/deployed.

- [ ] **Step 1: Confirm pre-deploy symptom on dev**

Run:

```bash
ssh root@100.86.116.20 'docker exec silo-postgres-1 psql -U continuum -d continuum -P pager=off -c "SELECT count(*) AS unlinked_date_named FROM media_files WHERE content_id = '\''120822227783385091'\'' AND episode_id IS NULL AND file_path LIKE '\''%/Season 11/%'\'' AND file_path ~ '\'' - [0-9]{4}-[0-9]{2}-[0-9]{2} - '\'';"'
```

Expected before deploy: greater than zero.

- [ ] **Step 2: Deploy using the repo's normal dev workflow**

Use the existing Silo dev deploy command for this repository. If no deploy command is available in the current branch, build locally first:

```bash
make build
```

Expected: build succeeds.

- [ ] **Step 3: Trigger metadata processing for the affected series**

Use the admin UI refresh action or the existing admin refresh endpoint for item `120822227783385091`. If an auth token is not available to the agent, open the item in the admin UI and trigger "Refresh metadata" manually, then continue verification.

- [ ] **Step 4: Verify fallback rows and links**

Run:

```bash
ssh root@100.86.116.20 'docker exec silo-postgres-1 psql -U continuum -d continuum -P pager=off -c "SELECT e.air_date, e.season_number, e.episode_number, e.title, e.metadata_source, mf.id AS file_id FROM episodes e JOIN media_files mf ON mf.episode_id = e.content_id WHERE e.series_id = '\''120822227783385091'\'' AND e.season_number = 11 AND e.air_date >= DATE '\''2026-04-20'\'' ORDER BY e.air_date;"'
```

Expected after refresh: rows exist for the date-named files after `2026-04-16`, `metadata_source` is `scanner_fallback` until provider metadata catches up, and each row has a `file_id`.

- [ ] **Step 5: Verify catalog sort date improves**

Run:

```bash
ssh root@100.86.116.20 'docker exec silo-postgres-1 psql -U continuum -d continuum -P pager=off -c "SELECT content_id, last_air_date, last_air_date_at, episode_metadata_incomplete FROM media_items WHERE content_id = '\''120822227783385091'\'';"'
```

Expected: `last_air_date_at` reflects the newest fallback episode air date that is not in the future, and `episode_metadata_incomplete` is true while fallback rows remain.

---

## Implementation Notes

- `scanner_fallback` rows are intentionally provisional. The real stable identity remains the episode `content_id`; provider adoption preserves that `content_id` while updating the natural key when exact `air_date` provider metadata appears.
- Do not synthesize season `0` date fallback rows.
- Do not infer episode numbers from day-of-year, weekdays, show schedules, or filenames.
- Do not change plugin repos for this fix.
- Do not add a `media_files.air_date` column in this implementation. If later performance profiling shows repeated filename parsing is expensive, add a separate migration-backed optimization.

## Self-Review

- Spec coverage: parser pinning, air-date season disambiguation, date fallback synthesis, same-season provider adoption, cross-season adoption refusal, refresh-debt visibility, and dev verification are covered.
- Placeholder scan: no TBD/TODO placeholders remain.
- Type consistency: new metadata interface methods are named consistently across `service.go`, `episode_repo.go`, and fake repos.
- Scope check: one server-side feature, no plugin or client changes required.
