package access

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type stubUserRepo struct {
	user *models.User
	err  error
}

func (s stubUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.user == nil || s.user.ID != id {
		return nil, errors.New("user not found")
	}
	return s.user, nil
}

type stubStoreProvider struct {
	store userstore.UserStore
	err   error
}

func (s stubStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return s.store, s.err
}
func (s stubStoreProvider) Close() error { return nil }

type stubStore struct {
	profile  *userstore.Profile
	err      error
	settings map[string]string
	// settingValues are the canonical setting rows the resolver may read
	// through ListSettingValuesForResolution. Scope matching is the
	// resolver's job, so the stub returns them unfiltered.
	settingValues []userstore.SettingValue
}

func (s stubStore) CreateProfile(context.Context, userstore.Profile) error { panic("unused") }
func (s stubStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.profile == nil || s.profile.ID != id {
		return nil, nil
	}
	return s.profile, nil
}
func (s stubStore) ListProfiles(context.Context) ([]userstore.Profile, error) { panic("unused") }
func (s stubStore) UpdateProfile(context.Context, string, userstore.UpdateProfileInput) error {
	panic("unused")
}
func (s stubStore) DeleteProfile(context.Context, string) error             { panic("unused") }
func (s stubStore) VerifyPIN(context.Context, string, string) (bool, error) { panic("unused") }
func (s stubStore) UpdateProgress(context.Context, string, string, float64, float64, userstore.ProgressThresholds) error {
	panic("unused")
}
func (s stubStore) SetProgress(context.Context, string, string, float64, float64, userstore.ProgressThresholds) error {
	panic("unused")
}
func (s stubStore) SetProgressAt(context.Context, string, string, float64, float64, bool, time.Time) error {
	panic("unused")
}
func (s stubStore) SetProgressIfNewer(context.Context, string, string, float64, float64, bool, time.Time) (bool, error) {
	panic("unused")
}
func (s stubStore) UpdateProgressHints(_ context.Context, _, _ string, _ userstore.VersionHints) error {
	return nil
}
func (s stubStore) MarkWatched(context.Context, string, string, float64) error { panic("unused") }
func (s stubStore) MarkProgressBatch(context.Context, string, []string, time.Time) error {
	panic("unused")
}
func (s stubStore) ClearProgressBatch(context.Context, string, []string, time.Time) error {
	panic("unused")
}
func (s stubStore) ClearProgress(context.Context, string, string) error { panic("unused") }
func (s stubStore) GetProgress(context.Context, string, string) (*userstore.WatchProgress, error) {
	panic("unused")
}
func (s stubStore) ListProgress(context.Context, string, string, int, int) ([]userstore.WatchProgress, error) {
	panic("unused")
}
func (s stubStore) ListProgressFiltered(context.Context, string, string, []string, *int, int, int) ([]userstore.WatchProgress, error) {
	panic("unused")
}
func (s stubStore) ListProgressByMediaItems(context.Context, string, []string) (map[string]userstore.WatchProgress, error) {
	panic("unused")
}
func (s stubStore) ListProgressSince(context.Context, string, string) ([]userstore.WatchProgress, string, error) {
	panic("unused")
}
func (s stubStore) AddHistory(context.Context, userstore.WatchHistoryEntry) error { panic("unused") }
func (s stubStore) AddHistoryIfMissing(context.Context, userstore.WatchHistoryEntry) (bool, error) {
	panic("unused")
}
func (s stubStore) ListHistory(context.Context, string, int, int) ([]userstore.WatchHistoryEntry, error) {
	panic("unused")
}
func (s stubStore) ListCompletedHistory(context.Context, userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	panic("unused")
}
func (s stubStore) ListCompletedHistoryItems(context.Context, userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	panic("unused")
}
func (s stubStore) RemoveHistoryItems(context.Context, string, []string, time.Time) error {
	panic("unused")
}
func (s stubStore) DeleteHistoryBySource(context.Context, string, []string, userstore.WatchHistorySource) error {
	panic("unused")
}
func (s stubStore) ListHomeDismissals(context.Context, string, string) ([]userstore.HomeItemDismissal, error) {
	panic("unused")
}
func (s stubStore) UpsertHomeDismissal(context.Context, userstore.HomeItemDismissal) error {
	panic("unused")
}
func (s stubStore) DeleteHomeDismissal(context.Context, string, string, string) error {
	panic("unused")
}
func (s stubStore) AddFavorite(context.Context, string, string) error { panic("unused") }
func (s stubStore) AddFavoriteAt(context.Context, string, string, time.Time) (bool, error) {
	panic("unused")
}
func (s stubStore) RemoveFavorite(context.Context, string, string) error {
	panic("unused")
}
func (s stubStore) ListFavorites(context.Context, string, int, int) ([]userstore.Favorite, error) {
	panic("unused")
}
func (s stubStore) ListFavoritesByMediaItems(context.Context, string, []string) (map[string]bool, error) {
	panic("unused")
}
func (s stubStore) IsFavorite(context.Context, string, string) (bool, error) { panic("unused") }
func (s stubStore) AddToWatchlist(context.Context, string, string) error     { panic("unused") }
func (s stubStore) AddToWatchlistAt(context.Context, string, string, time.Time) (bool, error) {
	panic("unused")
}
func (s stubStore) RemoveWatchedFromWatchlist(context.Context, string) (bool, error) {
	return true, nil
}
func (s stubStore) RemoveFromWatchlist(context.Context, string, string) error { panic("unused") }
func (s stubStore) ReplaceWatchlistOrder(context.Context, string, []string) error {
	panic("unused")
}
func (s stubStore) ListWatchlist(context.Context, string, int, int) ([]userstore.WatchlistEntry, error) {
	panic("unused")
}
func (s stubStore) ListWatchlistByMediaItems(context.Context, string, []string) (map[string]bool, error) {
	panic("unused")
}
func (s stubStore) InWatchlist(context.Context, string, string) (bool, error) { panic("unused") }
func (s stubStore) CreateCollection(context.Context, userstore.CreateCollectionInput) (*userstore.Collection, error) {
	panic("unused")
}
func (s stubStore) GetCollection(context.Context, string) (*userstore.Collection, error) {
	panic("unused")
}
func (s stubStore) ListCollections(context.Context, string) ([]userstore.Collection, error) {
	panic("unused")
}
func (s stubStore) UpdateCollection(context.Context, userstore.UpdateCollectionInput) error {
	panic("unused")
}
func (s stubStore) DeleteCollection(context.Context, string) error { panic("unused") }
func (s stubStore) AddCollectionItem(context.Context, string, string, int) error {
	panic("unused")
}
func (s stubStore) RemoveCollectionItem(context.Context, string, string) error { panic("unused") }
func (s stubStore) ListCollectionItems(context.Context, string) ([]userstore.CollectionItem, error) {
	panic("unused")
}
func (s stubStore) ReplaceCollectionItems(context.Context, string, []userstore.CollectionItemReplacement) error {
	panic("unused")
}
func (s stubStore) ReorderCollectionItems(context.Context, string, []string) error {
	panic("unused")
}
func (s stubStore) ReorderCollections(context.Context, string, *string, []string) error {
	panic("unused")
}
func (s stubStore) UpdateCollectionSyncState(context.Context, userstore.UpdateCollectionSyncStateInput) error {
	panic("unused")
}
func (s stubStore) ListCollectionGroups(context.Context) ([]userstore.CollectionGroup, error) {
	panic("unused")
}
func (s stubStore) EnsureCollectionGroup(context.Context, string) error { panic("unused") }
func (s stubStore) CreateCollectionGroup(context.Context, string, string, userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	panic("unused")
}
func (s stubStore) UpdateCollectionGroup(context.Context, string, *string, *string, *userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	panic("unused")
}
func (s stubStore) DeleteCollectionGroup(context.Context, string) error { panic("unused") }
func (s stubStore) ReorderCollectionGroups(context.Context, []string) error {
	panic("unused")
}
func (s stubStore) ListSectionOverrides(context.Context, string, string, string) ([]userstore.SectionOverride, error) {
	panic("unused")
}
func (s stubStore) SaveSectionOverrides(context.Context, string, string, string, []userstore.SectionOverride) error {
	panic("unused")
}
func (s stubStore) ResetSectionOverrides(context.Context, string, string, string) error {
	panic("unused")
}
func (s stubStore) GetSetting(_ context.Context, key string) (string, error) {
	if s.settings != nil {
		return s.settings[key], nil
	}
	return "", nil
}
func (s stubStore) SetSetting(context.Context, string, string) error { panic("unused") }
func (s stubStore) GetOnboardingState(context.Context, string, string) (*userstore.OnboardingState, error) {
	panic("unused")
}
func (s stubStore) UpsertOnboardingState(context.Context, userstore.OnboardingState) error {
	panic("unused")
}
func (s stubStore) GetJellycompatDisplayPrefs(context.Context, string, string) (string, error) {
	panic("unused")
}
func (s stubStore) SetJellycompatDisplayPrefs(context.Context, string, string, string) error {
	panic("unused")
}
func (s stubStore) DeleteSetting(context.Context, string) error                    { panic("unused") }
func (s stubStore) ListSettings(context.Context) ([]userstore.SettingEntry, error) { panic("unused") }
func (s stubStore) GetDeviceSetting(context.Context, string, string, string) (*userstore.DeviceSettingEntry, error) {
	panic("unused")
}
func (s stubStore) SetDeviceSetting(context.Context, userstore.DeviceSettingEntry) error {
	panic("unused")
}
func (s stubStore) DeleteDeviceSetting(context.Context, string, string, string) error {
	panic("unused")
}
func (s stubStore) DeleteAllDeviceSettings(context.Context, string, string) error { panic("unused") }
func (s stubStore) DeleteDeviceSettingsByKey(context.Context, string) error       { panic("unused") }
func (s stubStore) ListDeviceSettings(context.Context, string) ([]userstore.DeviceSettingEntry, error) {
	panic("unused")
}
func (s stubStore) ListAllDeviceSettings(context.Context) ([]userstore.DeviceSettingEntry, error) {
	panic("unused")
}
func (s stubStore) SetSubtitlePreference(context.Context, userstore.SubtitlePreference) error {
	panic("unused")
}
func (s stubStore) GetSubtitlePreference(context.Context, string, string) (*userstore.SubtitlePreference, error) {
	panic("unused")
}
func (s stubStore) DeleteSubtitlePreference(context.Context, string, string) error { panic("unused") }
func (s stubStore) SetAudioPreference(context.Context, userstore.AudioPreference) error {
	panic("unused")
}
func (s stubStore) GetAudioPreference(context.Context, string, string) (*userstore.AudioPreference, error) {
	panic("unused")
}
func (s stubStore) DeleteAudioPreference(context.Context, string, string) error { panic("unused") }
func (s stubStore) SetSeriesPlaybackPreference(context.Context, userstore.SeriesPlaybackPreference) error {
	panic("unused")
}
func (s stubStore) GetSeriesPlaybackPreference(context.Context, string, string) (*userstore.SeriesPlaybackPreference, error) {
	panic("unused")
}
func (s stubStore) DeleteSeriesPlaybackPreference(context.Context, string, string) error {
	panic("unused")
}
func (s stubStore) SetCollectionSortPreference(context.Context, userstore.CollectionSortPreference) error {
	panic("unused")
}
func (s stubStore) GetCollectionSortPreference(context.Context, string, string, string) (*userstore.CollectionSortPreference, error) {
	panic("unused")
}
func (s stubStore) ClearCollectionSortPreference(context.Context, string, string, string) error {
	panic("unused")
}
func (s stubStore) GetLibraryPlaybackPreference(context.Context, string, int) (*userstore.LibraryPlaybackPreference, error) {
	panic("unused")
}
func (s stubStore) ListLibraryPlaybackPreferences(context.Context, string) ([]userstore.LibraryPlaybackPreference, error) {
	panic("unused")
}
func (s stubStore) UpsertLibraryPlaybackPreference(context.Context, userstore.LibraryPlaybackPreference) error {
	panic("unused")
}
func (s stubStore) DeleteLibraryPlaybackPreference(context.Context, string, int) error {
	panic("unused")
}
func (s stubStore) GetSettingValue(context.Context, userstore.SettingIdentity) (*userstore.SettingValue, error) {
	panic("unused")
}
func (s stubStore) ListSettingValuesForResolution(context.Context, userstore.SettingResolutionQuery) ([]userstore.SettingValue, error) {
	return s.settingValues, nil
}
func (s stubStore) ListAllSettingValues(context.Context) ([]userstore.SettingValue, error) {
	panic("unused")
}
func (s stubStore) UpsertSettingValue(context.Context, userstore.SettingIdentity, json.RawMessage) (*userstore.SettingValue, error) {
	panic("unused")
}
func (s stubStore) DeleteSettingValue(context.Context, userstore.SettingIdentity) (bool, error) {
	panic("unused")
}
func (s stubStore) DeleteSettingValuesForProfile(context.Context, string) (int64, error) {
	panic("unused")
}
func (s stubStore) DeleteSettingValuesForDevice(context.Context, string, string) (int64, error) {
	panic("unused")
}
func (s stubStore) DeleteSettingValuesForLibrary(context.Context, int) (int64, error) {
	panic("unused")
}
func (s stubStore) DeleteSettingValuesForSeries(context.Context, string) (int64, error) {
	panic("unused")
}
func (s stubStore) GetSettingMutation(context.Context, string) (*userstore.SettingMutationRecord, error) {
	panic("unused")
}
func (s stubStore) PutSettingMutation(context.Context, userstore.SettingMutationRecord) (userstore.SettingMutationRecord, bool, error) {
	panic("unused")
}
func (s stubStore) DeleteExpiredSettingMutations(context.Context, time.Time) (int64, error) {
	panic("unused")
}

