package catalog

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AC4: forced AI writes fill empty fields and refresh earlier AI text, but
// never replace provider or manual text.
func TestAITranslationNeverOverwritesProviderOrManualText(t *testing.T) {
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
	if _, err := pool.Exec(t.Context(), `
		CREATE TEMP TABLE media_item_localizations (LIKE public.media_item_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		CREATE TEMP TABLE season_localizations (LIKE public.season_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		CREATE TEMP TABLE episode_localizations (LIKE public.episode_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		INSERT INTO media_item_localizations (content_id, language, title, sort_title, overview, tagline, poster_path, poster_thumbhash,
			backdrop_path, backdrop_thumbhash, logo_path, overview_source, tagline_source)
		VALUES ('provider', 'de', '', '', 'Anbieter.', 'Von Hand.', '', '', '', '', '', 'provider', 'manual'),
		       ('ai', 'de', '', '', 'Alt KI.', '', '', '', '', '', '', 'ai', 'provider');
		INSERT INTO season_localizations (season_content_id, language, title, overview, poster_path, poster_thumbhash, overview_source)
		VALUES ('season', 'de', '', 'Anbieter.', '', '', 'provider');
		INSERT INTO episode_localizations (episode_content_id, language, title, overview, overview_source)
		VALUES ('episode', 'de', '', 'Anbieter.', 'provider');
	`); err != nil {
		t.Fatal(err)
	}
	items := NewMediaItemLocalizationRepository(pool)
	text := func(v string) *string { return &v }
	for _, id := range []string{"provider", "ai"} {
		if err := items.UpsertAITranslation(t.Context(), id, "de", text("Neu KI."), text("Neu KI."), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewSeasonLocalizationRepository(pool).UpsertAIOverview(t.Context(), "season", "de", "Neu KI.", true); err != nil {
		t.Fatal(err)
	}
	if err := NewEpisodeLocalizationRepository(pool).UpsertAIOverview(t.Context(), "episode", "de", "Neu KI.", true); err != nil {
		t.Fatal(err)
	}

	provider, err := items.Get(t.Context(), "provider", "de")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Overview != "Anbieter." || provider.OverviewSource != "provider" || provider.Tagline != "Von Hand." || provider.TaglineSource != "manual" {
		t.Errorf("provider/manual item = %+v, want both kept", provider)
	}
	ai, err := items.Get(t.Context(), "ai", "de")
	if err != nil {
		t.Fatal(err)
	}
	if ai.Overview != "Neu KI." || ai.OverviewSource != "ai" || ai.Tagline != "Neu KI." || ai.TaglineSource != "ai" {
		t.Errorf("ai item = %+v, want the AI overview refreshed and the empty tagline filled", ai)
	}
	season, err := NewSeasonLocalizationRepository(pool).Get(t.Context(), "season", "de")
	if err != nil {
		t.Fatal(err)
	}
	if season.Overview != "Anbieter." || season.OverviewSource != "provider" {
		t.Errorf("season = %+v, want the provider overview kept", season)
	}
	episode, err := NewEpisodeLocalizationRepository(pool).Get(t.Context(), "episode", "de")
	if err != nil {
		t.Fatal(err)
	}
	if episode.Overview != "Anbieter." || episode.OverviewSource != "provider" {
		t.Errorf("episode = %+v, want the provider overview kept", episode)
	}
}

// A plain AI job fills only fields that are still empty when it writes, so it
// can't undo a forced re-translation that finished after it read the source
// (#2472).
func TestPlainAITranslationFillsOnlyEmptyFields(t *testing.T) {
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
	if _, err := pool.Exec(t.Context(), `
		CREATE TEMP TABLE media_item_localizations (LIKE public.media_item_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		CREATE TEMP TABLE season_localizations (LIKE public.season_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		CREATE TEMP TABLE episode_localizations (LIKE public.episode_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		INSERT INTO media_item_localizations (content_id, language, title, sort_title, overview, tagline, poster_path, poster_thumbhash,
			backdrop_path, backdrop_thumbhash, logo_path, overview_source, tagline_source)
		VALUES ('item', 'de', '', '', 'Neuer erzwungen.', '', '', '', '', '', '', 'ai', 'provider');
		INSERT INTO season_localizations (season_content_id, language, title, overview, poster_path, poster_thumbhash, overview_source)
		VALUES ('season', 'de', '', 'Neuer erzwungen.', '', '', 'ai');
		INSERT INTO episode_localizations (episode_content_id, language, title, overview, overview_source)
		VALUES ('episode', 'de', '', 'Neuer erzwungen.', 'ai');
	`); err != nil {
		t.Fatal(err)
	}
	items := NewMediaItemLocalizationRepository(pool)
	stale := "Alter Text."
	if err := items.UpsertAITranslation(t.Context(), "item", "de", &stale, &stale, false); err != nil {
		t.Fatal(err)
	}
	seasons, episodes := NewSeasonLocalizationRepository(pool), NewEpisodeLocalizationRepository(pool)
	if err := seasons.UpsertAIOverview(t.Context(), "season", "de", stale, false); err != nil {
		t.Fatal(err)
	}
	if err := episodes.UpsertAIOverview(t.Context(), "episode", "de", stale, false); err != nil {
		t.Fatal(err)
	}

	item, err := items.Get(t.Context(), "item", "de")
	if err != nil {
		t.Fatal(err)
	}
	if item.Overview != "Neuer erzwungen." || item.Tagline != stale || item.TaglineSource != "ai" {
		t.Errorf("item = %+v, want the forced overview kept and the empty tagline filled", item)
	}
	season, err := seasons.Get(t.Context(), "season", "de")
	if err != nil {
		t.Fatal(err)
	}
	if season.Overview != "Neuer erzwungen." {
		t.Errorf("season overview = %q, want the forced one kept", season.Overview)
	}
	episode, err := episodes.Get(t.Context(), "episode", "de")
	if err != nil {
		t.Fatal(err)
	}
	if episode.Overview != "Neuer erzwungen." {
		t.Errorf("episode overview = %q, want the forced one kept", episode.Overview)
	}
}
