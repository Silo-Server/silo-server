package notifications

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Preserve the interface upgrades callers probe for. Both are conditional
	// on the backing store: advertising a capability it does not have would
	// send callers down a fast path that can only fail.
	registry, hasDevices := store.(userstore.DeviceRegistry)
	rollup, hasRollup := store.(userstore.SeriesEpisodeRollupStore)
	switch {
	case hasDevices && hasRollup:
		return &interestTrackingStoreWithDevicesAndRollup{
			interestTrackingStore:    tracked,
			DeviceRegistry:           registry,
			SeriesEpisodeRollupStore: rollup,
		}, nil
	case hasDevices:
		return &interestTrackingStoreWithDevices{
			interestTrackingStore: tracked,
			DeviceRegistry:        registry,
		}, nil
	case hasRollup:
		return &interestTrackingStoreWithRollup{
			interestTrackingStore:    tracked,
			SeriesEpisodeRollupStore: rollup,
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

// interestTrackingStoreWithRollup adds the series-rollup capability only when
// the backing store actually has it. Unlike the other capabilities, this one
// cannot be forwarded unconditionally: the per-user SQLite backend has no
// catalog tables and genuinely cannot answer the query, and callers treat
// "implements the interface" as "can do this". Advertising it anyway would
// send every jellycompat series request down the fast path to fail, logging a
// warning each time before falling back — turning an expected capability
// absence into recurring noise on successful requests.
type interestTrackingStoreWithRollup struct {
	*interestTrackingStore
	userstore.SeriesEpisodeRollupStore
}

type interestTrackingStoreWithDevicesAndRollup struct {
	*interestTrackingStore
	userstore.DeviceRegistry
	userstore.SeriesEpisodeRollupStore
}

var _ userstore.SettingValueCompareAndSetter = (*interestTrackingStore)(nil)
var _ userstore.SettingMutationTransactioner = (*interestTrackingStore)(nil)
var _ userstore.SettingValueCompareAndSetter = (*interestTrackingStoreWithDevices)(nil)
var _ userstore.SettingMutationTransactioner = (*interestTrackingStoreWithDevices)(nil)

// Optional store capabilities must survive the decorator. Embedding the
// UserStore interface promotes only that interface's methods, so each of these
// needs an explicit forward below; the assertions make a missing one a compile
// error instead of a silent production slowdown.
//
// SeriesEpisodeRollupStore is deliberately absent here: it is conditional on
// the backing store, so it lives on the wrapper types above rather than being
// forwarded unconditionally.
var _ userstore.WatchedBatchWriter = (*interestTrackingStore)(nil)
var _ userstore.VisibleHistoryAdder = (*interestTrackingStore)(nil)
var _ userstore.HistoryVisibilityStore = (*interestTrackingStore)(nil)
var _ userstore.WatchedBatchWriter = (*interestTrackingStoreWithDevices)(nil)
var _ userstore.VisibleHistoryAdder = (*interestTrackingStoreWithDevices)(nil)
var _ userstore.HistoryVisibilityStore = (*interestTrackingStoreWithDevices)(nil)
var _ userstore.SeriesEpisodeRollupStore = (*interestTrackingStoreWithRollup)(nil)
var _ userstore.SeriesEpisodeRollupStore = (*interestTrackingStoreWithDevicesAndRollup)(nil)
var _ userstore.DeviceRegistry = (*interestTrackingStoreWithDevicesAndRollup)(nil)

// WithPreferenceSettingsTransaction preserves the optional atomic-settings
// capability of the wrapped store. Preference writes do not affect interest
// signals, so the transaction can pass through unchanged; keeping the method
// on the decorator is what lets settings handlers reach the real backend's
// transaction boundary in production.
func (s *interestTrackingStore) WithPreferenceSettingsTransaction(
	ctx context.Context,
	fn func(userstore.PreferenceSettingsWriter) error,
) error {
	transactioner, ok := s.UserStore.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		return fmt.Errorf("wrapped user store does not support atomic preference settings synchronization")
	}
	return transactioner.WithPreferenceSettingsTransaction(ctx, fn)
}

// CompareAndSetSettingValue preserves the semantic-document CAS capability of
// the wrapped store. Settings writes do not affect notification interests, so
// this decorator must not intercept or downgrade the backend primitive.
func (s *interestTrackingStore) CompareAndSetSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
	expectedRevision int64,
) (*userstore.SettingValue, error) {
	cas, ok := s.UserStore.(userstore.SettingValueCompareAndSetter)
	if !ok {
		return nil, fmt.Errorf("wrapped user store does not support atomic setting updates")
	}
	return cas.CompareAndSetSettingValue(ctx, id, value, expectedRevision)
}

