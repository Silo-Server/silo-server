package reattribute

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestMovedHistoryKeepsItsCutoffsDB checks that history moving to another item
// takes the profile's history cutoffs with it. A mark unwatched keeps plays in
// history, so a play that moves without its cutoff would count as watched on
// the destination.
func TestMovedHistoryKeepsItsCutoffsDB(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	older := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	unmarked := older.Add(24 * time.Hour)
	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// read answers an item's watched-state cutoff and its history-listing
	// cutoff, nil when nothing was removed from history.
	read := func(t *testing.T, item string) (hidden time.Time, removed *time.Time) {
		t.Helper()
		if err := tx.QueryRow(ctx, `
			SELECT hidden_before, NULLIF(COALESCE(removed_before, hidden_before), '-infinity')
			FROM user_history_hidden_items
			WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
			env.userID, env.profileID, item).Scan(&hidden, &removed); err != nil {
			t.Fatalf("cutoffs of %s: %v", item, err)
		}
		return hidden, removed
	}

	t.Run("merge", func(t *testing.T) {
		from := fmt.Sprintf("reattr-cutoff-from-%d", env.suffix)
		to := fmt.Sprintf("reattr-cutoff-to-%d", env.suffix)
		// The destination's own row predates removed_before (NULL); the
		// source was unmarked later.
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, removed_before, updated_at)
			VALUES ($1, $2, $3, $5, NULL, $5), ($1, $2, $4, $6, '-infinity', $6)`,
			env.userID, env.profileID, to, from, older, unmarked); err != nil {
			t.Fatal(err)
		}
		var report Report
		if err := movePairs(ctx, tx, []IDPair{{From: from, To: to}}, &report); err != nil {
			t.Fatal(err)
		}
		// A raised cutoff changes what the profile has watched, so the
		// merge refreshes its recommendations.
		if !report.MovedUserState() {
			t.Fatal("a merge that raised the destination's cutoff reported no user-state change")
		}
		hidden, removed := read(t, to)
		if !hidden.Equal(unmarked) || removed == nil || !removed.Equal(older) {
			t.Fatalf("destination cutoffs = %s, %v; want the source's unmark %s and its own removal %s", hidden, removed, unmarked, older)
		}
	})

	t.Run("evidence split", func(t *testing.T) {
		from := fmt.Sprintf("reattr-cutoff-split-from-%d", env.suffix)
		to := fmt.Sprintf("reattr-cutoff-split-to-%d", env.suffix)
		const movedFile = 501
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin_playback_history (session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at)
			VALUES ($1, $2, $3, $4, $5, 'direct_play', $6, $6)`,
			fmt.Sprintf("reattr-cutoff-session-%d", env.suffix), env.userID, env.profileID, from, movedFile, older); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed)
			VALUES (gen_random_uuid()::text, $1, $2, $3, $4, true)`,
			env.userID, env.profileID, from, older); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, removed_before, updated_at)
			VALUES ($1, $2, $3, $4, '-infinity', $4)`,
			env.userID, env.profileID, from, unmarked); err != nil {
			t.Fatal(err)
		}
		var report Report
		opts := Options{FromContentID: from, ToContentID: to, MovedFileIDs: []int{movedFile}, Mode: HistoryModeEvidence}
		if err := moveHistorySubset(ctx, tx, opts, &report); err != nil {
			t.Fatal(err)
		}
		if report.HistoryMoved != 1 || report.IntentMoved != 1 {
			t.Fatalf("history moved = %d, cutoffs carried = %d; want 1 and 1", report.HistoryMoved, report.IntentMoved)
		}
		if hidden, _ := read(t, to); !hidden.Equal(unmarked) {
			t.Fatalf("destination watched-state cutoff = %s, want %s", hidden, unmarked)
		}
		if hidden, _ := read(t, from); !hidden.Equal(unmarked) {
			t.Fatalf("source watched-state cutoff = %s, want it kept at %s", hidden, unmarked)
		}
	})
}
