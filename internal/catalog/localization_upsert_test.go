package catalog

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// localizationTestPool opens one connection, so the temp tables a test creates
// are the ones its repository sees.
func localizationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// A provider refresh with no translated text keeps stored translations, but
// clears a provider value that is only a copy of the base item's text, so the
// field falls back to the base and AI translation can fill it.
func TestProviderUpsertClearsCopiedBaseTextOnly(t *testing.T) {
	pool := localizationTestPool(t)
	if _, err := pool.Exec(t.Context(), `
		CREATE TEMP TABLE media_items (content_id text PRIMARY KEY, overview text, tagline text);
		CREATE TEMP TABLE media_item_localizations (LIKE public.media_item_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		INSERT INTO media_items (content_id, overview, tagline)
		VALUES ('copy', 'English overview.', 'English tagline'),
		       ('kept', 'English overview.', 'English tagline'),
		       ('manual', 'English overview.', 'English tagline');
		INSERT INTO media_item_localizations (content_id, language, title, sort_title, overview, tagline, poster_path, poster_thumbhash,
			backdrop_path, backdrop_thumbhash, logo_path, overview_source, tagline_source)
		VALUES ('copy', 'fr', 'The Show', '', 'English overview.', 'English tagline', '', '', '', '', '', 'provider', 'provider'),
		       ('kept', 'fr', 'La Série', '', 'Résumé fournisseur.', 'Accroche IA', '', '', '', '', '', 'provider', 'ai'),
		       ('manual', 'fr', 'La Série', '', 'English overview.', 'English tagline', '', '', '', '', '', 'manual', 'manual');
	`); err != nil {
		t.Fatal(err)
	}
	repo := NewMediaItemLocalizationRepository(pool)
	for _, id := range []string{"copy", "kept", "manual"} {
		if err := repo.Upsert(t.Context(), &models.MediaItemLocalization{ContentID: id, Language: "fr", Title: "La Série"}); err != nil {
			t.Fatal(err)
		}
	}

	want := map[string][4]string{
		"copy":   {"", "provider", "", "provider"},
		"kept":   {"Résumé fournisseur.", "provider", "Accroche IA", "ai"},
		"manual": {"English overview.", "manual", "English tagline", "manual"},
	}
	for id, w := range want {
		got, err := repo.Get(t.Context(), id, "fr")
		if err != nil {
			t.Fatal(err)
		}
		if g := [4]string{got.Overview, got.OverviewSource, got.Tagline, got.TaglineSource}; g != w {
			t.Errorf("%s: overview/source/tagline/source = %q, want %q", id, g, w)
		}
	}

	// Provider text replaces the copy as usual.
	if err := repo.Upsert(t.Context(), &models.MediaItemLocalization{
		ContentID: "copy", Language: "fr", Title: "La Série", Overview: "Un résumé.", Tagline: "Accroche",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(t.Context(), "copy", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if got.Overview != "Un résumé." || got.Tagline != "Accroche" || got.OverviewSource != "provider" {
		t.Errorf("copy after provider text = %+v", got)
	}
}

// A correction empties provider and AI text in every language of the corrected
// item only. A manual translation and a locked field stay, and a cleared AI
// field goes back to provider provenance, open to the next provider text.
func TestClearMatchTextDB(t *testing.T) {
	pool := localizationTestPool(t)
	if _, err := pool.Exec(t.Context(), `
		CREATE TEMP TABLE media_item_localizations (LIKE public.media_item_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		INSERT INTO media_item_localizations (content_id, language, title, sort_title, overview, tagline, poster_path, poster_thumbhash,
			backdrop_path, backdrop_thumbhash, logo_path, overview_source, tagline_source)
		VALUES ('fixed', 'fr', 'Mauvais', 'mauvais', 'Résumé', 'Accroche', 'poster.jpg', '', '', '', '', 'provider', 'ai'),
		       ('fixed', 'de', 'Falsch', 'falsch', 'Manuell', 'Maschine', 'poster.jpg', '', '', '', '', 'manual', 'ai'),
		       ('locked', 'fr', 'Mauvais', 'mauvais', 'Résumé', 'Accroche', 'poster.jpg', '', '', '', '', 'provider', 'ai'),
		       ('titlelock', 'fr', 'Mauvais', 'mauvais', 'Résumé', 'Accroche', 'poster.jpg', '', '', '', '', 'provider', 'ai'),
		       ('textlock', 'fr', 'Mauvais', 'mauvais', 'Résumé', 'Accroche', 'poster.jpg', '', '', '', '', 'provider', 'ai'),
		       ('other', 'fr', 'Autre', 'autre', 'Résumé', 'Accroche', 'poster.jpg', '', '', '', '', 'provider', 'provider');
	`); err != nil {
		t.Fatal(err)
	}
	repo := NewMediaItemLocalizationRepository(pool)
	if err := repo.ClearMatchText(t.Context(), "fixed", false, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClearMatchText(t.Context(), "locked", true, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClearMatchText(t.Context(), "titlelock", true, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClearMatchText(t.Context(), "textlock", false, true); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id, language string
		want         [6]string
	}{
		{"fixed", "fr", [6]string{"", "", "", "provider", "", "provider"}},
		{"fixed", "de", [6]string{"", "", "Manuell", "manual", "", "provider"}},
		{"locked", "fr", [6]string{"Mauvais", "mauvais", "Résumé", "provider", "Accroche", "ai"}},
		{"titlelock", "fr", [6]string{"Mauvais", "mauvais", "", "provider", "", "provider"}},
		{"textlock", "fr", [6]string{"", "", "Résumé", "provider", "Accroche", "ai"}},
		{"other", "fr", [6]string{"Autre", "autre", "Résumé", "provider", "Accroche", "provider"}},
	} {
		got, err := repo.Get(t.Context(), tc.id, tc.language)
		if err != nil {
			t.Fatal(err)
		}
		if g := [6]string{got.Title, got.SortTitle, got.Overview, got.OverviewSource, got.Tagline, got.TaglineSource}; g != tc.want {
			t.Errorf("%s/%s: title/sort/overview/source/tagline/source = %q, want %q", tc.id, tc.language, g, tc.want)
		}
		if got.PosterPath != "poster.jpg" {
			t.Errorf("%s/%s: poster = %q, want it untouched", tc.id, tc.language, got.PosterPath)
		}
	}
}