// WithSettingMutationTransaction preserves the durable setting+receipt
// transaction used by mutation IDs. Passing the transaction-scoped writer
// through unchanged keeps both operations on the concrete backend transaction.
func (s *interestTrackingStore) WithSettingMutationTransaction(
	ctx context.Context,
	mutationID string,
	fn func(userstore.SettingMutationWriter) error,
) error {
	transactioner, ok := s.UserStore.(userstore.SettingMutationTransactioner)
	if !ok {
		return fmt.Errorf("wrapped user store does not support atomic idempotent setting mutations")
	}
	return transactioner.WithSettingMutationTransaction(ctx, mutationID, fn)
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

func (s *interestTrackingStore) AddFavoriteAt(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	inserted, err := s.UserStore.AddFavoriteAt(ctx, profileID, mediaItemID, addedAt)
	if err == nil && inserted {
		s.updater.QueueItemMutation(s.userID, profileID, mediaItemID)
	}
	return inserted, err
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

// --- Optional store capabilities.
//
// This decorator embeds the userstore.UserStore *interface*, which promotes
// only the methods declared on that interface. Any optional capability the
// backing store implements is invisible through the wrapper unless it is
// forwarded explicitly here — and because callers reach these via type
// assertion with a working fallback, a missing forward is silent: no error, no
// test failure, just a much slower path in production. Marking a series
// watched regressed exactly this way. When adding a new optional capability to
// userstore, forward it here and extend the compile-time assertions below.

// MarkWatchedBatch forwards the transactional batch mark-watched write, whose
// whole purpose is that a large series commits as one unit. Falling through to
// the per-target loop would restore the partial-write window it removes.
func (s *interestTrackingStore) MarkWatchedBatch(
	ctx context.Context,
	profileID string,
	targets []userstore.MarkWatchedTarget,
	entries []userstore.WatchHistoryEntry,
) ([]userstore.WatchHistoryEntry, error) {
	writer, ok := s.UserStore.(userstore.WatchedBatchWriter)
	if !ok {
		// The helper runs against the inner store, so this decorator's own
		// MarkWatched hook never fires; queue here or the recompute is lost.
		// A mid-loop error still leaves earlier targets written, so queue
		// regardless of err — a redundant queue only costs one recompute,
		// while a missing one leaves profile_series_interest stale until some
		// unrelated mutation or the rebuild task touches the series.
		written, err := userstore.MarkWatchedBatch(ctx, s.UserStore, profileID, targets, entries)
		s.queueTargetMutations(profileID, targets)
		return written, err
	}
	written, err := writer.MarkWatchedBatch(ctx, profileID, targets, entries)
	// The batch write is one transaction: on error nothing landed, so there is
	// nothing to recompute.
	if err == nil {
		s.queueItemMutations(profileID, written)
	}
	return written, err
}

// queueItemMutations records one interest mutation per written entry, matching
// what the per-target path queues through MarkWatched.
func (s *interestTrackingStore) queueItemMutations(profileID string, entries []userstore.WatchHistoryEntry) {
	for _, entry := range entries {
		s.updater.QueueItemMutation(s.userID, profileID, entry.MediaItemID)
	}
}

// queueTargetMutations queues by requested target rather than written entry,
// for the non-transactional path where a partial write may have occurred.
func (s *interestTrackingStore) queueTargetMutations(profileID string, targets []userstore.MarkWatchedTarget) {
	for _, target := range targets {
		s.updater.QueueItemMutation(s.userID, profileID, target.MediaItemID)
	}
}

// AddVisibleHistory forwards the watermark-aware history insert. The generic
// fallback needs two round-trips (timestamp lookup, then insert) to do the
// same job.
func (s *interestTrackingStore) AddVisibleHistory(ctx context.Context, entry userstore.WatchHistoryEntry) (userstore.WatchHistoryEntry, error) {
	adder, ok := s.UserStore.(userstore.VisibleHistoryAdder)
	if !ok {
		return userstore.AddVisibleHistory(ctx, s.UserStore, entry)
	}
	return adder.AddVisibleHistory(ctx, entry)
}

// VisibleHistoryTimestamps forwards the batched watermark lookup; without it
// callers assume a single wall-clock timestamp for every item.
func (s *interestTrackingStore) VisibleHistoryTimestamps(ctx context.Context, profileID string, mediaItemIDs []string, at time.Time) (map[string]string, error) {
	visibility, ok := s.UserStore.(userstore.HistoryVisibilityStore)
	if !ok {
		return userstore.VisibleHistoryTimestamps(ctx, s.UserStore, profileID, mediaItemIDs, at)
	}
	return visibility.VisibleHistoryTimestamps(ctx, profileID, mediaItemIDs, at)
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
