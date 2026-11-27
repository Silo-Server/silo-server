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
// in the restored data (sameAccount), and the highest account id its
// sequence has handed out (accountIDsUsed).
type liveAccounts struct {
	ids     []int64
	created []time.Time
	used    int64
}

// readLiveAccounts reads every account of the live snapshot tx.
func readLiveAccounts(ctx context.Context, tx pgx.Tx) (liveAccounts, error) {
	var a liveAccounts
	err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(id ORDER BY id), '{}'), coalesce(array_agg(created_at ORDER BY id), '{}')
		FROM users`).Scan(&a.ids, &a.created)
	if err != nil {
		return a, fmt.Errorf("reading the live accounts: %w", err)
	}
	if a.used, err = accountIDsUsed(ctx, tx); err != nil {
		return a, fmt.Errorf("reading the live account id sequence: %w", err)
	}
	return a, nil
}

// accountIDsUsed is the highest id the account id sequence of tx's database
// has handed out, 0 for none. It reads the sequence itself: last_value is
// handed out only once is_called is set, which a sequence never used, or one
// set back with setval(..., false), does not have. pg_sequence_last_value
// gives NULL for both and so loses the position a setval left.
func accountIDsUsed(ctx context.Context, tx pgx.Tx) (int64, error) {
	var seq string
	if err := tx.QueryRow(ctx, `SELECT pg_get_serial_sequence('users', 'id')`).Scan(&seq); err != nil {
		return 0, err
	}
	var used int64
	// seq is the catalog's own quoted name of the sequence.
	err := tx.QueryRow(ctx, `SELECT CASE WHEN is_called THEN last_value ELSE last_value - 1 END FROM `+seq).Scan(&used)
	return used, err
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
// live server has used: the highest its sequence handed out, an account
// deleted since included, and its highest account, as carryInto does for the
// carried tables. The backup's sequence would otherwise give the next account
// an id the live server gave an account the restore removes, and a later
// restore would meet two accounts under it. Restoring the same backup again
// keeps the position the first restore reached. With no live server (no
// position and no accounts) the sequence stays as the backup has it.
func advanceAccountIDs(ctx context.Context, tx pgx.Tx, live liveAccounts) error {
	highest := live.used
	if n := len(live.ids); n > 0 {
		highest = max(highest, live.ids[n-1])
	}
	if highest == 0 {
		return nil
	}
	restored, err := accountIDsUsed(ctx, tx)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT setval(pg_get_serial_sequence('users', 'id'), GREATEST($1::bigint, $2::bigint, (SELECT coalesce(max(id), 0) FROM users)))`,
		highest, restored)
	return err
}

// AdvanceAccountIDs moves dbURL's account id sequence past every id liveURL
// has used (advanceAccountIDs), as CarryCredentials does from the live state
// it reads. A restore of this deployment's backup that carries none of the
// live credentials, because the backup's older schema cannot take them,
// runs it in their place.
func (l PGLoader) AdvanceAccountIDs(ctx context.Context, dbURL, liveURL string) error {
	var live liveAccounts
	conn, err := pgx.Connect(ctx, liveURL)
	if err != nil {
		return fmt.Errorf("connecting to the live server: %w", err)
	}
	err = pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		live, err = readLiveAccounts(ctx, tx)
		return err
	})
	_ = conn.Close(context.WithoutCancel(ctx))
	if err != nil {
		return fmt.Errorf("reading the live server's accounts: %w", err)
	}
	conn, err = pgx.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connecting to the restored database: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		return advanceAccountIDs(ctx, tx, live)
	})
}
