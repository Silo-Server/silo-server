package userdb

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SQLiteUserStore implements userstore.UserStore using a per-user SQLite database.
type SQLiteUserStore struct {
	db *sql.DB
}

// NewSQLiteUserStore wraps an existing *sql.DB as a UserStore.
func NewSQLiteUserStore(db *sql.DB) *SQLiteUserStore {
	return &SQLiteUserStore{db: db}
}

// Compile-time interface check.
var _ userstore.UserStore = (*SQLiteUserStore)(nil)
var _ userstore.DeviceRegistry = (*SQLiteUserStore)(nil)

// --- Profiles ---

func (s *SQLiteUserStore) CreateProfile(_ context.Context, p userstore.Profile) error {
	return CreateProfile(s.db, p)
}

func (s *SQLiteUserStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	return GetProfile(s.db, id)
}

func (s *SQLiteUserStore) ListProfiles(_ context.Context) ([]userstore.Profile, error) {
	return ListProfiles(s.db)
}

func (s *SQLiteUserStore) UpdateProfile(_ context.Context, id string, u userstore.UpdateProfileInput) error {
	return UpdateProfile(s.db, id, u)
}

func (s *SQLiteUserStore) DeleteProfile(_ context.Context, id string) error {
	return DeleteProfile(s.db, id)
}

func (s *SQLiteUserStore) VerifyPIN(_ context.Context, profileID, pin string) (bool, error) {
	return VerifyPIN(s.db, profileID, pin)
}

// --- Progress ---

func (s *SQLiteUserStore) UpdateProgress(_ context.Context, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	return UpdateProgress(s.db, profileID, mediaItemID, position, duration, thresholds)
}

func (s *SQLiteUserStore) SetProgress(_ context.Context, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	return SetProgress(s.db, profileID, mediaItemID, position, duration, thresholds)
}

func (s *SQLiteUserStore) SetProgressAt(_ context.Context, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) error {
	return SetProgressAt(s.db, profileID, mediaItemID, position, duration, completed, updatedAt)
}

func (s *SQLiteUserStore) SetProgressIfNewer(_ context.Context, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) (bool, error) {
	return SetProgressIfNewer(s.db, profileID, mediaItemID, position, duration, completed, updatedAt)
}

func (s *SQLiteUserStore) UpdateProgressHints(_ context.Context, profileID, mediaItemID string, hints userstore.VersionHints) error {
	return UpdateProgressHints(s.db, profileID, mediaItemID, hints)
}

func (s *SQLiteUserStore) MarkWatched(_ context.Context, profileID, mediaItemID string, duration float64) error {
	return MarkWatched(s.db, profileID, mediaItemID, duration)
}

func (s *SQLiteUserStore) ClearProgress(_ context.Context, profileID, mediaItemID string) error {
	return ClearProgress(s.db, profileID, mediaItemID)
}

func (s *SQLiteUserStore) MarkProgressBatch(_ context.Context, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	return MarkProgressBatch(s.db, profileID, mediaItemIDs, updatedAt)
}

func (s *SQLiteUserStore) ClearProgressBatch(_ context.Context, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	return ClearProgressBatch(s.db, profileID, mediaItemIDs, updatedAt)
}

func (s *SQLiteUserStore) GetProgress(_ context.Context, profileID, mediaItemID string) (*userstore.WatchProgress, error) {
	return GetProgress(s.db, profileID, mediaItemID)
}

func (s *SQLiteUserStore) ListProgress(_ context.Context, profileID, status string, limit, offset int) ([]userstore.WatchProgress, error) {
	return ListProgress(s.db, profileID, status, limit, offset)
}

