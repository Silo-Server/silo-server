package policy

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPermissionEffectivePermissionParity(t *testing.T) {
	ctx := context.Background()
	pdp := newPermissionParityPDP(t)

	assignedCases := []struct {
		name        string
		permissions []string
	}{
		{name: "none"},
		{name: "marker_edit", permissions: []string{string(auth.PermissionMarkerEdit)}},
		{name: "both", permissions: []string{string(auth.PermissionMarkerEdit), string(auth.PermissionMetadataCuration)}},
	}
	permissions := []auth.Permission{
		auth.PermissionMarkerEdit,
		auth.PermissionMetadataCuration,
	}

	for _, role := range []string{"admin", "user"} {
		for _, enabled := range []bool{false, true} {
			for _, assigned := range assignedCases {
				t.Run(role+"/"+assigned.name, func(t *testing.T) {
					user := &models.User{
						ID:          7,
						Role:        role,
						Enabled:     enabled,
						Permissions: clonePermissionStrings(assigned.permissions),
					}
					for _, permission := range permissions {
						input := permissionInputForUser(user, string(permission))
						decision, _, err := pdp.CheckPermission(ctx, input)
						if err != nil {
							t.Fatalf("CheckPermission(%s) error: %v", permission, err)
						}
						want := auth.HasEffectivePermission(user, permission)
						if decision.Allowed != want {
							t.Fatalf("CheckPermission(%s) allowed = %t, want %t (decision %#v)", permission, decision.Allowed, want, decision)
						}
					}

					gotEffective := policyEffectivePermissions(t, ctx, pdp, user)
					wantEffective := auth.EffectivePermissions(user)
					sort.Strings(wantEffective)
					if !reflect.DeepEqual(gotEffective, wantEffective) {
						t.Fatalf("policy effective permissions = %#v, want %#v", gotEffective, wantEffective)
					}
				})
			}
		}
	}
}

func TestPermissionActingAdminParityCases(t *testing.T) {
	ctx := context.Background()
	pdp := newPermissionParityPDP(t)

	tests := []struct {
		name              string
		role              string
		enabled           bool
		declaredProfileID string
		actingAsPrimary   bool
		wantAllowed       bool
	}{
		{name: "non_admin", role: "user", enabled: true, wantAllowed: false},
		{name: "disabled_admin", role: "admin", enabled: false, wantAllowed: false},
		{name: "admin_without_declared_profile", role: "admin", enabled: true, wantAllowed: true},
		{name: "admin_primary_profile", role: "admin", enabled: true, declaredProfileID: "prof-1", actingAsPrimary: true, wantAllowed: true},
		{name: "admin_non_primary_profile", role: "admin", enabled: true, declaredProfileID: "prof-2", actingAsPrimary: false, wantAllowed: false},
		{name: "admin_other_account_profile", role: "admin", enabled: true, declaredProfileID: "prof-x", actingAsPrimary: false, wantAllowed: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, _, err := pdp.CheckPermission(ctx, PermissionInput{
				SchemaVersion:     1,
				UserID:            7,
				Role:              test.role,
				UserEnabled:       test.enabled,
				Permission:        PermissionActingAdmin,
				DeclaredProfileID: test.declaredProfileID,
				ActingAsPrimary:   test.actingAsPrimary,
				RequestTime:       "2026-07-02T12:00:00Z",
			})
			if err != nil {
				t.Fatalf("CheckPermission() error: %v", err)
			}
			if decision.Allowed != test.wantAllowed {
				t.Fatalf("Allowed = %t, want %t (decision %#v)", decision.Allowed, test.wantAllowed, decision)
			}
		})
	}
}

