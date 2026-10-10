package catalog

import (
	"os"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Section cards are shared across profiles, so localization must return
// per-viewer clones: translated text where a row exists, the source text and
// a pending language where it does not, and episode cards through episode rows.
func TestLocalizeSectionItems(t *testing.T) {
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
	_, err = pool.Exec(t.Context(), `
		CREATE TEMP TABLE media_item_localizations (LIKE public.media_item_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		CREATE TEMP TABLE episodes (LIKE public.episodes INCLUDING DEFAULTS);
		CREATE TEMP TABLE episode_localizations (LIKE public.episode_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview)
		VALUES ('ep-1', 'show', 1, 1, 'Pilot', 'Source episode.'),
		       ('ep-2', 'show', 1, 2, 'Second', 'Untranslated episode.');
		INSERT INTO episode_localizations (episode_content_id, language, title, overview, overview_source)
		VALUES ('ep-1', 'de', 'Pilotfolge', 'KI-Folge.', 'ai');
	`)
	if err != nil {
		t.Fatal(err)
	}
	itemLocs := NewMediaItemLocalizationRepository(pool)
	if err := itemLocs.Upsert(t.Context(), &models.MediaItemLocalization{ContentID: "translated", Language: "de", Title: "Übersetzt", Overview: "Anbieter.", PosterPath: "posters/de.jpg", PosterThumbhash: "de-hash"}); err != nil {
		t.Fatal(err)
	}
	svc := &DetailService{
		itemLocRepo:    itemLocs,
		episodeRepo:    NewEpisodeRepository(pool),
		episodeLocRepo: NewEpisodeLocalizationRepository(pool),
	}
	movie := func(id string) *models.MediaItem {
		return &models.MediaItem{ContentID: id, Type: "movie", Title: "Source " + id, Overview: "Source overview.", DefaultMetadataLanguage: "en", PosterPath: "posters/en.jpg", PosterThumbhash: "en-hash"}
	}
	items := []*models.MediaItem{
		movie("translated"),
		movie("missing"),
		{ContentID: "ep-1", Type: "episode", Title: "Pilot", Overview: "Source episode."},
		{ContentID: "ep-2", Type: "episode", Title: "Second", Overview: "Untranslated episode."},
	}

	localized, pending, err := svc.LocalizeSectionItems(t.Context(), items, AccessFilter{ProfilePreferredLanguage: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if got := localized[0]; got.Title != "Übersetzt" || got.Overview != "Anbieter." || len(got.MachineTranslatedFields) != 0 {
		t.Errorf("translated movie = %q / %q / %v", got.Title, got.Overview, got.MachineTranslatedFields)
	}
	if got := localized[1]; got.Overview != "Source overview." {
		t.Errorf("untranslated movie overview = %q, want the source text", got.Overview)
	}
	if got := localized[2]; got.Title != "Pilotfolge" || got.Overview != "KI-Folge." || !slices.Equal(got.MachineTranslatedFields, []string{MachineTranslatedOverview}) {
		t.Errorf("episode card = %q / %q / %v", got.Title, got.Overview, got.MachineTranslatedFields)
	}
	// Image URLs are resolved from the source items, so the card keeps the
	// source artwork and its thumbhash.
	if got := localized[0]; got.PosterPath != "posters/en.jpg" || got.PosterThumbhash != "en-hash" {
		t.Errorf("translated movie artwork = %q / %q, want the source poster", got.PosterPath, got.PosterThumbhash)
	}
	if len(pending) != 2 || pending["missing"] != "de" || pending["ep-2"] != "de" {
		t.Errorf("pending = %v, want missing:de and ep-2:de", pending)
	}
	if items[0].Title != "Source translated" || items[2].Title != "Pilot" {
		t.Errorf("shared section items were mutated: %+v %+v", items[0], items[2])
	}

	// The source-language profile sees the cached items unchanged.
	localized, pending, err = svc.LocalizeSectionItems(t.Context(), items, AccessFilter{ProfilePreferredLanguage: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if localized[0].Title != "Source translated" || localized[2].Title != "Pilot" || len(pending) != 0 {
		t.Errorf("english view = %q, %q, pending %v", localized[0].Title, localized[2].Title, pending)
	}
}