// ListProgressFiltered cannot push the type/library predicate down: the
// per-user SQLite store has no catalog tables (media_items/episodes/
// media_item_libraries live in the shared Postgres schema). It therefore
// returns the status page unfiltered — a valid coarse superset — and relies on
// the caller's in-memory type check and library-scoped hydration to narrow it.
func (s *SQLiteUserStore) ListProgressFiltered(_ context.Context, profileID, status string, _ []string, _ *int, limit, offset int) ([]userstore.WatchProgress, error) {
	return ListProgress(s.db, profileID, status, limit, offset)
}

func (s *SQLiteUserStore) ListProgressByMediaItems(_ context.Context, profileID string, mediaItemIDs []string) (map[string]userstore.WatchProgress, error) {
	return ListProgressByMediaItems(s.db, profileID, mediaItemIDs)
}

func (s *SQLiteUserStore) ListProgressSince(_ context.Context, profileID, cursor string) ([]userstore.WatchProgress, string, error) {
	c, _ := strconv.ParseInt(cursor, 10, 64) // empty/invalid cursor → 0 (full delta)
	rows, next, err := ListProgressSince(s.db, profileID, c, 0)
	if err != nil {
		return nil, cursor, err
	}
	return rows, strconv.FormatInt(next, 10), nil
}

func (s *SQLiteUserStore) AddHistory(_ context.Context, entry userstore.WatchHistoryEntry) error {
	return AddHistory(s.db, entry)
}

func (s *SQLiteUserStore) AddVisibleHistory(_ context.Context, entry userstore.WatchHistoryEntry) (userstore.WatchHistoryEntry, error) {
	return AddVisibleHistory(s.db, entry)
}

func (s *SQLiteUserStore) AddHistoryIfMissing(_ context.Context, entry userstore.WatchHistoryEntry) (bool, error) {
	return AddHistoryIfMissing(s.db, entry)
}

func (s *SQLiteUserStore) ListHistory(_ context.Context, profileID string, limit, offset int) ([]userstore.WatchHistoryEntry, error) {
	return ListHistory(s.db, profileID, limit, offset)
}

func (s *SQLiteUserStore) ListCompletedHistory(_ context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	return ListCompletedHistory(s.db, query)
}

func (s *SQLiteUserStore) ListCompletedHistoryItems(_ context.Context, query userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	return ListCompletedHistoryItems(s.db, query)
}

func (s *SQLiteUserStore) VisibleHistoryTimestamps(_ context.Context, profileID string, mediaItemIDs []string, at time.Time) (map[string]string, error) {
	return VisibleHistoryTimestamps(s.db, profileID, mediaItemIDs, at)
}

func (s *SQLiteUserStore) RemoveHistoryItems(_ context.Context, profileID string, mediaItemIDs []string, removedAt time.Time) error {
	return RemoveHistoryItems(s.db, profileID, mediaItemIDs, removedAt)
}

func (s *SQLiteUserStore) DeleteHistoryBySource(_ context.Context, profileID string, mediaItemIDs []string, source userstore.WatchHistorySource) error {
	return DeleteHistoryBySource(s.db, profileID, mediaItemIDs, source)
}

func (s *SQLiteUserStore) ListHomeDismissals(_ context.Context, profileID, surface string) ([]userstore.HomeItemDismissal, error) {
	return ListHomeDismissals(s.db, profileID, surface)
}

func (s *SQLiteUserStore) UpsertHomeDismissal(_ context.Context, dismissal userstore.HomeItemDismissal) error {
	return UpsertHomeDismissal(s.db, dismissal)
}

func (s *SQLiteUserStore) DeleteHomeDismissal(_ context.Context, profileID, surface, mediaItemID string) error {
	return DeleteHomeDismissal(s.db, profileID, surface, mediaItemID)
}

// --- Favorites & Watchlist ---

func (s *SQLiteUserStore) AddFavorite(_ context.Context, profileID, mediaItemID string) error {
	return AddFavorite(s.db, profileID, mediaItemID)
}

func (s *SQLiteUserStore) AddFavoriteAt(_ context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	return AddFavoriteAt(s.db, profileID, mediaItemID, addedAt)
}

