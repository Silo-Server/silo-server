package metadata

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	scannerrepo "github.com/Silo-Server/silo-server/internal/scanner"
)

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

// The matcher reads a group's operator override, so a row parked or backed off
// without it must wake when the override is saved, changed, or removed.
func TestMatchQueueGroupOverrideWakesBackedOffRows(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("/test/override-wake-%d", time.Now().UnixNano())

	check := func(t *testing.T, table, where string, args []any, folderID int, groupKey string, enqueue, wakeChanged func() error) {
		t.Helper()
		backOff := func() {
			t.Helper()
			if _, err := pool.Exec(ctx, `UPDATE `+table+` SET available_at = NOW() + interval '24 hours',
				failure_kind = 'candidate_rejected', last_error = 'no acceptable candidate'
				WHERE `+where, args...); err != nil {
				t.Fatalf("back off %s row: %v", table, err)
			}
		}
		awake := func() bool {
			t.Helper()
			var availableAt time.Time
			if err := pool.QueryRow(ctx, `SELECT available_at FROM `+table+` WHERE `+where, args...).Scan(&availableAt); err != nil {
				t.Fatalf("load %s row: %v", table, err)
			}
			return !availableAt.After(time.Now().Add(time.Minute))
		}
		override := func(query string, values ...any) {
			t.Helper()
			if _, err := pool.Exec(ctx, query, append([]any{folderID, groupKey}, values...)...); err != nil {
				t.Fatalf("write group override: %v", err)
			}
		}

		if err := enqueue(); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		backOff()
		if err := enqueue(); err != nil || awake() {
			t.Fatalf("unchanged inputs woke a backed-off row (err %v)", err)
		}

		override(`INSERT INTO media_group_overrides (media_folder_id, group_key_version, content_group_key, forced_title)
			VALUES ($1, 1, $2, $3)`, "Example Title")
		if err := enqueue(); err != nil || !awake() {
			t.Fatalf("a saved override left the row backed off (err %v)", err)
		}

		backOff()
		override(`UPDATE media_group_overrides SET note = $3, updated_at = NOW()
			WHERE media_folder_id = $1 AND group_key_version = 1 AND content_group_key = $2`, "why this override exists")
		if err := enqueue(); err != nil || awake() {
			t.Fatalf("a note-only change woke a backed-off row (err %v)", err)
		}

		override(`UPDATE media_group_overrides SET forced_tvdb_id = $3
			WHERE media_folder_id = $1 AND group_key_version = 1 AND content_group_key = $2`, "123456")
		if err := enqueue(); err != nil || !awake() {
			t.Fatalf("a changed override left the row backed off (err %v)", err)
		}

		backOff()
		override(`DELETE FROM media_group_overrides
			WHERE media_folder_id = $1 AND group_key_version = 1 AND content_group_key = $2`)
		if err := wakeChanged(); err != nil || !awake() {
			t.Fatalf("a removed override left the row backed off (err %v)", err)
		}
	}

	t.Run("movie", func(t *testing.T) {
		folderID := insertTestFolder(t, pool, "movie")
		path := prefix + "/Example Movie 2049.mkv"
		var fileID int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, base_type, content_group_key, file_size)
			VALUES ($1, $2, 'movie', 'movie-group', 0) RETURNING id
		`, folderID, path).Scan(&fileID); err != nil {
			t.Fatalf("seed movie file: %v", err)
		}
		// A file with no override must keep the identity it had before overrides
		// were part of it, or every queued row would wake on upgrade.
		var identity string
		if err := pool.QueryRow(ctx, `SELECT `+movieMatchQueueFileIdentitySQL+` FROM media_files mf WHERE mf.id = $1`, fileID).Scan(&identity); err != nil {
			t.Fatalf("load movie queue identity: %v", err)
		}
		if want := path + "|movie-group"; identity != want {
			t.Fatalf("movie queue identity without an override = %q, want %q", identity, want)
		}
		movies := NewMovieMatchQueueRepository(pool, scannerrepo.NewFileRepository(pool))
		check(t, "movie_match_queue", "media_file_id = $1", []any{fileID}, folderID, "movie-group",
			func() error { return movies.EnqueueMovieFile(ctx, fileID) },
			func() error { _, err := movies.WakeForChangedInputs(ctx); return err })
	})

	t.Run("series", func(t *testing.T) {
		folderID := insertTestFolder(t, pool, "series")
		root := prefix + "/Example Show"
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, observed_root_path, base_type, content_group_key, file_size)
			VALUES ($1, $2, $3, 'series', 'series-group', 0)
		`, folderID, root+"/Example.Show.S01E01.mkv", root); err != nil {
			t.Fatalf("seed series file: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM series_root_match_queue WHERE media_folder_id = $1`, folderID)
		})
		series := NewSeriesRootMatchQueueRepository(pool)
		check(t, "series_root_match_queue", "media_folder_id = $1 AND observed_root_path = $2", []any{folderID, root}, folderID, "series-group",
			func() error { return series.EnqueueSeriesRoot(ctx, folderID, root) },
			func() error { _, err := series.WakeForChangedInputs(ctx); return err })
	})
}
