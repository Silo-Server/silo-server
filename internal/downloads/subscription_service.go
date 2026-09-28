package downloads

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
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
	sub, err := s.prepareSubscription(ctx, userID, req, filter)
	if err != nil {
		return nil, err
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
		slog.WarnContext(ctx, "download subscription initial sync failed", "component", "downloads", "subscription_id", stored.ID, "error", err)
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
	shouldSync, err := s.applySubscriptionPatch(ctx, sub, patch)
	if err != nil {
		return nil, err
	}

	if err := s.subRepo.Update(ctx, sub); err != nil {
		return nil, err
	}

	// A paused subscription never syncs — including when this very patch
	// paused it while also changing scope. SyncSubscriptions applies the same
	// guard; registering episodes the user just stopped monitoring would make
	// the device pull them anyway.
	if !shouldSync {
		return &SubscriptionResult{Subscription: sub}, nil
	}
	registered, err := s.syncSubscription(ctx, sub)
	if err != nil {
		slog.WarnContext(ctx, "download subscription update sync failed", "component", "downloads", "subscription_id", sub.ID, "error", err)
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
			slog.WarnContext(ctx, "download subscription sync failed", "component", "downloads", "subscription_id", sub.ID, "error", err)
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
	current, err := s.subRepo.GetByID(ctx, sub.ID, sub.UserID, sub.ProfileID, sub.DeviceID)
	if err != nil {
		return 0, err
	}
	if !current.Active {
		return 0, nil
	}
	episodes, err := s.episodeRepo.ListBySeries(ctx, current.SeriesID)
	if err != nil {
		return 0, fmt.Errorf("listing episodes: %w", err)
	}
	items, err := s.subscriptionEpisodeItems(ctx, current, episodes)
	if err != nil {
		return 0, err
	}
	var registered int
	err = s.subRepo.WithLocked(ctx, current.UserID, current.ProfileID, current.DeviceID, current.ID, func(locked *Subscription, tx pgx.Tx) error {
		if !locked.Active || !locked.UpdatedAt.Equal(current.UpdatedAt) {
			return nil
		}
		registered, err = s.registerSubscriptionItems(ctx, locked, items, managedRegistryStore{tx})
		return err
	})
	return registered, err
}

// subscriptionEpisodeItems preserves scope, the delete_watched filter and
// shared file selection before a monitor lock is acquired, avoiding pool waits
// while holding that lock. The locked callers re-check the monitor's
// updated_at, so an edit to delete_watched in between cancels the sync.
func (s *Service) subscriptionEpisodeItems(ctx context.Context, sub *Subscription, episodes []*models.Episode) ([]managedItem, error) {
	inScope := make([]*models.Episode, 0, len(episodes))
	for _, ep := range episodes {
		if sub.coversEpisode(ep) {
			inScope = append(inScope, ep)
		}
	}
	if sub.DeleteWatched {
		// Fail open, like the storage gate: a progress-store outage must not
		// stop new episodes arriving. A finished episode registered meanwhile
		// is deleted by the client's retention pass, and that delete's
		// exclusion keeps it from coming back.
		if unwatched, err := s.dropWatchedEpisodes(ctx, sub, inScope); err != nil {
			slog.WarnContext(ctx, "download subscription watched filter: progress lookup failed; registering without it", "component", "downloads", "subscription_id", sub.ID, "error", err)
		} else {
			inScope = unwatched
		}
	}
	return s.episodeItems(ctx, sub.SeriesID, inScope)
}

// watchedLookupChunk bounds one progress lookup, as the catalog's playable
// targets do: the SQLite user store binds one parameter per ID, and a bridge
// sync passes a whole series.
const watchedLookupChunk = 500

// dropWatchedEpisodes removes the episodes the monitor's profile has finished.
// A delete_watched client deletes a finished download at the end of its
// monitoring run, so registering one only makes the device fetch a file it is
// about to delete. "Finished" is the progress row's completed flag, the state
// the client reads to decide what to delete.
func (s *Service) dropWatchedEpisodes(ctx context.Context, sub *Subscription, episodes []*models.Episode) ([]*models.Episode, error) {
	if s.progressStores == nil || len(episodes) == 0 {
		return episodes, nil
	}
	store, err := s.progressStores.ForUser(ctx, sub.UserID)
	if err != nil {
		return nil, fmt.Errorf("opening progress store: %w", err)
	}
	ids := make([]string, len(episodes))
	for i, ep := range episodes {
		ids[i] = ep.ContentID
	}
	watched := make(map[string]bool)
	for start := 0; start < len(ids); start += watchedLookupChunk {
		progress, err := store.ListProgressByMediaItems(ctx, sub.ProfileID, ids[start:min(start+watchedLookupChunk, len(ids))])
		if err != nil {
			return nil, fmt.Errorf("listing episode progress: %w", err)
		}
		for id, p := range progress {
			if p.Completed {
				watched[id] = true
			}
		}
	}
	if len(watched) == 0 {
		return episodes, nil
	}
	kept := make([]*models.Episode, 0, len(episodes)-len(watched))
	for _, ep := range episodes {
		if !watched[ep.ContentID] {
			kept = append(kept, ep)
		}
	}
	return kept, nil
}

// registerSubscriptionItems registers each item as a ready original managed
// entry under the monitor's batch, inside the monitor lock (repo is the lock's
// transaction). Before applying the storage cap it skips items the device
// already holds (their bytes already count toward the device's usage) and
// episodes the device deleted while monitored (see Repository.DeleteManaged),
// so neither consumes the budget. Unlike the interactive ensureManaged path it
// does NOT consume the QuantityLimiter — the subscription is the
// authorization. Returns only the NEWLY registered count: the sync response's
// "registered" is documented as new episodes, so a steady-state sync must
// report 0, not the full in-scope set.
func (s *Service) registerSubscriptionItems(ctx context.Context, sub *Subscription, items []managedItem, repo managedRegistrationRepository) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	keys := make([]ManagedEntryKey, len(items))
	for i, it := range items {
		keys[i] = ManagedEntryKey{ContentID: it.contentID, EpisodeID: it.episodeID}
	}
	candidates, err := repo.MonitorEntriesToRegister(ctx, sub, keys)
	if err != nil {
		return 0, err
	}
	fresh := make([]managedItem, 0, len(candidates))
	for i, it := range items {
		if candidates[keys[i]] {
			fresh = append(fresh, it)
		}
	}
	fresh = s.capItemsToStorage(ctx, sub, fresh, repo)
	toInsert := make([]*Download, 0, len(fresh))
	for _, it := range fresh {
		d, err := buildManagedOriginal(sub.UserID, sub.ProfileID, sub.DeviceID, it, originalDecision(), sub.ID)
		if err != nil {
			return 0, err
		}
		toInsert = append(toInsert, d)
	}
	rows, err := repo.CreateManagedEntriesBatch(ctx, toInsert)
	return len(rows), err
}

