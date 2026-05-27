package auth

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNormalizePermissions_DeduplicatesAndSorts(t *testing.T) {
	got, err := NormalizePermissions([]string{
		" metadata_curation ",
		"metadata_curation",
		"",
	})
	if err != nil {
		t.Fatalf("NormalizePermissions returned error: %v", err)
	}
	want := []string{"metadata_curation"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %#v, want %#v", got, want)
	}
}

func TestNormalizePermissions_RejectsUnknownPermission(t *testing.T) {
	if _, err := NormalizePermissions([]string{"server_owner"}); err == nil {
		t.Fatal("expected unknown permission error")
	}
}

func TestHasEffectivePermission_AdminImpliesMetadataCuration(t *testing.T) {
	user := &models.User{Role: "admin", Enabled: true}
	if !HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("admin should have metadata curation")
	}
}

func TestHasEffectivePermission_UserRequiresAssignedPermission(t *testing.T) {
	user := &models.User{Role: "user", Enabled: true}
	if HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("plain user should not have metadata curation")
	}
	user.Permissions = []string{"metadata_curation"}
	if !HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("assigned user should have metadata curation")
	}
}
