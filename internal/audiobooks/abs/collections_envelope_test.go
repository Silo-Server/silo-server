package abs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCollectionEnvelope_HasRequiredKeys asserts the seven top-level
// keys ABS Android pattern-matches on are present even when description
// is empty and books[] is empty. Fixes the continuum-reference bug where
// description always emitted as "" regardless of stored value.
func TestCollectionEnvelope_HasRequiredKeys(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	out := collectionToABS(Collection{
		ID:          "01HCOLL",
		UserID:      "1",
		Name:        "Favorites",
		Description: "",
		IsPublic:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, []map[string]any{})
	body, _ := json.Marshal(out)
	js := string(body)
	for _, key := range []string{
		`"id":`, `"userId":`, `"name":`, `"description":`,
		`"isPublic":`, `"lastUpdate":`, `"createdAt":`, `"books":`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("envelope missing %s; got %s", key, js)
		}
	}
	if out["description"] != "" {
		t.Errorf("description = %v, want empty string", out["description"])
	}
	wantMs := now.UnixMilli()
	if out["createdAt"] != wantMs {
		t.Errorf("createdAt = %v, want %d", out["createdAt"], wantMs)
	}
}

// TestCollectionListShape_OmitsBooks asserts the list shape (passed
// nil books) emits no "books" key — clients distinguish list vs detail
// by presence/absence of this field.
func TestCollectionListShape_OmitsBooks(t *testing.T) {
	out := collectionToABS(Collection{
		ID: "01HCOLL", UserID: "1", Name: "x",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, nil)
	if _, ok := out["books"]; ok {
		t.Errorf("list-shape must not include books key; got %v", out)
	}
}
