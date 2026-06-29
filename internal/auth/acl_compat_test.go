package auth

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCompatibilityGroupsForAdminAndUser(t *testing.T) {
	adminGroups := CompatibilityGroupsForUser(&models.User{ID: 1, Role: "admin", Enabled: true})
	if len(adminGroups) != 1 || adminGroups[0] != GroupAdmin {
		t.Fatalf("admin groups = %#v, want [%q]", adminGroups, GroupAdmin)
	}

	userGroups := CompatibilityGroupsForUser(&models.User{ID: 2, Role: "user", Enabled: true})
	if len(userGroups) != 1 || userGroups[0] != GroupViewer {
		t.Fatalf("user groups = %#v, want [%q]", userGroups, GroupViewer)
	}
}

func TestCompatibilityRulesForLegacyPermissions(t *testing.T) {
	user := &models.User{
		ID:          7,
		Role:        "user",
		Enabled:     true,
		Permissions: []string{"marker_edit", "metadata_curation"},
	}

	rules := CompatibilityRulesForUser(user)
	actions := map[ACLAction]bool{}
	for _, rule := range rules {
		if rule.Effect == EffectAllow {
			actions[rule.Action] = true
		}
	}

	if !actions[ActionMarkersEdit] {
		t.Fatalf("expected marker_edit compatibility rule, got %#v", rules)
	}
	if !actions[ActionMetadataCurate] {
		t.Fatalf("expected metadata_curation compatibility rule, got %#v", rules)
	}
}

func TestCompatibilityEffectivePolicyPreservesUserLimits(t *testing.T) {
	user := &models.User{
		ID:                       7,
		Enabled:                  true,
		LibraryIDs:               []int{10, 20},
		MaxPlaybackQuality:       "1080p",
		MaxStreams:               3,
		MaxTranscodes:            1,
		DownloadAllowed:          true,
		DownloadTranscodeAllowed: false,
	}

	policy := CompatibilityEffectivePolicyForUser(user)
	if len(policy.LibraryIDs) != 2 || policy.LibraryIDs[0] != 10 || policy.LibraryIDs[1] != 20 {
		t.Fatalf("library ids = %#v, want [10 20]", policy.LibraryIDs)
	}
	if policy.MaxPlaybackQuality != "1080p" {
		t.Fatalf("max quality = %q, want 1080p", policy.MaxPlaybackQuality)
	}
	if policy.MaxStreams != 3 {
		t.Fatalf("max streams = %d, want 3", policy.MaxStreams)
	}
	if policy.MaxTranscodes != 1 {
		t.Fatalf("max transcodes = %d, want 1", policy.MaxTranscodes)
	}
	if !policy.DirectDownloadsAllowed {
		t.Fatalf("direct downloads should be allowed")
	}
	if policy.TranscodedDownloadsAllowed {
		t.Fatalf("transcoded downloads should not be allowed")
	}
}

func TestCompatibilityEffectivePolicyPreservesLibraryIDNilness(t *testing.T) {
	unrestrictedUser := &models.User{
		ID:      8,
		Enabled: true,
	}
	unrestrictedPolicy := CompatibilityEffectivePolicyForUser(unrestrictedUser)
	if unrestrictedPolicy.LibraryIDs != nil {
		t.Fatalf("unrestricted library ids = %#v, want nil", unrestrictedPolicy.LibraryIDs)
	}

	restrictedUser := &models.User{
		ID:         9,
		Enabled:    true,
		LibraryIDs: []int{},
	}
	restrictedPolicy := CompatibilityEffectivePolicyForUser(restrictedUser)
	if restrictedPolicy.LibraryIDs == nil {
		t.Fatalf("restricted library ids is nil, want empty non-nil slice")
	}
	if len(restrictedPolicy.LibraryIDs) != 0 {
		t.Fatalf("restricted library ids len = %d, want 0", len(restrictedPolicy.LibraryIDs))
	}
}
