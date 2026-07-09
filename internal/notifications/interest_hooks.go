package notifications

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// WrapUserStoreProvider decorates the shared user-store provider so every
// favorites, watchlist, watch-progress, and watch-history mutation —
// regardless of which path performed it (REST handlers, jellycompat, history
// imports, playback stop, watch sync) — queues an interest recompute. Hooking
// the lowest shared layer keeps the seven-plus mutation call sites hook-free
// and drift-free.
//
// Progress writes queue only on state *transitions* (a row appearing, the
// in-progress flag flipping, completion crossing, rows being cleared):
// progress sync ticks fire continuously during playback on a busy server, and
// recomputing interest on every tick would be a pointless hot write path.
func WrapUserStoreProvider(inner userstore.UserStoreProvider, system *System) userstore.UserStoreProvider {
	if inner == nil || system == nil {
		return inner
	}
	return &interestTrackingProvider{inner: inner, system: system}
}

type interestTrackingProvider struct {
	inner  userstore.UserStoreProvider
	system *System
}

func (p *interestTrackingProvider) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	store, err := p.inner.ForUser(ctx, userID)
	if err != nil || store == nil {
		return store, err
	}
	tracked := &interestTrackingStore{UserStore: store, userID: userID, system: p.system, updater: p.system.Interest}
	// Preserve the DeviceRegistry interface upgrade some callers probe for.
	if registry, ok := store.(userstore.DeviceRegistry); ok {
		return &interestTrackingStoreWithDevices{
			interestTrackingStore: tracked,
			DeviceRegistry:        registry,
		}, nil
	}
	return tracked, nil
}

func (p *interestTrackingProvider) Close() error {
	return p.inner.Close()
}

type interestTrackingStore struct {
	userstore.UserStore
	userID  int
	system  *System
	updater *InterestUpdater
}

type interestTrackingStoreWithDevices struct {
	*interestTrackingStore
	userstore.DeviceRegistry
}

// progressState is the transition-relevant projection of a progress row.
type progressState struct {
	exists     bool
	inProgress bool
	completed  bool
}

func (s *interestTrackingStore) currentProgressState(ctx context.Context, profileID, mediaItemID string) progressState {
	entry, err := s.GetProgress(ctx, profileID, mediaItemID)
	if err != nil || entry == nil {
		return progressState{}
	}
	return progressState{
		exists:     true,
		inProgress: !entry.Completed && entry.PositionSeconds > 0,
		completed:  entry.Completed,
	}
}

func progressStateFromValues(position, duration float64, thresholds userstore.ProgressThresholds) progressState {
	completed := duration > 0 && position/duration > userstore.WatchedFraction(thresholds.WatchedPct)
	return progressState{
		exists:     true,
		inProgress: !completed && position > 0,
		completed:  completed,
	}
}

func (s *interestTrackingStore) queueOnTransition(profileID, mediaItemID string, before, after progressState) {
	if before != after {
		s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
	}
}

// --- Favorites & watchlist: every mutation queues (user-action frequency).

func (s *interestTrackingStore) AddFavorite(ctx context.Context, profileID, mediaItemID string) error {
	err := s.UserStore.AddFavorite(ctx, profileID, mediaItemID)
	if err == nil {
		s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
	}
	return err
}

func (s *interestTrackingStore) AddFavoriteAt(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) error {
	err := s.UserStore.AddFavoriteAt(ctx, profileID, mediaItemID, addedAt)
	if err == nil {
		s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
	}
	return err
}

func (s *interestTrackingStore) RemoveFavorite(ctx context.Context, profileID, mediaItemID string) error {
	err := s.UserStore.RemoveFavorite(ctx, profileID, mediaItemID)
	if err == nil {
		s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
	}
	return err
}

func (s *interestTrackingStore) AddToWatchlist(ctx context.Context, profileID, mediaItemID string) error {
	err := s.UserStore.AddToWatchlist(ctx, profileID, mediaItemID)
	if err == nil {
		s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
	}
	return err
}

func (s *interestTrackingStore) RemoveFromWatchlist(ctx context.Context, profileID, mediaItemID string) error {
	err := s.UserStore.RemoveFromWatchlist(ctx, profileID, mediaItemID)
	if err == nil {
		s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
	}
	return err
}

// --- Progress: queue on transitions only.

func (s *interestTrackingStore) UpdateProgress(ctx context.Context, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	before := s.currentProgressState(ctx, profileID, mediaItemID)
	err := s.UserStore.UpdateProgress(ctx, profileID, mediaItemID, position, duration, thresholds)
	if err == nil {
		s.queueOnTransition(profileID, mediaItemID, before, progressStateFromValues(position, duration, thresholds))
	}
	return err
}