func (s *SQLiteUserStore) RemoveFavorite(_ context.Context, profileID, mediaItemID string) error {
	return RemoveFavorite(s.db, profileID, mediaItemID)
}

func (s *SQLiteUserStore) ListFavorites(_ context.Context, profileID string, limit, offset int) ([]userstore.Favorite, error) {
	return ListFavorites(s.db, profileID, limit, offset)
}

func (s *SQLiteUserStore) ListFavoritesByMediaItems(_ context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error) {
	return ListFavoritesByMediaItems(s.db, profileID, mediaItemIDs)
}

func (s *SQLiteUserStore) IsFavorite(_ context.Context, profileID, mediaItemID string) (bool, error) {
	return IsFavorite(s.db, profileID, mediaItemID)
}

func (s *SQLiteUserStore) AddToWatchlist(_ context.Context, profileID, mediaItemID string) error {
	return AddToWatchlist(s.db, profileID, mediaItemID)
}

func (s *SQLiteUserStore) AddToWatchlistAt(_ context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	return AddToWatchlistAt(s.db, profileID, mediaItemID, addedAt)
}

func (s *SQLiteUserStore) RemoveFromWatchlist(_ context.Context, profileID, mediaItemID string) error {
	return RemoveFromWatchlist(s.db, profileID, mediaItemID)
}

func (s *SQLiteUserStore) ReplaceWatchlistOrder(_ context.Context, profileID string, orderedMediaItemIDs []string) error {
	return ReplaceWatchlistOrder(s.db, profileID, orderedMediaItemIDs)
}

func (s *SQLiteUserStore) ListWatchlist(_ context.Context, profileID string, limit, offset int) ([]userstore.WatchlistEntry, error) {
	return ListWatchlist(s.db, profileID, limit, offset)
}

func (s *SQLiteUserStore) ListWatchlistByMediaItems(_ context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error) {
	return ListWatchlistByMediaItems(s.db, profileID, mediaItemIDs)
}

func (s *SQLiteUserStore) InWatchlist(_ context.Context, profileID, mediaItemID string) (bool, error) {
	return InWatchlist(s.db, profileID, mediaItemID)
}

// RemoveWatchedFromWatchlist defaults on for the embedded sqlite backend, which
// does not persist the per-profile preference.
func (s *SQLiteUserStore) RemoveWatchedFromWatchlist(_ context.Context, _ string) (bool, error) {
	return true, nil
}

// --- Collections ---

func (s *SQLiteUserStore) CreateCollection(_ context.Context, input userstore.CreateCollectionInput) (*userstore.Collection, error) {
	return CreateCollection(s.db, input)
}

func (s *SQLiteUserStore) GetCollection(_ context.Context, id string) (*userstore.Collection, error) {
	return GetCollection(s.db, id)
}

func (s *SQLiteUserStore) ListCollections(_ context.Context, profileID string) ([]userstore.Collection, error) {
	return ListCollections(s.db, profileID)
}

func (s *SQLiteUserStore) UpdateCollection(_ context.Context, input userstore.UpdateCollectionInput) error {
	return UpdateCollection(s.db, input)
}

func (s *SQLiteUserStore) DeleteCollection(_ context.Context, id string) error {
	return DeleteCollection(s.db, id)
}

func (s *SQLiteUserStore) AddCollectionItem(_ context.Context, collectionID, mediaItemID string, position int) error {
	return AddCollectionItem(s.db, collectionID, mediaItemID, position)
}

func (s *SQLiteUserStore) RemoveCollectionItem(_ context.Context, collectionID, mediaItemID string) error {
	return RemoveCollectionItem(s.db, collectionID, mediaItemID)
}

func (s *SQLiteUserStore) ListCollectionItems(_ context.Context, collectionID string) ([]userstore.CollectionItem, error) {
	return ListCollectionItems(s.db, collectionID)
}

