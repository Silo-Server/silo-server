package markers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

type contributionStoreFixture struct {
	store    *ContributionStore
	pool     *pgxpool.Pool
	provider string
	folderID int
	suffix   int64
	fileIDs  [2]int
}

func newContributionStoreFixture(t *testing.T) contributionStoreFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var migrated bool
	if err := pool.QueryRow(ctx, `
		SELECT to_regclass('public.marker_contributions_provider_target_active_uidx') IS NOT NULL
		   AND EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'marker_contributions'
			  AND column_name = 'target_key'
		)`).Scan(&migrated); err != nil {
		t.Fatalf("check contribution claim migration: %v", err)
	}
	if !migrated {
		t.Skip("marker contribution claim migration has not been applied")
	}

	suffix := time.Now().UnixNano()
	provider := fmt.Sprintf("claim-test-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name)
		VALUES ('shows', $1)
		RETURNING id`, provider).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	var fileIDs [2]int
	for i := range fileIDs {
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path)
			VALUES ($1, $2)
			RETURNING id`, folderID, fmt.Sprintf("/claim-test/%d-%d.mkv", suffix, i)).Scan(&fileIDs[i]); err != nil {
			t.Fatalf("seed media file: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM marker_contributions WHERE provider = $1`, provider)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = ANY($1)`, fileIDs[:])
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	return contributionStoreFixture{
		store:    NewContributionStore(pool),
		pool:     pool,
		provider: provider,
		folderID: folderID,
		suffix:   suffix,
		fileIDs:  fileIDs,
	}
}

func (f contributionStoreFixture) row(fileID int, hash string) ContributionRow {
	start, end, duration := int64(0), int64(60_000), int64(1_800_000)
	return ContributionRow{
		MediaFileID:      fileID,
		Provider:         f.provider,
		SegmentKind:      "intro",
		Source:           "manual",
		SubmittedStartMs: &start,
		SubmittedEndMs:   &end,
		VideoDurationMs:  &duration,
		ContentHash:      hash,
		TargetKey:        "episode|tmdb:1|1|" + hash,
		Status:           contributionStatusClaim,
	}
}

// seedEpisodeFile seeds a series with TMDB 1396, IMDb and TVDB IDs, its
// episode season-episode, and a 30-minute file of it without markers.
func (f contributionStoreFixture) seedEpisodeFile(t *testing.T, name string, season, episode int) *models.MediaFile {
	t.Helper()
	ctx := context.Background()
	seriesID := fmt.Sprintf("claim-test-%s-%d", name, f.suffix)
	file := &models.MediaFile{EpisodeID: seriesID + "-e1", Duration: 1800}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, tmdb_id, imdb_id, tvdb_id)
		VALUES ($1, 'series', $1, 'matched', '{}'::text[], '1396', 'tt0903747', '81189')`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, $3, $4, 'Ep')`, file.EpisodeID, seriesID, season, episode); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, episode_id, duration)
		VALUES ($1, $2, $3, 1800)
		RETURNING id`, f.folderID, fmt.Sprintf("/claim-test/%d-%s.mkv", f.suffix, name), file.EpisodeID).Scan(&file.ID); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, file.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = $1`, seriesID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})
	return file
}

// seedMovieFile seeds a movie with TMDB 1396, IMDb and TVDB IDs and a
// 30-minute file of it without markers.
func (f contributionStoreFixture) seedMovieFile(t *testing.T, name string) *models.MediaFile {
	t.Helper()
	ctx := context.Background()
	file := &models.MediaFile{ContentID: fmt.Sprintf("claim-test-%s-%d", name, f.suffix), Duration: 1800}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, tmdb_id, imdb_id, tvdb_id)
		VALUES ($1, 'movie', $1, 'matched', '{}'::text[], '1396', 'tt0903747', '81189')`, file.ContentID); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, content_id, duration)
		VALUES ($1, $2, $3, 1800)
		RETURNING id`, f.folderID, fmt.Sprintf("/claim-test/%d-%s.mkv", f.suffix, name), file.ContentID).Scan(&file.ID); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, file.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, file.ContentID)
	})
	return file
}