func TestResolver_UnrestrictedAccountRestrictedProfile(t *testing.T) {
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{profile: &userstore.Profile{
			ID:                         "prof-1",
			LibraryRestrictionsEnabled: true,
			AllowedLibraryIDs:          []int{2, 4},
			MaxContentRating:           "PG-13",
			MaxPlaybackQuality:         "720p",
		}}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{
		UserID:    1,
		SessionID: "sess-1",
		ProfileID: "prof-1",
	})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if !scope.LibrariesRestricted || len(scope.AllowedLibraryIDs) != 2 || scope.AllowedLibraryIDs[0] != 2 || scope.AllowedLibraryIDs[1] != 4 {
		t.Fatalf("unexpected scope libraries: %+v", scope)
	}
	if scope.MaxContentRating != "PG-13" || scope.MaxPlaybackQuality != "1080p" {
		t.Fatalf("unexpected scope ceilings: %+v", scope)
	}
}

func TestResolver_RestrictedAccountInheritingProfile(t *testing.T) {
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, LibraryIDs: []int{1, 3}, MaxPlaybackQuality: ptr("1080p"), AccessPolicyRevision: 4}},
		stubStoreProvider{store: stubStore{profile: &userstore.Profile{ID: "prof-1"}}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if !scope.LibrariesRestricted || len(scope.AllowedLibraryIDs) != 2 || scope.AllowedLibraryIDs[0] != 1 || scope.AllowedLibraryIDs[1] != 3 {
		t.Fatalf("unexpected scope libraries: %+v", scope)
	}
	if scope.MaxPlaybackQuality != "1080p" {
		t.Fatalf("MaxPlaybackQuality = %q, want 1080p", scope.MaxPlaybackQuality)
	}
}

