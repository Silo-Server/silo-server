package downloads

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const subscriptionColumns = `id, user_id, profile_id, device_id, series_id, mode,
	season_numbers, target_season, delete_watched, max_storage_bytes, active, created_at, updated_at`

// SubscriptionRepository provides CRUD for download_subscriptions. Monitoring
// is client-pull only: devices sync on app open / background refresh; there is
// no server-side auto-register worker.
type SubscriptionRepository struct {
	pool *pgxpool.Pool
}

// NewSubscriptionRepository creates a SubscriptionRepository backed by pool.
func NewSubscriptionRepository(pool *pgxpool.Pool) *SubscriptionRepository {
	return &SubscriptionRepository{pool: pool}
}

func scanSubscriptionInto(row pgx.Row, s *Subscription) error {
	var seasons []int32
	var target *int32
	if err := row.Scan(
		&s.ID, &s.UserID, &s.ProfileID, &s.DeviceID, &s.SeriesID, &s.Mode,
		&seasons, &target, &s.DeleteWatched, &s.MaxStorageBytes, &s.Active,
		&s.CreatedAt, &s.UpdatedAt,
	); err != nil {
		return err
	}
	s.SeasonNumbers = int32sToInts(seasons)
	if target != nil {
		t := int(*target)
		s.TargetSeason = &t
	}
	return nil
}

func scanSubscription(row pgx.Row) (*Subscription, error) {
	var s Subscription
	if err := scanSubscriptionInto(row, &s); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSubscriptionNotFound
		}
		return nil, fmt.Errorf("scanning subscription: %w", err)
	}
	return &s, nil
}

func scanSubscriptions(rows pgx.Rows) ([]*Subscription, error) {
	var subs []*Subscription
	for rows.Next() {
		var s Subscription
		if err := scanSubscriptionInto(rows, &s); err != nil {
			return nil, fmt.Errorf("scanning subscription row: %w", err)
		}
		subs = append(subs, &s)
	}
	return subs, rows.Err()
}

// Upsert inserts the subscription, or updates the existing one for the same
// (user, profile, device, series) — re-monitoring a series is idempotent and
// rewrites the mode/options in place. created_at is preserved on update so the
// future-only cutoff stays anchored to the first subscribe. Returns the stored row.
func (r *SubscriptionRepository) Upsert(ctx context.Context, s *Subscription) (*Subscription, error) {
	const q = `INSERT INTO download_subscriptions
		(id, user_id, profile_id, device_id, series_id, mode, season_numbers, target_season,
		 delete_watched, max_storage_bytes, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, true, now(), now())
		ON CONFLICT (user_id, profile_id, device_id, series_id) DO UPDATE SET
			mode = excluded.mode,
			season_numbers = excluded.season_numbers,
			target_season = excluded.target_season,
			delete_watched = excluded.delete_watched,
			max_storage_bytes = excluded.max_storage_bytes,
			active = true,
			updated_at = now()
		RETURNING ` + subscriptionColumns
	return scanSubscription(r.pool.QueryRow(ctx, q,
		s.ID, s.UserID, s.ProfileID, s.DeviceID, s.SeriesID, s.Mode,
		intsToInt32s(s.SeasonNumbers), int32Ptr(s.TargetSeason),
		s.DeleteWatched, s.MaxStorageBytes))
}

// GetByID returns a subscription by id, authorized on (user, profile, device).
// A mismatch yields ErrSubscriptionNotFound so the endpoint never reveals the
// existence of another profile's/device's subscription.
func (r *SubscriptionRepository) GetByID(ctx context.Context, id string, userID int, profileID, deviceID string) (*Subscription, error) {
	q := `SELECT ` + subscriptionColumns + ` FROM download_subscriptions
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4`
	return scanSubscription(r.pool.QueryRow(ctx, q, id, userID, profileID, deviceID))
}

// ListByDevice returns the calling device's subscriptions, most recent first.
func (r *SubscriptionRepository) ListByDevice(ctx context.Context, userID int, profileID, deviceID string) ([]*Subscription, error) {
	q := `SELECT ` + subscriptionColumns + ` FROM download_subscriptions
		WHERE user_id = $1 AND profile_id = $2 AND device_id = $3
		ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, q, userID, profileID, deviceID)
	if err != nil {
		return nil, fmt.Errorf("listing subscriptions: %w", err)
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

// Update writes the mutable fields of s (mode/seasons/target/delete_watched/
// max_storage_bytes/active), authorized on (user, profile, device). Returns
// ErrSubscriptionNotFound when nothing matches.
func (r *SubscriptionRepository) Update(ctx context.Context, s *Subscription) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE download_subscriptions
		 SET mode = $5, season_numbers = $6, target_season = $7, delete_watched = $8,
		     max_storage_bytes = $9, active = $10, updated_at = now()
		 WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4`,
		s.ID, s.UserID, s.ProfileID, s.DeviceID,
		s.Mode, intsToInt32s(s.SeasonNumbers), int32Ptr(s.TargetSeason),
		s.DeleteWatched, s.MaxStorageBytes, s.Active,
	)
	if err != nil {
		return fmt.Errorf("updating subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

// Delete removes a subscription, authorized on (user, profile, device). It does
// NOT delete already-downloaded episodes (the client owns on-device deletion).
// Returns ErrSubscriptionNotFound when nothing matches.
func (r *SubscriptionRepository) Delete(ctx context.Context, id string, userID int, profileID, deviceID string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM download_subscriptions WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4`,
		id, userID, profileID, deviceID,
	)
	if err != nil {
		return fmt.Errorf("deleting subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

func intsToInt32s(in []int) []int32 {
	out := make([]int32, len(in))
	for i, v := range in {
		out[i] = int32(v)
	}
	return out
}

func int32sToInts(in []int32) []int {
	if len(in) == 0 {
		return nil
	}
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}

func int32Ptr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}
