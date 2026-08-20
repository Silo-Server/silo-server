package handlers

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestUpdateUserRequestPolicyFieldsAreTriState(t *testing.T) {
	var req updateUserRequest
	body := `{
		"max_streams": 4,
		"max_transcodes": null,
		"download_allowed": false,
		"max_playback_quality": null,
		"library_ids": null,
		"requests_allowed": true
	}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := req.MaxStreams.Optional(); !got.Set || got.Value == nil || *got.Value != 4 {
		t.Fatalf("max_streams = %+v, want set 4", got)
	}
	if got := req.MaxTranscodes.Optional(); !got.Set || got.Value != nil {
		t.Fatalf("max_transcodes = %+v, want set to inherit (nil)", got)
	}
	if got := req.TranscodeAllowed.Optional(); got.Set {
		t.Fatalf("transcode_allowed absent should leave the column alone, got %+v", got)
	}
	if got := req.DownloadAllowed.Optional(); !got.Set || got.Value == nil || *got.Value {
		t.Fatalf("download_allowed = %+v, want set false", got)
	}
	if got := req.MaxPlaybackQuality.Optional(); !got.Set || got.Value != nil {
		t.Fatalf("max_playback_quality = %+v, want set to inherit", got)
	}
	if got := req.LibraryIDs.Optional(); !got.Set || got.Value != nil {
		t.Fatalf("library_ids null = %+v, want set to inherit", got)
	}
	if got := req.RequestsAllowed.Optional(); !got.Set || got.Value == nil || !*got.Value {
		t.Fatalf("requests_allowed = %+v, want set true", got)
	}

	var empty updateUserRequest
	if err := json.Unmarshal([]byte(`{"library_ids": []}`), &empty); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := empty.LibraryIDs.Optional(); !got.Set || got.Value == nil || len(*got.Value) != 0 {
		t.Fatalf("library_ids [] = %+v, want explicit empty override", got)
	}
}

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
		MaxProfiles:     5,
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
		RequestsAllowed:          true,
	}

	resp := toAdminUserResponse(user, group)

	// Stored overrides: only the set fields are non-null.
	if resp.MaxStreams == nil || *resp.MaxStreams != 6 || resp.DownloadAllowed == nil || !*resp.DownloadAllowed {
		t.Fatalf("overrides = streams %v download %v, want 6/true", resp.MaxStreams, resp.DownloadAllowed)
	}
	if resp.MaxTranscodes != nil || resp.MaxPlaybackQuality != nil || resp.TranscodeAllowed != nil ||
		resp.AudioTranscodeAllowed != nil || resp.DownloadTranscodeAllowed != nil || resp.RequestsAllowed != nil || resp.LibraryIDs != nil {
		t.Fatalf("inherited fields must serialize as null, got %+v", resp)
	}

	// Effective policy: overrides win, everything else comes from the group.
	want := effectivePolicyResp{
		LibraryIDs:               []int{1, 2},
		MaxPlaybackQuality:       access.PlaybackQualityStandard,
		MaxStreams:               6,
		MaxTranscodes:            1,
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
