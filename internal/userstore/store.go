package userstore

import (
	"context"
	"errors"
	"time"
)

var ErrCollectionGroupNotFound = errors.New("collection group not found")

// UserStore defines the interface for per-user data storage.
// Both SQLite and Postgres backends implement this interface.
type UserStore interface {
	// Profiles
	CreateProfile(ctx context.Context, p Profile) error
	GetProfile(ctx context.Context, id string) (*Profile, error)
	ListProfiles(ctx context.Context) ([]Profile, error)
	UpdateProfile(ctx context.Context, id string, u UpdateProfileInput) error
	DeleteProfile(ctx context.Context, id string) error
	VerifyPIN(ctx context.Context, profileID, pin string) (bool, error)

	// Progress
	UpdateProgress(ctx context.Context, profileID, mediaItemID string, position, duration float64, thresholds ProgressThresholds) error
	SetProgress(ctx context.Context, profileID, mediaItemID string, position, duration float64, thresholds ProgressThresholds) error
	SetProgressAt(ctx context.Context, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) error
	SetProgressIfNewer(ctx context.Context, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) (bool, error)
	UpdateProgressHints(ctx context.Context, profileID, mediaItemID string, hints VersionHints) error
	MarkWatched(ctx context.Context, profileID, mediaItemID string, duration float64) error
	MarkProgressBatch(ctx context.Context, profileID string, mediaItemIDs []string, updatedAt time.Time) error
	ClearProgressBatch(ctx context.Context, profileID string, mediaItemIDs []string, updatedAt time.Time) error
	ClearProgress(ctx context.Context, profileID, mediaItemID string) error
	GetProgress(ctx context.Context, profileID, mediaItemID string) (*WatchProgress, error)
	ListProgress(ctx context.Context, profileID, status string, limit, offset int) ([]WatchProgress, error)
	ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]WatchProgress, error)
	AddHistory(ctx context.Context, entry WatchHistoryEntry) error
	AddHistoryIfMissing(ctx context.Context, entry WatchHistoryEntry) (bool, error)
	ListHistory(ctx context.Context, profileID string, limit, offset int) ([]WatchHistoryEntry, error)
	ListCompletedHistory(ctx context.Context, query CompletedHistoryQuery) ([]WatchHistoryEntry, error)
	RemoveHistoryItems(ctx context.Context, profileID string, mediaItemIDs []string, removedAt time.Time) error
	DeleteHistoryBySource(ctx context.Context, profileID string, mediaItemIDs []string, source WatchHistorySource) error
	ListHomeDismissals(ctx context.Context, profileID, surface string) ([]HomeItemDismissal, error)
	UpsertHomeDismissal(ctx context.Context, dismissal HomeItemDismissal) error
	DeleteHomeDismissal(ctx context.Context, profileID, surface, mediaItemID string) error

	// Favorites & Watchlist
	AddFavorite(ctx context.Context, profileID, mediaItemID string) error
	AddFavoriteAt(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) error
	RemoveFavorite(ctx context.Context, profileID, mediaItemID string) error
	ListFavorites(ctx context.Context, profileID string, limit, offset int) ([]Favorite, error)
	ListFavoritesByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error)
	IsFavorite(ctx context.Context, profileID, mediaItemID string) (bool, error)
	AddToWatchlist(ctx context.Context, profileID, mediaItemID string) error
	RemoveFromWatchlist(ctx context.Context, profileID, mediaItemID string) error
	ListWatchlist(ctx context.Context, profileID string, limit, offset int) ([]WatchlistEntry, error)
	ListWatchlistByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error)
	InWatchlist(ctx context.Context, profileID, mediaItemID string) (bool, error)

	// Collections
	CreateCollection(ctx context.Context, input CreateCollectionInput) (*Collection, error)
	GetCollection(ctx context.Context, id string) (*Collection, error)
	ListCollections(ctx context.Context, profileID string) ([]Collection, error)
	UpdateCollection(ctx context.Context, input UpdateCollectionInput) error
	DeleteCollection(ctx context.Context, id string) error
	AddCollectionItem(ctx context.Context, collectionID, mediaItemID string, position int) error
	RemoveCollectionItem(ctx context.Context, collectionID, mediaItemID string) error
	ListCollectionItems(ctx context.Context, collectionID string) ([]CollectionItem, error)
	ReplaceCollectionItems(ctx context.Context, collectionID string, items []CollectionItemReplacement) error
	ReorderCollectionItems(ctx context.Context, collectionID string, orderedMediaItemIDs []string) error
	// ReorderCollections scopes to the supplied group_id. A nil groupID means
	// the implicit Ungrouped bucket.
	ReorderCollections(ctx context.Context, profileID string, groupID *string, orderedIDs []string) error
	UpdateCollectionSyncState(ctx context.Context, input UpdateCollectionSyncStateInput) error
	ListCollectionGroups(ctx context.Context) ([]CollectionGroup, error)
	EnsureCollectionGroup(ctx context.Context, id string) error
	CreateCollectionGroup(ctx context.Context, name, slug string, defaultSortMode GroupSortMode) (*CollectionGroup, error)
	UpdateCollectionGroup(ctx context.Context, id string, name *string, slug *string, defaultSortMode *GroupSortMode) (*CollectionGroup, error)
	DeleteCollectionGroup(ctx context.Context, id string) error
	ReorderCollectionGroups(ctx context.Context, orderedIDs []string) error

	// Section Overrides
	ListSectionOverrides(ctx context.Context, profileID, scope, libraryID string) ([]SectionOverride, error)
	SaveSectionOverrides(ctx context.Context, profileID, scope, libraryID string, overrides []SectionOverride) error
	ResetSectionOverrides(ctx context.Context, profileID, scope, libraryID string) error

	// Settings & Preferences
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	DeleteSetting(ctx context.Context, key string) error
	ListSettings(ctx context.Context) ([]SettingEntry, error)
	GetDeviceSetting(ctx context.Context, profileID, deviceID, key string) (*DeviceSettingEntry, error)
	SetDeviceSetting(ctx context.Context, entry DeviceSettingEntry) error
	DeleteDeviceSetting(ctx context.Context, profileID, deviceID, key string) error
	DeleteAllDeviceSettings(ctx context.Context, profileID, deviceID string) error
	DeleteDeviceSettingsByKey(ctx context.Context, key string) error
	ListDeviceSettings(ctx context.Context, key string) ([]DeviceSettingEntry, error)
	ListAllDeviceSettings(ctx context.Context) ([]DeviceSettingEntry, error)
	SetSubtitlePreference(ctx context.Context, pref SubtitlePreference) error
	GetSubtitlePreference(ctx context.Context, profileID, seriesID string) (*SubtitlePreference, error)
	DeleteSubtitlePreference(ctx context.Context, profileID, seriesID string) error
	SetAudioPreference(ctx context.Context, pref AudioPreference) error
	GetAudioPreference(ctx context.Context, profileID, seriesID string) (*AudioPreference, error)
	DeleteAudioPreference(ctx context.Context, profileID, seriesID string) error
	SetSeriesPlaybackPreference(ctx context.Context, pref SeriesPlaybackPreference) error
	GetSeriesPlaybackPreference(ctx context.Context, profileID, seriesID string) (*SeriesPlaybackPreference, error)
	DeleteSeriesPlaybackPreference(ctx context.Context, profileID, seriesID string) error
	GetLibraryPlaybackPreference(ctx context.Context, profileID string, libraryID int) (*LibraryPlaybackPreference, error)
	ListLibraryPlaybackPreferences(ctx context.Context, profileID string) ([]LibraryPlaybackPreference, error)
	UpsertLibraryPlaybackPreference(ctx context.Context, pref LibraryPlaybackPreference) error
	DeleteLibraryPlaybackPreference(ctx context.Context, profileID string, libraryID int) error
}

// DeviceRegistry is implemented by stores that track observed devices even
// when they do not currently have any device-scoped overrides.
type DeviceRegistry interface {
	RegisterDevice(ctx context.Context, entry DeviceEntry) error
	ListDevices(ctx context.Context) ([]DeviceEntry, error)
}