func expireContributionClaim(t *testing.T, fixture contributionStoreFixture, id string) {
	t.Helper()
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE marker_contributions
		SET updated_at = now() - interval '1 hour'
		WHERE id = $1`, id); err != nil {
		t.Fatalf("expire contribution claim: %v", err)
	}
}

func TestContributionStoreAllowsOnlyOneConcurrentStaleTakeover(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := context.Background()
	row := fixture.row(fixture.fileIDs[0], "concurrent-stale")
	first, claimed, err := fixture.store.Claim(ctx, row, contributionClaimLease)
	if err != nil || !claimed {
		t.Fatalf("first Claim = (%+v, %v, %v), want claimed", first, claimed, err)
	}
	expireContributionClaim(t, fixture, first.ID)

	const workers = 12
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	start := make(chan struct{})
	for range workers {
		go func() {
			ready.Done()
			<-start
			_, claimed, err := fixture.store.Claim(ctx, row, contributionClaimLease)
			results <- claimed
			errs <- err
		}()
	}
	ready.Wait()
	close(start)

	claimedCount := 0
	for range workers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Claim: %v", err)
		}
		if <-results {
			claimedCount++
		}
	}
	if claimedCount != 1 {
		t.Fatalf("concurrent stale claims won = %d, want 1", claimedCount)
	}
}

func TestContributionStoreBlocksSameTargetWithDifferentTimes(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := context.Background()
	firstRow := fixture.row(fixture.fileIDs[0], "target-first-payload")
	firstRow.TargetKey = "episode|tmdb:77|2|5"
	first, claimed, err := fixture.store.Claim(ctx, firstRow, contributionClaimLease)
	if err != nil || !claimed {
		t.Fatalf("first Claim = (%+v, %v, %v), want claimed", first, claimed, err)
	}
	firstRow.ID, firstRow.ClaimToken, firstRow.Status = first.ID, first.Token, SubmissionStatusPending
	if err := fixture.store.Record(ctx, firstRow); err != nil {
		t.Fatalf("record pending: %v", err)
	}

	secondRow := fixture.row(fixture.fileIDs[1], "target-second-payload")
	secondRow.TargetKey = firstRow.TargetKey
	if _, claimed, err := fixture.store.Claim(ctx, secondRow, contributionClaimLease); err != nil || claimed {
		t.Fatalf("second payload for the same target = (claimed=%v, %v), want blocked", claimed, err)
	}

	otherTarget := fixture.row(fixture.fileIDs[1], "target-other-payload")
	otherTarget.TargetKey = "episode|tmdb:77|2|6"
	if _, claimed, err := fixture.store.Claim(ctx, otherTarget, contributionClaimLease); err != nil || !claimed {
		t.Fatalf("other target Claim = (claimed=%v, %v), want claimed", claimed, err)
	}
}

// Each provider is checked against its own minimum confidence, required
// external IDs, and claim target, so a file every provider declines is not a
// candidate even though none of them holds a claim on it.
func TestContributionStoreCandidatesApplyEachProvidersRules(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("claim-test-rules-%d", fixture.suffix)
	type seed struct {
		name, tmdb, tvdb string
		confidence       float64
	}
	seeds := []seed{
		{"tvdb-only", "", "81189", 0.98},
		{"both", "1396", "81190", 0.93},
		{"unmatched", "", "", 0.99},
	}
	fileIDs := make([]int, len(seeds))
	for i, sd := range seeds {
		seriesID := prefix + "-" + sd.name
		if _, err := fixture.pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, tmdb_id, tvdb_id)
			VALUES ($1, 'series', 'Rules Series', 'matched', '{}'::text[], $2, $3)`,
			seriesID, sd.tmdb, sd.tvdb); err != nil {
			t.Fatalf("seed series: %v", err)
		}
		if _, err := fixture.pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, 1, 'Ep')`, seriesID+"-e1", seriesID); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
		if err := fixture.pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, episode_id, intro_start, intro_end,
			                         intro_markers_source, intro_markers_confidence)
			VALUES ($1, $2, $3, 0, 60, 'scanner', $4)
			RETURNING id`, fixture.folderID, fmt.Sprintf("/claim-test/%d-rules-%d.mkv", fixture.suffix, i),
			seriesID+"-e1", sd.confidence).Scan(&fileIDs[i]); err != nil {
			t.Fatalf("seed candidate file: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM marker_contributions WHERE media_file_id = ANY($1)`, fileIDs)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM media_files WHERE id = ANY($1)`, fileIDs)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM episodes WHERE series_id LIKE $1`, prefix+"-%")
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"-%")
	})

	needsTMDB := AutoContributor{Provider: fixture.provider, MinConfidence: 0.9, RequiredExternalIDs: []string{ExternalIDKeyTMDB}}
	strictTVDB := AutoContributor{Provider: fixture.provider + "-tvdb", MinConfidence: 0.97, RequiredExternalIDs: []string{ExternalIDKeyTVDB}}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM marker_contributions WHERE provider = $1`, strictTVDB.Provider)
	})
	candidates := func(providers ...AutoContributor) []int {
		t.Helper()
		page, err := fixture.store.CandidateLocalMarkerFiles(ctx, MarkerKindIntro, providers, nil, 10_000)
		if err != nil {
			t.Fatalf("CandidateLocalMarkerFiles: %v", err)
		}
		var got []int
		for _, c := range page {
			if slices.Contains(fileIDs, c.FileID) {
				got = append(got, c.FileID)
			}
		}
		return got
	}

	// The unmatched series has no ID any provider could key it by.
	if got, want := candidates(needsTMDB), []int{fileIDs[1]}; !slices.Equal(got, want) {
		t.Fatalf("candidates for the TMDB provider = %v, want %v (the TVDB-only series lacks a TMDB ID)", got, want)
	}
	if got, want := candidates(strictTVDB), []int{fileIDs[0]}; !slices.Equal(got, want) {
		t.Fatalf("candidates for the strict provider = %v, want %v (0.93 is below its minimum)", got, want)
	}
	if got, want := candidates(needsTMDB, strictTVDB), []int{fileIDs[0], fileIDs[1]}; !slices.Equal(got, want) {
		t.Fatalf("candidates for both providers = %v, want %v", got, want)
	}

	// Each provider claims the file it accepts, on the target keyed by its
	// required ID. Neither file is a candidate afterwards.
	claim := func(provider string, fileID int, targetKey string) {
		t.Helper()
		row := fixture.row(fileID, "rules-"+provider)
		row.Provider, row.TargetKey = provider, targetKey
		if _, ok, err := fixture.store.Claim(ctx, row, contributionClaimLease); err != nil || !ok {
			t.Fatalf("seed claim = (%v, %v)", ok, err)
		}
	}
	claim(needsTMDB.Provider, fileIDs[1], "episode|tmdb:1396|1|1")
	claim(strictTVDB.Provider, fileIDs[0], "episode|tvdb:81189|1|1")
	if got := candidates(needsTMDB, strictTVDB); len(got) != 0 {
		t.Fatalf("candidates after both claims = %v, want none", got)
	}

	// The strict provider keys by TVDB, so a TMDB ID added later leaves its
	// claim current; the TMDB provider can now take the file.
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_items SET tmdb_id = '1399' WHERE content_id = $1`, prefix+"-tvdb-only"); err != nil {
		t.Fatalf("add tmdb id: %v", err)
	}
	if got, want := candidates(strictTVDB), []int(nil); !slices.Equal(got, want) {
		t.Fatalf("candidates for the strict provider after a TMDB ID was added = %v, want none", got)
	}
	if got, want := candidates(needsTMDB, strictTVDB), []int{fileIDs[0]}; !slices.Equal(got, want) {
		t.Fatalf("candidates after a TMDB ID was added = %v, want %v", got, want)
	}
}

