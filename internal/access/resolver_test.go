package access

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

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

// stubStore implements the store reads the resolver makes; the embedded nil
// interface panics on any other call.
type stubStore struct {
	userstore.UserStore
	profile  *userstore.Profile
	err      error
	settings map[string]string
	// settingValues are the canonical setting rows the resolver may read
	// through ListSettingValuesForResolution. Scope matching is the
	// resolver's job, so the stub returns them unfiltered.
	settingValues []userstore.SettingValue
}

func (s stubStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.profile == nil || s.profile.ID != id {
		return nil, nil
	}
	return s.profile, nil
}
func (s stubStore) UpdateProgressHints(_ context.Context, _, _ string, _ userstore.VersionHints) error {
	return nil
}
func (s stubStore) RemoveWatchedFromWatchlist(context.Context, string) (bool, error) {
	return true, nil
}
func (s stubStore) GetSetting(_ context.Context, key string) (string, error) {
	if s.settings != nil {
		return s.settings[key], nil
	}
	return "", nil
}
func (s stubStore) ListSettingValuesForResolution(context.Context, userstore.SettingResolutionQuery) ([]userstore.SettingValue, error) {
	return s.settingValues, nil
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
			PINRevision:                6,
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
	// Credentials bound to the profile's PIN revision compare against this.
	if scope.ProfilePINRevision != 6 {
		t.Fatalf("ProfilePINRevision = %d, want 6", scope.ProfilePINRevision)
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
			profile: &userstore.Profile{ID: "prof-1"},
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
			profile: &userstore.Profile{ID: "prof-1"},
			settingValues: []userstore.SettingValue{{
				SettingIdentity: userstore.SettingIdentity{
					Key:       settingskeys.UiDisabledLibraryIds,
					Scope:     settingscontract.ScopeProfile,
					ProfileID: "prof-1",
				},
				Value: json.RawMessage(`[2,4]`),
			}},
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

func TestResolver_DisabledLibraries_LegacyAccountKeyIsNotRead(t *testing.T) {
	// The retired-fallback migration copied the legacy account value onto
	// every profile without a canonical row, so a profile with none hides
	// nothing even while the frozen legacy row is still stored.
	resolver := NewResolver(
		stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
		stubStoreProvider{store: stubStore{
			profile:  &userstore.Profile{ID: "prof-1"},
			settings: map[string]string{"disabled_library_ids": "[9]"},
		}},
		nil,
	)

	scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if len(scope.DisabledLibraryIDs) != 0 {
		t.Fatalf("DisabledLibraryIDs = %v, want none from the legacy account key", scope.DisabledLibraryIDs)
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
	// Hidden libraries are profile-scoped, so a request without a profile
	// hides nothing, whatever the frozen legacy account key says.
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
	if len(scope.DisabledLibraryIDs) != 0 {
		t.Fatalf("DisabledLibraryIDs = %v, want none", scope.DisabledLibraryIDs)
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

func TestResolver_NextUpModeRidesTheScope(t *testing.T) {
	// ui.next_up_mode resolves in the same read as the other viewer keys, so
	// home-section requests can take it from the scope.
	tests := []struct {
		name   string
		values []userstore.SettingValue
		want   string
	}{
		{name: "contract default", want: "combined"},
		{
			name: "stored row",
			values: []userstore.SettingValue{{
				SettingIdentity: userstore.SettingIdentity{
					Key: settingskeys.UiNextUpMode, Scope: settingscontract.ScopeProfile, ProfileID: "prof-1",
				},
				Value: json.RawMessage(`"separate"`),
			}},
			want: "separate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewResolver(
				stubUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}},
				stubStoreProvider{store: stubStore{
					profile:       &userstore.Profile{ID: "prof-1"},
					settingValues: tt.values,
				}},
				nil,
			)
			scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1"})
			if err != nil {
				t.Fatalf("Resolve() error: %v", err)
			}
			if scope.NextUpMode != tt.want {
				t.Fatalf("NextUpMode = %q, want %q", scope.NextUpMode, tt.want)
			}
		})
	}
}

func TestScopeJSONLeavesOutNextUpMode(t *testing.T) {
	// Socket tickets and progress snapshots hash the scope's JSON as its
	// access fingerprint. A presentation preference must not change it.
	withMode, err := json.Marshal(Scope{UserID: 1, ProfileID: "prof-1", NextUpMode: "separate"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	without, err := json.Marshal(Scope{UserID: 1, ProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(withMode) != string(without) {
		t.Fatalf("scope JSON changed with NextUpMode:\n%s\n%s", withMode, without)
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

func (s stubStore) LatestHistoryIDs(context.Context, string, map[string][]string) (map[string]string, error) {
	return nil, nil
}

// A content-only resolve answers what a profile may access, not what it
// browses: the profile's hidden libraries are a browsing preference, so they
// neither shrink a restricted allow list nor ride along as disabled libraries.
func TestResolver_ContentAccessOnlyIgnoresHiddenLibraries(t *testing.T) {
	hidden := []userstore.SettingValue{{
		SettingIdentity: userstore.SettingIdentity{
			Key:       settingskeys.UiDisabledLibraryIds,
			Scope:     settingscontract.ScopeProfile,
			ProfileID: "prof-1",
		},
		Value: json.RawMessage(`[2,4]`),
	}}
	cases := []struct {
		name        string
		libraries   []int
		wantAllowed []int
	}{
		{"unrestricted account", nil, nil},
		{"restricted account", []int{1, 2, 3, 4}, []int{1, 2, 3, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := NewResolver(
				stubUserRepo{user: &models.User{ID: 1, LibraryIDs: tc.libraries, AccessPolicyRevision: 5}},
				stubStoreProvider{store: stubStore{
					profile:       &userstore.Profile{ID: "prof-1", MaxContentRating: "PG", PINHash: "locked"},
					settingValues: hidden,
				}},
				nil,
			)
			scope, err := resolver.Resolve(context.Background(), ResolveInput{UserID: 1, ProfileID: "prof-1", SkipPINVerification: true, ContentAccessOnly: true})
			if err != nil {
				t.Fatalf("Resolve() error: %v", err)
			}
			if !reflect.DeepEqual(scope.AllowedLibraryIDs, tc.wantAllowed) {
				t.Fatalf("AllowedLibraryIDs = %#v, want %#v", scope.AllowedLibraryIDs, tc.wantAllowed)
			}
			if scope.DisabledLibraryIDs != nil {
				t.Fatalf("DisabledLibraryIDs = %v, want none", scope.DisabledLibraryIDs)
			}
			if scope.MaxContentRating != "PG" {
				t.Fatalf("MaxContentRating = %q, want the profile's ceiling", scope.MaxContentRating)
			}
		})
	}
}
