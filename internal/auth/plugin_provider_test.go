package auth

import (
	"slices"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestRandomPluginOnlyPasswordFitsBcryptLimit(t *testing.T) {
	password, err := randomPluginOnlyPassword()
	if err != nil {
		t.Fatalf("randomPluginOnlyPassword() error = %v", err)
	}

	if len(password) > 72 {
		t.Fatalf("password length = %d, want <= 72", len(password))
	}
	if _, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost); err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", err)
	}
}

func authResponseWithClaims(t *testing.T, values map[string]any) *pluginv1.AuthenticateResponse {
	t.Helper()
	claims, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &pluginv1.AuthenticateResponse{Claims: claims}
}

func TestPluginRoleFromResponseRequiresManagedMarker(t *testing.T) {
	response := authResponseWithClaims(t, map[string]any{pluginRoleClaimKey: "admin"})
	role, present, err := pluginRoleFromResponse(response)
	if err != nil {
		t.Fatalf("pluginRoleFromResponse() error = %v", err)
	}
	if present || role != "" {
		t.Fatalf("role = %q, present = %v; unmanaged role claim must be ignored", role, present)
	}

	response = authResponseWithClaims(t, map[string]any{
		pluginRoleManagedClaimKey: false,
		pluginRoleClaimKey:        "admin",
	})
	role, present, err = pluginRoleFromResponse(response)
	if err != nil {
		t.Fatalf("pluginRoleFromResponse() error = %v", err)
	}
	if present || role != "" {
		t.Fatalf("role = %q, present = %v; disabled managed role claim must be ignored", role, present)
	}
}

func TestPluginRoleFromResponseAcceptsManagedAdmin(t *testing.T) {
	response := authResponseWithClaims(t, map[string]any{
		pluginRoleManagedClaimKey: true,
		pluginRoleClaimKey:        "ADMIN",
	})
	role, present, err := pluginRoleFromResponse(response)
	if err != nil {
		t.Fatalf("pluginRoleFromResponse() error = %v", err)
	}
	if !present || role != "admin" {
		t.Fatalf("role = %q, present = %v; want admin, true", role, present)
	}
}

func TestPluginRoleFromResponseRejectsMalformedManagedClaims(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]any
	}{
		{
			name: "managed marker is not boolean",
			claims: map[string]any{
				pluginRoleManagedClaimKey: "true",
				pluginRoleClaimKey:        "admin",
			},
		},
		{
			name: "managed role is missing",
			claims: map[string]any{
				pluginRoleManagedClaimKey: true,
			},
		},
		{
			name: "managed role is empty",
			claims: map[string]any{
				pluginRoleManagedClaimKey: true,
				pluginRoleClaimKey:        " ",
			},
		},
		{
			name: "managed role is unsupported",
			claims: map[string]any{
				pluginRoleManagedClaimKey: true,
				pluginRoleClaimKey:        "owner",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := pluginRoleFromResponse(authResponseWithClaims(t, test.claims)); err == nil {
				t.Fatal("expected malformed managed role claim to be rejected")
			}
		})
	}
}

func TestRoleSyncUpdateInputPromotesAndDemotes(t *testing.T) {
	groupID := int64(42)
	user := &models.User{ID: 7, Role: "user", AccessGroupID: &groupID}
	promote, changed := roleSyncUpdateInput(user, "admin", nil)
	if !changed || promote.Role == nil || *promote.Role != "admin" {
		t.Fatalf("promotion input = %#v, changed = %v", promote, changed)
	}
	if !promote.AccessGroupIDSet || promote.AccessGroupID != nil {
		t.Fatalf("promotion must remove the normal access group: %#v", promote)
	}

	admin := &models.User{ID: 7, Role: "admin"}
	demote, changed := roleSyncUpdateInput(admin, "user", &groupID)
	if !changed || demote.Role == nil || *demote.Role != "user" {
		t.Fatalf("demotion input = %#v, changed = %v", demote, changed)
	}
	if demote.Permissions == nil || !slices.Equal(*demote.Permissions, DefaultUserPermissions()) {
		t.Fatalf("demotion permissions = %#v, want defaults", demote.Permissions)
	}
	if !demote.AccessGroupIDSet || demote.AccessGroupID == nil || *demote.AccessGroupID != groupID {
		t.Fatalf("demotion must assign the default access group: %#v", demote)
	}
}
