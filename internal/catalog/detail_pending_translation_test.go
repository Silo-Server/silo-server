package catalog

import (
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pendingTranslationFixture holds French localizations in session-local
// tables: the season overview and ep-1 are translated, ep-2 is not.
func pendingTranslationFixture(t *testing.T) *DetailService {
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
	_, err = pool.Exec(t.Context(), `
		CREATE TEMP TABLE season_localizations (LIKE public.season_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		CREATE TEMP TABLE episode_localizations (LIKE public.episode_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		INSERT INTO season_localizations (season_content_id, language, title, overview, poster_path, poster_thumbhash)
		VALUES ('season-1', 'fr', '', 'Saison.', '', '');
		INSERT INTO episode_localizations (episode_content_id, language, title, overview)
		VALUES ('ep-1', 'fr', '', 'Premier.');
	`)
	if err != nil {
		t.Fatal(err)
	}
	return &DetailService{
		seasonLocRepo:  NewSeasonLocalizationRepository(pool),
		episodeLocRepo: NewEpisodeLocalizationRepository(pool),
	}
}

func TestPendingSeasonTranslationIncludesItsEpisodes(t *testing.T) {
	svc := pendingTranslationFixture(t)
	season := &models.Season{ContentID: "season-1", SeriesID: "show", SeasonNumber: 1, Overview: "Season.", DefaultMetadataLanguage: "en"}
	french := AccessFilter{ProfilePreferredLanguage: "fr"}
	episode := func(id string, number int) *models.Episode {
		return &models.Episode{ContentID: id, SeriesID: "show", SeasonID: "season-1", SeasonNumber: 1, EpisodeNumber: number,
			Overview: "Source text.", DefaultMetadataLanguage: "en"}
	}
	episodes := []*models.Episode{episode("ep-1", 1), episode("ep-2", 2)}

	// The season overview is translated, but ep-2 is not.
	if got := svc.pendingSeasonTranslationLanguage(t.Context(), season, french, episodes); got != "fr" {
		t.Fatalf("pending = %q, want fr while an episode lacks a French overview", got)
	}
	if got := svc.pendingSeasonTranslationLanguage(t.Context(), season, french, episodes[:1]); got != "" {
		t.Fatalf("pending = %q with every episode translated, want none", got)
	}
	// An English profile reads the source text; nothing is pending.
	if got := svc.pendingSeasonTranslationLanguage(t.Context(), season, AccessFilter{ProfilePreferredLanguage: "en"}, episodes); got != "" {
		t.Fatalf("pending = %q for the source language, want none", got)
	}

	localized, err := svc.LocalizeEpisodeModels(t.Context(), episodes, french)
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]string{}
	for _, episode := range localized {
		pending[episode.ContentID] = episode.PendingTranslationLanguage
	}
	if pending["ep-1"] != "" || pending["ep-2"] != "fr" {
		t.Fatalf("episode pending = %v, want only ep-2 pending fr", pending)
	}
	for _, episode := range episodes {
		if episode.PendingTranslationLanguage != "" {
			t.Fatalf("LocalizeEpisodeModels mutated its input: %+v", episode)
		}
	}
}
