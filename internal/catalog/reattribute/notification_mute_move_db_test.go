package reattribute

import (
	"context"
	"fmt"
	"testing"
)

// TestMovePairsMovesNotificationMutesDB checks that a whole-item move carries
// a profile's notification mute to the destination series, and that a mute
// the profile already holds there wins the collision.
func TestMovePairsMovesNotificationMutesDB(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := fmt.Sprintf("reattr-mute-from-%d", env.suffix)
	to := fmt.Sprintf("reattr-mute-to-%d", env.suffix)
	onlyFrom := fmt.Sprintf("reattr-mute-only-%d", env.suffix)
	onlyTo := fmt.Sprintf("reattr-mute-only-to-%d", env.suffix)
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO profile_series_notification_mutes (user_id, profile_id, series_id)
		VALUES ($1, $2, $3), ($1, $2, $4), ($1, $2, $5)
	`, env.userID, env.profileID, from, to, onlyFrom); err != nil {
		t.Fatalf("seed mutes: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(ctx, `DELETE FROM profile_series_notification_mutes WHERE user_id = $1`, env.userID)
	})

	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var report Report
	if err := movePairs(ctx, tx, []IDPair{{From: from, To: to}, {From: onlyFrom, To: onlyTo}}, &report); err != nil {
		t.Fatal(err)
	}
	var left, moved int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE series_id = ANY($2)), count(*) FILTER (WHERE series_id = ANY($3))
		FROM profile_series_notification_mutes WHERE user_id = $1`,
		env.userID, []string{from, onlyFrom}, []string{to, onlyTo}).Scan(&left, &moved); err != nil {
		t.Fatal(err)
	}
	if left != 0 || moved != 2 {
		t.Fatalf("source mutes left = %d, destination mutes = %d; want 0 and 2", left, moved)
	}
}
