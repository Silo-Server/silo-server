package abs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestPlaylistEnvelope_HasRequiredKeys asserts the eight (or nine with
// coverPath) top-level keys are present when populated. coverPath is
// emitted only when non-empty.
func TestPlaylistEnvelope_HasRequiredKeys(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	out := playlistToABS(Playlist{
		ID:          "01HPL",
		UserID:      "1",
		Name:        "queue",
		Description: "",
		CoverItem:   "01HCOVER",
		IsPublic:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, []map[string]any{})
	body, _ := json.Marshal(out)
	js := string(body)
	for _, key := range []string{
		`"id":`, `"libraryId":`, `"userId":`, `"name":`, `"description":`,
		`"isPublic":`, `"coverPath":`, `"createdAt":`, `"lastUpdate":`, `"items":`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("envelope missing %s; got %s", key, js)
		}
	}
	if out["coverPath"] != "01HCOVER" {
		t.Errorf("coverPath = %v, want 01HCOVER", out["coverPath"])
	}
}

// TestPlaylistEnvelope_OmitsCoverPathWhenEmpty asserts cover_item=""
// produces no coverPath key (matches continuum).
func TestPlaylistEnvelope_OmitsCoverPathWhenEmpty(t *testing.T) {
	out := playlistToABS(Playlist{
		ID: "01HPL", UserID: "1", Name: "x",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, []map[string]any{})
	if _, has := out["coverPath"]; has {
		t.Errorf("coverPath emitted when empty: %v", out)
	}
}

// TestPlaylistListShape_OmitsItems asserts nil items produces no items key.
func TestPlaylistListShape_OmitsItems(t *testing.T) {
	out := playlistToABS(Playlist{
		ID: "01HPL", UserID: "1", Name: "x",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, nil)
	if _, has := out["items"]; has {
		t.Errorf("list-shape includes items key (should be detail-only): %v", out)
	}
}
