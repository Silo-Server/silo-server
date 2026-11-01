package watchlist

// Title writes inside a caller's transaction, for imports (backup restore
// and profile import) that write many watchlist rows in one transaction.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnsureTitlesInTx makes every snapshot's title exist and locks it, in tx,
// before an import writes any watchlist row: in media type and TMDB id order,
// it locks the title holding the TMDB id, current or former, or creates it
// (under a savepoint, attaching to the winner when another add races it to
// the alias). Holding them all first, the import never waits for a title
// after writing a library watchlist entry, which promotion, holding the
// title's lock, may be writing too.
func EnsureTitlesInTx(ctx context.Context, tx pgx.Tx, snaps []Snapshot, now time.Time) error {
	sorted := slices.Clone(snaps)
	slices.SortFunc(sorted, func(a, b Snapshot) int {
		if c := strings.Compare(a.MediaType, b.MediaType); c != 0 {
			return c
		}
		return a.TMDBID - b.TMDBID
	})
	for i, snap := range sorted {
		if i > 0 && snap.Key() == sorted[i-1].Key() {
			continue
		}
		if err := ensureTitle(ctx, tx, snap, now); err != nil {
			return err
		}
	}
	return nil
}

func ensureTitle(ctx context.Context, tx pgx.Tx, snap Snapshot, now time.Time) error {
	if snap.MediaType != mediaTypeMovie && snap.MediaType != mediaTypeSeries || snap.TMDBID <= 0 {
		return fmt.Errorf("watchlist: invalid title %s/%d", snap.MediaType, snap.TMDBID)
	}
	for attempt := 0; attempt < maxAddAttempts; attempt++ {
		if _, found, err := lockTitleByTMDB(ctx, tx, snap.MediaType, snap.TMDBID); err != nil || found {
			return err
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, raced, err := createTitle(ctx, sp, snap, now)
		if err != nil || raced {
			_ = sp.Rollback(ctx)
			if err != nil {
				return err
			}
			continue
		}
		return sp.Commit(ctx)
	}
	return fmt.Errorf("creating watchlist title: lost the race to create it %d times", maxAddAttempts)
}

// AddSnapshotInTx puts a title on a profile's watchlist in the caller's
// transaction, as AddSnapshot does: the title holding the TMDB ID, current
// or former, is reused, or created from the snapshot. A lost race to create
// the title rolls back to a savepoint and attaches to the winner.
func AddSnapshotInTx(ctx context.Context, tx pgx.Tx, userID int, profileID string, snap Snapshot, addedAt, now time.Time) (bool, error) {
	if snap.MediaType != mediaTypeMovie && snap.MediaType != mediaTypeSeries || snap.TMDBID <= 0 {
		return false, fmt.Errorf("watchlist: invalid title %s/%d", snap.MediaType, snap.TMDBID)
	}
	for attempt := 0; attempt < maxAddAttempts; attempt++ {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return false, err
		}
		_, inserted, raced, err := addInTx(ctx, sp, userID, profileID, snap, addedAt, now)
		if err != nil || raced {
			_ = sp.Rollback(ctx)
			if err != nil {
				return false, err
			}
			continue
		}
		return inserted, sp.Commit(ctx)
	}
	return false, fmt.Errorf("adding watchlist title: lost the race to create it %d times", maxAddAttempts)
}
