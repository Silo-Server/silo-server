package handlers

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

type accountPolicyTestGroups struct {
	policy *access.GroupPolicy
	err    error
}

func (g accountPolicyTestGroups) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	return g.policy, g.err
}

// The account payload must report the permissions the route gates enforce,
// so a group whose allowed_permissions excludes one hides it from clients.
func TestEffectiveAccountPolicyAppliesGroupPermissionMask(t *testing.T) {
	groupID := int64(4)
	member := &models.User{
		ID:            9,
		Role:          models.RoleUser,
		Enabled:       true,
		Permissions:   []string{"marker_edit", "metadata_curation"},
		AccessGroupID: &groupID,
	}
	group := access.NoGroupPolicy()
	group.ID = groupID
	group.AllowedPermissions = []string{"metadata_curation"}

	tests := []struct {
		name   string
		user   func() *models.User
		groups access.GroupPolicyProvider
		want   accountPolicy
	}{
		{
			name:   "group mask removes a permission",
			user:   func() *models.User { return member },
			groups: accountPolicyTestGroups{policy: &group},
			want:   accountPolicy{DownloadAllowed: true, Permissions: []string{"metadata_curation"}},
		},
		{
			name:   "group without a mask keeps assigned permissions",
			user:   func() *models.User { return member },
			groups: accountPolicyTestGroups{policy: &access.GroupPolicy{ID: groupID, DownloadAllowed: true}},
			want:   accountPolicy{DownloadAllowed: true, Permissions: []string{"marker_edit", "metadata_curation"}},
		},
		{
			name:   "failed group lookup fails closed",
			user:   func() *models.User { return member },
			groups: accountPolicyTestGroups{err: errors.New("db down")},
			want:   accountPolicy{Permissions: []string{}},
		},
		{
			// The admin role grant applies only while acting through the
			// primary profile, which clients resolve per profile; the payload
			// carries what the admin keeps on any profile. Groups never mask
			// an admin.
			name: "admin reports its assigned permissions unmasked",
			user: func() *models.User {
				return &models.User{ID: 1, Role: models.RoleAdmin, Enabled: true, Permissions: []string{"marker_edit"}, AccessGroupID: &groupID}
			},
			groups: accountPolicyTestGroups{policy: &group},
			want:   accountPolicy{DownloadAllowed: true, Permissions: []string{"marker_edit"}},
		},
		{
			name: "disabled account reports no permissions",
			user: func() *models.User {
				u := *member
				u.Enabled = false
				return &u
			},
			groups: accountPolicyTestGroups{policy: &group},
			want:   accountPolicy{DownloadAllowed: true, Permissions: []string{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := tt.user()
			got := effectiveAccountPolicy(context.Background(), user, tt.groups)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("effectiveAccountPolicy = %+v, want %+v", got, tt.want)
			}
			view := buildUserResponse(user, got, nil, nil)
			if !reflect.DeepEqual(view.Permissions, tt.want.Permissions) {
				t.Fatalf("UserView.Permissions = %#v, want %#v", view.Permissions, tt.want.Permissions)
			}
		})
	}
}

// The admin view's effective_policy.permissions and the account payload come
// from the same resolution.
func TestAdminUserEffectivePermissionsMatchAccountPayload(t *testing.T) {
	groupID := int64(4)
	group := access.NoGroupPolicy()
	group.ID = groupID
	group.AllowedPermissions = []string{"metadata_curation"}

	for _, user := range []*models.User{
		{ID: 9, Role: models.RoleUser, Enabled: true, Permissions: []string{"marker_edit", "metadata_curation"}, AccessGroupID: &groupID},
		{ID: 1, Role: models.RoleAdmin, Enabled: true, Permissions: []string{"marker_edit"}},
	} {
		admin := toAdminUserResponse(user, &group)
		account := effectiveAccountPolicy(context.Background(), user, accountPolicyTestGroups{policy: &group})
		if !reflect.DeepEqual(admin.EffectivePolicy.Permissions, account.Permissions) {
			t.Fatalf("role %s: admin effective permissions = %#v, account permissions = %#v",
				user.Role, admin.EffectivePolicy.Permissions, account.Permissions)
		}
	}
}

// File-path visibility follows the same masked metadata_curation grant as the
// curation gate.
func TestHasMaskedMetadataCuration(t *testing.T) {
	groupID := int64(4)
	curator := &models.User{ID: 9, Role: models.RoleUser, Enabled: true, Permissions: []string{"metadata_curation"}, AccessGroupID: &groupID}
	masking := access.NoGroupPolicy()
	masking.AllowedPermissions = []string{"marker_edit"}
	open := access.NoGroupPolicy()

	tests := []struct {
		name   string
		user   *models.User
		groups access.GroupPolicyProvider
		want   bool
	}{
		{name: "group allows", user: curator, groups: accountPolicyTestGroups{policy: &open}, want: true},
		{name: "group masks", user: curator, groups: accountPolicyTestGroups{policy: &masking}},
		{name: "lookup fails", user: curator, groups: accountPolicyTestGroups{err: errors.New("db down")}},
		{name: "not assigned", user: &models.User{ID: 3, Role: models.RoleUser, Enabled: true}, groups: accountPolicyTestGroups{policy: &open}},
		{name: "nil user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasMaskedMetadataCuration(context.Background(), tt.user, tt.groups); got != tt.want {
				t.Fatalf("hasMaskedMetadataCuration = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestBuildUserResponseNeverReturnsNilPermissions(t *testing.T) {
	view := buildUserResponse(&models.User{ID: 1, Role: models.RoleUser}, accountPolicy{}, nil, nil)
	if view.Permissions == nil {
		t.Fatal("Permissions = nil, want empty slice so v1 serializes []")
	}
}