// A provider that supports amend is offered a file again when its marker's
// hash changes, here through a new detection version, even past a conflict.
// The unchanged marker stays claimed, and a provider without amend keeps its
// one submission. The candidate query must derive the hash and file target
// exactly as contributeSegment does.
func TestContributionStoreAmendResubmitsChangedMarkers(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := context.Background()
	// 1.005 s is 1004.999… ms in floating point; both sides must truncate.
	file := fixture.seedEpisodeFile(t, "amend", 2, 4)
	file.IntroStart, file.IntroEnd = floatPtr(1.005), floatPtr(72.5)
	file.IntroMarkersSource, file.IntroMarkersConfidence = strPtr(models.MarkerSourceScanner), floatPtr(0.95)
	file.IntroMarkersAlgorithm = strPtr("chapter:silence:v1")
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE media_files SET intro_start = 1.005, intro_end = 72.5, intro_markers_source = 'scanner',
		       intro_markers_confidence = 0.95, intro_markers_algorithm = 'chapter:silence:v1'
		WHERE id = $1`, file.ID); err != nil {
		t.Fatalf("seed intro: %v", err)
	}
	amend := &fakeSubmitter{id: fixture.provider, amend: true, err: &SubmissionConflictError{Provider: fixture.provider, HTTPStatus: 409}}
	once := &fakeSubmitter{id: fixture.provider + "-once"}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM marker_contributions WHERE provider = $1`, once.id)
	})
	reg := NewRegistry(nil)
	_ = reg.Register(amend)
	_ = reg.Register(once)
	cfg := fakeConfig{}
	for _, id := range []string{amend.id, once.id} {
		cfg[id] = ProviderConfig{Provider: id, ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9}
	}
	svc := NewContributionService(reg, NewDBExternalIDResolver(fixture.pool), cfg, fixture.store, nil)
	contributors, err := svc.AutoContributors(ctx)
	if err != nil {
		t.Fatalf("AutoContributors: %v", err)
	}
	if len(contributors) != 2 || contributors[0].Provider != amend.id {
		t.Fatalf("AutoContributors = %+v, want the amend provider, then the other", contributors)
	}
	candidate := func(provider string) bool {
		t.Helper()
		c := contributors[slices.IndexFunc(contributors, func(c AutoContributor) bool { return c.Provider == provider })]
		page, err := fixture.store.CandidateLocalMarkerFiles(ctx, MarkerKindIntro, []AutoContributor{c}, nil, 10_000)
		if err != nil {
			t.Fatalf("CandidateLocalMarkerFiles: %v", err)
		}
		return slices.ContainsFunc(page, func(c ContributionCandidate) bool { return c.FileID == file.ID })
	}
	contribute := func() {
		t.Helper()
		if _, err := svc.ContributeFile(ctx, file, ContributeOptions{Auto: true}); err != nil {
			t.Fatalf("ContributeFile: %v", err)
		}
	}

	if !candidate(amend.id) || !candidate(once.id) {
		t.Fatal("the unsubmitted file is not a candidate for both providers")
	}
	contribute()
	contribute()
	if len(amend.submitted) != 1 || len(once.submitted) != 1 {
		t.Fatalf("submissions after two runs: amend %d, once %d; want one each", len(amend.submitted), len(once.submitted))
	}
	if candidate(amend.id) || candidate(once.id) {
		t.Fatal("an unchanged marker is still a candidate after both providers took it")
	}

	stale := *file
	file.IntroMarkersAlgorithm = strPtr("chapter:silence:v2")
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_files SET intro_markers_algorithm = $2 WHERE id = $1`, file.ID, *file.IntroMarkersAlgorithm); err != nil {
		t.Fatalf("bump algorithm: %v", err)
	}
	if !candidate(amend.id) || candidate(once.id) {
		t.Fatal("after a new detection version, want a candidate for the amend provider only")
	}
	// A payload with the same times but a version the file no longer holds,
	// as from a run that read the file earlier, must not take the target.
	stale.IntroMarkersAlgorithm = strPtr("chapter:silence:v0")
	outcomes, err := svc.ContributeFile(ctx, &stale, ContributeOptions{Auto: true, Provider: amend.id})
	if err != nil || len(outcomes) != 1 || outcomes[0].Reason != contributionSupersededReason || len(amend.submitted) != 1 {
		t.Fatalf("stale detection version: outcomes %+v, err %v; want a superseded skip", outcomes, err)
	}
	contribute()
	if len(amend.submitted) != 2 || len(once.submitted) != 1 {
		t.Fatalf("submissions after the change: amend %d, once %d; want 2 and 1", len(amend.submitted), len(once.submitted))
	}
	if candidate(amend.id) {
		t.Fatal("the amended marker is still a candidate")
	}

	var active int
	var targetKey string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE claim_active), max(target_key) FILTER (WHERE claim_active)
		FROM marker_contributions WHERE provider = $1`, amend.id).Scan(&active, &targetKey); err != nil {
		t.Fatalf("read amend claims: %v", err)
	}
	if want := fmt.Sprintf("episode|tmdb:1396|2|4|file:%d", file.ID); active != 1 || targetKey != want {
		t.Fatalf("active amend claims = %d on %q, want 1 on %q", active, targetKey, want)
	}
}

