package translation

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seasonFixture holds one English series in session-local tables. Season 1
// lists ep-1 (English) and ep-2 (French source text); ep-3 is a provider-only
// episode with no library membership, which the season page does not list.
func seasonFixture(t *testing.T) *PgRepository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1 // temp tables are per session
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `
		CREATE TEMP TABLE media_items (LIKE public.media_items INCLUDING DEFAULTS);
		CREATE TEMP TABLE media_item_localizations (LIKE public.media_item_localizations INCLUDING DEFAULTS);
		CREATE TEMP TABLE seasons (LIKE public.seasons INCLUDING DEFAULTS);
		CREATE TEMP TABLE season_localizations (LIKE public.season_localizations INCLUDING DEFAULTS);
		CREATE TEMP TABLE episodes (LIKE public.episodes INCLUDING DEFAULTS);
		CREATE TEMP TABLE episode_localizations (LIKE public.episode_localizations INCLUDING DEFAULTS);
		CREATE TEMP TABLE episode_libraries (LIKE public.episode_libraries INCLUDING DEFAULTS);
		INSERT INTO media_items (content_id, type, title, default_metadata_language)
		VALUES ('show', 'series', 'Show', 'en');
		INSERT INTO seasons (content_id, series_id, season_number, overview, default_metadata_language)
		VALUES ('season-1', 'show', 1, '', 'en');
		INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, overview, default_metadata_language)
		VALUES ('ep-1', 'show', 'season-1', 1, 1, 'English text.', 'en'),
		       ('ep-2', 'show', 'season-1', 1, 2, 'Texte français.', 'FR'),
		       ('ep-3', 'show', 'season-1', 1, 3, 'Not collected.', 'en');
		INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ('ep-1', 1), ('ep-2', 1);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return NewPgRepository(pool)
}

func TestSeasonEpisodeTextsListsOnlyEpisodesInALibrary(t *testing.T) {
	repo := seasonFixture(t)
	texts, err := repo.SeasonEpisodeTexts(t.Context(), "season-1")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, text := range texts {
		ids = append(ids, text.ContentID)
	}
	if len(ids) != 2 || ids[0] != "ep-1" || ids[1] != "ep-2" {
		t.Fatalf("season episode texts = %v, want [ep-1 ep-2] without the uncollected ep-3", ids)
	}
}

func TestCountMissingFieldsSkipsChildrenAlreadyInTheLanguage(t *testing.T) {
	repo := seasonFixture(t)
	// ep-1 and ep-3 need French; ep-2 is already written in French, and a
	// job would skip it, so counting it would keep the auto-translate gate
	// open forever.
	missing, err := repo.CountMissingFields(t.Context(), "show", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if missing != 2 {
		t.Fatalf("missing = %d, want 2 (ep-1 and ep-3)", missing)
	}
}