func TestResolver_IntersectsAccountAndProfileLibraries(t *testing.T) {
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, LibraryIDs: []int{1, 2, 3}, AccessPolicyRevision: 4}},
		stubStoreProvider{store: stubStore{profile: &userstore.Profile{
			ID:                         "prof-1",
			LibraryRestrictionsEnabled: true,
			AllowedLibraryIDs:          []int{2, 4},
		}}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if len(scope.AllowedLibraryIDs) != 1 || scope.AllowedLibraryIDs[0] != 2 {
		t.Fatalf("AllowedLibraryIDs = %v, want [2]", scope.AllowedLibraryIDs)
	}
}

func TestResolver_EmptyEffectiveLibraries(t *testing.T) {
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, LibraryIDs: []int{1}, AccessPolicyRevision: 4}},
		stubStoreProvider{store: stubStore{profile: &userstore.Profile{
			ID:                         "prof-1",
			LibraryRestrictionsEnabled: true,
			AllowedLibraryIDs:          []int{2},
		}}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if len(scope.AllowedLibraryIDs) != 0 || !scope.LibrariesRestricted {
		t.Fatalf("unexpected scope: %+v", scope)
	}
}

func TestResolver_DisabledLibraries_UnrestrictedUser(t *testing.T) {
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			profile:  &userstore.Profile{ID: "prof-1"},
			settings: map[string]string{"disabled_library_ids": "[3,5]"},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	// Unrestricted user: AllowedLibraryIDs stays nil, DisabledLibraryIDs is populated.
	if scope.AllowedLibraryIDs != nil {
		t.Fatalf("AllowedLibraryIDs = %v, want nil", scope.AllowedLibraryIDs)
	}
	if len(scope.DisabledLibraryIDs) != 2 || scope.DisabledLibraryIDs[0] != 3 || scope.DisabledLibraryIDs[1] != 5 {
		t.Fatalf("DisabledLibraryIDs = %v, want [3 5]", scope.DisabledLibraryIDs)
	}
}

