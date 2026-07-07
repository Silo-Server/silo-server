package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestResolveViewerScopeParity(t *testing.T) {
	ctx := context.Background()
	engine, err := NewEngine(ctx)
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	pdp := NewPDP(engine)

	accountCases := []struct {
		name       string
		libraryIDs []int
	}{
		{name: "account_unrestricted", libraryIDs: nil},
		{name: "account_empty", libraryIDs: []int{}},
		{name: "account_1_2_3", libraryIDs: []int{1, 2, 3}},
	}
	profileCases := []struct {
		name    string
		profile *userstore.Profile
	}{
		{name: "profile_absent"},
		{name: "profile_unrestricted", profile: parityProfile(false, nil)},
		{name: "profile_empty", profile: parityProfile(true, []int{})},
		{name: "profile_2_3_4", profile: parityProfile(true, []int{2, 3, 4})},
	}
	disabledCases := []struct {
		name string
		ids  []int
	}{
		{name: "disabled_none"},
		{name: "disabled_2", ids: []int{2}},
	}
	accountQualityCases := []namedString{
		{name: "account_quality_empty", value: ""},
		{name: "account_quality_any", value: "any"},
		{name: "account_quality_standard", value: "standard"},
		{name: "account_quality_4k", value: "4k"},
		{name: "account_quality_480p", value: "480p"},
		{name: "account_quality_2160P", value: "2160P"},
	}

	for _, accountCase := range accountCases {
		for _, profileCase := range profileCases {
			for _, disabledCase := range disabledCases {
				for _, verified := range []bool{true, false} {
					for _, accountQualityCase := range accountQualityCases {
						for _, profileQualityCase := range profileQualityCases(profileCase.profile) {
							for _, ratingCase := range profileRatingCases(profileCase.profile) {
								name := fmt.Sprintf(
									"%s/%s/%s/verified_%t/%s/%s/%s",
									accountCase.name,
									profileCase.name,
									disabledCase.name,
									verified,
									accountQualityCase.name,
									profileQualityCase.name,
									ratingCase.name,
								)
								t.Run(name, func(t *testing.T) {
									user := &models.User{
										ID:                   42,
										LibraryIDs:           cloneParityInts(accountCase.libraryIDs),
										MaxPlaybackQuality:   accountQualityCase.value,
										AccessPolicyRevision: 9,
									}
									profile := cloneParityProfile(profileCase.profile)
									if profile != nil {
										profile.MaxPlaybackQuality = profileQualityCase.value
										profile.MaxContentRating = ratingCase.value
									}
									store := parityStore{
										profile:  profile,
										settings: disabledSetting(disabledCase.ids),
									}
									resolver := access.NewResolver(
										parityUserRepo{user: user},
										parityStoreProvider{store: store},
										nil,
									)
									profileID := ""
									if profile != nil {
										profileID = profile.ID
									}

									input := scopeInputFromParity(user, profile, disabledCase.ids, verified)
									decision, _, pdpErr := pdp.ResolveViewerScope(ctx, input)
									if pdpErr != nil {
										t.Fatalf("ResolveViewerScope() error: %v", pdpErr)
									}

									goScope, goErr := resolver.Resolve(ctx, access.ResolveInput{
										UserID:              user.ID,
										SessionID:           input.SessionID,
										ProfileID:           profileID,
										SkipPINVerification: profile != nil && verified,
									})
									if profile != nil && !verified {
										if !errors.Is(goErr, access.ErrProfileUnverified) {
											t.Fatalf("Resolve() error = %v, want ErrProfileUnverified", goErr)
										}
										if decision.ProfileVerified {
											t.Fatalf("ProfileVerified = true, want false")
										}
										return
									}
									if goErr != nil {
										t.Fatalf("Resolve() error: %v", goErr)
									}

									policyScope := decisionToAccessScope(input, decision)
									if !reflect.DeepEqual(policyScope, goScope) {
										t.Fatalf("scope mismatch\npolicy: %#v\nlegacy: %#v\ndecision: %#v", policyScope, goScope, decision)
									}
									if decision.Unrestricted != (policyScope.AllowedLibraryIDs == nil) {
										t.Fatalf("unrestricted = %t, AllowedLibraryIDs = %#v", decision.Unrestricted, policyScope.AllowedLibraryIDs)
									}
									if policyScope.AllowedLibraryIDs != nil && len(policyScope.DisabledLibraryIDs) != 0 {
										t.Fatalf("restricted scope carried disabled libraries: %#v", policyScope)
									}
								})
							}
						}
					}
				}
			}
		}
	}
}

type namedString struct {
	name  string
	value string
}