func (s *interestTrackingStore) SetProgress(ctx context.Context, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	before := s.currentProgressState(ctx, profileID, mediaItemID)
	err := s.UserStore.SetProgress(ctx, profileID, mediaItemID, position, duration, thresholds)
	if err == nil {
		s.queueOnTransition(profileID, mediaItemID, before, progressStateFromValues(position, duration, thresholds))
	}
	return err
}

func (s *interestTrackingStore) SetProgressAt(ctx context.Context, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) error {
	before := s.currentProgressState(ctx, profileID, mediaItemID)
	err := s.UserStore.SetProgressAt(ctx, profileID, mediaItemID, position, duration, completed, updatedAt)
	if err == nil {
		after := progressState{exists: true, inProgress: !completed && position > 0, completed: completed}
		s.queueOnTransition(profileID, mediaItemID, before, after)
	}
	return err
}

func (s *interestTrackingStore) SetProgressIfNewer(ctx context.Context, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) (bool, error) {
	before := s.currentProgressState(ctx, profileID, mediaItemID)
	applied, err := s.UserStore.SetProgressIfNewer(ctx, profileID, mediaItemID, position, duration, completed, updatedAt)
	if err == nil && applied {
		after := progressState{exists: true, inProgress: !completed && position > 0, completed: completed}
		s.queueOnTransition(profileID, mediaItemID, before, after)
	}
	return applied, err
}

func (s *interestTrackingStore) MarkWatched(ctx context.Context, profileID, mediaItemID string, duration float64) error {
	before := s.currentProgressState(ctx, profileID, mediaItemID)
	err := s.UserStore.MarkWatched(ctx, profileID, mediaItemID, duration)
	if err == nil {
		s.queueOnTransition(profileID, mediaItemID, before, progressState{exists: true, completed: true})
	}
	return err
}

func (s *interestTrackingStore) MarkProgressBatch(ctx context.Context, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	beforeStates, _ := s.ListProgressByMediaItems(ctx, profileID, mediaItemIDs)
	err := s.UserStore.MarkProgressBatch(ctx, profileID, mediaItemIDs, updatedAt)
	if err == nil {
		for _, mediaItemID := range mediaItemIDs {
			if entry, ok := beforeStates[mediaItemID]; ok && entry.Completed {
				continue // already completed: no transition
			}
			s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
		}
	}
	return err
}

func (s *interestTrackingStore) ClearProgress(ctx context.Context, profileID, mediaItemID string) error {
	err := s.UserStore.ClearProgress(ctx, profileID, mediaItemID)
	if err == nil {
		s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
	}
	return err
}

func (s *interestTrackingStore) ClearProgressBatch(ctx context.Context, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	err := s.UserStore.ClearProgressBatch(ctx, profileID, mediaItemIDs, updatedAt)
	if err == nil {
		for _, mediaItemID := range mediaItemIDs {
			s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
		}
	}
	return err
}

// --- Watch history: history imports and watch-provider syncs may record a
// completed watch without any progress write, so the progress hooks alone
// would never see them. AddHistory (the live playback path) is deliberately
// not hooked: playback always writes progress alongside it, and those writes
// already queue on transitions.

func (s *interestTrackingStore) AddHistoryIfMissing(ctx context.Context, entry userstore.WatchHistoryEntry) (bool, error) {
	created, err := s.UserStore.AddHistoryIfMissing(ctx, entry)
	if err == nil && created && entry.Completed {
		s.updater.QueueItemMutation(s.userID, entry.ProfileID, entry.MediaItemID)
	}
	return created, err
}

func (s *interestTrackingStore) RemoveHistoryItems(ctx context.Context, profileID string, mediaItemIDs []string, removedAt time.Time) error {
	err := s.UserStore.RemoveHistoryItems(ctx, profileID, mediaItemIDs, removedAt)
	if err == nil {
		for _, mediaItemID := range mediaItemIDs {
			s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
		}
	}
	return err
}

func (s *interestTrackingStore) DeleteHistoryBySource(ctx context.Context, profileID string, mediaItemIDs []string, source userstore.WatchHistorySource) error {
	err := s.UserStore.DeleteHistoryBySource(ctx, profileID, mediaItemIDs, source)
	if err == nil {
		for _, mediaItemID := range mediaItemIDs {
			s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
		}
	}
	return err
}

// DeleteProfile purges notification state alongside the profile itself;
// profiles may live outside Postgres, so no cascade covers these tables.
// The purge is best-effort: a failure is logged, never surfaced as a
// profile-deletion failure (the retention task prunes leftovers).
func (s *interestTrackingStore) DeleteProfile(ctx context.Context, id string) error {
	err := s.UserStore.DeleteProfile(ctx, id)
	if err == nil {
		purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if purgeErr := s.system.PurgeProfile(purgeCtx, id); purgeErr != nil {
			slog.WarnContext(ctx, "notifications: profile purge failed", "component", "notifications", "profile_id", id, "error", purgeErr)
		}
	}
	return err
}
