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

func TestBuiltInGroupSlugsAreStable(t *testing.T) {
	tests := map[string]BuiltInGroupSlug{
		"owner":             GroupOwner,
		"admin":             GroupAdmin,
		"library_manager":   GroupLibraryManager,
		"metadata_curator":  GroupMetadataCurator,
		"viewer":            GroupViewer,
		"restricted_viewer": GroupRestrictedViewer,
	}
	for want, got := range tests {
		if string(got) != want {
			t.Fatalf("group slug = %q, want %q", got, want)
		}
	}
}
