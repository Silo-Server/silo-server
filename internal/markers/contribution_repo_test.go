package markers

import (
	"context"
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
	seriesID := fmt.Sprintf("claim-test-amend-%d", fixture.suffix)
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, tmdb_id, imdb_id, tvdb_id)
		VALUES ($1, 'series', 'Amend Series', 'matched', '{}'::text[], '1396', 'tt0903747', '81189')`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, 2, 4, 'Ep')`, seriesID+"-e1", seriesID); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	// 1.005 s is 1004.999… ms in floating point; both sides must truncate.
	file := &models.MediaFile{
		EpisodeID: seriesID + "-e1", Duration: 1800,
		IntroStart: floatPtr(1.005), IntroEnd: floatPtr(72.5),
		IntroMarkersSource: strPtr(models.MarkerSourceScanner), IntroMarkersConfidence: floatPtr(0.95),
		IntroMarkersAlgorithm: strPtr("chapter:silence:v1"),
	}
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, episode_id, duration, intro_start, intro_end,
		                         intro_markers_source, intro_markers_confidence, intro_markers_algorithm)
		VALUES ($1, $2, $3, 1800, 1.005, 72.5, 'scanner', 0.95, 'chapter:silence:v1')
		RETURNING id`, fixture.folderID, fmt.Sprintf("/claim-test/%d-amend.mkv", fixture.suffix),
		file.EpisodeID).Scan(&file.ID); err != nil {
		t.Fatalf("seed candidate file: %v", err)
	}
	amend := &fakeSubmitter{id: fixture.provider, amend: true, err: &SubmissionConflictError{Provider: fixture.provider, HTTPStatus: 409}}
	once := &fakeSubmitter{id: fixture.provider + "-once"}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM marker_contributions WHERE provider = $1`, once.id)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, file.ID)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = $1`, seriesID)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
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

	file.IntroMarkersAlgorithm = strPtr("chapter:silence:v2")
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_files SET intro_markers_algorithm = $2 WHERE id = $1`, file.ID, *file.IntroMarkersAlgorithm); err != nil {
		t.Fatalf("bump algorithm: %v", err)
	}
	if !candidate(amend.id) || candidate(once.id) {
		t.Fatal("after a new detection version, want a candidate for the amend provider only")
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
