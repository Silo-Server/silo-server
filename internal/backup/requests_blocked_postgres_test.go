package backup

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// checkCarryKeepsRequestsBlock is part of checkCarryAuthority: the live
// profile is blocked from requests. A backup taken before profiles could be
// blocked gets the column, with the live block kept, before authority is
// compared, so the key is kept; so is it for a backup that has the column but
// not the block. keptAs re-runs the carry and checks the account's key.
func checkCarryKeepsRequestsBlock(t *testing.T, pool, restored *pgxpool.Pool, profile string, keptAs func(want bool, why string)) {
	t.Helper()
	ctx := t.Context()
	exec := func(db *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE user_profiles SET requests_blocked = false WHERE id = $1`, profile)
	})
	exec(pool, `UPDATE user_profiles SET requests_blocked = true WHERE id = $1`, profile)
	exec(restored, `ALTER TABLE user_profiles DROP COLUMN requests_blocked`)
	keptAs(true, "want the key kept: the restore adds the column and keeps the live block from requests")
	var blocked bool
	if err := restored.QueryRow(ctx, `SELECT requests_blocked FROM user_profiles WHERE id = $1`, profile).Scan(&blocked); err != nil || !blocked {
		t.Fatalf("restored profile blocked = %v (%v), want the live block kept", blocked, err)
	}
	exec(restored, `UPDATE user_profiles SET requests_blocked = false WHERE id = $1`, profile)
	keptAs(true, "want the key kept: the restore keeps the live block from requests")
}
