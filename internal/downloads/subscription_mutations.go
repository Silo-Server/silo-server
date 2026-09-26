package downloads

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5"
)

// CreateSubscriptionMonitor persists a native monitor without overwriting an
// existing monitor's options or triggering unbounded backfill. Clients explicitly
// sync after receipt; the bridge retains its immediate best-effort backfill.
func (s *Service) CreateSubscriptionMonitor(ctx context.Context, userID int, req SubscriptionRequest, filter catalog.AccessFilter) (*Subscription, error) {
	sub, err := s.prepareSubscription(ctx, userID, req, filter)
	if err != nil {
		return nil, err
	}
	return s.subRepo.CreateOrGet(ctx, sub)
}

// CreateOrGet never turns a repeated create into an edit of a newer monitor.
func (r *SubscriptionRepository) CreateOrGet(ctx context.Context, sub *Subscription) (*Subscription, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO download_subscriptions
 (id,user_id,profile_id,device_id,series_id,mode,season_numbers,target_season,delete_watched,max_storage_bytes,active,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,now(),now())
 ON CONFLICT(user_id,profile_id,device_id,series_id) DO NOTHING`, sub.ID, sub.UserID, sub.ProfileID, sub.DeviceID, sub.SeriesID, sub.Mode, intsToInt32s(sub.SeasonNumbers), int32Ptr(sub.TargetSeason), sub.DeleteWatched, sub.MaxStorageBytes)
	if err != nil {
		return nil, err
	}
	stored, err := scanSubscription(tx.QueryRow(ctx, `SELECT `+subscriptionColumns+` FROM download_subscriptions WHERE user_id=$1 AND profile_id=$2 AND device_id=$3 AND series_id=$4`, sub.UserID, sub.ProfileID, sub.DeviceID, sub.SeriesID))
	if err != nil {
		return nil, err
	}
	// Clients patch a monitor they know, so a create that finds one (a monitor
	// an earlier install left behind, say) asks for the series from scratch:
	// forget the episodes deleted under it.
	if _, err := tx.Exec(ctx, `DELETE FROM download_subscription_exclusions WHERE subscription_id = $1`, stored.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return stored, nil
}

// UpdateSubscriptionMonitor validates the current row while locked, merges with
// the shared bridge rules and returns the stored timestamp. It does not register
// episodes from a stale pre-edit snapshot: backfill belongs to explicit sync.
func (s *Service) UpdateSubscriptionMonitor(ctx context.Context, userID int, profileID, deviceID, id string, patch SubscriptionPatch, filter catalog.AccessFilter, check func(*Subscription) error) (*Subscription, error) {
	if s.subRepo == nil {
		return nil, ErrSubscriptionsUnavailable
	}
	if profileID == "" || deviceID == "" {
		return nil, ErrProfileRequired
	}
	if _, _, err := s.downloadConfigForUser(ctx, userID, deviceID); err != nil {
		return nil, err
	}

	current, err := s.subRepo.GetByID(ctx, id, userID, profileID, deviceID)
	if err != nil {
		return nil, err
	}
	if err := s.itemAccess.EnsureAccessible(ctx, current.SeriesID, filter); err != nil {
		return nil, err
	}
	if err := check(current); err != nil {
		return nil, err
	}
	candidate := *current
	if _, err := s.applySubscriptionPatch(ctx, &candidate, patch); err != nil {
		return nil, err
	}
	return s.subRepo.Mutate(ctx, userID, profileID, deviceID, id, false, func(locked *Subscription) error {
		if err := check(locked); err != nil {
			return err
		}
		if !locked.UpdatedAt.Equal(current.UpdatedAt) {
			return ErrStatusConflict
		}
		locked.Mode = candidate.Mode
		locked.SeasonNumbers = candidate.SeasonNumbers
		locked.TargetSeason = candidate.TargetSeason
		locked.DeleteWatched = candidate.DeleteWatched
		locked.MaxStorageBytes = candidate.MaxStorageBytes
		locked.Active = candidate.Active
		return nil
	})
}
func (s *Service) DeleteSubscriptionMonitor(ctx context.Context, userID int, profileID, deviceID, id string, check func(*Subscription) error) error {
	if s.subRepo == nil {
		return ErrSubscriptionsUnavailable
	}
	if profileID == "" || deviceID == "" {
		return ErrProfileRequired
	}
	_, err := s.subRepo.Mutate(ctx, userID, profileID, deviceID, id, true, check)
	return err
}

// Mutate holds the identity-authorized monitor lock across validation and write.
// Deletion only stops monitoring; already-registered downloads are untouched.
func (r *SubscriptionRepository) Mutate(ctx context.Context, userID int, profileID, deviceID, id string, remove bool, change func(*Subscription) error) (*Subscription, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := scanSubscription(tx.QueryRow(ctx, `SELECT `+subscriptionColumns+` FROM download_subscriptions WHERE id=$1 AND user_id=$2 AND profile_id=$3 AND device_id=$4 FOR UPDATE`, id, userID, profileID, deviceID))
	if err != nil {
		return nil, err
	}
	if err := change(row); err != nil {
		return nil, err
	}
	if remove {
		_, err = tx.Exec(ctx, `DELETE FROM download_subscriptions WHERE id=$1`, id)
	} else {
		row, err = scanSubscription(tx.QueryRow(ctx, `UPDATE download_subscriptions SET mode=$2,season_numbers=$3,target_season=$4,delete_watched=$5,max_storage_bytes=$6,active=$7,updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING `+subscriptionColumns, id, row.Mode, intsToInt32s(row.SeasonNumbers), int32Ptr(row.TargetSeason), row.DeleteWatched, row.MaxStorageBytes, row.Active))
	}
	if err != nil {
		return nil, fmt.Errorf("mutating subscription: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return row, nil
}

// WithLocked serializes registration with monitor edits/deletion. The callback
// uses the current row, so a delayed sync cannot register from a paused snapshot.
func (r *SubscriptionRepository) WithLocked(ctx context.Context, userID int, profileID, deviceID, id string, run func(*Subscription, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := scanSubscription(tx.QueryRow(ctx, `SELECT `+subscriptionColumns+` FROM download_subscriptions WHERE id=$1 AND user_id=$2 AND profile_id=$3 AND device_id=$4 FOR UPDATE`, id, userID, profileID, deviceID))
	if err != nil {
		return err
	}
	if err := run(row, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
