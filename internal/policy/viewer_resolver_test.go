package policy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestViewerResolverParityWithLegacyResolver(t *testing.T) {
	ctx := context.Background()
	pdp := newViewerResolverTestPDP(t, ctx)

	tests := []struct {
		name             string
		user             *models.User
		profile          *userstore.Profile
		settings         map[string]string
		input            access.ResolveInput
		tokens           access.ProfileTokenValidator
		wantNilAllowed   bool
		wantEmptyAllowed bool
		wantDisabled     []int
	}{
		{
			name: "no profile unrestricted",
			user: &models.User{
				ID:                   1,
				AccessPolicyRevision: 5,
			},
			settings:       map[string]string{"disabled_library_ids": "[7]"},
			input:          access.ResolveInput{UserID: 1, SessionID: "sess-1"},
			wantNilAllowed: true,
			wantDisabled:   []int{7},
		},
		{
			name: "profile unrestricted",
			user: &models.User{
				ID:                   1,
				MaxPlaybackQuality:   "any",
				AccessPolicyRevision: 5,
			},
			profile: &userstore.Profile{
				ID:                        "prof-1",
				MaxContentRating:          "PG-13",
				MaxPlaybackQuality:        "4k",
				PreferredMetadataLanguage: "fr",
			},
			input:          access.ResolveInput{UserID: 1, SessionID: "sess-1", ProfileID: "prof-1"},
			wantNilAllowed: true,
		},
		{
			name: "account and profile restrictions intersect",
			user: &models.User{
				ID:                   1,
				LibraryIDs:           []int{1, 2, 3},
				AccessPolicyRevision: 5,
			},
			profile: &userstore.Profile{
				ID:                         "prof-1",
				LibraryRestrictionsEnabled: true,
				AllowedLibraryIDs:          []int{2, 3, 4},
			},
			input: access.ResolveInput{UserID: 1, SessionID: "sess-1", ProfileID: "prof-1"},
		},
		{
			name: "restricted scope subtracts disabled libraries",
			user: &models.User{
				ID:                   1,
				LibraryIDs:           []int{1, 2, 3, 4},
				AccessPolicyRevision: 5,
			},
			profile:  &userstore.Profile{ID: "prof-1"},
			settings: map[string]string{"disabled_library_ids": "[2,4]"},
			input:    access.ResolveInput{UserID: 1, SessionID: "sess-1", ProfileID: "prof-1"},
		},
		{
			name: "unrestricted scope carries disabled libraries",
			user: &models.User{
				ID:                   1,
				AccessPolicyRevision: 5,
			},
			profile:        &userstore.Profile{ID: "prof-1"},
			settings:       map[string]string{"disabled_library_ids": "[3,5]"},
			input:          access.ResolveInput{UserID: 1, SessionID: "sess-1", ProfileID: "prof-1"},
			wantNilAllowed: true,
			wantDisabled:   []int{3, 5},
		},
		{
			name: "empty restricted library set stays non nil",
			user: &models.User{
				ID:                   1,
				LibraryIDs:           []int{1},
				AccessPolicyRevision: 5,
			},
			profile: &userstore.Profile{
				ID:                         "prof-1",
				LibraryRestrictionsEnabled: true,
				AllowedLibraryIDs:          []int{2},
			},
			input:            access.ResolveInput{UserID: 1, SessionID: "sess-1", ProfileID: "prof-1"},
			wantEmptyAllowed: true,
		},
		{
			name: "pin profile with skip verification",
			user: &models.User{
				ID:                   1,
				AccessPolicyRevision: 5,
			},
			profile: &userstore.Profile{
				ID:      "prof-1",
				PINHash: "pin-hash",
			},
			input: access.ResolveInput{
				UserID:              1,
				SessionID:           "sess-1",
				ProfileID:           "prof-1",
				SkipPINVerification: true,
			},
			wantNilAllowed: true,
		},
		{
			name: "pin profile with valid token",
			user: &models.User{
				ID:                   1,
				AccessPolicyRevision: 5,
			},
			profile: &userstore.Profile{
				ID:      "prof-1",
				PINHash: "pin-hash",
			},
			input: access.ResolveInput{
				UserID:       1,
				SessionID:    "sess-1",
				ProfileID:    "prof-1",
				ProfileToken: "valid",
			},
			tokens: stubProfileTokenValidator{
				claims: &access.ProfileTokenClaims{
					UserID:         1,
					SessionID:      "sess-1",
					ProfileID:      "prof-1",
					PolicyRevision: 5,
				},
			},
			wantNilAllowed: true,
		},
		{
			name: "quality and rating ceilings use policy normalization",
			user: &models.User{
				ID:                   1,
				MaxPlaybackQuality:   "2160P",
				AccessPolicyRevision: 5,
			},
			profile: &userstore.Profile{
				ID:                        "prof-1",
				MaxContentRating:          "PG-13",
				MaxPlaybackQuality:        "standard",
				PreferredMetadataLanguage: "de",
			},
			input:          access.ResolveInput{UserID: 1, SessionID: "sess-1", ProfileID: "prof-1"},
			wantNilAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := viewerResolverTestStore{
				profile:  tt.profile,
				settings: tt.settings,
			}
			users := viewerResolverUserRepo{user: tt.user}
			stores := viewerResolverStoreProvider{store: store}
			legacyResolver := access.NewResolver(users, stores, tt.tokens)
			viewerResolver := NewViewerResolver(users, stores, tt.tokens, pdp)

			legacyScope, legacyErr := legacyResolver.Resolve(ctx, tt.input)
			policyScope, policyErr := viewerResolver.Resolve(ctx, tt.input)
			if legacyErr != nil || policyErr != nil {
				t.Fatalf("Resolve() errors: legacy=%v policy=%v", legacyErr, policyErr)
			}
			if !reflect.DeepEqual(policyScope, legacyScope) {
				t.Fatalf("scope mismatch\npolicy: %#v\nlegacy: %#v", policyScope, legacyScope)
			}
			if tt.wantNilAllowed && policyScope.AllowedLibraryIDs != nil {
				t.Fatalf("AllowedLibraryIDs = %#v, want nil", policyScope.AllowedLibraryIDs)
			}
			if tt.wantEmptyAllowed {
				if policyScope.AllowedLibraryIDs == nil || len(policyScope.AllowedLibraryIDs) != 0 {
					t.Fatalf("AllowedLibraryIDs = %#v, want non-nil empty slice", policyScope.AllowedLibraryIDs)
				}
			}
			if tt.wantDisabled != nil && !reflect.DeepEqual(policyScope.DisabledLibraryIDs, tt.wantDisabled) {
				t.Fatalf("DisabledLibraryIDs = %#v, want %#v", policyScope.DisabledLibraryIDs, tt.wantDisabled)
			}

			decisionInput := viewerResolverExpectedInput(tt.user, tt.profile, tt.input, policyScope.ProfileVerified, access.DisabledLibraryIDs(ctx, store))
			decision, _, err := pdp.ResolveViewerScope(ctx, decisionInput)
			if err != nil {
				t.Fatalf("ResolveViewerScope() error: %v", err)
			}
			if decision.ProfileVerified != policyScope.ProfileVerified {
				t.Fatalf("decision ProfileVerified = %t, scope ProfileVerified = %t", decision.ProfileVerified, policyScope.ProfileVerified)
			}
			if decisionInput.AccountMaxQuality != tt.user.MaxPlaybackQuality {
				t.Fatalf("AccountMaxQuality = %q, want raw %q", decisionInput.AccountMaxQuality, tt.user.MaxPlaybackQuality)
			}
			if decisionInput.IsAPIKey {
				t.Fatal("IsAPIKey = true, want false because ResolveInput cannot truthfully distinguish API keys")
			}
			if decisionInput.DeviceID != "" || decisionInput.ClientIP != "" {
				t.Fatalf("request identity fields = device %q client %q, want empty", decisionInput.DeviceID, decisionInput.ClientIP)
			}
			if _, err := time.Parse(time.RFC3339, decisionInput.RequestTime); err != nil {
				t.Fatalf("RequestTime = %q, want RFC3339: %v", decisionInput.RequestTime, err)
			}
		})
	}
}

