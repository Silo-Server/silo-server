package abs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBookmarkEnvelope_HasRequiredKeys asserts the wire shape ABS Android
// builds against: id, libraryItemId, time, title, createdAt, updatedAt,
// all camelCase and all present (no omitempty), including when title is
// empty — Android shows an "Untitled" placeholder client-side rather
// than treating missing-title differently from empty-title.
func TestBookmarkEnvelope_HasRequiredKeys(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	out := bookmarkToABS(Bookmark{
		ID:            "01HXX",
		LibraryItemID: "126887",
		Time:          1234.5,
		Title:         "",
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(body)
	for _, key := range []string{
		`"id":`, `"libraryItemId":`, `"time":`, `"title":`,
		`"createdAt":`, `"updatedAt":`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("envelope missing %s; got %s", key, js)
		}
	}
	if out["title"] != "" {
		t.Errorf("title = %v, want empty string", out["title"])
	}
	wantMs := now.UnixMilli()
	if out["createdAt"] != wantMs {
		t.Errorf("createdAt = %v, want %d (UnixMilli)", out["createdAt"], wantMs)
	}
}
