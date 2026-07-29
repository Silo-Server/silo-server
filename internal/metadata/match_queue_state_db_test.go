package metadata

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	scannerrepo "github.com/Silo-Server/silo-server/internal/scanner"
)

const queueStatePending = "pending"

func TestBoundedMatchFailureMessageRedactsAndLimits(t *testing.T) {
	t.Parallel()
	message := "provider failed?api_key=secret&token=also-secret " + strings.Repeat("x", 1200)
	got := boundedMatchFailureMessage(message)
	if strings.Contains(got, "secret") {
		t.Fatalf("failure message leaked a secret: %q", got)
	}
	if len([]rune(got)) != 1000 {
		t.Fatalf("failure message length = %d, want 1000", len([]rune(got)))
	}
}

func TestBoundedMatchFailureMessageRedactsHeaderAndBearerForms(t *testing.T) {
	t.Parallel()
	got := boundedMatchFailureMessage("Authorization: Bearer secret-token; password: hunter2")
	for _, secret := range []string{"secret-token", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Fatalf("failure message leaked %q: %q", secret, got)
		}
	}
}

func TestBoundedMatchDecisionLimitsProviderControlledFields(t *testing.T) {
	t.Parallel()
	decision := &MatchDecision{Outcome: MatchOutcome(strings.Repeat("x", 100)), CandidateCount: 99, Threshold: 55}
	for i := 0; i < 5; i++ {
		candidate := MatchDecisionCandidate{
			Title: strings.Repeat("t", 400), MatchedTitle: strings.Repeat("m", 400),
			ProviderIDs: make(map[string]string), Score: 42,
			Sources: make([]string, 12), Reasons: make([]string, 12),
		}
		for j := 0; j < 12; j++ {
			candidate.ProviderIDs[fmt.Sprintf("provider-%02d", j)] = strings.Repeat("i", 400)
			candidate.Sources[j] = strings.Repeat("s", 100)
			candidate.Reasons[j] = strings.Repeat("r", 200)
		}
		candidate.ProviderIDs["api_key"] = "must-not-persist"
		decision.TopCandidates = append(decision.TopCandidates, candidate)
	}

	got := boundedMatchDecision(decision)
	if len(got.TopCandidates) != 3 || len(got.TopCandidates[0].ProviderIDs) != 8 || len(got.TopCandidates[0].Sources) != 8 || len(got.TopCandidates[0].Reasons) != 8 {
		t.Fatalf("bounded decision sizes = candidates:%d ids:%d sources:%d reasons:%d", len(got.TopCandidates), len(got.TopCandidates[0].ProviderIDs), len(got.TopCandidates[0].Sources), len(got.TopCandidates[0].Reasons))
	}
	if len([]rune(got.TopCandidates[0].Title)) != 256 || len([]rune(got.TopCandidates[0].MatchedTitle)) != 256 {
		t.Fatalf("bounded title lengths = %d/%d", len([]rune(got.TopCandidates[0].Title)), len([]rune(got.TopCandidates[0].MatchedTitle)))
	}
	if _, exists := got.TopCandidates[0].ProviderIDs["api_key"]; exists {
		t.Fatal("bounded decision retained a credential-shaped provider ID")
	}
}

func TestNormalizeMatchFailureKindTreatsUnknownAsTransient(t *testing.T) {
	t.Parallel()
	if got := normalizeMatchFailureKind(MatchOutcomeCandidateRejected); got != MatchOutcomeCandidateRejected {
		t.Fatalf("known failure kind = %q", got)
	}
	if got := normalizeMatchFailureKind(MatchOutcome("unexpected-" + strings.Repeat("x", 500))); got != MatchOutcomeProviderTransient {
		t.Fatalf("unknown failure kind = %q, want provider_transient", got)
	}
}