// capItemsToStorage trims items so registering them keeps the device under the
// subscription's max_storage_bytes (0 = unlimited). The server sum is a
// best-effort view; the client enforces the hard cap and owns deletion.
func (s *Service) capItemsToStorage(ctx context.Context, sub *Subscription, items []managedItem, repo managedRegistrationRepository) []managedItem {
	if sub.MaxStorageBytes <= 0 || len(items) == 0 {
		return items
	}
	used, err := repo.SumManagedFileSize(ctx, sub.UserID, sub.ProfileID, sub.DeviceID)
	if err != nil {
		slog.WarnContext(ctx, "download subscription storage gate: sum failed; skipping cap", "component", "downloads", "subscription_id", sub.ID, "error", err)
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

// prepareSubscription is the shared validation, authorization and monitor-scope
// construction used by bridge creation and the native persist-then-sync flow.
func (s *Service) prepareSubscription(ctx context.Context, userID int, req SubscriptionRequest, filter catalog.AccessFilter) (*Subscription, error) {
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

	return sub, nil
}

// applySubscriptionPatch owns merge validation and latest-season anchoring for
// both transports. The caller persists the result before starting any sync.
func (s *Service) applySubscriptionPatch(ctx context.Context, sub *Subscription, patch SubscriptionPatch) (bool, error) {
	scopeChanged := patch.Mode != nil || patch.SeasonNumbers != nil
	wasActive := sub.Active
	oldMaxStorageBytes := sub.MaxStorageBytes
	modeChanged := false
	if patch.Mode != nil {
		if !ValidSubMode(*patch.Mode) {
			return false, ErrInvalidSubscriptionMode
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
		return false, ErrSeasonsRequired
	}
	if !validSeasonNumbers(sub.SeasonNumbers) {
		return false, ErrInvalidSeasonNumbers
	}
	sub.SeasonNumbers = normalizeSeasons(sub.Mode, sub.SeasonNumbers)
	switch {
	case sub.Mode == SubModeLatestSeason && (modeChanged || sub.TargetSeason == nil):
		target, err := s.latestSeason(ctx, sub.SeriesID)
		if err != nil {
			return false, err
		}
		sub.TargetSeason = target
	case sub.Mode != SubModeLatestSeason:
		sub.TargetSeason = nil
	}
	reactivated := patch.Active != nil && !wasActive && sub.Active
	storageIncreased := patch.MaxStorageBytes != nil &&
		oldMaxStorageBytes > 0 &&
		(sub.MaxStorageBytes <= 0 || sub.MaxStorageBytes > oldMaxStorageBytes)
	return sub.Active && (scopeChanged || reactivated || storageIncreased), nil
}
