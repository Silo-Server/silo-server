package downloads

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
)

// SubscriptionResult is the outcome of creating/updating a subscription: the
// stored subscription plus how many in-scope episodes were registered as managed
// downloads.
type SubscriptionResult struct {
	Subscription *Subscription
	Registered   int
}

// CreateSubscription creates (or idempotently re-creates) a device-scoped series
// monitor and registers the in-scope episodes that already exist as managed
// downloads. New episodes are picked up by later client-triggered syncs (see
// SyncSubscriptions). Series monitoring is original-only, like the rest of the
// series flow.
func (s *Service) CreateSubscription(ctx context.Context, userID int, req SubscriptionRequest, filter catalog.AccessFilter) (*SubscriptionResult, error) {
	if s.subRepo == nil {
		return nil, ErrSubscriptionsUnavailable
	}
	if _, _, err := s.downloadConfigForUser(ctx, userID, req.DeviceID); err != nil {
		return nil, err
	}
	if req.ProfileID == "" || req.DeviceID == "" {
		return nil, ErrProfileRequired
	}
	if !ValidSubMode(req.Mode) {
		return nil, ErrInvalidSubscriptionMode
	}
	if req.Mode == SubModeSpecificSeasons && len(req.SeasonNumbers) == 0 {
		return nil, ErrSeasonsRequired
	}
	if !validSeasonNumbers(req.SeasonNumbers) {
		return nil, ErrInvalidSeasonNumbers
	}

	item, err := s.itemRepo.GetByID(ctx, req.SeriesID)
	if err != nil {
		return nil, fmt.Errorf("loading series: %w", err)
	}
	if item.Type != "series" {
		return nil, ErrNotSeries
	}
	if err := s.itemAccess.EnsureAccessible(ctx, req.SeriesID, filter); err != nil {
		return nil, err
	}

	// The device row must exist for the subscription's composite FK.
	if err := s.repo.EnsureDevice(ctx, userID, req.ProfileID, req.DeviceID, req.DeviceName, req.DevicePlatform); err != nil {
		return nil, err
	}

	id, err := idgen.NextID()
	if err != nil {
		return nil, fmt.Errorf("generating subscription ID: %w", err)
	}
	sub := &Subscription{
		ID:              id,
		UserID:          userID,
		ProfileID:       req.ProfileID,
		DeviceID:        req.DeviceID,
		SeriesID:        req.SeriesID,
		Mode:            req.Mode,
		SeasonNumbers:   normalizeSeasons(req.Mode, req.SeasonNumbers),
		DeleteWatched:   req.DeleteWatched,
		MaxStorageBytes: req.MaxStorageBytes,
		Active:          true,
	}
	if req.Mode == SubModeLatestSeason {
		target, err := s.latestSeason(ctx, req.SeriesID)
		if err != nil {
			return nil, err
		}
		sub.TargetSeason = target
	}

	stored, err := s.subRepo.Upsert(ctx, sub)
	if err != nil {
		return nil, err
	}

	// The initial registration is best-effort: the subscription is durably stored
	// and later client syncs still pick up episodes; a transient failure here can
	// be retried by syncing or re-monitoring. So log it rather than failing.
	registered, err := s.syncSubscription(ctx, stored)
	if err != nil {
		slog.Warn("download subscription initial sync failed", "subscription_id", stored.ID, "error", err)
	}
	return &SubscriptionResult{Subscription: stored, Registered: registered}, nil
}

// ListSubscriptions returns the calling device's subscriptions.
func (s *Service) ListSubscriptions(ctx context.Context, userID int, profileID, deviceID string) ([]*Subscription, error) {
	if s.subRepo == nil {
		return nil, ErrSubscriptionsUnavailable
	}
	if profileID == "" || deviceID == "" {
		return nil, ErrProfileRequired
	}
	return s.subRepo.ListByDevice(ctx, userID, profileID, deviceID)
}

// GetSubscription returns one subscription, authorized on (user, profile, device).
func (s *Service) GetSubscription(ctx context.Context, userID int, profileID, deviceID, id string) (*Subscription, error) {
	if s.subRepo == nil {
		return nil, ErrSubscriptionsUnavailable
	}
	if profileID == "" || deviceID == "" {
		return nil, ErrProfileRequired
	}
	return s.subRepo.GetByID(ctx, id, userID, profileID, deviceID)
}

