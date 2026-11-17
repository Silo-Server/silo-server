package handlers

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestToAdminUserResponseReportsOverridesAndEffectivePolicy(t *testing.T) {
	groupID := int64(3)
	user := &models.User{
		ID:              9,
		Username:        "ada",
		Email:           "ada@example.com",
		Role:            "user",
		Permissions:     []string{"marker_edit"},
		Enabled:         true,
		MaxStreams:      ptrOf(6),
		DownloadAllowed: ptrOf(true),
		AccessGroupID:   &groupID,
	}
	group := &access.GroupPolicy{
		ID:                       groupID,
		LibraryIDs:               []int{2, 1},
		MaxPlaybackQuality:       access.PlaybackQualityStandard,
		DownloadAllowed:          false,
		DownloadTranscodeAllowed: false,
		TranscodeAllowed:         true,
		AudioTranscodeAllowed:    true,
		MaxStreams:               2,
		MaxTranscodes:            1,
		MaxProfiles:              3,
		RequestsAllowed:          true,
	}

	resp := toAdminUserResponse(user, group)

	// Stored overrides: only the set fields are non-null.
	if resp.MaxStreams == nil || *resp.MaxStreams != 6 || resp.DownloadAllowed == nil || !*resp.DownloadAllowed {
		t.Fatalf("overrides = streams %v download %v, want 6/true", resp.MaxStreams, resp.DownloadAllowed)
	}
	if resp.MaxTranscodes != nil || resp.MaxPlaybackQuality != nil || resp.TranscodeAllowed != nil ||
		resp.AudioTranscodeAllowed != nil || resp.DownloadTranscodeAllowed != nil || resp.RequestsAllowed != nil || resp.LibraryIDs != nil ||
		resp.MaxProfilesOverride != nil {
		t.Fatalf("inherited fields must serialize as null, got %+v", resp)
	}

	// Effective policy: overrides win, everything else comes from the group.
	want := EffectivePolicyView{
		LibraryIDs:               []int{1, 2},
		MaxPlaybackQuality:       access.PlaybackQualityStandard,
		MaxStreams:               6,
		MaxTranscodes:            1,
		MaxProfiles:              3,
		TranscodeAllowed:         true,
		AudioTranscodeAllowed:    true,
		DownloadAllowed:          true,
		DownloadTranscodeAllowed: false,
		RequestsAllowed:          true,
		Permissions:              []string{"marker_edit"},
	}
	if !reflect.DeepEqual(resp.EffectivePolicy, want) {
		t.Fatalf("effective_policy = %+v, want %+v", resp.EffectivePolicy, want)
	}

	// An explicit empty library override must stay empty, never nil: nil is
	// inherit, and collapsing it would let an open+save silently delete a
	// deny-all override.
	locked := toAdminUserResponse(&models.User{ID: 2, LibraryIDs: []int{}}, group)
	if locked.LibraryIDs == nil || len(locked.LibraryIDs) != 0 {
		t.Fatalf("empty library override = %#v, want non-nil empty", locked.LibraryIDs)
	}

	// Ungrouped: the permissive no-group default fills the gaps.
	ungrouped := toAdminUserResponse(&models.User{ID: 1, MaxStreams: ptrOf(2)}, nil)
	if ungrouped.EffectivePolicy.MaxStreams != 2 || !ungrouped.EffectivePolicy.DownloadAllowed || ungrouped.EffectivePolicy.LibraryIDs != nil {
		t.Fatalf("ungrouped effective_policy = %+v, want override + permissive defaults", ungrouped.EffectivePolicy)
	}
}

func ptrOf[T any](value T) *T { return &value }
