package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrActingProfileChanged refuses an operation whose main-profile check no
// longer holds once the account is locked: the profile's PIN changed, it
// stopped being the main profile, or it is gone. The caller verifies the
// profile again and repeats the request.
var ErrActingProfileChanged = errors.New("the acting profile changed")

// ActingProfile is the main profile a request acted as, with the PIN
// revision its check ran against. The route checks the profile before the
// operation's transaction starts, so the transaction checks it again under
// the account lock (recheckActingProfile). The zero value has nothing to
// check again: the request named no profile, or the profiles are stored
// outside Postgres, where this transaction can't read them.
type ActingProfile struct {
	ID          string
	PINRevision int64
}

// recheckActingProfile refuses with ErrActingProfileChanged unless acting is
// still the account's main profile at the PIN revision the route checked. The
// caller holds the account's row lock. The share lock it takes keeps the PIN
// as it is until the transaction ends.
//
// It never waits for the profile row. A profile write holds that row and may
// then touch the account row this transaction has locked, so waiting could
// deadlock; a row that is being written counts as changed instead.
func recheckActingProfile(ctx context.Context, tx pgx.Tx, userID int, acting ActingProfile) error {
	if acting.ID == "" {
		return nil
	}
	var (
		revision int64
		primary  bool
	)
	err := tx.QueryRow(ctx, `SELECT pin_revision, is_primary FROM user_profiles
		WHERE user_id = $1 AND id = $2 FOR SHARE NOWAIT`, userID, acting.ID).Scan(&revision, &primary)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrActingProfileChanged
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "55P03" {
		return ErrActingProfileChanged
	}
	if err != nil {
		return fmt.Errorf("checking the acting profile: %w", err)
	}
	if revision != acting.PINRevision || !primary {
		return ErrActingProfileChanged
	}
	return nil
}