// manualCorrections drives one amend provider, with automatic sharing off,
// and a seeded episode or movie file whose marker of kind a test saves by
// hand.
type manualCorrections struct {
	t        *testing.T
	fixture  contributionStoreFixture
	sub      *fakeSubmitter
	svc      *ContributionService
	resolver *DBExternalIDResolver
	kind     MarkerKind
	file     models.MediaFile // identity only: ID, EpisodeID or ContentID
	item     string           // the series or movie whose IDs the file resolves to
}

func newManualCorrections(t *testing.T, kind MarkerKind, movie bool) *manualCorrections {
	fixture := newContributionStoreFixture(t)
	var file *models.MediaFile
	var item string
	if movie {
		file = fixture.seedMovieFile(t, "manual")
		item = file.ContentID
	} else {
		file = fixture.seedEpisodeFile(t, "manual", 1, 3)
		item = fmt.Sprintf("claim-test-manual-%d", fixture.suffix)
	}
	sub := &fakeSubmitter{id: fixture.provider, amend: true}
	reg := NewRegistry(nil)
	_ = reg.Register(sub)
	cfg := fakeConfig{sub.id: {Provider: sub.id, ContributeEnabled: true}}
	resolver := NewDBExternalIDResolver(fixture.pool)
	svc := NewContributionService(reg, resolver, cfg, fixture.store, nil)
	return &manualCorrections{t: t, fixture: fixture, sub: sub, svc: svc, resolver: resolver, kind: kind,
		file: models.MediaFile{ID: file.ID, EpisodeID: file.EpisodeID, ContentID: file.ContentID}, item: item}
}