func profileQualityCases(profile *userstore.Profile) []namedString {
	if profile == nil {
		return []namedString{{name: "profile_quality_absent"}}
	}
	return []namedString{
		{name: "profile_quality_empty", value: ""},
		{name: "profile_quality_standard", value: "standard"},
		{name: "profile_quality_4k", value: "4k"},
	}
}

func profileRatingCases(profile *userstore.Profile) []namedString {
	if profile == nil {
		return []namedString{{name: "profile_rating_absent"}}
	}
	return []namedString{
		{name: "profile_rating_pg13", value: "PG-13"},
		{name: "profile_rating_empty", value: ""},
	}
}

func parityProfile(restricted bool, allowed []int) *userstore.Profile {
	return &userstore.Profile{
		ID:                         "prof-1",
		PINHash:                    "pin-hash",
		MaxContentRating:           "PG-13",
		MaxPlaybackQuality:         "720p",
		PreferredMetadataLanguage:  "fr",
		LibraryRestrictionsEnabled: restricted,
		AllowedLibraryIDs:          cloneParityInts(allowed),
	}
}

func scopeInputFromParity(user *models.User, profile *userstore.Profile, disabled []int, verified bool) ScopeInput {
	input := ScopeInput{
		SchemaVersion:        1,
		UserID:               user.ID,
		SessionID:            "sess-1",
		AccountLibraryIDs:    cloneParityInts(user.LibraryIDs),
		AccountRestricted:    user.LibraryIDs != nil,
		AccountMaxQuality:    user.MaxPlaybackQuality,
		AccessPolicyRevision: user.AccessPolicyRevision,
		DisabledLibraryIDs:   cloneParityInts(disabled),
		ProfileVerified:      true,
		RequestTime:          "2026-07-02T12:00:00Z",
		DeviceID:             "device-1",
		ClientIP:             "192.0.2.10",
		IsAPIKey:             false,
	}
	if profile != nil {
		input.ProfileID = profile.ID
		input.ProfilePresent = true
		input.ProfileMaxRating = profile.MaxContentRating
		input.ProfileMaxQuality = profile.MaxPlaybackQuality
		input.ProfileLibraryLimited = profile.LibraryRestrictionsEnabled
		input.ProfileLibraryIDs = cloneParityInts(profile.AllowedLibraryIDs)
		input.ProfileHasPIN = profile.PINHash != ""
		input.ProfileVerified = verified
		input.ProfileMetadataLang = profile.PreferredMetadataLanguage
	}
	return input
}

func decisionToAccessScope(input ScopeInput, decision ScopeDecision) access.Scope {
	var allowed []int
	if !decision.Unrestricted {
		allowed = cloneParityInts(decision.AllowedLibraryIDs)
		if allowed == nil {
			allowed = []int{}
		}
	}
	disabled := cloneParityInts(decision.DisabledLibraryIDs)
	if len(disabled) == 0 {
		disabled = nil
	}
	return access.Scope{
		UserID:                    input.UserID,
		ProfileID:                 input.ProfileID,
		AllowedLibraryIDs:         allowed,
		DisabledLibraryIDs:        disabled,
		LibrariesRestricted:       decision.LibrariesRestricted,
		MaxContentRating:          decision.MaxContentRating,
		MaxPlaybackQuality:        decision.MaxPlaybackQuality,
		PreferredMetadataLanguage: decision.PreferredMetadataLanguage,
		PolicyRevision:            decision.PolicyRevision,
		ProfileVerified:           decision.ProfileVerified,
	}
}

func disabledSetting(ids []int) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	raw, _ := json.Marshal(ids)
	return map[string]string{"disabled_library_ids": string(raw)}
}

func cloneParityInts(values []int) []int {
	if values == nil {
		return nil
	}
	out := make([]int, len(values))
	copy(out, values)
	return out
}

func cloneParityProfile(profile *userstore.Profile) *userstore.Profile {
	if profile == nil {
		return nil
	}
	clone := *profile
	clone.AllowedLibraryIDs = cloneParityInts(profile.AllowedLibraryIDs)
	return &clone
}

type parityUserRepo struct {
	user *models.User
}

func (r parityUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	if r.user == nil || r.user.ID != id {
		return nil, errors.New("user not found")
	}
	return r.user, nil
}

type parityStoreProvider struct {
	store userstore.UserStore
}

func (p parityStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p parityStoreProvider) Close() error {
	return nil
}

type parityStore struct {
	userstore.UserStore
	profile  *userstore.Profile
	settings map[string]string
}

func (s parityStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	if s.profile == nil || s.profile.ID != id {
		return nil, nil
	}
	return s.profile, nil
}

func (s parityStore) GetSetting(_ context.Context, key string) (string, error) {
	return s.settings[key], nil
}