func TestMatchQueueFingerprintIncludesMatcherAndProviderConfiguration(t *testing.T) {
	t.Parallel()
	expression := matchQueueInputFingerprintSQL("mf.file_path", "'movie'", "mf.media_folder_id", "folders.metadata_language", movieMatcherRevision)
	for _, required := range []string{
		"mf.file_path", "'movie'", "folders.metadata_language", "installation.version",
		"chain.priority", "chain.capability_id", "plugin_runtime_configs", "config.updated_at::text",
		fmt.Sprintf("|%d|", movieMatcherRevision),
	} {
		if !strings.Contains(expression, required) {
			t.Fatalf("fingerprint expression %q does not contain %q", expression, required)
		}
	}
}

func TestSeriesMatchQueueFingerprintIncludesEpisodePathShape(t *testing.T) {
	t.Parallel()
	expression := seriesMatchQueueInputFingerprintSQL("q.observed_root_path", "q.media_folder_id", "folders.metadata_language")
	for _, required := range []string{"shape_file.file_path", "shape_file.observed_root_path", "shape_file.missing_since", "shape_file.extra_id", fmt.Sprintf("|%d|", seriesMatcherRevision)} {
		if !strings.Contains(expression, required) {
			t.Fatalf("series fingerprint expression %q does not contain %q", expression, required)
		}
	}
	if movieMatcherRevision != 10 {
		t.Fatalf("movie matcher revision = %d, want shared title-normalization revision 10", movieMatcherRevision)
	}
	if seriesMatcherRevision != 10 {
		t.Fatalf("series matcher revision = %d, want consensus revision 10", seriesMatcherRevision)
	}
}

func TestMatchQueueSharedTitleRevisionWakesMoviesAndSeries(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	previousMatcherRevision := movieMatcherRevision - 1

	movieFolderID := insertTestFolder(t, pool, "movie")
	moviePath := fmt.Sprintf("/test/revision-isolation-%d/Movie.mkv", time.Now().UnixNano())
	var movieFileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, base_type, file_size)
		VALUES ($1, $2, 'movie', 0) RETURNING id
	`, movieFolderID, moviePath).Scan(&movieFileID); err != nil {
		t.Fatalf("seed movie file: %v", err)
	}
	movieRepo := NewMovieMatchQueueRepository(pool, scannerrepo.NewFileRepository(pool))
	if err := movieRepo.EnqueueMovieFile(ctx, movieFileID); err != nil {
		t.Fatalf("EnqueueMovieFile(): %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE movie_match_queue
		SET state = 'parked', available_at = NOW() + interval '24 hours', parked_at = NOW(),
			matcher_revision = $2
		WHERE media_file_id = $1
	`, movieFileID, previousMatcherRevision); err != nil {
		t.Fatalf("park movie row: %v", err)
	}

	seriesFolderID := insertTestFolder(t, pool, "series")
	seriesRoot := fmt.Sprintf("/test/revision-isolation-%d/Series", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (
			media_folder_id, file_path, observed_root_path, base_type,
			season_number, episode_number, file_size
		) VALUES ($1, $2, $3, 'series', 1, 1, 0)
	`, seriesFolderID, seriesRoot+"/Season 01/Series S01E01.mkv", seriesRoot); err != nil {
		t.Fatalf("seed series file: %v", err)
	}
	seriesRepo := NewSeriesRootMatchQueueRepository(pool)
	if err := seriesRepo.EnqueueSeriesRoot(ctx, seriesFolderID, seriesRoot); err != nil {
		t.Fatalf("EnqueueSeriesRoot(): %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE series_root_match_queue
		SET state = 'parked', available_at = NOW() + interval '24 hours', parked_at = NOW(),
			matcher_revision = $3
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, seriesFolderID, seriesRoot, previousMatcherRevision); err != nil {
		t.Fatalf("seed pre-change series revision: %v", err)
	}

	if _, err := movieRepo.WakeForChangedInputs(ctx); err != nil {
		t.Fatalf("movie WakeForChangedInputs(): %v", err)
	}
	var movieState string
	var movieRevision int
	if err := pool.QueryRow(ctx, `
		SELECT state, matcher_revision FROM movie_match_queue WHERE media_file_id = $1
	`, movieFileID).Scan(&movieState, &movieRevision); err != nil {
		t.Fatalf("load movie queue row: %v", err)
	}
	if movieState != queueStatePending || movieRevision != movieMatcherRevision {
		t.Fatalf("movie row was not awakened: state=%q revision=%d", movieState, movieRevision)
	}

	if _, err := seriesRepo.WakeForChangedInputs(ctx); err != nil {
		t.Fatalf("series WakeForChangedInputs(): %v", err)
	}
	var seriesState string
	var seriesRevision int
	if err := pool.QueryRow(ctx, `
		SELECT state, matcher_revision FROM series_root_match_queue
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, seriesFolderID, seriesRoot).Scan(&seriesState, &seriesRevision); err != nil {
		t.Fatalf("load series queue row: %v", err)
	}
	if seriesState != queueStatePending || seriesRevision != seriesMatcherRevision {
		t.Fatalf("series row was not awakened: state=%q revision=%d", seriesState, seriesRevision)
	}
}

