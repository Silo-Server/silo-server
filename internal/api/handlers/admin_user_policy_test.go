package handlers

import (
	"encoding/json"
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

	// JSON shape: null for inherited overrides, concrete effective block.
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["max_transcodes"] != nil || decoded["library_ids"] != nil {
		t.Fatalf("inherited fields should be JSON null, got %v / %v", decoded["max_transcodes"], decoded["library_ids"])
	}
	// The frozen v1 body has no inherit state for the profile limit: it
	// carries the limit that applies.
	if decoded["max_profiles"] != float64(3) {
		t.Fatalf("max_profiles JSON = %v, want the inherited 3", decoded["max_profiles"])
	}
	effective, ok := decoded["effective_policy"].(map[string]any)
	if !ok || effective["max_streams"] != float64(6) || effective["download_allowed"] != true {
		t.Fatalf("effective_policy JSON = %v", decoded["effective_policy"])
	}

	// An explicit empty library override must round-trip as [], never null:
	// null is the wire encoding for inherit, and collapsing it would let an
	// open+save silently delete a deny-all override.
	locked := toAdminUserResponse(&models.User{ID: 2, LibraryIDs: []int{}}, group)
	if locked.LibraryIDs == nil || len(locked.LibraryIDs) != 0 {
		t.Fatalf("empty library override = %#v, want non-nil empty", locked.LibraryIDs)
	}
	lockedJSON, err := json.Marshal(locked)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var lockedDecoded map[string]any
	if err := json.Unmarshal(lockedJSON, &lockedDecoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if list, ok := lockedDecoded["library_ids"].([]any); !ok || len(list) != 0 {
		t.Fatalf("empty library override JSON = %v, want []", lockedDecoded["library_ids"])
	}

	// Ungrouped: the permissive no-group default fills the gaps.
	ungrouped := toAdminUserResponse(&models.User{ID: 1, MaxStreams: ptrOf(2)}, nil)
	if ungrouped.EffectivePolicy.MaxStreams != 2 || !ungrouped.EffectivePolicy.DownloadAllowed || ungrouped.EffectivePolicy.LibraryIDs != nil {
		t.Fatalf("ungrouped effective_policy = %+v, want override + permissive defaults", ungrouped.EffectivePolicy)
	}
}

func ptrOf[T any](value T) *T { return &value }
