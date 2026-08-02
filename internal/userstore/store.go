package userstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrCollectionGroupNotFound = errors.New("collection group not found")

// PreferenceSettingsWriter is the subset of the user store that participates
// in legacy-preference/canonical-setting synchronization. Implementations pass
// a transaction-scoped writer to WithPreferenceSettingsTransaction so callers
// can commit the legacy row and every canonical row as one unit.
type PreferenceSettingsWriter interface {
	CreateProfile(ctx context.Context, profile Profile) error
	ListSettings(ctx context.Context) ([]SettingEntry, error)
	// ListProfileIDs reads the current household membership inside the same
	// transaction as a legacy account-setting mutation. This closes the
	// create/write window where a newly committed profile could miss fan-out.
	ListProfileIDs(ctx context.Context) ([]string, error)
	// UpdateProfile writes the legacy profile preference columns that shipped
	// clients still mutate during the canonical-settings cutover.
	UpdateProfile(ctx context.Context, id string, u UpdateProfileInput) error
	SetSubtitlePreference(ctx context.Context, pref SubtitlePreference) error
	DeleteSubtitlePreference(ctx context.Context, profileID, seriesID string) error
	SetAudioPreference(ctx context.Context, pref AudioPreference) error
	DeleteAudioPreference(ctx context.Context, profileID, seriesID string) error
	UpsertLibraryPlaybackPreference(ctx context.Context, pref LibraryPlaybackPreference) error
	DeleteLibraryPlaybackPreference(ctx context.Context, profileID string, libraryID int) error
	SetSetting(ctx context.Context, key, value string) error
	DeleteSetting(ctx context.Context, key string) error
	SetDeviceSetting(ctx context.Context, entry DeviceSettingEntry) error
	DeleteDeviceSetting(ctx context.Context, profileID, deviceID, key string) error
	// UpsertSettingValue writes one explicit value and increments its revision.
	UpsertSettingValue(ctx context.Context, id SettingIdentity, value json.RawMessage) (*SettingValue, error)
	// DeleteSettingValue removes one explicit value and reports whether it existed.
	DeleteSettingValue(ctx context.Context, id SettingIdentity) (bool, error)
}