func TestSeriesMatchQueueDeterministicFailuresParkAndRetryNowResets(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	folderID := insertTestFolder(t, pool, "series")
	root := fmt.Sprintf("/test/match-queue-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO series_root_match_queue (media_folder_id, observed_root_path, available_at)
		VALUES ($1, $2, NOW())`, folderID, root); err != nil {
		t.Fatalf("seed series match queue: %v", err)
	}

	repo := NewSeriesRootMatchQueueRepository(pool)
	for attempt, wantDelay := range []time.Duration{time.Hour, 24 * time.Hour, 24 * time.Hour} {
		leaseToken := fmt.Sprintf("deterministic-lease-%d", attempt)
		if _, err := pool.Exec(ctx, `UPDATE series_root_match_queue SET lease_token = $3 WHERE media_folder_id = $1 AND observed_root_path = $2`, folderID, root, leaseToken); err != nil {
			t.Fatalf("seed lease token: %v", err)
		}
		before := time.Now()
		if err := repo.UpdateFailure(ctx, folderID, root, leaseToken, MatchFailure{Kind: MatchOutcomeCandidateRejected, Message: "score below threshold"}); err != nil {
			t.Fatalf("UpdateFailure(%d): %v", attempt+1, err)
		}
		var state string
		var deterministicCount int
		var availableAt time.Time
		var parkedAt *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT state, deterministic_attempt_count, available_at, parked_at
			FROM series_root_match_queue WHERE media_folder_id = $1 AND observed_root_path = $2`,
			folderID, root).Scan(&state, &deterministicCount, &availableAt, &parkedAt); err != nil {
			t.Fatalf("load queue state: %v", err)
		}
		if deterministicCount != attempt+1 {
			t.Fatalf("deterministic count = %d, want %d", deterministicCount, attempt+1)
		}
		wantState := queueStatePending
		if attempt == 2 {
			wantState = "parked"
		}
		if state != wantState {
			t.Fatalf("state = %q, want %q", state, wantState)
		}
		if attempt == 2 && parkedAt == nil {
			t.Fatal("parked_at is nil after third deterministic failure")
		}
		if availableAt.Before(before.Add(wantDelay - time.Minute)) {
			t.Fatalf("available_at = %v, want approximately %v later", availableAt, wantDelay)
		}
	}

	if _, err := repo.RetryNowByFolder(ctx, folderID); err != nil {
		t.Fatalf("RetryNowByFolder(): %v", err)
	}
	var state, failureKind string
	var deterministicCount int
	var availableAt time.Time
	var parkedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT state, failure_kind, deterministic_attempt_count, available_at, parked_at
		FROM series_root_match_queue WHERE media_folder_id = $1 AND observed_root_path = $2`,
		folderID, root).Scan(&state, &failureKind, &deterministicCount, &availableAt, &parkedAt); err != nil {
		t.Fatalf("load retried queue state: %v", err)
	}
	if state != queueStatePending || failureKind != "" || deterministicCount != 0 || parkedAt != nil {
		t.Fatalf("retry state = (%q, %q, %d, %v)", state, failureKind, deterministicCount, parkedAt)
	}
	if availableAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("RetryNow left available_at in the future: %v", availableAt)
	}
}

func TestSeriesMatchQueueTransientFailureDoesNotConsumeDeterministicBudget(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	folderID := insertTestFolder(t, pool, "series")
	root := fmt.Sprintf("/test/match-queue-transient-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO series_root_match_queue (media_folder_id, observed_root_path, available_at)
		VALUES ($1, $2, NOW())`, folderID, root); err != nil {
		t.Fatalf("seed series match queue: %v", err)
	}

	repo := NewSeriesRootMatchQueueRepository(pool)
	const leaseToken = "transient-lease"
	if _, err := pool.Exec(ctx, `UPDATE series_root_match_queue SET lease_token = $3 WHERE media_folder_id = $1 AND observed_root_path = $2`, folderID, root, leaseToken); err != nil {
		t.Fatalf("seed lease token: %v", err)
	}
	if err := repo.UpdateFailure(ctx, folderID, root, leaseToken, MatchFailure{Kind: MatchOutcomeProviderTransient, Message: "HTTP 429"}); err != nil {
		t.Fatalf("UpdateFailure(): %v", err)
	}
	var state string
	var deterministicCount int
	if err := pool.QueryRow(ctx, `
		SELECT state, deterministic_attempt_count FROM series_root_match_queue
		WHERE media_folder_id = $1 AND observed_root_path = $2`, folderID, root).Scan(&state, &deterministicCount); err != nil {
		t.Fatalf("load transient queue state: %v", err)
	}
	if state != queueStatePending || deterministicCount != 0 {
		t.Fatalf("transient state = (%q, %d), want pending with zero deterministic attempts", state, deterministicCount)
	}
}