// UpdateSubscription applies a partial update, re-anchoring the latest-season
// target when the mode changes, then registers any newly in-scope episodes.
func (s *Service) UpdateSubscription(ctx context.Context, userID int, profileID, deviceID, id string, patch SubscriptionPatch, filter catalog.AccessFilter) (*SubscriptionResult, error) {
	if s.subRepo == nil {
		return nil, ErrSubscriptionsUnavailable
	}
	if profileID == "" || deviceID == "" {
		return nil, ErrProfileRequired
	}
	// Same feature/permission gate as CreateSubscription and SyncSubscriptions:
	// a patch can re-activate or widen a monitor and backfill managed rows, so
	// it must not bypass an admin disabling downloads or revoking the user.
	if _, _, err := s.downloadConfigForUser(ctx, userID, deviceID); err != nil {
		return nil, err
	}
	sub, err := s.subRepo.GetByID(ctx, id, userID, profileID, deviceID)
	if err != nil {
		return nil, err
	}
	// Re-check the requesting profile's access to the series before mutating or
	// backfilling — access may have been revoked since the subscription was made.
	if err := s.itemAccess.EnsureAccessible(ctx, sub.SeriesID, filter); err != nil {
		return nil, err
	}
	scopeChanged := patch.Mode != nil || patch.SeasonNumbers != nil
	wasActive := sub.Active
	oldMaxStorageBytes := sub.MaxStorageBytes
	modeChanged := false
	if patch.Mode != nil {
		if !ValidSubMode(*patch.Mode) {
			return nil, ErrInvalidSubscriptionMode
		}
		modeChanged = *patch.Mode != sub.Mode
		sub.Mode = *patch.Mode
	}
	if patch.SeasonNumbers != nil {
		sub.SeasonNumbers = *patch.SeasonNumbers
	}
	if patch.DeleteWatched != nil {
		sub.DeleteWatched = *patch.DeleteWatched
	}
	if patch.MaxStorageBytes != nil {
		sub.MaxStorageBytes = *patch.MaxStorageBytes
	}
	if patch.Active != nil {
		sub.Active = *patch.Active
	}
	if sub.Mode == SubModeSpecificSeasons && len(sub.SeasonNumbers) == 0 {
		return nil, ErrSeasonsRequired
	}
	if !validSeasonNumbers(sub.SeasonNumbers) {
		return nil, ErrInvalidSeasonNumbers
	}
	sub.SeasonNumbers = normalizeSeasons(sub.Mode, sub.SeasonNumbers)
	switch {
	case sub.Mode == SubModeLatestSeason && (modeChanged || sub.TargetSeason == nil):
		target, err := s.latestSeason(ctx, sub.SeriesID)
		if err != nil {
			return nil, err
		}
		sub.TargetSeason = target
	case sub.Mode != SubModeLatestSeason:
		sub.TargetSeason = nil
	}
	if err := s.subRepo.Update(ctx, sub); err != nil {
		return nil, err
	}
	reactivated := patch.Active != nil && !wasActive && sub.Active
	storageIncreased := patch.MaxStorageBytes != nil &&
		oldMaxStorageBytes > 0 &&
		(sub.MaxStorageBytes <= 0 || sub.MaxStorageBytes > oldMaxStorageBytes)
	// A paused subscription never syncs — including when this very patch
	// paused it while also changing scope. SyncSubscriptions applies the same
	// guard; registering episodes the user just stopped monitoring would make
	// the device pull them anyway.
	if !sub.Active || (!scopeChanged && !reactivated && !storageIncreased) {
		return &SubscriptionResult{Subscription: sub}, nil
	}
	registered, err := s.syncSubscription(ctx, sub)
	if err != nil {
		slog.Warn("download subscription update sync failed", "subscription_id", sub.ID, "error", err)
	}
	return &SubscriptionResult{Subscription: sub, Registered: registered}, nil
}

// DeleteSubscription stops monitoring a series for the device. It never deletes
// already-downloaded episodes — the client owns on-device deletion.
func (s *Service) DeleteSubscription(ctx context.Context, userID int, profileID, deviceID, id string) error {
	if s.subRepo == nil {
		return ErrSubscriptionsUnavailable
	}
	if profileID == "" || deviceID == "" {
		return ErrProfileRequired
	}
	return s.subRepo.Delete(ctx, id, userID, profileID, deviceID)
}

