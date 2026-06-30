package auth

import "testing"

func TestACLActionConstantsCoverLegacyPermissions(t *testing.T) {
	if LegacyPermissionAction(PermissionMarkerEdit) != ActionMarkersEdit {
		t.Fatalf("marker_edit maps to %q, want %q", LegacyPermissionAction(PermissionMarkerEdit), ActionMarkersEdit)
	}
	if LegacyPermissionAction(PermissionMetadataCuration) != ActionMetadataCurate {
		t.Fatalf("metadata_curation maps to %q, want %q", LegacyPermissionAction(PermissionMetadataCuration), ActionMetadataCurate)
	}
}

func TestACLActionConstantsIncludeAdminRecommendationActions(t *testing.T) {
	for _, action := range []ACLAction{
		ActionRecommendationsView,
		ActionRecommendationsManage,
	} {
		if !ValidACLAction(action) {
			t.Fatalf("ValidACLAction(%q) = false, want true", action)
		}
	}
}

func TestACLActionConstantsIncludePersonalListMutationAction(t *testing.T) {
	if !ValidACLAction(ActionPersonalListsManage) {
		t.Fatalf("ValidACLAction(%q) = false, want true", ActionPersonalListsManage)
	}
}

func TestUserFacingCapabilityActionsAreStable(t *testing.T) {
	got := UserFacingCapabilityActions()
	want := []ACLAction{
		ActionPlaybackPlay,
		ActionPlaybackTranscode,
		ActionDownloadsDirect,
		ActionDownloadsTranscode,
		ActionProfilesManage,
		ActionPersonalListsManage,
		ActionRequestsCreate,
	}
	if len(got) != len(want) {
		t.Fatalf("actions = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("actions = %#v, want %#v", got, want)
		}
	}
}

func TestBuiltInGroupSlugsAreStable(t *testing.T) {
	tests := map[string]BuiltInGroupSlug{
		"owner":            GroupOwner,
		"admin":            GroupAdmin,
		"library_manager":  GroupLibraryManager,
		"metadata_curator": GroupMetadataCurator,
		"standard_user":    GroupStandardUser,
		"restricted_user":  GroupRestrictedUser,
	}
	for want, got := range tests {
		if string(got) != want {
			t.Fatalf("group slug = %q, want %q", got, want)
		}
	}
}

func TestDefaultACLGroupSlugsForRole(t *testing.T) {
	tests := map[string][]string{
		"admin": {string(GroupAdmin)},
		"user":  {string(GroupStandardUser)},
		"":      {string(GroupStandardUser)},
	}
	for role, want := range tests {
		got := DefaultACLGroupSlugsForRole(role)
		if len(got) != len(want) {
			t.Fatalf("role %q default groups = %#v, want %#v", role, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("role %q default groups = %#v, want %#v", role, got, want)
			}
		}
	}
}

func TestNormalizeACLGroupSlugs(t *testing.T) {
	got, err := NormalizeACLGroupSlugs([]string{
		" standard_user ",
		"metadata_curator",
		"standard_user",
		"",
	})
	if err != nil {
		t.Fatalf("NormalizeACLGroupSlugs() error = %v", err)
	}
	want := []string{"standard_user", "metadata_curator"}
	if len(got) != len(want) {
		t.Fatalf("normalized groups = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized groups = %#v, want %#v", got, want)
		}
	}
}

func TestNormalizeACLGroupSlugsRejectsInvalidSlug(t *testing.T) {
	if _, err := NormalizeACLGroupSlugs([]string{"standard user"}); err == nil {
		t.Fatalf("NormalizeACLGroupSlugs() error = nil, want invalid slug error")
	}
}