// User-collection imports are Postgres-only; SQLite never receives sync writes.
func (s *SQLiteUserStore) ReplaceCollectionItems(_ context.Context, _ string, _ []userstore.CollectionItemReplacement) error {
	return fmt.Errorf("user collection imports are not supported on the SQLite user store")
}

func (s *SQLiteUserStore) ReorderCollectionItems(_ context.Context, _ string, _ []string) error {
	return fmt.Errorf("collection reordering is not supported on the SQLite user store")
}

func (s *SQLiteUserStore) ReorderCollections(_ context.Context, _ string, _ *string, _ []string) error {
	return fmt.Errorf("collection reordering is not supported on the SQLite user store")
}

func (s *SQLiteUserStore) UpdateCollectionSyncState(_ context.Context, _ userstore.UpdateCollectionSyncStateInput) error {
	return fmt.Errorf("user collection imports are not supported on the SQLite user store")
}

func (s *SQLiteUserStore) ListCollectionGroups(_ context.Context) ([]userstore.CollectionGroup, error) {
	return nil, nil
}

func (s *SQLiteUserStore) EnsureCollectionGroup(_ context.Context, _ string) error {
	return nil
}

func (s *SQLiteUserStore) CreateCollectionGroup(_ context.Context, _, _ string, _ userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	return nil, fmt.Errorf("collection groups are not supported on the SQLite user store")
}

func (s *SQLiteUserStore) UpdateCollectionGroup(_ context.Context, _ string, _ *string, _ *string, _ *userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	return nil, fmt.Errorf("collection groups are not supported on the SQLite user store")
}

func (s *SQLiteUserStore) DeleteCollectionGroup(_ context.Context, _ string) error {
	return fmt.Errorf("collection groups are not supported on the SQLite user store")
}

func (s *SQLiteUserStore) ReorderCollectionGroups(_ context.Context, _ []string) error {
	return fmt.Errorf("collection groups are not supported on the SQLite user store")
}

// --- Section Overrides ---

func (s *SQLiteUserStore) ListSectionOverrides(_ context.Context, profileID, scope, libraryID string) ([]userstore.SectionOverride, error) {
	return ListSectionOverrides(s.db, profileID, scope, libraryID)
}

func (s *SQLiteUserStore) SaveSectionOverrides(_ context.Context, profileID, scope, libraryID string, overrides []userstore.SectionOverride) error {
	return SaveSectionOverrides(s.db, profileID, scope, libraryID, overrides)
}

func (s *SQLiteUserStore) ResetSectionOverrides(_ context.Context, profileID, scope, libraryID string) error {
	return ResetSectionOverrides(s.db, profileID, scope, libraryID)
}

// --- Settings & Preferences ---

func (s *SQLiteUserStore) GetSetting(_ context.Context, key string) (string, error) {
	return GetSetting(s.db, key)
}

func (s *SQLiteUserStore) SetSetting(_ context.Context, key, value string) error {
	return SetSetting(s.db, key, value)
}

func (s *SQLiteUserStore) DeleteSetting(_ context.Context, key string) error {
	return DeleteSetting(s.db, key)
}

func (s *SQLiteUserStore) ListSettings(_ context.Context) ([]userstore.SettingEntry, error) {
	return ListSettings(s.db)
}

func (s *SQLiteUserStore) GetDeviceSetting(_ context.Context, profileID, deviceID, key string) (*userstore.DeviceSettingEntry, error) {
	return GetDeviceSetting(s.db, profileID, deviceID, key)
}

func (s *SQLiteUserStore) RegisterDevice(_ context.Context, entry userstore.DeviceEntry) error {
	return RegisterDevice(s.db, entry)
}

func (s *SQLiteUserStore) ListDevices(_ context.Context) ([]userstore.DeviceEntry, error) {
	return ListDevices(s.db)
}