// ids resolves the file's external IDs as they are now.
func (m *manualCorrections) ids() ExternalIDs {
	m.t.Helper()
	ids, err := m.resolver.ResolveForFile(context.Background(), &m.file)
	if err != nil || !ids.HasAnyID() {
		m.t.Fatalf("resolve the file's IDs: %+v, %v", ids, err)
	}
	return ids
}

func (m *manualCorrections) setDuration(seconds int) {
	m.t.Helper()
	if _, err := m.fixture.pool.Exec(context.Background(), `UPDATE media_files SET duration = $2 WHERE id = $1`, m.file.ID, seconds); err != nil {
		m.t.Fatalf("set the file's duration: %v", err)
	}
}

// exec runs a statement whose first parameter is the file's item.
func (m *manualCorrections) exec(query string) {
	m.t.Helper()
	if _, err := m.fixture.pool.Exec(context.Background(), query, m.item); err != nil {
		m.t.Fatalf("%s: %v", query, err)
	}
}

// save stores a manual marker from start to 60 s with algorithm and returns
// the file read then.
func (m *manualCorrections) save(start float64, algorithm string) *models.MediaFile {
	m.t.Helper()
	name := markerKindName(m.kind)
	if _, err := m.fixture.pool.Exec(context.Background(), fmt.Sprintf(`
		UPDATE media_files SET %[1]s_start = $2, %[1]s_end = 60, %[1]s_markers_source = 'manual',
		       %[1]s_markers_confidence = 1, %[1]s_markers_algorithm = $3
		WHERE id = $1`, name), m.file.ID, start, algorithm); err != nil {
		m.t.Fatalf("save %s: %v", name, err)
	}
	file := m.file
	file.Duration = 1800
	source := models.MarkerSourceManual
	for _, seg := range []struct {
		kind              MarkerKind
		start, end        **float64
		source, algorithm **string
		confidence        **float64
	}{
		{MarkerKindIntro, &file.IntroStart, &file.IntroEnd, &file.IntroMarkersSource, &file.IntroMarkersAlgorithm, &file.IntroMarkersConfidence},
		{MarkerKindRecap, &file.RecapStart, &file.RecapEnd, &file.RecapMarkersSource, &file.RecapMarkersAlgorithm, &file.RecapMarkersConfidence},
	} {
		if seg.kind == m.kind {
			*seg.start, *seg.end, *seg.source, *seg.algorithm, *seg.confidence = floatPtr(start), floatPtr(60), &source, &algorithm, floatPtr(1)
		}
	}
	return &file
}