func TestResolver_DisabledLibraries_RestrictedUser(t *testing.T) {
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, LibraryIDs: []int{1, 2, 3, 4}, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			profile:  &userstore.Profile{ID: "prof-1"},
			settings: map[string]string{"disabled_library_ids": "[2,4]"},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	// Restricted user: disabled IDs subtracted from AllowedLibraryIDs.
	if len(scope.AllowedLibraryIDs) != 2 || scope.AllowedLibraryIDs[0] != 1 || scope.AllowedLibraryIDs[1] != 3 {
		t.Fatalf("AllowedLibraryIDs = %v, want [1 3]", scope.AllowedLibraryIDs)
	}
	if len(scope.DisabledLibraryIDs) != 0 {
		t.Fatalf("DisabledLibraryIDs = %v, want empty", scope.DisabledLibraryIDs)
	}
}

func TestResolver_DisabledLibraries_CanonicalRowWins(t *testing.T) {
	// The canonical profile-scoped ui.disabled_library_ids row wins; the
	// legacy account key carries a decoy value that must not be read once a
	// canonical row exists.
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			profile:  &userstore.Profile{ID: "prof-1"},
			settings: map[string]string{"disabled_library_ids": "[9]"},
			settingValues: []userstore.SettingValue{{
				SettingIdentity: userstore.SettingIdentity{
					Key:       settingskeys.UiDisabledLibraryIds,
					Scope:     settingscontract.ScopeProfile,
					ProfileID: "prof-1",
				},
				Value: json.RawMessage(`[3,5]`),
			}},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if len(scope.DisabledLibraryIDs) != 2 || scope.DisabledLibraryIDs[0] != 3 || scope.DisabledLibraryIDs[1] != 5 {
		t.Fatalf("DisabledLibraryIDs = %v, want canonical [3 5]", scope.DisabledLibraryIDs)
	}
}