func (s *SQLiteUserStore) SetDeviceSetting(_ context.Context, entry userstore.DeviceSettingEntry) error {
	return SetDeviceSetting(s.db, entry)
}

func (s *SQLiteUserStore) DeleteDeviceSetting(_ context.Context, profileID, deviceID, key string) error {
	return DeleteDeviceSetting(s.db, profileID, deviceID, key)
}

func (s *SQLiteUserStore) DeleteAllDeviceSettings(_ context.Context, profileID, deviceID string) error {
	return DeleteAllDeviceSettings(s.db, profileID, deviceID)
}

func (s *SQLiteUserStore) DeleteDeviceSettingsByKey(_ context.Context, key string) error {
	return DeleteDeviceSettingsByKey(s.db, key)
}

func (s *SQLiteUserStore) ListDeviceSettings(_ context.Context, key string) ([]userstore.DeviceSettingEntry, error) {
	return ListDeviceSettings(s.db, key)
}

func (s *SQLiteUserStore) ListAllDeviceSettings(_ context.Context) ([]userstore.DeviceSettingEntry, error) {
	return ListAllDeviceSettings(s.db)
}

func (s *SQLiteUserStore) SetSubtitlePreference(_ context.Context, pref userstore.SubtitlePreference) error {
	return SetSubtitlePreference(s.db, pref)
}

func (s *SQLiteUserStore) GetSubtitlePreference(_ context.Context, profileID, seriesID string) (*userstore.SubtitlePreference, error) {
	return GetSubtitlePreference(s.db, profileID, seriesID)
}

func (s *SQLiteUserStore) DeleteSubtitlePreference(_ context.Context, profileID, seriesID string) error {
	return DeleteSubtitlePreference(s.db, profileID, seriesID)
}

func (s *SQLiteUserStore) SetAudioPreference(_ context.Context, pref userstore.AudioPreference) error {
	return SetAudioPreference(s.db, pref)
}

func (s *SQLiteUserStore) GetAudioPreference(_ context.Context, profileID, seriesID string) (*userstore.AudioPreference, error) {
	return GetAudioPreference(s.db, profileID, seriesID)
}

func (s *SQLiteUserStore) DeleteAudioPreference(_ context.Context, profileID, seriesID string) error {
	return DeleteAudioPreference(s.db, profileID, seriesID)
}

func (s *SQLiteUserStore) SetSeriesPlaybackPreference(_ context.Context, pref userstore.SeriesPlaybackPreference) error {
	return SetSeriesPlaybackPreference(s.db, pref)
}

func (s *SQLiteUserStore) GetSeriesPlaybackPreference(_ context.Context, profileID, seriesID string) (*userstore.SeriesPlaybackPreference, error) {
	return GetSeriesPlaybackPreference(s.db, profileID, seriesID)
}

func (s *SQLiteUserStore) DeleteSeriesPlaybackPreference(_ context.Context, profileID, seriesID string) error {
	return DeleteSeriesPlaybackPreference(s.db, profileID, seriesID)
}

func (s *SQLiteUserStore) GetLibraryPlaybackPreference(_ context.Context, profileID string, libraryID int) (*userstore.LibraryPlaybackPreference, error) {
	return GetLibraryPlaybackPreference(s.db, profileID, libraryID)
}

func (s *SQLiteUserStore) ListLibraryPlaybackPreferences(_ context.Context, profileID string) ([]userstore.LibraryPlaybackPreference, error) {
	return ListLibraryPlaybackPreferences(s.db, profileID)
}

func (s *SQLiteUserStore) UpsertLibraryPlaybackPreference(_ context.Context, pref userstore.LibraryPlaybackPreference) error {
	return UpsertLibraryPlaybackPreference(s.db, pref)
}

func (s *SQLiteUserStore) DeleteLibraryPlaybackPreference(_ context.Context, profileID string, libraryID int) error {
	return DeleteLibraryPlaybackPreference(s.db, profileID, libraryID)
}