func TestSeriesMatchQueueWakeForChangedInputsResetsOnlyChangedRows(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	folderID := insertTestFolder(t, pool, "series")
	root := fmt.Sprintf("/test/match-input-wake-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO series_root_match_queue (
			media_folder_id, observed_root_path, available_at, state,
			failure_kind, failure_detail, deterministic_attempt_count,
			input_fingerprint, matcher_revision, parked_at, last_error
		) VALUES ($1, $2, NOW() + interval '24 hours', 'parked',
			'candidate_rejected', '{"message":"old"}'::jsonb, 3,
			'old-fingerprint', 0, NOW(), 'old')
	`, folderID, root); err != nil {
		t.Fatalf("seed changed-input queue row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM series_root_match_queue WHERE media_folder_id = $1 AND observed_root_path = $2`, folderID, root)
	})

	repo := NewSeriesRootMatchQueueRepository(pool)
	const staleLeaseToken = "series-stale-lease"
	if _, err := pool.Exec(ctx, `
		UPDATE series_root_match_queue SET lease_token = $3
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, root, staleLeaseToken); err != nil {
		t.Fatalf("seed active lease: %v", err)
	}
	woken, err := repo.WakeForChangedInputs(ctx)
	if err != nil {
		t.Fatalf("WakeForChangedInputs(): %v", err)
	}
	if woken < 1 {
		t.Fatalf("woken = %d, want at least seeded row", woken)
	}
	var state, failureKind, lastError, fingerprint, leaseToken string
	var deterministicCount, revision int
	var availableAt time.Time
	var parkedAt *time.Time
	var rerunRequested bool
	if err := pool.QueryRow(ctx, `
		SELECT state, failure_kind, last_error, deterministic_attempt_count,
			input_fingerprint, matcher_revision, available_at, parked_at,
			lease_token, rerun_requested
		FROM series_root_match_queue
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, root).Scan(&state, &failureKind, &lastError, &deterministicCount, &fingerprint, &revision, &availableAt, &parkedAt, &leaseToken, &rerunRequested); err != nil {
		t.Fatalf("load woken row: %v", err)
	}
	if state != queueStatePending || failureKind != "" || lastError != "" || deterministicCount != 0 || fingerprint == "" || fingerprint == "old-fingerprint" || revision != seriesMatcherRevision || parkedAt != nil {
		t.Fatalf("woken row = state:%q failure:%q last:%q deterministic:%d fingerprint:%q revision:%d available:%v parked:%v", state, failureKind, lastError, deterministicCount, fingerprint, revision, availableAt, parkedAt)
	}
	if leaseToken != staleLeaseToken || !rerunRequested || availableAt.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("woken lease ownership was not preserved: rerun:%v available:%v", rerunRequested, availableAt)
	}
	if woken, err := repo.WakeForChangedInputs(ctx); err != nil || woken != 0 {
		t.Fatalf("unchanged WakeForChangedInputs() = (%d, %v), want (0, nil)", woken, err)
	}
	if err := repo.UpdateFailure(ctx, folderID, root, staleLeaseToken, MatchFailure{
		Kind: MatchOutcomeCandidateRejected, Message: "stale worker result",
	}); err != nil {
		t.Fatalf("stale UpdateFailure(): %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT failure_kind FROM series_root_match_queue
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, root).Scan(&failureKind); err != nil {
		t.Fatalf("load row after stale series update: %v", err)
	}
	if failureKind != "" {
		t.Fatalf("stale series worker overwrote awakened row with failure %q", failureKind)
	}
	if err := pool.QueryRow(ctx, `
		SELECT lease_token, rerun_requested, available_at
		FROM series_root_match_queue
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, root).Scan(&leaseToken, &rerunRequested, &availableAt); err != nil {
		t.Fatalf("load released series rerun: %v", err)
	}
	if leaseToken != "" || !rerunRequested || availableAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("released rerun retained lease ownership: rerun:%v available:%v", rerunRequested, availableAt)
	}

	if _, err := pool.Exec(ctx, `UPDATE media_folders SET metadata_language = 'da' WHERE id = $1`, folderID); err != nil {
		t.Fatalf("change folder language: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE series_root_match_queue
		SET available_at = NOW() + interval '24 hours', deterministic_attempt_count = 2,
			failure_kind = 'candidate_rejected', last_error = 'old'
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, root); err != nil {
		t.Fatalf("back off queue row before language change wake: %v", err)
	}
	if woken, err := repo.WakeForChangedInputs(ctx); err != nil || woken < 1 {
		t.Fatalf("language-change WakeForChangedInputs() = (%d, %v), want seeded row", woken, err)
	}

	installationID := insertTestInstallation(t, pool, "plugin", true)
	insertTestCapability(t, pool, installationID, "config-fingerprint", `{}`)
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_provider_chains (
			media_folder_id, plugin_installation_id, capability_id, capability_type,
			content_level, priority, enabled
		) VALUES ($1, $2, 'config-fingerprint', 'metadata_provider.v1', 'item', 1, true)
	`, folderID, installationID); err != nil {
		t.Fatalf("seed relevant provider chain: %v", err)
	}
	if woken, err := repo.WakeForChangedInputs(ctx); err != nil || woken < 1 {
		t.Fatalf("provider-chain WakeForChangedInputs() = (%d, %v), want seeded row", woken, err)
	}
	if woken, err := repo.WakeForChangedInputs(ctx); err != nil || woken != 0 {
		t.Fatalf("stable provider chain wake = (%d, %v), want (0, nil)", woken, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (
			media_folder_id, file_path, observed_root_path, base_type,
			season_number, episode_number, file_size
		) VALUES ($1, $2, $3, 'series', 1, 8, 0)
	`, folderID, root+"/Season 01/Show S01E08.mkv", root); err != nil {
		t.Fatalf("add episode path to series shape: %v", err)
	}
	if woken, err := repo.WakeForChangedInputs(ctx); err != nil || woken < 1 {
		t.Fatalf("episode-shape WakeForChangedInputs() = (%d, %v), want seeded row", woken, err)
	}
	if woken, err := repo.WakeForChangedInputs(ctx); err != nil || woken != 0 {
		t.Fatalf("stable episode shape wake = (%d, %v), want (0, nil)", woken, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
		VALUES ($1, 'metadata', '{"api_key":"changed-but-never-persisted-in-the-queue"}'::jsonb)
	`, installationID); err != nil {
		t.Fatalf("change relevant provider config: %v", err)
	}
	if woken, err := repo.WakeForChangedInputs(ctx); err != nil || woken < 1 {
		t.Fatalf("provider-config WakeForChangedInputs() = (%d, %v), want seeded row", woken, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT input_fingerprint FROM series_root_match_queue
		WHERE media_folder_id = $1 AND observed_root_path = $2
	`, folderID, root).Scan(&fingerprint); err != nil {
		t.Fatalf("load provider-config fingerprint: %v", err)
	}
	if strings.Contains(fingerprint, "api_key") || strings.Contains(fingerprint, "changed-but-never") {
		t.Fatalf("queue fingerprint leaked provider configuration: %q", fingerprint)
	}
}

func TestMovieMatchQueueRetryDuringLeaseQueuesFencedRerun(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	folderID := insertTestFolder(t, pool, "movie")
	root := fmt.Sprintf("/test/claim-lease-%d", time.Now().UnixNano())
	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, base_type, file_size)
		VALUES ($1, $2, 'movie', 0) RETURNING id
	`, folderID, root+"/Movie.mkv").Scan(&fileID); err != nil {
		t.Fatalf("seed movie file: %v", err)
	}

	repo := NewMovieMatchQueueRepository(pool, scannerrepo.NewFileRepository(pool))
	if err := repo.EnqueueMovieFile(ctx, fileID); err != nil {
		t.Fatalf("EnqueueMovieFile(): %v", err)
	}
	claimTestMovie := func() ([]models.MovieMatchJob, error) {
		return repo.ClaimByFolderAndPathPrefix(ctx, folderID, root, 1, time.Time{})
	}
	claimed, err := claimTestMovie()
	if err != nil || len(claimed) != 1 || claimed[0].File == nil || claimed[0].File.ID != fileID || claimed[0].LeaseToken == "" {
		t.Fatalf("first Claim() = (%#v, %v), want file %d", claimed, err, fileID)
	}
	claimedAgain, err := claimTestMovie()
	if err != nil || len(claimedAgain) != 0 {
		t.Fatalf("second Claim() during lease = (%#v, %v), want empty", claimedAgain, err)
	}

	var leasedUntil time.Time
	if err := pool.QueryRow(ctx, `SELECT available_at FROM movie_match_queue WHERE media_file_id = $1`, fileID).Scan(&leasedUntil); err != nil {
		t.Fatalf("load claim lease: %v", err)
	}
	if leasedUntil.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("claim lease = %v, want comfortably beyond one hour", leasedUntil)
	}

	if affected, err := repo.RetryNowByFolder(ctx, folderID); err != nil || affected != 1 {
		t.Fatalf("RetryNowByFolder() = (%d, %v), want (1, nil)", affected, err)
	}
	var state, leaseToken string
	var availableAt time.Time
	var rerunRequested bool
	if err := pool.QueryRow(ctx, `
		SELECT state, available_at, lease_token, rerun_requested
		FROM movie_match_queue
		WHERE media_file_id = $1
	`, fileID).Scan(&state, &availableAt, &leaseToken, &rerunRequested); err != nil {
		t.Fatalf("load retried movie row: %v", err)
	}
	if state != queueStatePending || availableAt.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("retried movie row = state %q available %v, want active lease preserved", state, availableAt)
	}
	if leaseToken != claimed[0].LeaseToken || !rerunRequested {
		t.Fatalf("retried movie ownership was not preserved: rerun %v", rerunRequested)
	}
	if reclaimed, err := claimTestMovie(); err != nil || len(reclaimed) != 0 {
		t.Fatalf("Claim() while original worker runs = (%#v, %v), want empty", reclaimed, err)
	}
	if err := repo.Delete(ctx, fileID, claimed[0].LeaseToken); err != nil {
		t.Fatalf("original leased completion: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM movie_match_queue WHERE media_file_id = $1`, fileID).Scan(&remaining); err != nil {
		t.Fatalf("count retried movie row: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("original completion deleted requested rerun; remaining = %d", remaining)
	}
	if err := repo.UpdateFailure(ctx, fileID, claimed[0].LeaseToken, MatchFailure{
		Kind: MatchOutcomeCandidateRejected, Message: "stale worker result",
	}); err != nil {
		t.Fatalf("stale leased failure: %v", err)
	}
	var failureKind string
	if err := pool.QueryRow(ctx, `SELECT failure_kind FROM movie_match_queue WHERE media_file_id = $1`, fileID).Scan(&failureKind); err != nil {
		t.Fatalf("load retried movie failure kind: %v", err)
	}
	if failureKind != "" {
		t.Fatalf("stale lease overwrote newly awakened row with failure %q", failureKind)
	}

	reclaimed, err := claimTestMovie()
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("Claim() after original completion = (%#v, %v), want one row", reclaimed, err)
	}
	if !reclaimed[0].RerunRequested {
		t.Fatal("reclaimed job did not carry the forced-rerun marker")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE movie_match_queue SET available_at = NOW()
		WHERE media_file_id = $1
	`, fileID); err != nil {
		t.Fatalf("expire forced rerun lease: %v", err)
	}
	expiredReplacement, err := claimTestMovie()
	if err != nil || len(expiredReplacement) != 1 {
		t.Fatalf("Claim() after forced lease expiry = (%#v, %v), want one row", expiredReplacement, err)
	}
	if !expiredReplacement[0].RerunRequested {
		t.Fatal("expired forced rerun lost its durable intent")
	}
	if expiredReplacement[0].LeaseToken == reclaimed[0].LeaseToken {
		t.Fatal("expired forced rerun was not assigned fresh ownership")
	}
	if affected, err := repo.ReleaseLease(ctx, expiredReplacement[0].LeaseToken); err != nil || affected != 1 {
		t.Fatalf("ReleaseLease() = (%d, %v), want (1, nil)", affected, err)
	}
	reclaimedAgain, err := claimTestMovie()
	if err != nil || len(reclaimedAgain) != 1 {
		t.Fatalf("Claim() after ReleaseLease = (%#v, %v), want one immediately claimable row", reclaimedAgain, err)
	}
	if reclaimedAgain[0].LeaseToken == expiredReplacement[0].LeaseToken {
		t.Fatal("released claim was not assigned a fresh ownership token")
	}
	if !reclaimedAgain[0].RerunRequested {
		t.Fatal("released forced rerun lost its durable intent")
	}
	if err := repo.UpdateFailure(ctx, fileID, reclaimedAgain[0].LeaseToken, MatchFailure{
		Kind: "provider_transient", Message: "temporary provider outage",
	}); err != nil {
		t.Fatalf("forced rerun failure: %v", err)
	}
	var rerunAfterFailure, leaseRerunAfterFailure bool
	if err := pool.QueryRow(ctx, `
		SELECT rerun_requested, lease_forced_rerun
		FROM movie_match_queue
		WHERE media_file_id = $1
	`, fileID).Scan(&rerunAfterFailure, &leaseRerunAfterFailure); err != nil {
		t.Fatalf("load failed forced rerun: %v", err)
	}
	if !rerunAfterFailure || leaseRerunAfterFailure {
		t.Fatalf("failed forced rerun state = requested:%v leased:%v", rerunAfterFailure, leaseRerunAfterFailure)
	}
}

func TestMatchQueueSyncDeletesIneligibleReruns(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()

	t.Run("movie", func(t *testing.T) {
		folderID := insertTestFolder(t, pool, "movie")
		var fileID int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, base_type, file_size)
			VALUES ($1, $2, 'movie', 0) RETURNING id
		`, folderID, fmt.Sprintf("/test/ineligible-rerun-%d/Movie.mkv", time.Now().UnixNano())).Scan(&fileID); err != nil {
			t.Fatalf("seed movie file: %v", err)
		}
		repo := NewMovieMatchQueueRepository(pool, scannerrepo.NewFileRepository(pool))
		if err := repo.EnqueueMovieFile(ctx, fileID); err != nil {
			t.Fatalf("EnqueueMovieFile(): %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE movie_match_queue
			SET rerun_requested = true, lease_token = $2, available_at = NOW() + interval '24 hours'
			WHERE media_file_id = $1
		`, fileID, "cleanup-owner"); err != nil {
			t.Fatalf("seed movie rerun: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE media_files SET missing_since = NOW() WHERE id = $1`, fileID); err != nil {
			t.Fatalf("mark movie missing: %v", err)
		}
		if err := repo.SyncForFolder(ctx, folderID); err != nil {
			t.Fatalf("SyncForFolder(): %v", err)
		}
		var remaining int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM movie_match_queue WHERE media_file_id = $1`, fileID).Scan(&remaining); err != nil {
			t.Fatalf("count movie rerun: %v", err)
		}
		if remaining != 0 {
			t.Fatalf("ineligible movie reruns remaining = %d, want 0", remaining)
		}
	})

	t.Run("series", func(t *testing.T) {
		folderID := insertTestFolder(t, pool, "series")
		root := fmt.Sprintf("/test/ineligible-series-rerun-%d", time.Now().UnixNano())
		var fileID int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (
				media_folder_id, file_path, observed_root_path, base_type,
				season_number, episode_number, file_size
			) VALUES ($1, $2, $3, 'series', 1, 1, 0)
			RETURNING id
		`, folderID, root+"/Season 01/Show S01E01.mkv", root).Scan(&fileID); err != nil {
			t.Fatalf("seed series file: %v", err)
		}
		repo := NewSeriesRootMatchQueueRepository(pool)
		if err := repo.EnqueueSeriesRoot(ctx, folderID, root); err != nil {
			t.Fatalf("EnqueueSeriesRoot(): %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE series_root_match_queue
			SET rerun_requested = true, lease_token = $3, available_at = NOW() + interval '24 hours'
			WHERE media_folder_id = $1 AND observed_root_path = $2
		`, folderID, root, "cleanup-owner"); err != nil {
			t.Fatalf("seed series rerun: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE media_files SET missing_since = NOW() WHERE id = $1`, fileID); err != nil {
			t.Fatalf("mark series file missing: %v", err)
		}
		if err := repo.SyncForFolder(ctx, folderID); err != nil {
			t.Fatalf("SyncForFolder(): %v", err)
		}
		var remaining int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM series_root_match_queue
			WHERE media_folder_id = $1 AND observed_root_path = $2
		`, folderID, root).Scan(&remaining); err != nil {
			t.Fatalf("count series rerun: %v", err)
		}
		if remaining != 0 {
			t.Fatalf("ineligible series reruns remaining = %d, want 0", remaining)
		}
	})
}
