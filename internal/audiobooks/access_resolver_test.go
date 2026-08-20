package audiobooks

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type recordingScopeResolver struct {
	input access.ResolveInput
	err   error
}

func (r *recordingScopeResolver) Resolve(_ context.Context, input access.ResolveInput) (access.Scope, error) {
	r.input = input
	if r.err != nil {
		return access.Scope{}, r.err
	}
	return access.Scope{UserID: input.UserID, ProfileID: input.ProfileID}, nil
}

func TestABSAccessResolverPreservesSelectedProfileScope(t *testing.T) {
	resolver := &recordingScopeResolver{}
	absResolver := NewABSAccessResolver(nil, nil, resolver)

	filter, err := absResolver.ResolveABSAccess(context.Background(), "42", "primary-profile")
	if err != nil {
		t.Fatalf("ResolveABSAccess: %v", err)
	}
	if resolver.input.ProfileID != "primary-profile" {
		t.Fatalf("resolved profile = %q, want selected profile", resolver.input.ProfileID)
	}
	if !resolver.input.SkipPINVerification {
		t.Fatal("ABS-authenticated request did not skip duplicate PIN verification")
	}
	if filter.ProfileID != "primary-profile" {
		t.Fatalf("access filter profile = %q, want selected profile", filter.ProfileID)
	}
}

func TestABSAccessResolverMapsProfileDenials(t *testing.T) {
	resolver := NewABSAccessResolver(nil, nil, &recordingScopeResolver{err: access.ErrProfileNotFound})
	_, err := resolver.ResolveABSAccess(context.Background(), "42", "missing")
	if !errors.Is(err, abs.ErrAccessDenied) {
		t.Fatalf("error = %v, want ErrAccessDenied", err)
	}
}

type accessResolverUserRepo struct{ user *models.User }

func (r accessResolverUserRepo) GetByID(context.Context, int) (*models.User, error) {
	return r.user, nil
}

type accessResolverUserStore struct {
	userstore.UserStore
	profile *userstore.Profile
}

func (s accessResolverUserStore) GetProfile(context.Context, string) (*userstore.Profile, error) {
	return s.profile, nil
}

type accessResolverStoreProvider struct{ store userstore.UserStore }

func (p accessResolverStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (accessResolverStoreProvider) Close() error { return nil }

type accessResolverGroupProvider struct{ policy *access.GroupPolicy }

func (p accessResolverGroupProvider) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	return p.policy, nil
}

func TestABSAccessResolverMetadataCurationPolicy(t *testing.T) {
	groupID := int64(7)
	tests := []struct {
		name      string
		user      *models.User
		profile   *userstore.Profile
		profileID string
		group     *access.GroupPolicy
		wantAllow bool
	}{
		{
			name:      "explicit curator",
			user:      &models.User{ID: 42, Role: "user", Enabled: true, Permissions: []string{"metadata_curation"}},
			profileID: "viewer",
			wantAllow: true,
		},
		{
			name:      "group removes explicit permission",
			user:      &models.User{ID: 42, Role: "user", Enabled: true, Permissions: []string{"metadata_curation"}, AccessGroupID: &groupID},
			profileID: "viewer",
			group:     &access.GroupPolicy{AllowedPermissions: []string{"marker_edit"}},
		},
		{
			name:      "primary profile admin",
			user:      &models.User{ID: 42, Role: "admin", Enabled: true},
			profile:   &userstore.Profile{ID: "primary", IsPrimary: true},
			profileID: "primary",
			wantAllow: true,
		},
		{
			name:      "non-primary profile admin",
			user:      &models.User{ID: 42, Role: "admin", Enabled: true},
			profile:   &userstore.Profile{ID: "viewer", IsPrimary: false},
			profileID: "viewer",
		},
		{
			name:      "disabled explicit curator",
			user:      &models.User{ID: 42, Role: "user", Enabled: false, Permissions: []string{"metadata_curation"}},
			profileID: "viewer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := accessResolverStoreProvider{store: accessResolverUserStore{profile: tt.profile}}
			resolver := NewABSAccessResolver(
				accessResolverUserRepo{user: tt.user},
				provider,
				&recordingScopeResolver{},
				accessResolverGroupProvider{policy: tt.group},
			)
			resolver.SetPermissionDecider(newTestPermissionDecider(t))
			allowed, err := resolver.CanCurateMetadata(context.Background(), "42", tt.profileID)
			if err != nil {
				t.Fatalf("CanCurateMetadata: %v", err)
			}
			if allowed != tt.wantAllow {
				t.Fatalf("allowed = %v, want %v", allowed, tt.wantAllow)
			}
		})
	}
}

// newTestPermissionDecider builds the real PDP over the vendored policy bundle
// so these cases assert the decision ABS clients actually get, overrides and
// all — not a stand-in with its own rules.
func newTestPermissionDecider(t *testing.T) policy.PermissionDecider {
	t.Helper()
	engine, err := policy.NewEngine(context.Background())
	if err != nil {
		t.Fatalf("policy.NewEngine: %v", err)
	}
	return policy.NewPDP(engine)
}

// TestABSAccessResolverMetadataCurationFailsClosedWithoutDecider pins the
// no-PDP behavior: the gate errors instead of falling back to a hand-rolled
// permission check, which would silently bypass custom policy overrides and
// decision logging.
func TestABSAccessResolverMetadataCurationFailsClosedWithoutDecider(t *testing.T) {
	resolver := NewABSAccessResolver(
		accessResolverUserRepo{user: &models.User{ID: 42, Role: "admin", Enabled: true}},
		accessResolverStoreProvider{store: accessResolverUserStore{}},
		&recordingScopeResolver{},
	)

	allowed, err := resolver.CanCurateMetadata(context.Background(), "42", "")
	if allowed {
		t.Fatal("metadata curation allowed without a policy decider")
	}
	if !errors.Is(err, policy.ErrNoPermissionDecider) {
		t.Fatalf("error = %v, want ErrNoPermissionDecider", err)
	}
}

// TestABSAccessResolverMetadataCurationHonorsCustomOverride proves the ABS
// gate goes through the PDP: a custom override that denies metadata curation
// has to bind here exactly as it does on the native route gate.
func TestABSAccessResolverMetadataCurationHonorsCustomOverride(t *testing.T) {
	engine, err := policy.NewEngineWithCustom(context.Background(), map[string]policy.ActiveSource{
		policy.DomainPermission: {DocumentID: 1, VersionID: 1, Source: `package silo_custom.permission

import rego.v1

override(base, i) := {"allowed": false, "reason": "curation frozen"} if {
	i.permission == "metadata_curation"
} else := base`},
	})
	if err != nil {
		t.Fatalf("policy.NewEngineWithCustom: %v", err)
	}

	resolver := NewABSAccessResolver(
		accessResolverUserRepo{user: &models.User{ID: 42, Role: "admin", Enabled: true}},
		accessResolverStoreProvider{store: accessResolverUserStore{}},
		&recordingScopeResolver{},
	)
	resolver.SetPermissionDecider(policy.NewPDP(engine))

	allowed, err := resolver.CanCurateMetadata(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("CanCurateMetadata: %v", err)
	}
	if allowed {
		t.Fatal("custom policy override denying metadata curation was not applied to the ABS gate")
	}
}