func TestViewerResolverPINErrorsMatchLegacy(t *testing.T) {
	ctx := context.Background()
	user := &models.User{
		ID:                   1,
		AccessPolicyRevision: 5,
	}
	profile := &userstore.Profile{
		ID:      "prof-1",
		PINHash: "pin-hash",
	}
	pdp := newViewerResolverTestPDP(t, ctx)

	tests := []struct {
		name   string
		input  access.ResolveInput
		tokens access.ProfileTokenValidator
	}{
		{
			name: "no token validator",
			input: access.ResolveInput{
				UserID:    1,
				SessionID: "sess-1",
				ProfileID: "prof-1",
			},
		},
		{
			name: "bad token",
			input: access.ResolveInput{
				UserID:       1,
				SessionID:    "sess-1",
				ProfileID:    "prof-1",
				ProfileToken: "bad",
			},
			tokens: stubProfileTokenValidator{
				err: fmt.Errorf("%w: bad token", access.ErrProfileUnverified),
			},
		},
		{
			name: "revision mismatch",
			input: access.ResolveInput{
				UserID:       1,
				SessionID:    "sess-1",
				ProfileID:    "prof-1",
				ProfileToken: "valid",
			},
			tokens: stubProfileTokenValidator{
				claims: &access.ProfileTokenClaims{
					UserID:         1,
					SessionID:      "sess-1",
					ProfileID:      "prof-1",
					PolicyRevision: 4,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := viewerResolverUserRepo{user: user}
			stores := viewerResolverStoreProvider{store: viewerResolverTestStore{profile: profile}}
			legacyResolver := access.NewResolver(users, stores, tt.tokens)
			viewerResolver := NewViewerResolver(users, stores, tt.tokens, pdp)

			_, legacyErr := legacyResolver.Resolve(ctx, tt.input)
			policyScope, policyErr := viewerResolver.Resolve(ctx, tt.input)
			if !errors.Is(legacyErr, access.ErrProfileUnverified) {
				t.Fatalf("legacy error = %v, want ErrProfileUnverified", legacyErr)
			}
			if !errors.Is(policyErr, access.ErrProfileUnverified) {
				t.Fatalf("policy error = %v, want ErrProfileUnverified", policyErr)
			}
			assertZeroScope(t, policyScope)
		})
	}
}

func TestViewerResolverProfileNotFoundMatchesLegacy(t *testing.T) {
	ctx := context.Background()
	user := &models.User{ID: 1, AccessPolicyRevision: 5}
	users := viewerResolverUserRepo{user: user}
	stores := viewerResolverStoreProvider{store: viewerResolverTestStore{}}
	input := access.ResolveInput{UserID: 1, SessionID: "sess-1", ProfileID: "missing"}
	legacyResolver := access.NewResolver(users, stores, nil)
	viewerResolver := NewViewerResolver(users, stores, nil, newViewerResolverTestPDP(t, ctx))

	_, legacyErr := legacyResolver.Resolve(ctx, input)
	policyScope, policyErr := viewerResolver.Resolve(ctx, input)
	if !errors.Is(legacyErr, access.ErrProfileNotFound) {
		t.Fatalf("legacy error = %v, want ErrProfileNotFound", legacyErr)
	}
	if !errors.Is(policyErr, access.ErrProfileNotFound) {
		t.Fatalf("policy error = %v, want ErrProfileNotFound", policyErr)
	}
	assertZeroScope(t, policyScope)
}

func TestViewerResolverEvalFailureFailsClosed(t *testing.T) {
	ctx := context.Background()
	users := viewerResolverUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}}
	stores := viewerResolverStoreProvider{store: viewerResolverTestStore{}}
	resolver := NewViewerResolver(users, stores, nil, NewPDP(newEngine()))

	scope, err := resolver.Resolve(ctx, access.ResolveInput{UserID: 1, SessionID: "sess-1"})
	if err == nil {
		t.Fatal("Resolve() error = nil, want policy evaluation error")
	}
	if errors.Is(err, access.ErrProfileNotFound) || errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("Resolve() error = %v, want wrapped internal policy error", err)
	}
	assertZeroScope(t, scope)
}

