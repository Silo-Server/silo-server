package markers

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

func TestContributionStoreRecoversStaleClaimAcrossFiles(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := context.Background()
	firstRow := fixture.row(fixture.fileIDs[0], "cross-file-stale")
	first, claimed, err := fixture.store.Claim(ctx, firstRow, contributionClaimLease)
	if err != nil || !claimed {
		t.Fatalf("first Claim = (%+v, %v, %v), want claimed", first, claimed, err)
	}

	secondRow := fixture.row(fixture.fileIDs[1], firstRow.ContentHash)
	if _, claimed, err := fixture.store.Claim(ctx, secondRow, contributionClaimLease); err != nil || claimed {
		t.Fatalf("fresh duplicate Claim = (claimed=%v, %v), want not claimed", claimed, err)
	}

	expireContributionClaim(t, fixture, first.ID)
	second, claimed, err := fixture.store.Claim(ctx, secondRow, contributionClaimLease)
	if err != nil || !claimed {
		t.Fatalf("stale cross-file Claim = (%+v, %v, %v), want claimed", second, claimed, err)
	}
	if second.ID == first.ID || second.Token == first.Token {
		t.Fatalf("reclaimed claim = %+v, want a new row for the second file with a fresh token", second)
	}

	staleResult := firstRow
	staleResult.ID, staleResult.ClaimToken = first.ID, first.Token
	staleResult.Status = OutcomeStatusError
	if err := fixture.store.Record(ctx, staleResult); err == nil {
		t.Fatal("stale worker recorded over reclaimed claim")
	}

	terminal := secondRow
	terminal.ID, terminal.ClaimToken = second.ID, second.Token
	terminal.Status = OutcomeStatusConflict
	if err := fixture.store.Record(ctx, terminal); err != nil {
		t.Fatalf("record current claim: %v", err)
	}
	expireContributionClaim(t, fixture, second.ID)
	if _, claimed, err := fixture.store.Claim(ctx, firstRow, contributionClaimLease); err != nil || claimed {
		t.Fatalf("terminal Claim = (claimed=%v, %v), want permanently blocked", claimed, err)
	}
}

