package catalog

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Changes catalog.item.changed carries for the administrator catalog mirror.
// Triggers on the membership tables and media_files queue them (see migration
// 20261027195324_catalog_item_change_events.sql).
const (
	ItemChangeAdded        = "added"
	ItemChangeRemoved      = "removed"
	ItemChangeFilesChanged = "files_changed"
)

// ItemChange is one coalesced change to an item in one library. ContentID
// names a movie, series or episode.
type ItemChange struct {
	ContentID string
	LibraryID int
	Change    string
}

// ItemChangeOutbox drains catalog_item_change_events. Every API server may
// call Claim; the drain row lets one claim through per interval across the
// cluster.
type ItemChangeOutbox struct {
	pool *pgxpool.Pool
}

func NewItemChangeOutbox(pool *pgxpool.Pool) *ItemChangeOutbox {
	return &ItemChangeOutbox{pool: pool}
}

// itemChangeRow is one outbox row as Claim reads it.
type itemChangeRow struct {
	ID        int64
	ContentID string
	LibraryID int
	Change    string
	CreatedAt time.Time
}

// Claim takes up to limit of the oldest outbox rows, plus every later row for
// the same item and library, deletes them, and returns their net changes. It
// returns nothing when the outbox is empty or any server claimed less than
// interval ago. more reports a full batch, so the caller can claim again after
// interval instead of idling. Rows older than retention are dropped
// unpublished: a consumer that was connected then has reconnected since and
// re-listed. Claimed rows are gone even if the caller fails to publish them;
// delivery is at most once.
func (o *ItemChangeOutbox) Claim(ctx context.Context, limit int, interval, retention time.Duration) (changes []ItemChange, more bool, err error) {
	tx, err := o.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, false, fmt.Errorf("claiming catalog item changes: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The row lock serializes servers. drained_at is stamped just before
	// commit, so one that waited re-reads it and finds the interval not yet
	// over, however long the claim it waited for took.
	var gated bool
	err = tx.QueryRow(ctx, `
		SELECT true FROM catalog_item_change_drain
		WHERE drained_at <= clock_timestamp() - $1::interval
			AND EXISTS (SELECT 1 FROM catalog_item_change_events)
		FOR UPDATE`, interval).Scan(&gated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claiming catalog item changes: %w", err)
	}

	rows, err := tx.Query(ctx, `
		WITH head AS (
			SELECT DISTINCT content_id, library_id
			FROM (SELECT content_id, library_id FROM catalog_item_change_events ORDER BY id LIMIT $1) oldest
		)
		DELETE FROM catalog_item_change_events e
		USING head
		WHERE e.content_id = head.content_id AND e.library_id = head.library_id
		RETURNING e.id, e.content_id, e.library_id, e.change, e.created_at`, limit)
	if err != nil {
		return nil, false, fmt.Errorf("claiming catalog item changes: %w", err)
	}
	defer rows.Close()
	var claimed []itemChangeRow
	for rows.Next() {
		var row itemChangeRow
		if err := rows.Scan(&row.ID, &row.ContentID, &row.LibraryID, &row.Change, &row.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("scanning catalog item change: %w", err)
		}
		claimed = append(claimed, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("claiming catalog item changes: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE catalog_item_change_drain SET drained_at = clock_timestamp()`); err != nil {
		return nil, false, fmt.Errorf("claiming catalog item changes: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("claiming catalog item changes: %w", err)
	}
	// RETURNING has no order; coalescing needs the rows as they were queued.
	slices.SortFunc(claimed, func(a, b itemChangeRow) int { return cmp.Compare(a.ID, b.ID) })
	return coalesceItemChanges(claimed, time.Now().Add(-retention)), len(claimed) >= limit, nil
}

type itemChangeKey struct {
	contentID string
	libraryID int
}

// coalesceItemChanges reduces rows, in queue order, to at most one change per
// item and library, in the order each key first appeared. Membership wins: a
// key whose last membership row is an add is added, also after a removal,
// since a re-created membership can carry a new first-seen time; one whose
// rows are all removals is removed. A key added and then removed was absent
// before and after: it yields files_changed if a file changed, else nothing.
// Rows queued before cutoff are left out.
func coalesceItemChanges(rows []itemChangeRow, cutoff time.Time) []ItemChange {
	type state struct {
		first, last string
		files       bool
	}
	var order []itemChangeKey
	states := make(map[itemChangeKey]*state)
	for _, row := range rows {
		if row.CreatedAt.Before(cutoff) {
			continue
		}
		key := itemChangeKey{row.ContentID, row.LibraryID}
		s := states[key]
		if s == nil {
			s = &state{}
			states[key] = s
			order = append(order, key)
		}
		switch row.Change {
		case ItemChangeAdded, ItemChangeRemoved:
			if s.first == "" {
				s.first = row.Change
			}
			s.last = row.Change
		case ItemChangeFilesChanged:
			s.files = true
		}
	}
	changes := make([]ItemChange, 0, len(order))
	for _, key := range order {
		s := states[key]
		var change string
		switch {
		case s.last == ItemChangeAdded:
			change = ItemChangeAdded
		case s.first == ItemChangeRemoved:
			change = ItemChangeRemoved
		case s.files:
			change = ItemChangeFilesChanged
		default:
			continue
		}
		changes = append(changes, ItemChange{ContentID: key.contentID, LibraryID: key.libraryID, Change: change})
	}
	return changes
}