func TestResolver_DisabledLibraries_CanonicalNullClearsLegacy(t *testing.T) {
	// A stored null spells "no hidden libraries" and still wins over the
	// legacy key: the row exists, so the profile has decided.
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			profile:  &userstore.Profile{ID: "prof-1"},
			settings: map[string]string{"disabled_library_ids": "[9]"},
			settingValues: []userstore.SettingValue{{
				SettingIdentity: userstore.SettingIdentity{
					Key:       settingskeys.UiDisabledLibraryIds,
					Scope:     settingscontract.ScopeProfile,
					ProfileID: "prof-1",
				},
				Value: json.RawMessage(`null`),
			}},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if len(scope.DisabledLibraryIDs) != 0 {
		t.Fatalf("DisabledLibraryIDs = %v, want empty", scope.DisabledLibraryIDs)
	}
}

func TestResolver_DisabledLibraries_ProfileIsolation(t *testing.T) {
	// Profile A's canonical hidden-library list must not leak into profile B:
	// with no canonical row of its own and no legacy key, B hides nothing.
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			profile: &userstore.Profile{ID: "prof-b"},
			settingValues: []userstore.SettingValue{{
				SettingIdentity: userstore.SettingIdentity{
					Key:       settingskeys.UiDisabledLibraryIds,
					Scope:     settingscontract.ScopeProfile,
					ProfileID: "prof-a",
				},
				Value: json.RawMessage(`[3,5]`),
			}},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-b"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if len(scope.DisabledLibraryIDs) != 0 {
		t.Fatalf("DisabledLibraryIDs = %v, want empty for the other profile", scope.DisabledLibraryIDs)
	}
}

func TestResolver_DisabledLibraries_NoProfile(t *testing.T) {
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			settings: map[string]string{"disabled_library_ids": "[7]"},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if scope.AllowedLibraryIDs != nil {
		t.Fatalf("AllowedLibraryIDs = %v, want nil", scope.AllowedLibraryIDs)
	}
	if len(scope.DisabledLibraryIDs) != 1 || scope.DisabledLibraryIDs[0] != 7 {
		t.Fatalf("DisabledLibraryIDs = %v, want [7]", scope.DisabledLibraryIDs)
	}
}