func (m *manualCorrections) candidate() bool {
	m.t.Helper()
	amenders, err := m.svc.AmendContributors(context.Background())
	if err != nil {
		m.t.Fatalf("AmendContributors: %v", err)
	}
	page, err := m.fixture.store.CandidateManualCorrections(context.Background(), amenders, nil, 10_000)
	if err != nil {
		m.t.Fatalf("CandidateManualCorrections: %v", err)
	}
	return slices.ContainsFunc(page, func(c ContributionCandidate) bool { return c.FileID == m.file.ID })
}

// contribute runs file with opts and returns the outcome, or a zero one when
// not attempted.
func (m *manualCorrections) contribute(file *models.MediaFile, opts ContributeOptions) ContributionOutcome {
	m.t.Helper()
	outcomes, err := m.svc.ContributeFile(context.Background(), file, opts)
	if err != nil || len(outcomes) > 1 {
		m.t.Fatalf("ContributeFile: outcomes %+v, err %v; want at most one", outcomes, err)
	}
	if len(outcomes) == 0 {
		return ContributionOutcome{}
	}
	return outcomes[0]
}

func (m *manualCorrections) submitted(want ...float64) {
	m.t.Helper()
	var got []float64
	for _, req := range m.sub.submitted {
		got = append(got, req.Start.Seconds())
	}
	if !slices.Equal(got, want) {
		m.t.Fatalf("submitted starts %v, want %v", got, want)
	}
}

// sendsTheFilesCurrentState checks, after the file's correction at 4 s was
// sent, that a run that read the file before its duration or IDs changed
// cannot claim, that new IDs send the correction again, and that a claim left
// on a target the IDs no longer name does not hold a restored correction.
func (m *manualCorrections) sendsTheFilesCurrentState(save, daily ContributeOptions) {
	m.t.Helper()
	// A run that read the file before a re-probe changed its duration
	// cannot claim; the file's current state is sent instead.
	held := m.save(5, ManualAlgorithm)
	m.setDuration(1900)
	if got := m.contribute(held, save); got.Reason != contributionSupersededReason {
		m.t.Fatalf("correction read before a duration change: %+v, want a superseded skip", got)
	}
	m.setDuration(1800)
	m.contribute(held, daily)
	m.submitted(2, 3, 4, 5)

	// New IDs send the correction again, and a payload built from the
	// earlier IDs can no longer take the target.
	before := m.ids()
	m.exec(`UPDATE media_items SET imdb_id = 'tt9999999' WHERE content_id = $1`)
	if !m.candidate() {
		m.t.Fatal("a correction is not offered after the item's IDs changed")
	}
	m.contribute(held, daily)
	m.submitted(2, 3, 4, 5, 5)
	seg := fileSegments(held)[slices.IndexFunc(fileSegments(held), func(s segmentData) bool { return s.kind == m.kind })]
	if _, _, err := m.fixture.store.Claim(context.Background(), newContributionRow(m.sub, held, before, seg, models.MarkerSourceManual), time.Minute); !errors.Is(err, errContributionSuperseded) {
		m.t.Fatalf("claim with the earlier IDs: %v, want errContributionSuperseded", err)
	}

	// A new TMDB ID names a new target. The claim left on the old one
	// matches a restored correction by value but does not hold it.
	m.exec(`UPDATE media_items SET tmdb_id = '1397' WHERE content_id = $1`)
	m.contribute(held, daily)
	m.contribute(m.save(6, ManualAlgorithm), save)
	restored := m.save(5, ManualAlgorithm)
	if !m.candidate() {
		m.t.Fatal("a claim on the item's old target holds back the restored correction")
	}
	m.contribute(restored, daily)
	m.submitted(2, 3, 4, 5, 5, 5, 6, 5)
}

