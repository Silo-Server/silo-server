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

func TestPluginRoleFromResponse(t *testing.T) {
	claims, err := structpb.NewStruct(map[string]any{pluginRoleClaimKey: "ADMIN"})
	if err != nil {
		t.Fatal(err)
	}
	role, present, err := pluginRoleFromResponse(&pluginv1.AuthenticateResponse{Claims: claims})
	if err != nil {
		t.Fatalf("pluginRoleFromResponse() error = %v", err)
	}
	if !present || role != "admin" {
		t.Fatalf("role = %q, present = %v; want admin, true", role, present)
	}

	claims, err = structpb.NewStruct(map[string]any{pluginRoleClaimKey: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pluginRoleFromResponse(&pluginv1.AuthenticateResponse{Claims: claims}); err == nil {
		t.Fatal("expected unsupported plugin role to be rejected")
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