// PreferenceSettingsTransactioner is implemented by stores that can atomically
// synchronize a shipped legacy preference row with its canonical values.
type PreferenceSettingsTransactioner interface {
	WithPreferenceSettingsTransaction(ctx context.Context, fn func(PreferenceSettingsWriter) error) error
}

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
	// ListProgressFiltered is ListProgress with an additional SQL pre-filter on
	// the backing catalog item's type and/or library, so the watched-items path
	// no longer scans the whole status set before discarding non-matching rows.
	// types is matched case-insensitively against media_items.type ("episode"
	// resolves through the separate episodes table); a nil libraryID drops the
	// library predicate, and an empty types + nil libraryID degrades to the
	// plain status listing. It is a coarse pre-filter: callers still apply
	// access/parental exclusions over the returned rows.
	ListProgressFiltered(ctx context.Context, profileID, status string, types []string, libraryID *int, limit, offset int) ([]WatchProgress, error)
	ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]WatchProgress, error)
	// ListProgressSince returns rows whose server cursor exceeds the opaque
	// cursor token (empty = full delta), in cursor order, with the next cursor.
	// Cross-device delta delivery depends only on the server-assigned synced_seq.
	ListProgressSince(ctx context.Context, profileID, cursor string) ([]WatchProgress, string, error)
	AddHistory(ctx context.Context, entry WatchHistoryEntry) error
	AddHistoryIfMissing(ctx context.Context, entry WatchHistoryEntry) (bool, error)
	ListHistory(ctx context.Context, profileID string, limit, offset int) ([]WatchHistoryEntry, error)
	ListCompletedHistory(ctx context.Context, query CompletedHistoryQuery) ([]WatchHistoryEntry, error)
	ListCompletedHistoryItems(ctx context.Context, query CompletedHistoryItemQuery) ([]CompletedHistoryItem, error)
	RemoveHistoryItems(ctx context.Context, profileID string, mediaItemIDs []string, removedAt time.Time) error
	DeleteHistoryBySource(ctx context.Context, profileID string, mediaItemIDs []string, source WatchHistorySource) error
	ListHomeDismissals(ctx context.Context, profileID, surface string) ([]HomeItemDismissal, error)
	UpsertHomeDismissal(ctx context.Context, dismissal HomeItemDismissal) error
	DeleteHomeDismissal(ctx context.Context, profileID, surface, mediaItemID string) error

	// Favorites & Watchlist
	AddFavorite(ctx context.Context, profileID, mediaItemID string) error
	AddFavoriteAt(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error)
	RemoveFavorite(ctx context.Context, profileID, mediaItemID string) error
	ListFavorites(ctx context.Context, profileID string, limit, offset int) ([]Favorite, error)
	ListFavoritesByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error)
	IsFavorite(ctx context.Context, profileID, mediaItemID string) (bool, error)
	AddToWatchlist(ctx context.Context, profileID, mediaItemID string) error
	AddToWatchlistAt(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error)
	RemoveFromWatchlist(ctx context.Context, profileID, mediaItemID string) error
	// ReplaceWatchlistOrder mirrors a provider's watchlist order: the given ids
	// get sort_index 0..N-1 in order; all other rows reset to added_at ordering.
	ReplaceWatchlistOrder(ctx context.Context, profileID string, orderedMediaItemIDs []string) error
	ListWatchlist(ctx context.Context, profileID string, limit, offset int) ([]WatchlistEntry, error)
	ListWatchlistByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error)
	InWatchlist(ctx context.Context, profileID, mediaItemID string) (bool, error)
	// RemoveWatchedFromWatchlist reports the profile's preference for pruning
	// fully-watched entries from the watchlist (defaults true): movies are
	// removed outright on completion, while fully-watched series are only
	// hidden from display so they reappear when new episodes are added.
	RemoveWatchedFromWatchlist(ctx context.Context, profileID string) (bool, error)

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

	// Onboarding
	GetOnboardingState(ctx context.Context, profileID, tourID string) (*OnboardingState, error)
	UpsertOnboardingState(ctx context.Context, state OnboardingState) error

	// Jellyfin DisplayPreferences blobs, keyed by (prefs id, client) per user.
	// They are the jellycompat subsystem's storage rather than user settings —
	// the contract neither validates nor resolves them — so they live in the
	// dedicated jellycompat_displayprefs table and hold opaque Jellyfin client
	// JSON verbatim. Get returns "" when nothing is stored.
	GetJellycompatDisplayPrefs(ctx context.Context, prefsID, client string) (string, error)
	SetJellycompatDisplayPrefs(ctx context.Context, prefsID, client, value string) error

	// Canonical typed setting values (contracts/settings/v1).
	//
	// These back the settings contract's storage layer. The manifest remains
	// the schema; the store holds validated JSON keyed by scope identity, and
	// knows nothing about definitions, defaults or resolution order.

	// GetSettingValue returns the explicit value at exactly one scope, or nil
	// when that identity is unset. It does not resolve fallbacks.
	GetSettingValue(ctx context.Context, id SettingIdentity) (*SettingValue, error)
	// ListSettingValuesForResolution returns every candidate row for one
	// resolution request in a single query, unranked. The resolver applies each
	// definition's resolution order in Go; issuing one lookup per scope is a
	// rejected implementation.
	ListSettingValuesForResolution(ctx context.Context, query SettingResolutionQuery) ([]SettingValue, error)
	// ListAllSettingValues returns every explicit value this user has stored,
	// across all scopes, in a stable (key, scope, identity) order. It serves
	// the admin inspection surface; resolution reads keep going through
	// ListSettingValuesForResolution.
	ListAllSettingValues(ctx context.Context) ([]SettingValue, error)
	// UpsertSettingValue writes the explicit value at one scope and increments
	// that row's revision. Concurrent writes to one identity are
	// last-write-wins in server receipt order; there is no compare-and-set
	// precondition in v1.
	UpsertSettingValue(ctx context.Context, id SettingIdentity, value json.RawMessage) (*SettingValue, error)
	// DeleteSettingValue removes the explicit value at one scope — the `unset`
	// operation — and reports whether a row existed.
	DeleteSettingValue(ctx context.Context, id SettingIdentity) (bool, error)

	// The scoped deletes below are application-enforced cleanup for identities
	// this table cannot reference: the per-user SQLite store declares no foreign
	// keys, and libraries, series and devices are not FK targets in Postgres
	// either. Each removes only the rows scoped to the named entity.
	DeleteSettingValuesForProfile(ctx context.Context, profileID string) (int64, error)
	DeleteSettingValuesForDevice(ctx context.Context, profileID, deviceID string) (int64, error)
	DeleteSettingValuesForLibrary(ctx context.Context, libraryID int) (int64, error)
	DeleteSettingValuesForSeries(ctx context.Context, seriesID string) (int64, error)

	// GetSettingMutation returns a recorded idempotency receipt, or nil.
	GetSettingMutation(ctx context.Context, mutationID string) (*SettingMutationRecord, error)
	// PutSettingMutation records a receipt without ever overwriting one. When
	// the id is already recorded it returns the stored record with
	// inserted=false, so the caller compares request hashes and answers
	// already_applied or mutation_id_conflict.
	PutSettingMutation(ctx context.Context, record SettingMutationRecord) (SettingMutationRecord, bool, error)
	// DeleteExpiredSettingMutations removes receipts that expired before the
	// given instant and reports how many. expires_at is not self-enforcing.
	DeleteExpiredSettingMutations(ctx context.Context, before time.Time) (int64, error)
}

// DeviceRegistry is implemented by stores that track observed devices even
// when they do not currently have any device-scoped overrides.
type DeviceRegistry interface {
	RegisterDevice(ctx context.Context, entry DeviceEntry) error
	ListDevices(ctx context.Context) ([]DeviceEntry, error)
	// DeviceExists reports whether one device is registered to one profile. It
	// exists so a write naming a device can be authorized without scanning the
	// whole account's registry: ListDevices is account-wide by construction, so
	// filtering its result per write would read every household member's rows.
	DeviceExists(ctx context.Context, profileID, deviceID string) (bool, error)
	// ForgetDevice removes one profile's registry row for a device. Settings
	// are deleted separately through the scoped setting deletes, so forgetting
	// a device shared by two profiles leaves the other profile's row intact.
	ForgetDevice(ctx context.Context, profileID, deviceID string) error
}
