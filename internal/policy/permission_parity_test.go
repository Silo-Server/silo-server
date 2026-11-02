package policy

import (
	"context"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestPermissionPayloadParity pins the account payload's permissions
// (auth.PolicyPermissions over the group-masked effective policy) against the
// PDP the route gates consult. Clients grant an admin every permission while
// it acts as admin and otherwise use the payload list. Whatever that shows,
// the PDP must allow; for non-admins the two must agree exactly.
func TestPermissionPayloadParity(t *testing.T) {
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
	groupCases := []struct {
		name    string
		allowed []string
		grouped bool
	}{
		{name: "ungrouped"},
		{name: "group_unmasked", grouped: true},
		{name: "group_masks_all", grouped: true, allowed: []string{}},
		{name: "group_allows_curation", grouped: true, allowed: []string{string(auth.PermissionMetadataCuration)}},
	}
	profileCases := []struct {
		name              string
		declaredProfileID string
		actingAsPrimary   bool
	}{
		{name: "no_profile"},
		{name: "primary_profile", declaredProfileID: "prof-1", actingAsPrimary: true},
		{name: "non_primary_profile", declaredProfileID: "prof-2"},
	}
	permissions := []auth.Permission{
		auth.PermissionMarkerEdit,
		auth.PermissionMetadataCuration,
	}

	for _, role := range []string{"admin", "user"} {
		for _, assigned := range assignedCases {
			for _, groupCase := range groupCases {
				for _, profile := range profileCases {
					t.Run(role+"/"+assigned.name+"/"+groupCase.name+"/"+profile.name, func(t *testing.T) {
						user := &models.User{
							ID:          7,
							Role:        role,
							Enabled:     true,
							Permissions: clonePermissionStrings(assigned.permissions),
						}
						var group *access.GroupPolicy
						if groupCase.grouped {
							groupID := int64(3)
							user.AccessGroupID = &groupID
							policy := access.NoGroupPolicy()
							if groupCase.allowed != nil {
								policy.AllowedPermissions = clonePermissionStrings(groupCase.allowed)
							}
							group = &policy
						}
						effective := access.ApplyGroupPolicy(user, group)
						payload := auth.PolicyPermissions(effective)
						actingAdmin := role == "admin" && (profile.declaredProfileID == "" || profile.actingAsPrimary)

						for _, permission := range permissions {
							input := permissionInputForUser(user, string(permission))
							input.AssignedPermissions = clonePermissionStrings(effective.Permissions)
							input.DeclaredProfileID = profile.declaredProfileID
							input.ActingAsPrimary = profile.actingAsPrimary
							decision, _, err := pdp.CheckPermission(ctx, input)
							if err != nil {
								t.Fatalf("CheckPermission(%s) error: %v", permission, err)
							}
							shown := actingAdmin || slices.Contains(payload, string(permission))
							if shown && !decision.Allowed {
								t.Fatalf("%s shown to client (payload %#v) but PDP denies (decision %#v)", permission, payload, decision)
							}
							if shown != decision.Allowed {
								t.Fatalf("%s shown = %t, PDP allowed = %t (payload %#v)", permission, shown, decision.Allowed, payload)
							}
						}
					})
				}
			}
		}
	}
}

func TestPermissionDisabledAccountDenied(t *testing.T) {
	ctx := context.Background()
	pdp := newPermissionParityPDP(t)
	for _, role := range []string{"admin", "user"} {
		user := &models.User{ID: 7, Role: role, Permissions: []string{string(auth.PermissionMarkerEdit), string(auth.PermissionMetadataCuration)}}
		for _, permission := range []string{PermissionMarkerEdit, PermissionMetadataCuration} {
			decision, _, err := pdp.CheckPermission(ctx, permissionInputForUser(user, permission))
			if err != nil {
				t.Fatalf("CheckPermission(%s) error: %v", permission, err)
			}
			if decision.Allowed {
				t.Fatalf("disabled %s allowed %s (decision %#v)", role, permission, decision)
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
