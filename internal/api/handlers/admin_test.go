package handlers

import "testing"

func TestUpdateRequiresSessionRevocation_ForPermissions(t *testing.T) {
	if !updateRequiresSessionRevocation(updateUserRequest{
		Permissions: updateStringSliceField{Set: true, Value: []string{"metadata_curation"}},
	}) {
		t.Fatal("permission updates should revoke sessions")
	}
}
