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
	return eachTitle(snaps, func(snap Snapshot) error { return ensureTitle(ctx, tx, snap, now) })
}

// LockTitlesInTx locks, in EnsureTitlesInTx's order, the titles snaps name
// that this server already has, and creates none: an untrusted document's
// snapshot is never written to a title other accounts read.
func LockTitlesInTx(ctx context.Context, tx pgx.Tx, snaps []Snapshot) error {
	return eachTitle(snaps, func(snap Snapshot) error {
		_, _, err := lockTitleByTMDB(ctx, tx, snap.MediaType, snap.TMDBID)
		return err
	})
}

// eachTitle runs fn once per title snaps name, in media type and TMDB id
// order, the order every title lock is taken in.
func eachTitle(snaps []Snapshot, fn func(Snapshot) error) error {
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
		if err := fn(snap); err != nil {
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

// LinkExistingTitleInTx adds a title to the profile's watchlist only when
// this server already has it, and writes nothing from snap to the shared
// title: an untrusted document's snapshot could carry any metadata. A title
// no entry references has a stale snapshot, so its provider check is moved
// up instead. found is false when the server has no such title.
func LinkExistingTitleInTx(ctx context.Context, tx pgx.Tx, userID int, profileID string, snap Snapshot, addedAt, now time.Time) (found, inserted bool, err error) {
	if snap.MediaType != mediaTypeMovie && snap.MediaType != mediaTypeSeries || snap.TMDBID <= 0 {
		return false, false, fmt.Errorf("watchlist: invalid title %s/%d", snap.MediaType, snap.TMDBID)
	}
	titleID, found, err := lockTitleByTMDB(ctx, tx, snap.MediaType, snap.TMDBID)
	if err != nil || !found {
		return false, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE watchlist_titles SET next_check_at = least(next_check_at, $2)
		WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM user_watchlist_titles WHERE title_id = $1)`, titleID, now); err != nil {
		return true, false, fmt.Errorf("scheduling watchlist title check: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO user_watchlist_titles (user_id, profile_id, title_id, added_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, profile_id, title_id) DO NOTHING`, userID, profileID, titleID, addedAt)
	if err != nil {
		return true, false, fmt.Errorf("adding watchlist title entry: %w", err)
	}
	return true, tag.RowsAffected() > 0, nil
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
