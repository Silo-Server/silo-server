package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func seedRelanguageItem(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.itemRepo.Upsert(context.Background(), &models.MediaItem{
		ContentID:               "existing-1",
		Type:                    "movie",
		Title:                   "Gammel Kinesisk Titel",
		Overview:                "Old-language overview",
		Year:                    2020,
		Status:                  "matched",
		DefaultMetadataLanguage: "zh",
		Studios:                 []string{},
		Networks:                []string{},
		Countries:               []string{},
		Genres:                  []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
}

// TestProcess_AdoptLanguageRewritesBaseRowAndRestamps covers the core of
// issue #211: a refresh carrying AdoptLanguage must treat req.Language as the
// item's new canonical metadata language — replacing the base-row title and
// overview and restamping default_metadata_language — instead of pinning to
// the language stamped at first match.
func TestProcess_AdoptLanguageRewritesBaseRowAndRestamps(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	seedRelanguageItem(t, h)

	provider := &capturingMetadataProvider{
		response: &MetadataResult{
			HasMetadata: true,
			Title:       "Ny Dansk Titel",
			Overview:    "Dansk oversigt",
			ProviderIDs: map[string]string{"tmdb": "42"},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID:     "existing-1",
		Language:      "da",
		Mode:          ModeManualRefresh,
		AdoptLanguage: true,
	}, []Provider{provider})
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
	if item.Title != "Ny Dansk Titel" {
		t.Errorf("title = %q, want base row rewritten to %q", item.Title, "Ny Dansk Titel")
	}
	if item.Overview != "Dansk oversigt" {
		t.Errorf("overview = %q, want base row rewritten", item.Overview)
	}
	if item.DefaultMetadataLanguage != "da" {
		t.Errorf("default_metadata_language = %q, want restamped to da", item.DefaultMetadataLanguage)
	}
}

// TestProcess_WithoutAdoptLanguageKeepsCanonicalPin pins the pre-existing
// behavior: a refresh in a non-canonical language without AdoptLanguage must
// NOT rewrite the base row or restamp the item (the fetch routes to the
// localization tables instead).
func TestProcess_WithoutAdoptLanguageKeepsCanonicalPin(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	seedRelanguageItem(t, h)

	provider := &capturingMetadataProvider{
		response: &MetadataResult{
			HasMetadata: true,
			Title:       "Ny Dansk Titel",
			Overview:    "Dansk oversigt",
			ProviderIDs: map[string]string{"tmdb": "42"},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "existing-1",
		Language:  "da",
		Mode:      ModeManualRefresh,
	}, []Provider{provider})
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
	if item.Title != "Gammel Kinesisk Titel" {
		t.Errorf("title = %q, want base row untouched", item.Title)
	}
	if item.DefaultMetadataLanguage != "zh" {
		t.Errorf("default_metadata_language = %q, want zh (unchanged)", item.DefaultMetadataLanguage)
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