func TestResolver_MetadataLanguageResolvesCanonically(t *testing.T) {
	// The canonical catalog.metadata_language row wins; the legacy profile
	// column carries a decoy value that must no longer be read.
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			profile: &userstore.Profile{
				ID:                        "prof-1",
				PreferredMetadataLanguage: "fr",
			},
			settingValues: []userstore.SettingValue{
				{
					SettingIdentity: userstore.SettingIdentity{
						Key:       settingskeys.CatalogMetadataLanguage,
						Scope:     settingscontract.ScopeProfile,
						ProfileID: "prof-1",
					},
					Value: json.RawMessage(`"de"`),
				},
				{
					SettingIdentity: userstore.SettingIdentity{
						Key:       settingskeys.CatalogMetadataLanguageOverrides,
						Scope:     settingscontract.ScopeProfile,
						ProfileID: "prof-1",
					},
					Value: json.RawMessage(`{"no":"x-silo-original"}`),
				},
			},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if scope.PreferredMetadataLanguage != "de" {
		t.Fatalf("PreferredMetadataLanguage = %q, want canonical value %q", scope.PreferredMetadataLanguage, "de")
	}
	if got := scope.MetadataLanguageOverrides["no"]; got != OriginalMetadataLanguage {
		t.Fatalf("MetadataLanguageOverrides[no] = %q, want %q", got, OriginalMetadataLanguage)
	}
}

func TestResolver_MetadataLanguageIgnoresLegacyColumn(t *testing.T) {
	// A profile with only the legacy column value falls to the contract
	// default ("" — inherit), proving the column is no longer read.
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			profile: &userstore.Profile{
				ID:                        "prof-1",
				PreferredMetadataLanguage: "fr",
			},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if scope.PreferredMetadataLanguage != "" {
		t.Fatalf("PreferredMetadataLanguage = %q, want contract default \"\"", scope.PreferredMetadataLanguage)
	}
}

func TestResolver_AppliesGroupPolicy(t *testing.T) {
	groupID := int64(9)
	group := &GroupPolicy{
		LibraryIDs:               []int{2, 4},
		MaxPlaybackQuality:       PlaybackQualityStandard,
		DownloadAllowed:          true,
		DownloadTranscodeAllowed: true,
		TranscodeAllowed:         true,
		AudioTranscodeAllowed:    true,
		RequestsAllowed:          true,
	}

	t.Run("unset account fields inherit the group", func(t *testing.T) {
		resolver := NewResolver(
			stubUserRepo{user: &models.User{ID: 1, AccessGroupID: &groupID, AccessPolicyRevision: 5}},
			stubStoreProvider{store: stubStore{}},
			nil,
			stubGroupProvider{group: group},
		)
		scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1})
		if err != nil {
			t.Fatalf("Resolve() error: %v", err)
		}
		if !scope.LibrariesRestricted || !reflect.DeepEqual(scope.AllowedLibraryIDs, []int{2, 4}) {
			t.Fatalf("scope libraries = restricted %t ids %#v, want [2 4]", scope.LibrariesRestricted, scope.AllowedLibraryIDs)
		}
		if scope.MaxPlaybackQuality != PlaybackQualityStandard {
			t.Fatalf("MaxPlaybackQuality = %q, want %q", scope.MaxPlaybackQuality, PlaybackQualityStandard)
		}
	})

	t.Run("account overrides replace the group values", func(t *testing.T) {
		resolver := NewResolver(
			stubUserRepo{user: &models.User{
				ID:                   1,
				AccessGroupID:        &groupID,
				LibraryIDs:           []int{1, 2, 3},
				MaxPlaybackQuality:   ptr(PlaybackQuality4K),
				AccessPolicyRevision: 5,
			}},
			stubStoreProvider{store: stubStore{}},
			nil,
			stubGroupProvider{group: group},
		)
		scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1})
		if err != nil {
			t.Fatalf("Resolve() error: %v", err)
		}
		if !scope.LibrariesRestricted || !reflect.DeepEqual(scope.AllowedLibraryIDs, []int{1, 2, 3}) {
			t.Fatalf("scope libraries = restricted %t ids %#v, want [1 2 3]", scope.LibrariesRestricted, scope.AllowedLibraryIDs)
		}
		if scope.MaxPlaybackQuality != PlaybackQuality4K {
			t.Fatalf("MaxPlaybackQuality = %q, want %q", scope.MaxPlaybackQuality, PlaybackQuality4K)
		}
	})
}

type stubGroupProvider struct {
	group *GroupPolicy
	err   error
}

func (p stubGroupProvider) GetPolicyForUser(context.Context, int) (*GroupPolicy, error) {
	return p.group, p.err
}