func TestPermissionMetadataCurationLibraryScope(t *testing.T) {
	ctx := context.Background()
	pdp := newPermissionParityPDP(t)

	tests := []struct {
		name              string
		role              string
		assigned          []string
		declaredProfileID string
		actingAsPrimary   bool
		targetLibraryIDs  []int
		userLibraryIDs    []int
		restricted        bool
		wantAllowed       bool
	}{
		{
			name:             "unrestricted",
			role:             "user",
			assigned:         []string{PermissionMetadataCuration},
			targetLibraryIDs: []int{8, 9},
			wantAllowed:      true,
		},
		{
			name:             "restricted_in_scope",
			role:             "user",
			assigned:         []string{PermissionMetadataCuration},
			targetLibraryIDs: []int{1, 3},
			userLibraryIDs:   []int{1, 2, 3},
			restricted:       true,
			wantAllowed:      true,
		},
		{
			name:             "restricted_out_of_scope",
			role:             "user",
			assigned:         []string{PermissionMetadataCuration},
			targetLibraryIDs: []int{1, 4},
			userLibraryIDs:   []int{1, 2, 3},
			restricted:       true,
			wantAllowed:      false,
		},
		{
			name:             "empty_targets_fail_closed",
			role:             "user",
			assigned:         []string{PermissionMetadataCuration},
			targetLibraryIDs: []int{},
			wantAllowed:      false,
		},
		{
			name:             "acting_admin_bypasses_out_of_scope",
			role:             "admin",
			targetLibraryIDs: []int{4},
			userLibraryIDs:   []int{1},
			restricted:       true,
			wantAllowed:      true,
		},
		{
			name:              "non_primary_admin_requires_explicit_assignment",
			role:              "admin",
			declaredProfileID: "prof-2",
			actingAsPrimary:   false,
			targetLibraryIDs:  []int{1},
			userLibraryIDs:    []int{1},
			restricted:        true,
			wantAllowed:       false,
		},
		{
			name:              "non_primary_admin_with_assignment",
			role:              "admin",
			assigned:          []string{PermissionMetadataCuration},
			declaredProfileID: "prof-2",
			actingAsPrimary:   false,
			targetLibraryIDs:  []int{1},
			userLibraryIDs:    []int{1},
			restricted:        true,
			wantAllowed:       true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, _, err := pdp.CheckPermission(ctx, PermissionInput{
				SchemaVersion:           1,
				UserID:                  7,
				Role:                    test.role,
				UserEnabled:             true,
				AssignedPermissions:     clonePermissionStrings(test.assigned),
				Permission:              PermissionMetadataCuration,
				DeclaredProfileID:       test.declaredProfileID,
				ActingAsPrimary:         test.actingAsPrimary,
				TargetLibraryIDs:        clonePermissionInts(test.targetLibraryIDs),
				UserLibraryIDs:          clonePermissionInts(test.userLibraryIDs),
				UserLibrariesRestricted: test.restricted,
				RequestTime:             "2026-07-02T12:00:00Z",
			})
			if err != nil {
				t.Fatalf("CheckPermission() error: %v", err)
			}
			if decision.Allowed != test.wantAllowed {
				t.Fatalf("Allowed = %t, want %t (decision %#v)", decision.Allowed, test.wantAllowed, decision)
			}
		})
	}
}

func newPermissionParityPDP(t *testing.T) *PDP {
	t.Helper()
	engine, err := NewEngine(context.Background())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	return NewPDP(engine)
}

func permissionInputForUser(user *models.User, permission string) PermissionInput {
	return PermissionInput{
		SchemaVersion:           1,
		UserID:                  user.ID,
		Role:                    user.Role,
		UserEnabled:             user.Enabled,
		AssignedPermissions:     clonePermissionStrings(user.Permissions),
		Permission:              permission,
		TargetLibraryIDs:        []int{1},
		UserLibrariesRestricted: false,
		RequestTime:             "2026-07-02T12:00:00Z",
	}
}

func policyEffectivePermissions(t *testing.T, ctx context.Context, pdp *PDP, user *models.User) []string {
	t.Helper()
	var out []string
	for _, permission := range []string{PermissionMarkerEdit, PermissionMetadataCuration} {
		decision, _, err := pdp.CheckPermission(ctx, permissionInputForUser(user, permission))
		if err != nil {
			t.Fatalf("CheckPermission(%s) error: %v", permission, err)
		}
		if decision.Allowed {
			out = append(out, permission)
		}
	}
	sort.Strings(out)
	if out == nil {
		return []string{}
	}
	return out
}

func clonePermissionStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func clonePermissionInts(values []int) []int {
	if values == nil {
		return nil
	}
	out := make([]int, len(values))
	copy(out, values)
	return out
}
