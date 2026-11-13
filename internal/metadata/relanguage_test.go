package metadata

import (
	"context"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func seedRelanguageItem(t *testing.T, h *testHarness, lockedFields ...MetadataField) {
	t.Helper()
	locked := make([]int, 0, len(lockedFields))
	for _, field := range lockedFields {
		locked = append(locked, int(field))
	}
	if err := h.itemRepo.Upsert(context.Background(), &models.MediaItem{
		ContentID:               "existing-1",
		Type:                    "movie",
		Title:                   "Gammel Kinesisk Titel",
		Overview:                "Old-language overview",
		Year:                    2020,
		Status:                  "matched",
		DefaultMetadataLanguage: "zh",
		LockedFields:            locked,
		Studios:                 []string{},
		Networks:                []string{},
		Countries:               []string{},
		Genres:                  []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
}

func newRelanguageProvider() *capturingMetadataProvider {
	return &capturingMetadataProvider{
		response: &MetadataResult{
			HasMetadata: true,
			Title:       "Ny Dansk Titel",
			Overview:    "Dansk oversigt",
			ProviderIDs: map[string]string{"tmdb": "42"},
		},
	}
}

// TestProcess_AdoptLanguage covers the core of issue #211: a refresh carrying
// AdoptLanguage must treat req.Language as the item's new canonical metadata
// language — replacing the base-row title and overview and restamping
// default_metadata_language — instead of pinning to the language stamped at
// first match. Locked fields stay, and the stamp stays when both language
// fields are locked.
func TestProcess_AdoptLanguage(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		adopt                   bool
		locked                  []MetadataField
		wantTitle, wantOverview string // wantOverview "" is not checked
		wantLanguage            string
	}{
		{name: "rewrites the base row and restamps", adopt: true, wantTitle: "Ny Dansk Titel", wantOverview: "Dansk oversigt", wantLanguage: "da"},
		{name: "without AdoptLanguage keeps the canonical pin", wantTitle: "Gammel Kinesisk Titel", wantLanguage: "zh"},
		{
			name: "skipped when both language fields are locked", adopt: true, locked: []MetadataField{FieldName, FieldOverview},
			wantTitle: "Gammel Kinesisk Titel", wantOverview: "Old-language overview", wantLanguage: "zh",
		},
		{
			name: "only the title locked", adopt: true, locked: []MetadataField{FieldName},
			wantTitle: "Gammel Kinesisk Titel", wantOverview: "Dansk oversigt", wantLanguage: "da",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			ctx := context.Background()
			seedRelanguageItem(t, h, tc.locked...)

			result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
				ContentID:     "existing-1",
				Language:      "da",
				Mode:          ModeManualRefresh,
				AdoptLanguage: tc.adopt,
			}, []Provider{newRelanguageProvider()})
			if err != nil {
				t.Fatalf("ProcessWithProviders: %v", err)
			}
			if result == nil || !result.Updated {
				t.Fatalf("result = %#v, want Updated=true", result)
			}

			item, err := h.itemRepo.GetByID(ctx, "existing-1")
			if err != nil {
				t.Fatalf("get item: %v", err)
			}
			if item.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", item.Title, tc.wantTitle)
			}
			if tc.wantOverview != "" && item.Overview != tc.wantOverview {
				t.Errorf("overview = %q, want %q", item.Overview, tc.wantOverview)
			}
			if item.DefaultMetadataLanguage != tc.wantLanguage {
				t.Errorf("default_metadata_language = %q, want %q", item.DefaultMetadataLanguage, tc.wantLanguage)
			}
		})
	}
}

// TestAdoptableFolderLanguage covers the decision for when a folder-scoped
// refresh should adopt the folder's language as the item's new canonical
// language (issue #211). Only manual-refresh passes adopt: scheduled
// refreshes merge fill-empty, which would restamp the language without
// rewriting the text.
func TestAdoptableFolderLanguage(t *testing.T) {
	cases := []struct {
		name     string
		mode     RefreshMode
		stamp    string
		language string
		want     bool
	}{
		{"manual refresh with mismatch adopts", ModeManualRefresh, "zh", "da", true},
		{"manual refresh case-insensitive match does not adopt", ModeManualRefresh, "DA", "da", false},
		{"scheduled refresh never adopts", ModeScheduledRefresh, "zh", "da", false},
		{"identify never adopts", ModeIdentify, "zh", "da", false},
		{"initial match never adopts", ModeInitialMatch, "zh", "da", false},
		{"unstamped item does not adopt", ModeManualRefresh, "", "da", false},
		{"empty target language does not adopt", ModeManualRefresh, "zh", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adoptableFolderLanguage(tc.mode, tc.stamp, tc.language); got != tc.want {
				t.Fatalf("adoptableFolderLanguage(%v, %q, %q) = %v, want %v", tc.mode, tc.stamp, tc.language, got, tc.want)
			}
		})
	}
}

// TestItemLibrariesAgreeOnLanguage covers the multi-library adoption gate: an
// item that lives in several libraries (media_item_libraries) must only adopt
// a language every containing library agrees on — otherwise libraries with
// different languages would rewrite the canonical base row back and forth on
// every refresh.
func TestItemLibrariesAgreeOnLanguage(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		languages []string
		lookupErr error
		target    string
		want      bool
	}{
		{"single library matching target agrees", []string{"da"}, nil, "da", true},
		{"case-insensitive match agrees", []string{"DA"}, nil, "da", true},
		{"disagreeing libraries block adoption", []string{"da", "en"}, nil, "da", false},
		{"single library with other language blocks adoption", []string{"de"}, nil, "da", false},
		{"no membership rows cannot disagree", nil, nil, "da", true},
		{"lookup failure blocks adoption", nil, fmt.Errorf("boom"), "da", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			h.libraryRepo.setMetadataLanguages("existing-1", tc.languages, tc.lookupErr)
			if got := h.service.itemLibrariesAgreeOnLanguage(ctx, "existing-1", tc.target); got != tc.want {
				t.Fatalf("itemLibrariesAgreeOnLanguage(%v, %q) = %v, want %v", tc.languages, tc.target, got, tc.want)
			}
		})
	}
}