// SyncSubscriptions registers newly in-scope episodes for all of the calling
// device's active monitors. The client calls this on open / background refresh
// and then pulls the registered files on its own schedule — so monitoring needs
// no server-side worker and no notifications subsystem. Per-series access is
// re-checked, and one series failing does not fail the whole sync.
func (s *Service) SyncSubscriptions(ctx context.Context, userID int, profileID, deviceID string, filter catalog.AccessFilter) (int, error) {
	if s.subRepo == nil {
		return 0, ErrSubscriptionsUnavailable
	}
	cfg, err := s.downloadConfigForFeature(ctx, userID, deviceID)
	if err != nil {
		return 0, err
	}
	if profileID == "" || deviceID == "" {
		return 0, ErrProfileRequired
	}
	if _, err := s.downloadUserForConfig(ctx, userID, cfg, deviceID); err != nil {
		return 0, err
	}
	subs, err := s.subRepo.ListByDevice(ctx, userID, profileID, deviceID)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, sub := range subs {
		if !sub.Active {
			continue
		}
		if err := s.itemAccess.EnsureAccessible(ctx, sub.SeriesID, filter); err != nil {
			continue // series no longer accessible to this profile; skip it silently
		}
		n, err := s.syncSubscription(ctx, sub)
		if err != nil {
			slog.Warn("download subscription sync failed", "subscription_id", sub.ID, "error", err)
			continue
		}
		total += n
	}
	return total, nil
}

// syncSubscription registers the in-scope, available episodes a subscription
// covers as managed downloads (idempotent — already-registered episodes are
// skipped) and returns how many were NEWLY registered, so a steady-state sync
// reports 0. Run at create/update time and on each client-triggered sync. One
// ListBySeries + coversEpisode filter handles every mode, including latest_season
// following new seasons (>= TargetSeason) and future-only excluding the back
// catalog (aired on/after the subscribe day).
func (s *Service) syncSubscription(ctx context.Context, sub *Subscription) (int, error) {
	episodes, err := s.episodeRepo.ListBySeries(ctx, sub.SeriesID)
	if err != nil {
		return 0, fmt.Errorf("listing episodes: %w", err)
	}
	inScope := make([]*models.Episode, 0, len(episodes))
	for _, ep := range episodes {
		if sub.coversEpisode(ep) {
			inScope = append(inScope, ep)
		}
	}
	items, err := s.episodeItems(ctx, sub.SeriesID, inScope)
	if err != nil {
		return 0, err
	}
	items = s.capItemsToStorage(ctx, sub, items)
	rows, err := registerManagedItems(ctx, s.repo, sub.UserID, sub.ProfileID, sub.DeviceID, items, sub.ID)
	return len(rows), err
}

// capItemsToStorage trims items so registering them keeps the device under the
// subscription's max_storage_bytes (0 = unlimited). The server sum is a
// best-effort view; the client enforces the hard cap and owns deletion.
func (s *Service) capItemsToStorage(ctx context.Context, sub *Subscription, items []managedItem) []managedItem {
	if sub.MaxStorageBytes <= 0 || len(items) == 0 {
		return items
	}
	used, err := s.repo.SumManagedFileSize(ctx, sub.UserID, sub.ProfileID, sub.DeviceID)
	if err != nil {
		slog.Warn("download subscription storage gate: sum failed; skipping cap", "subscription_id", sub.ID, "error", err)
		return items
	}
	kept := make([]managedItem, 0, len(items))
	for _, it := range items {
		if !sub.Admits(used, it.file.FileSize) {
			break
		}
		used += it.file.FileSize
		kept = append(kept, it)
	}
	return kept
}

// latestSeason returns the highest season number that has available episodes,
// used to anchor a latest_season subscription. Returns nil when the series has
// no available seasons.
func (s *Service) latestSeason(ctx context.Context, seriesID string) (*int, error) {
	seasons, err := s.episodeRepo.ListSeasons(ctx, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing seasons: %w", err)
	}
	if len(seasons) == 0 {
		return nil, nil
	}
	max := seasons[0].SeasonNumber
	for _, season := range seasons[1:] {
		if season.SeasonNumber > max {
			max = season.SeasonNumber
		}
	}
	return &max, nil
}

// maxSeasonNumber bounds client-supplied season numbers (Specials are season
// 0). Values are persisted as int32, so an unchecked int could silently wrap
// to an unrelated — possibly negative — season.
const maxSeasonNumber = 9999

// validSeasonNumbers reports whether every season number is within
// [0, maxSeasonNumber].
func validSeasonNumbers(nums []int) bool {
	for _, n := range nums {
		if n < 0 || n > maxSeasonNumber {
			return false
		}
	}
	return true
}

// normalizeSeasons keeps explicit season numbers only for specific-seasons mode;
// other modes derive their scope from the mode itself.
func normalizeSeasons(mode string, seasons []int) []int {
	if mode != SubModeSpecificSeasons {
		return nil
	}
	return seasons
}
