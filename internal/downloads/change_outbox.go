package downloads

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChangeEventName is the administrator event a managed download's change
// publishes on the downloads realtime channel (docs/realtime-api.md).
const ChangeEventName = "download.changed"

// What a download.changed event reports. A failure or a revocation is a
// changed row whose status says so.
const (
	ChangeCreated = "created"
	ChangeChanged = "changed"
	ChangeRemoved = "removed"
)

// The outbox drains at most changeBatch rows per changeDrainInterval across
// the cluster, as the catalog item change outbox does: each events connection
// buffers 64 envelopes, so a burst (a whole device revoked, an account with
// its downloads deleted) cannot crowd out the other channels' frames.
const (
	changeBatch         = 32
	changeDrainInterval = 100 * time.Millisecond
	changeIdlePoll      = time.Second
	// changeRetention drops changes nobody published for a day, such as
	// those queued while no API server ran.
	changeRetention = 24 * time.Hour
)

// ChangeEvent is the data of a download.changed event: identifiers and state
// only, never an artifact, path, storage key or error text. IDs are v2
// strings.
type ChangeEvent struct {
	DownloadID string `json:"download_id"`
	UserID     string `json:"user_id"`
	ProfileID  string `json:"profile_id"`
	DeviceID   string `json:"device_id"`
	ContentID  string `json:"content_id"`
	EpisodeID  string `json:"episode_id,omitempty"`
	Status     string `json:"status"`
	Change     string `json:"change"`
	// At is when the change was written, in the database's clock.
	At string `json:"at"`

	// seq is the outbox row id; publishers use it as the event id.
	seq int64
}

// EventID is a stable id for the envelope that carries the change.
func (e ChangeEvent) EventID() string { return "download-change:" + strconv.FormatInt(e.seq, 10) }

// ChangeOutbox drains download_change_events, which triggers on downloads
// fill (migration 20261119022849_download_change_events.sql). Every API
// server may call Claim; the drain row lets one claim through per interval
// across the cluster.
type ChangeOutbox struct {
	pool *pgxpool.Pool
}

func NewChangeOutbox(pool *pgxpool.Pool) *ChangeOutbox {
	return &ChangeOutbox{pool: pool}
}

// Claim deletes and returns up to limit of the oldest queued changes, in the
// order they were queued. It returns nothing when the outbox is empty or any
// server claimed less than interval ago; more reports a full batch. Rows
// older than retention are dropped unpublished. Claimed rows are gone even if
// the caller fails to publish them: delivery is at most once, and consumers
// re-read the admin download routes when they (re)subscribe.
func (o *ChangeOutbox) Claim(ctx context.Context, limit int, interval, retention time.Duration) (changes []ChangeEvent, more bool, err error) {
	tx, err := o.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, false, fmt.Errorf("claiming download changes: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The row lock serializes servers; drained_at is stamped just before
	// commit, so one that waited finds the interval not yet over.
	var gated bool
	err = tx.QueryRow(ctx, `
		SELECT true FROM download_change_drain
		WHERE drained_at <= clock_timestamp() - $1::interval
			AND EXISTS (SELECT 1 FROM download_change_events)
		FOR UPDATE`, interval).Scan(&gated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claiming download changes: %w", err)
	}

	rows, err := tx.Query(ctx, `
		DELETE FROM download_change_events
		WHERE id IN (SELECT id FROM download_change_events ORDER BY id LIMIT $1)
		RETURNING id, download_id, user_id, profile_id, device_id, content_id, COALESCE(episode_id, ''), status, change, created_at`, limit)
	if err != nil {
		return nil, false, fmt.Errorf("claiming download changes: %w", err)
	}
	defer rows.Close()
	cutoff := time.Now().Add(-retention)
	claimed := 0
	for rows.Next() {
		var (
			e      ChangeEvent
			userID int
			at     time.Time
		)
		if err := rows.Scan(&e.seq, &e.DownloadID, &userID, &e.ProfileID, &e.DeviceID, &e.ContentID, &e.EpisodeID, &e.Status, &e.Change, &at); err != nil {
			return nil, false, fmt.Errorf("scanning download change: %w", err)
		}
		claimed++
		if at.Before(cutoff) {
			continue
		}
		e.UserID = strconv.Itoa(userID)
		e.At = at.UTC().Format("2006-01-02T15:04:05.000Z")
		changes = append(changes, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("claiming download changes: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE download_change_drain SET drained_at = clock_timestamp()`); err != nil {
		return nil, false, fmt.Errorf("claiming download changes: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("claiming download changes: %w", err)
	}
	// RETURNING has no order.
	slices.SortFunc(changes, func(a, b ChangeEvent) int { return cmp.Compare(a.seq, b.seq) })
	return changes, claimed >= limit, nil
}

// RunChangePublisher hands each queued change to publish until ctx ends.
// publish should fan the event out to every API server's sockets.
func RunChangePublisher(ctx context.Context, outbox *ChangeOutbox, publish func(context.Context, ChangeEvent) error) {
	if outbox == nil || publish == nil {
		return
	}
	timer := time.NewTimer(changeIdlePoll)
	defer timer.Stop()
	// Each failure is logged once until the next success.
	claimFailing, publishFailing := false, false
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		changes, more, err := outbox.Claim(ctx, changeBatch, changeDrainInterval, changeRetention)
		if err != nil && ctx.Err() == nil && !claimFailing {
			slog.WarnContext(ctx, "download change outbox: claim failed", "component", "downloads", "error", err)
		}
		claimFailing = err != nil
		var failed int
		var publishErr error
		for _, change := range changes {
			if err := publish(ctx, change); err != nil {
				failed++
				publishErr = err
			}
		}
		if failed > 0 && ctx.Err() == nil && !publishFailing {
			slog.WarnContext(ctx, "download change: publish to other servers failed; consumers recover by re-listing",
				"component", "downloads", "failed", failed, "claimed", len(changes), "error", publishErr)
		}
		if len(changes) > 0 {
			publishFailing = failed > 0
		}
		if more {
			timer.Reset(changeDrainInterval)
		} else {
			timer.Reset(changeIdlePoll)
		}
	}
}
