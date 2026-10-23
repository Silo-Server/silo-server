package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeItemLocalizationRepo records the rows a refresh upserts. It stores them
// as written; the provenance rules of the real upsert are covered in catalog.
type fakeItemLocalizationRepo struct {
	rows map[string]*models.MediaItemLocalization
}

func newFakeItemLocalizationRepo() *fakeItemLocalizationRepo {
	return &fakeItemLocalizationRepo{rows: map[string]*models.MediaItemLocalization{}}
}

func (r *fakeItemLocalizationRepo) Get(_ context.Context, contentID, language string) (*models.MediaItemLocalization, error) {
	row, ok := r.rows[contentID+"/"+language]
	if !ok {
		return nil, nil
	}
	cp := *row
	return &cp, nil
}

func (r *fakeItemLocalizationRepo) Upsert(_ context.Context, loc *models.MediaItemLocalization) error {
	cp := *loc
	r.rows[loc.ContentID+"/"+loc.Language] = &cp
	return nil
}

// sharedItemHarness stores an item matched in canonicalLanguage, as the first
// of two libraries in different languages leaves it.
func sharedItemHarness(t *testing.T, canonicalLanguage, title, overview, tagline string, locked ...MetadataField) (*testHarness, *fakeItemLocalizationRepo) {
	t.Helper()
	h := newTestHarness()
	locs := newFakeItemLocalizationRepo()
	h.service.itemLocalizationRepo = locs
	lockedFields := make([]int, len(locked))
	for i, field := range locked {
		lockedFields[i] = int(field)
	}
	if err := h.itemRepo.Upsert(context.Background(), &models.MediaItem{
		ContentID: "series-shared", Type: "series", Status: "matched",
		Title: title, Overview: overview, Tagline: tagline, Year: 2008,
		DefaultMetadataLanguage: canonicalLanguage, LockedFields: lockedFields,
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	return h, locs
}

func refreshShared(t *testing.T, h *testHarness, mode RefreshMode, language string, fetched *MetadataResult) {
	t.Helper()
	fetched.HasMetadata = true
	fetched.Year = 2008
	fetched.ProviderIDs = map[string]string{}
	if _, err := h.service.mergeAndPersist(context.Background(), ProcessRequest{ContentID: "series-shared", Mode: mode, Language: language},
		fetched, nil, nil, nil, "series"); err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
}

// The second library to see a shared title stores its own language's provider
// text in the localization row and leaves the canonical item alone, whichever
// language was scanned first.
func TestSecondLibraryStoresItsOwnLanguageText(t *testing.T) {
	cases := []struct {
		name                 string
		canonical, requested string
		base, fetched        MetadataResult
	}{
		{
			name: "english first", canonical: "en", requested: "fr",
			base:    MetadataResult{Title: "The Show", Overview: "An English overview.", Tagline: "English tagline"},
			fetched: MetadataResult{Title: "La Série", Overview: "Un résumé en français.", Tagline: "Accroche"},
		},
		{
			name: "french first", canonical: "fr", requested: "en",
			base:    MetadataResult{Title: "La Série", Overview: "Un résumé en français.", Tagline: "Accroche"},
			fetched: MetadataResult{Title: "The Show", Overview: "An English overview.", Tagline: "English tagline"},
		},
	}
	for _, tc := range cases {
		for _, mode := range []RefreshMode{ModeInitialMatch, ModeScheduledRefresh, ModeManualRefresh} {
			h, locs := sharedItemHarness(t, tc.canonical, tc.base.Title, tc.base.Overview, tc.base.Tagline)
			fetched := tc.fetched
			refreshShared(t, h, mode, tc.requested, &fetched)

			loc := locs.rows["series-shared/"+tc.requested]
			if loc == nil {
				t.Fatalf("%s mode %d: no %s localization written", tc.name, mode, tc.requested)
			}
			if loc.Title != tc.fetched.Title || loc.Overview != tc.fetched.Overview || loc.Tagline != tc.fetched.Tagline {
				t.Errorf("%s mode %d: localization = %q/%q/%q, want the %s provider text",
					tc.name, mode, loc.Title, loc.Overview, loc.Tagline, tc.requested)
			}
			item, err := h.itemRepo.GetByID(context.Background(), "series-shared")
			if err != nil {
				t.Fatal(err)
			}
			if item.Title != tc.base.Title || item.Overview != tc.base.Overview || item.DefaultMetadataLanguage != tc.canonical {
				t.Errorf("%s mode %d: item = %q/%q/%s, want the canonical item unchanged",
					tc.name, mode, item.Title, item.Overview, item.DefaultMetadataLanguage)
			}
		}
	}
}

// A localization row an earlier release filled with a copy of the canonical
// text is repaired by a refresh in either merge mode. With no translation from
// the provider, the copied overview goes out empty so the upsert clears it.
func TestRefreshRepairsLocalizationHoldingCanonicalCopy(t *testing.T) {
	for _, mode := range []RefreshMode{ModeScheduledRefresh, ModeManualRefresh} {
		for _, translated := range []bool{true, false} {
			h, locs := sharedItemHarness(t, "en", "The Show", "An English overview.", "English tagline")
			locs.rows["series-shared/fr"] = &models.MediaItemLocalization{
				ContentID: "series-shared", Language: "fr",
				Title: "The Show", SortTitle: "Show, The", Overview: "An English overview.", Tagline: "English tagline",
				OverviewSource: models.LocalizationSourceProvider, TaglineSource: models.LocalizationSourceProvider,
			}
			fetched := MetadataResult{Title: "La Série"}
			if translated {
				fetched.Overview, fetched.Tagline = "Un résumé en français.", "Accroche"
			}
			refreshShared(t, h, mode, "fr", &fetched)

			loc := locs.rows["series-shared/fr"]
			// The copied sort title goes too; "La Série" derives none of its own.
			if loc.Title != "La Série" || loc.SortTitle != "" || loc.Overview != fetched.Overview || loc.Tagline != fetched.Tagline {
				t.Errorf("mode %d translated=%v: localization = %q/%q/%q/%q, want %q/%q/%q/%q", mode, translated,
					loc.Title, loc.SortTitle, loc.Overview, loc.Tagline, "La Série", "", fetched.Overview, fetched.Tagline)
			}
		}
	}
}

// A provider, AI, or manual translation is kept when a refresh brings no text
// for it, and a field lock keeps the canonical value in the localization row.
func TestLocalizationRefreshKeepsTranslationsAndLocks(t *testing.T) {
	for _, mode := range []RefreshMode{ModeScheduledRefresh, ModeManualRefresh} {
		h, locs := sharedItemHarness(t, "en", "The Show", "An English overview.", "English tagline")
		locs.rows["series-shared/fr"] = &models.MediaItemLocalization{
			ContentID: "series-shared", Language: "fr", Title: "La Série",
			Overview: "Résumé fournisseur.", OverviewSource: models.LocalizationSourceProvider,
			Tagline: "Accroche IA", TaglineSource: models.LocalizationSourceAI,
		}
		refreshShared(t, h, mode, "fr", &MetadataResult{Title: "La Série"})
		loc := locs.rows["series-shared/fr"]
		if loc.Overview != "Résumé fournisseur." || loc.Tagline != "Accroche IA" {
			t.Errorf("mode %d: localization = %q/%q, want the stored translations kept", mode, loc.Overview, loc.Tagline)
		}

		// Manual text equal to the canonical text is an admin's choice, not a
		// copy, so it is never cleared.
		manual := buildItemLocalizationRecord(&models.MediaItemLocalization{
			Overview: "An English overview.", OverviewSource: models.LocalizationSourceManual,
		}, "series-shared", "fr", "series", &MetadataResult{}, &MetadataResult{Overview: "An English overview."},
			nil, modeMergeMode(mode), "fr", false, true)
		if manual.Overview != "An English overview." {
			t.Errorf("mode %d: manual overview = %q, want it kept", mode, manual.Overview)
		}

		h, locs = sharedItemHarness(t, "en", "The Show", "An English overview.", "English tagline", FieldName, FieldOverview)
		refreshShared(t, h, mode, "fr", &MetadataResult{Title: "La Série", Overview: "Un résumé en français."})
		loc = locs.rows["series-shared/fr"]
		if loc.Title != "The Show" || loc.Overview != "An English overview." {
			t.Errorf("mode %d: locked localization = %q/%q, want the locked canonical values", mode, loc.Title, loc.Overview)
		}
	}
}

func modeMergeMode(mode RefreshMode) MergeMode {
	if mode == ModeManualRefresh {
		return MergeReplaceUnlocked
	}
	return MergeFillEmpty
}

// A fallback title is the original title, not a translation: the copied
// canonical title is cleared and the row falls back to the base title rather
// than showing the original-script one.
func TestLocalizationIgnoresFallbackTitle(t *testing.T) {
	for _, mode := range []RefreshMode{ModeScheduledRefresh, ModeManualRefresh} {
		h, locs := sharedItemHarness(t, "en", "Spirited Away", "An English overview.", "")
		locs.rows["series-shared/fr"] = &models.MediaItemLocalization{
			ContentID: "series-shared", Language: "fr", Title: "Spirited Away",
			Overview: "An English overview.", OverviewSource: models.LocalizationSourceProvider,
		}
		refreshShared(t, h, mode, "fr", &MetadataResult{
			Title: "千と千尋の神隠し", TitleIsFallback: true, Overview: "Un résumé en français.",
		})
		loc := locs.rows["series-shared/fr"]
		if loc.Title != "" || loc.Overview != "Un résumé en français." {
			t.Errorf("mode %d: localization = %q/%q, want no title and the French overview", mode, loc.Title, loc.Overview)
		}

		// A real stored translation stays when the refresh only has a fallback.
		locs.rows["series-shared/fr"].Title = "Le Voyage de Chihiro"
		refreshShared(t, h, mode, "fr", &MetadataResult{Title: "千と千尋の神隠し", TitleIsFallback: true})
		if got := locs.rows["series-shared/fr"].Title; got != "Le Voyage de Chihiro" {
			t.Errorf("mode %d: title = %q, want the stored translation kept", mode, got)
		}
	}
}

// A fill-empty pass that could not read the NFO keeps the stored values the
// NFO set, even where they match the canonical ones: remote text must not
// replace them while the sidecar is unreadable.
func TestLocalizationKeepsNFOTextWhenSidecarUnreadable(t *testing.T) {
	h, locs := sharedItemHarness(t, "en", "Curated Title", "Curated overview.", "")
	locs.rows["series-shared/fr"] = &models.MediaItemLocalization{
		ContentID: "series-shared", Language: "fr", Title: "Curated Title",
		Overview: "Curated overview.", OverviewSource: models.LocalizationSourceProvider,
	}
	ctx := withLocalMetadataUnreadable(context.Background(), localArtworkItem)
	fetched := &MetadataResult{Title: "La Série", Overview: "Un résumé en français.", HasMetadata: true, Year: 2008, ProviderIDs: map[string]string{}}
	if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: "series-shared", Mode: ModeScheduledRefresh, Language: "fr"},
		fetched, nil, nil, nil, "series"); err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	if loc := locs.rows["series-shared/fr"]; loc.Title != "Curated Title" || loc.Overview != "Curated overview." {
		t.Errorf("localization = %q/%q, want the stored NFO values kept", loc.Title, loc.Overview)
	}
}
