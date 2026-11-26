package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// sameAccount is the SQL condition, over the live row r, that the account in
// column is the same account in the restored data (same_accounts): an
// account there has its id and was created at the same time. An id alone
// does not say so: a full restore puts the account id sequence back, so an
// account created on the live server since can hold an id that another
// account has in the backup.
func sameAccount(column string) string {
	return `EXISTS (SELECT 1 FROM same_accounts s WHERE s.id = r.` + column + `)`
}

// liveAccounts are the live server's accounts: their ids and when each was
// created, which together tell an account from another that holds its id
// in the restored data (sameAccount).
type liveAccounts struct {
	ids     []int64
	created []time.Time
}

// readLiveAccounts reads every account of the live snapshot tx.
func readLiveAccounts(ctx context.Context, tx pgx.Tx) (liveAccounts, error) {
	var a liveAccounts
	err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(id ORDER BY id), '{}'), coalesce(array_agg(created_at ORDER BY id), '{}')
		FROM users`).Scan(&a.ids, &a.created)
	if err != nil {
		return a, fmt.Errorf("reading the live accounts: %w", err)
	}
	return a, nil
}

// sameAccounts writes same_accounts, the live accounts that are the same
// account in the restored data (sameAccount). A creation time survives a
// backup and a restore exactly: the column is a timestamptz only its
// default writes, and the dump copies it at full precision.
func sameAccounts(ctx context.Context, tx pgx.Tx, live liveAccounts) error {
	_, err := tx.Exec(ctx, `CREATE TEMP TABLE same_accounts ON COMMIT DROP AS
		SELECT u.id FROM users u JOIN unnest($1::bigint[], $2::timestamptz[]) AS l(id, created_at)
			ON l.id = u.id AND l.created_at = u.created_at`, live.ids, live.created)
	return err
}

// advanceAccountIDs moves the restored account id sequence past every id the
// live server has used, as carryInto does for the carried tables. The
// backup's sequence would otherwise give the next account an id the live
// server gave an account the restore removes, and a later restore would meet
// two accounts under it.
func advanceAccountIDs(ctx context.Context, tx pgx.Tx, live liveAccounts) error {
	if len(live.ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT setval(pg_get_serial_sequence('users', 'id'), GREATEST(
		coalesce(pg_sequence_last_value(pg_get_serial_sequence('users', 'id')::regclass), 0),
		(SELECT coalesce(max(id), 0) FROM users), $1, 1))`, live.ids[len(live.ids)-1])
	return err
}