func TestContributionStoreRecoversStaleClaimForSameFile(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := context.Background()
	row := fixture.row(fixture.fileIDs[0], "same-file-stale")
	first, claimed, err := fixture.store.Claim(ctx, row, contributionClaimLease)
	if err != nil || !claimed {
		t.Fatalf("first Claim = (%+v, %v, %v), want claimed", first, claimed, err)
	}
	expireContributionClaim(t, fixture, first.ID)
	second, claimed, err := fixture.store.Claim(ctx, row, contributionClaimLease)
	if err != nil || !claimed || second.ID != first.ID || second.Token == first.Token {
		t.Fatalf("same-file stale Claim = (%+v, %v, %v), want reclaimed row with fresh token", second, claimed, err)
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

func TestContributionStoreRechecksInvalidTargetAfterWindow(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := context.Background()
	row := fixture.row(fixture.fileIDs[0], "invalid-recheck")
	first, claimed, err := fixture.store.Claim(ctx, row, contributionClaimLease)
	if err != nil || !claimed {
		t.Fatalf("first Claim = (%+v, %v, %v), want claimed", first, claimed, err)
	}
	recorded := row
	recorded.ID, recorded.ClaimToken, recorded.Status = first.ID, first.Token, OutcomeStatusInvalid
	if err := fixture.store.Record(ctx, recorded); err != nil {
		t.Fatalf("record invalid: %v", err)
	}
	if _, claimed, err := fixture.store.Claim(ctx, row, contributionClaimLease); err != nil || claimed {
		t.Fatalf("fresh invalid target Claim = (claimed=%v, %v), want blocked", claimed, err)
	}

	if _, err := fixture.pool.Exec(ctx, `
		UPDATE marker_contributions SET updated_at = now() - interval '31 days' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("age invalid row: %v", err)
	}
	second, claimed, err := fixture.store.Claim(ctx, row, contributionClaimLease)
	if err != nil || !claimed || second.Token == first.Token {
		t.Fatalf("aged invalid target Claim = (%+v, %v, %v), want reclaimed with a fresh token", second, claimed, err)
	}
}

func TestContributionStoreCandidatesOrderByConfidenceAndSkipClaimed(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := context.Background()
	seriesID := fmt.Sprintf("claim-test-series-%d", fixture.suffix)
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, tmdb_id)
		VALUES ($1, 'series', 'Claim Test Series', 'matched', '{}'::text[], '4242')`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})

	type seed struct {
		season, episode int
		confidence      float64
	}
	// The last seed is a second version of S1E4.
	seeds := []seed{{1, 1, 0.95}, {1, 2, 0.98}, {1, 3, 0.96}, {1, 4, 0.99}, {0, 1, 0.99}, {1, 5, 0.50}, {1, 6, 0.97}, {1, 7, 0.975}, {1, 4, 0.985}}
	fileIDs := make([]int, len(seeds))
	for i, sd := range seeds {
		episodeID := fmt.Sprintf("%s-e%d-%d", seriesID, sd.season, sd.episode)
		if _, err := fixture.pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, $3, $4, 'Ep')
			ON CONFLICT DO NOTHING`, episodeID, seriesID, sd.season, sd.episode); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
		if err := fixture.pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, episode_id, intro_start, intro_end,
			                         intro_markers_source, intro_markers_confidence)
			VALUES ($1, $2, $3, 0, 60, 'scanner', $4)
			RETURNING id`, fixture.folderID, fmt.Sprintf("/claim-test/%d-cand-%d.mkv", fixture.suffix, i),
			episodeID, sd.confidence).Scan(&fileIDs[i]); err != nil {
			t.Fatalf("seed candidate file: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM marker_contributions WHERE media_file_id = ANY($1)`, fileIDs)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM media_files WHERE id = ANY($1)`, fileIDs)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = $1`, seriesID)
	})

	claim := func(fileID int, hash, targetKey string, status string) string {
		t.Helper()
		row := fixture.row(fileID, hash)
		row.TargetKey = targetKey
		claimed, ok, err := fixture.store.Claim(ctx, row, contributionClaimLease)
		if err != nil || !ok {
			t.Fatalf("seed claim = (%v, %v)", ok, err)
		}
		row.ID, row.ClaimToken, row.Status = claimed.ID, claimed.Token, status
		if err := fixture.store.Record(ctx, row); err != nil {
			t.Fatalf("record seed claim: %v", err)
		}
		return claimed.ID
	}
	// S1E4 (0.99) holds a pending claim on its current target: it and the
	// episode's second version (0.985) are skipped.
	claim(fileIDs[3], "candidate-claimed", "episode|tmdb:4242|1|4", SubmissionStatusPending)
	// S1E6 (0.97) holds a claim on the target it had before a rematch: eligible.
	claim(fileIDs[6], "candidate-rematched", "episode|tmdb:1111|1|6", SubmissionStatusPending)
	// S1E7 (0.975) was refused as invalid long enough ago to recheck: eligible.
	invalidID := claim(fileIDs[7], "candidate-invalid", "episode|tmdb:4242|1|7", OutcomeStatusInvalid)
	if _, err := fixture.pool.Exec(ctx, `UPDATE marker_contributions SET updated_at = now() - interval '31 days' WHERE id = $1`, invalidID); err != nil {
		t.Fatalf("age invalid claim: %v", err)
	}

	seeded := make(map[int]bool, len(fileIDs))
	for _, id := range fileIDs {
		seeded[id] = true
	}
	// candidates pages through every candidate and keeps the seeded files, so
	// other eligible rows in the test database cannot change the result.
	candidates := func(providers []string, pageSize int) []int {
		t.Helper()
		var got []int
		var after *ContributionCandidate
		for {
			page, err := fixture.store.CandidateLocalIntroFiles(ctx, 0.9, providers, after, pageSize)
			if err != nil {
				t.Fatalf("CandidateLocalIntroFiles: %v", err)
			}
			if len(page) == 0 {
				return got
			}
			for _, c := range page {
				if seeded[c.FileID] {
					got = append(got, c.FileID)
				}
			}
			after = &page[len(page)-1]
		}
	}

	got := candidates([]string{fixture.provider}, 1)
	want := []int{fileIDs[1], fileIDs[7], fileIDs[6], fileIDs[2], fileIDs[0]}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("candidates = %v, want %v (confidence order; current-target claim, season 0, and low confidence excluded)", got, want)
	}

	// Another provider without a claim still sees the claimed file.
	other := candidates([]string{fixture.provider, fixture.provider + "-other"}, 10)
	if len(other) != 7 || other[0] != fileIDs[3] {
		t.Fatalf("candidates for two providers = %v, want the claimed file first", other)
	}
}