func TestViewerResolverPolicyRevokedProfileVerification(t *testing.T) {
	ctx := context.Background()
	users := viewerResolverUserRepo{user: &models.User{ID: 1, AccessPolicyRevision: 5}}
	stores := viewerResolverStoreProvider{store: viewerResolverTestStore{}}
	engine, err := NewEngineWithCustom(ctx, map[string]ActiveSource{
		"scope": {Source: `package silo_custom.scope

import rego.v1

override(_, _) := {"profile_verified": false}
`},
	})
	if err != nil {
		t.Fatalf("NewEngineWithCustom() error: %v", err)
	}
	resolver := NewViewerResolver(users, stores, nil, NewPDP(engine))

	scope, err := resolver.Resolve(ctx, access.ResolveInput{UserID: 1, SessionID: "sess-1"})
	if !errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("Resolve() error = %v, want ErrProfileUnverified when policy revokes verification", err)
	}
	assertZeroScope(t, scope)
}

func TestViewerResolverAppliesGroupPolicy(t *testing.T) {
	ctx := context.Background()
	user := &models.User{
		ID:                   1,
		LibraryIDs:           []int{1, 2, 3},
		MaxPlaybackQuality:   access.PlaybackQuality4K,
		AccessPolicyRevision: 5,
	}
	group := &access.GroupPolicy{
		LibraryIDs:               []int{2, 4},
		MaxPlaybackQuality:       access.PlaybackQualityStandard,
		DownloadAllowed:          true,
		DownloadTranscodeAllowed: true,
		RequestsAllowed:          true,
	}
	users := viewerResolverUserRepo{user: user}
	stores := viewerResolverStoreProvider{store: viewerResolverTestStore{}}
	resolver := NewViewerResolver(
		users,
		stores,
		nil,
		newViewerResolverTestPDP(t, ctx),
		viewerResolverGroupProvider{group: group},
	)

	scope, err := resolver.Resolve(ctx, access.ResolveInput{UserID: 1, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if !scope.LibrariesRestricted || !reflect.DeepEqual(scope.AllowedLibraryIDs, []int{2}) {
		t.Fatalf("scope libraries = restricted %t ids %#v, want [2]", scope.LibrariesRestricted, scope.AllowedLibraryIDs)
	}
	if scope.MaxPlaybackQuality != access.PlaybackQualityStandard {
		t.Fatalf("MaxPlaybackQuality = %q, want %q", scope.MaxPlaybackQuality, access.PlaybackQualityStandard)
	}
}

type stubProfileTokenValidator struct {
	claims *access.ProfileTokenClaims
	err    error
}

func (v stubProfileTokenValidator) Validate(string) (*access.ProfileTokenClaims, error) {
	if v.err != nil {
		return nil, v.err
	}
	return v.claims, nil
}

type viewerResolverUserRepo struct {
	user *models.User
	err  error
}

type viewerResolverGroupProvider struct {
	group *access.GroupPolicy
	err   error
}

func (p viewerResolverGroupProvider) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	return p.group, p.err
}

func (r viewerResolverUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.user == nil || r.user.ID != id {
		return nil, errors.New("user not found")
	}
	return r.user, nil
}

type viewerResolverStoreProvider struct {
	store userstore.UserStore
	err   error
}

func (p viewerResolverStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, p.err
}

func (p viewerResolverStoreProvider) Close() error {
	return nil
}

type viewerResolverTestStore struct {
	userstore.UserStore
	profile  *userstore.Profile
	err      error
	settings map[string]string
}

func (s viewerResolverTestStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.profile == nil || s.profile.ID != id {
		return nil, nil
	}
	return s.profile, nil
}

func (s viewerResolverTestStore) GetSetting(_ context.Context, key string) (string, error) {
	return s.settings[key], nil
}

func viewerResolverExpectedInput(
	user *models.User,
	profile *userstore.Profile,
	input access.ResolveInput,
	profileVerified bool,
	disabled []int,
) ScopeInput {
	out := ScopeInput{
		SchemaVersion:        1,
		UserID:               user.ID,
		SessionID:            input.SessionID,
		ProfileID:            input.ProfileID,
		AccountLibraryIDs:    cloneViewerResolverInts(user.LibraryIDs),
		AccountRestricted:    user.LibraryIDs != nil,
		AccountMaxQuality:    user.MaxPlaybackQuality,
		AccessPolicyRevision: user.AccessPolicyRevision,
		DisabledLibraryIDs:   cloneViewerResolverInts(disabled),
		ProfileVerified:      profileVerified,
		RequestTime:          time.Now().UTC().Format(time.RFC3339),
		IsAPIKey:             false,
	}
	if profile != nil {
		out.ProfilePresent = true
		out.ProfileMaxRating = profile.MaxContentRating
		out.ProfileMaxQuality = profile.MaxPlaybackQuality
		out.ProfileLibraryLimited = profile.LibraryRestrictionsEnabled
		out.ProfileLibraryIDs = cloneViewerResolverInts(profile.AllowedLibraryIDs)
		out.ProfileHasPIN = profile.PINHash != ""
		out.ProfileMetadataLang = profile.PreferredMetadataLanguage
	}
	return out
}

func cloneViewerResolverInts(values []int) []int {
	if values == nil {
		return nil
	}
	out := make([]int, len(values))
	copy(out, values)
	return out
}

func newViewerResolverTestPDP(t *testing.T, ctx context.Context) *PDP {
	t.Helper()
	engine, err := NewEngine(ctx)
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	return NewPDP(engine)
}

func assertZeroScope(t *testing.T, scope access.Scope) {
	t.Helper()
	if scope.UserID != 0 ||
		scope.ProfileID != "" ||
		scope.AllowedLibraryIDs != nil ||
		scope.DisabledLibraryIDs != nil ||
		scope.LibrariesRestricted ||
		scope.MaxContentRating != "" ||
		scope.MaxPlaybackQuality != "" ||
		scope.PreferredMetadataLanguage != "" ||
		scope.PolicyRevision != 0 ||
		scope.ProfileVerified {
		t.Fatalf("scope = %#v, want zero Scope", scope)
	}
}