// An amend provider that holds an earlier submission for a file gets the
// file's manual correction of any kind from the daily task when the save's
// own attempt did not send it, whichever server saved it and with automatic
// sharing off: one skipped behind another submission in flight once that one
// settles, one whose submit was rate limited, and one whose server stopped
// before claiming. A manual marker the provider never had anything for is not
// offered, and an older correction, or one read before the file's duration or
// IDs changed, never replaces a newer one. A change to the IDs sends the
// correction again, and a claim under IDs the item no longer has does not
// count as holding it. A save that kept a provider's marker is never offered
// nor sent by any path, even after the item's IDs change.
func TestContributionStoreAmendSendsAnUnsentManualCorrection(t *testing.T) {
	daily := ContributeOptions{Corrections: true}
	for _, tc := range []struct {
		name  string
		kind  MarkerKind
		movie bool
	}{
		{"episode intro", MarkerKindIntro, false},
		{"episode recap", MarkerKindRecap, false},
		{"movie intro", MarkerKindIntro, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.kind
			m := newManualCorrections(t, kind, tc.movie)
			ctx := context.Background()
			save := ContributeOptions{Segments: []MarkerKind{kind}}

			// Nothing was ever submitted for the file, so nothing is corrected.
			if first := m.save(1, ManualAlgorithm); m.candidate() || m.contribute(first, daily).Provider != "" {
				t.Fatal("a manual marker is offered to a provider that holds nothing for the file")
			}

			// Behind a submission in flight: skipped now, offered once it settles.
			earlier := ContributionRow{
				MediaFileID: m.file.ID, Provider: m.sub.id, SegmentKind: markerKindName(kind), Source: models.MarkerSourceScanner,
				SubmittedStartMs: new(int64(9000)), SubmittedEndMs: new(int64(60000)),
				ContentHash: "earlier", TargetKey: fmt.Sprintf("%s|file:%d", contributionTargetKey(m.ids(), nil), m.file.ID),
				Status: contributionStatusClaim,
			}
			claim, claimed, err := m.fixture.store.Claim(ctx, earlier, time.Minute)
			if err != nil || !claimed {
				t.Fatalf("claim the earlier submission: claimed=%v err=%v", claimed, err)
			}
			older := m.save(2, ManualAlgorithm)
			if got := m.contribute(older, save); got.Reason != contributionInFlightReason {
				t.Fatalf("correction behind a submission in flight: %+v, want an in-flight skip", got)
			}
			if m.candidate() {
				t.Fatal("a correction is offered while the earlier submission is still in flight")
			}
			earlier.ID, earlier.ClaimToken, earlier.Status = claim.ID, claim.Token, SubmissionStatusPending
			if err := m.fixture.store.Record(ctx, earlier); err != nil {
				t.Fatalf("settle the earlier submission: %v", err)
			}
			if !m.candidate() {
				t.Fatal("the correction is not offered after the earlier submission settled")
			}
			m.contribute(older, daily)
			m.submitted(2)
			if m.candidate() {
				t.Fatal("the sent correction is still a candidate")
			}

			// A rate-limited submit is offered again; so is a newer save whose
			// server stopped before claiming, and the older payload can no
			// longer take the target.
			m.sub.err = &RetryAfterError{Provider: m.sub.id, RetryAfter: time.Minute, Message: "usage limited"}
			limited := m.save(3, ManualAlgorithm)
			if got := m.contribute(limited, save); got.Status != OutcomeStatusRateLimited {
				t.Fatalf("rate-limited correction: %+v", got)
			}
			m.sub.err = nil
			if !m.candidate() {
				t.Fatal("a correction whose submit was rate limited is not offered")
			}
			newer := m.save(4, ManualAlgorithm)
			if !m.candidate() {
				t.Fatal("a correction never attempted after its save is not offered")
			}
			if got := m.contribute(limited, save); got.Reason != contributionSupersededReason {
				t.Fatalf("older correction after a newer save: %+v, want a superseded skip", got)
			}
			m.contribute(newer, daily)
			m.submitted(2, 3, 4) // 3 was the attempt the rate limit refused

			m.sendsTheFilesCurrentState(save, daily)

			// A save that kept a provider's marker is withheld from every path,
			// and a change to the series' IDs does not release it.
			withheld := m.save(5, ManualWithheldAlgorithm)
			for range 2 {
				if m.candidate() {
					t.Fatal("a withheld save is offered as a correction")
				}
				for _, opts := range []ContributeOptions{save, daily, {Auto: true}, {Provider: m.sub.id}} {
					if got := m.contribute(withheld, opts); got.Provider != "" {
						t.Fatalf("a withheld save was attempted with %+v: %+v", opts, got)
					}
				}
				m.exec(`UPDATE media_items SET imdb_id = 'tt7777777' WHERE content_id = $1`)
			}
			m.submitted(2, 3, 4, 5, 5, 5, 6, 5)
		})
	}
}
