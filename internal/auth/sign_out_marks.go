package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// A sign-out everywhere revokes the login sessions that exist when it runs.
// A password sign-in checks its credentials before it knows which account it
// is for (a directory answers over the network, a local password costs a
// bcrypt hash), so a sign-out that commits after that check began but before
// the session is inserted would miss the session. Each sign-out everywhere
// therefore records its transaction in account_sign_outs, and a password
// sign-in takes a database snapshot before its credential check
// (signInSnapshot). Under the account row lock, which every sign-out
// everywhere also holds until it commits, the sign-in refuses to open a
// session when its snapshot cannot see the account's last sign-out
// (signedOutSince).

// markSignedOutInTransaction records tx as the last sign-out everywhere of
// each account in userIDs. The caller holds the accounts' users rows locked.
func markSignedOutInTransaction(ctx context.Context, tx pgx.Tx, userIDs []int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO account_sign_outs (user_id, xid)
		SELECT id, pg_current_xact_id() FROM users WHERE id = ANY($1::int[]) ORDER BY id
		ON CONFLICT (user_id) DO UPDATE SET xid = EXCLUDED.xid`, userIDs); err != nil {
		return fmt.Errorf("recording the sign-out: %w", err)
	}
	return nil
}

// signInSnapshot answers the database snapshot a password sign-in takes
// before checking its credentials, for signedOutSince. It must come from the
// primary that the sign-in's session transaction runs on.
func signInSnapshot(ctx context.Context, db rowQuerier) (string, error) {
	var snapshot string
	if err := db.QueryRow(ctx, `SELECT pg_current_snapshot()::text`).Scan(&snapshot); err != nil {
		return "", fmt.Errorf("taking the sign-in snapshot: %w", err)
	}
	return snapshot, nil
}

// signedOutSince reports whether account userID was signed out everywhere by
// a transaction that snapshot (signInSnapshot) cannot see, one that had not
// committed when the sign-in began. The caller holds the account's users row
// locked, so a sign-out still in progress has committed by now.
//
// A mark this statement's own snapshot could not see either (at or past its
// xmax) is not from this cluster's history: a logical restore carries the
// old cluster's transaction ids, which may lie ahead of the new one's. It is
// ignored rather than refusing every sign-in of the account until the
// cluster's transaction ids catch up.
func signedOutSince(ctx context.Context, db rowQuerier, userID int, snapshot string) (bool, error) {
	var since bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM account_sign_outs
		WHERE user_id = $1
		  AND NOT pg_visible_in_snapshot(xid, $2::text::pg_snapshot)
		  AND xid < pg_snapshot_xmax(pg_current_snapshot()))`, userID, snapshot).Scan(&since); err != nil {
		return false, fmt.Errorf("reading the account's last sign-out: %w", err)
	}
	return since, nil
}
