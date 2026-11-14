package sections

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Episode cards carry their season poster and series poster and backdrop
// from the queries that already load them: the section episode query (Home,
// Continue Watching, Next Up, Watch Tonight) and the listing card context
// (browse and search). Section cards also carry the season backdrop. An
// episode without a season row has no season artwork.
// Section episode cards also carry the air date as their release date and
// both ratings; an unaired, unrated episode carries none.
func TestEpisodeCardsCarryParentArtworkPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := "parent-art-" + uuid.NewString()[:8] + "-"
	series, season, withSeason, withoutSeason := prefix+"series", prefix+"season", prefix+"episode-1", prefix+"episode-2"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash)
		VALUES ($1, 'series', 'Show', 'matched', 'series/poster', 'sp', 'series/backdrop', 'sb')`, series); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO seasons (content_id, series_id, season_number, poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash)
		VALUES ($1, $2, 1, 'season/poster', 'ss', 'season/backdrop', 'sbd')`, season, series); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	for i, episode := range []struct{ id, seasonID string }{{withSeason, season}, {withoutSeason, ""}} {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title, overview, runtime, still_path, air_date, rating_imdb, rating_tmdb)
			VALUES ($1, $2, NULLIF($3, ''), 1, $4, 'Episode', '', 0, 'episode/still',
				CASE WHEN $3 = '' THEN NULL ELSE DATE '2021-06-11' END, CASE WHEN $3 = '' THEN NULL ELSE 7.9 END, CASE WHEN $3 = '' THEN NULL ELSE 8.39 END)`,
			episode.id, series, episode.seasonID, i+1); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
	}

	full := models.EpisodeParentArtwork{
		SeriesPosterPath: "series/poster", SeriesPosterThumbhash: "sp",
		SeriesBackdropPath: "series/backdrop", SeriesBackdropThumbhash: "sb",
		SeasonPosterPath: "season/poster", SeasonPosterThumbhash: "ss",
		SeasonBackdropPath: "season/backdrop", SeasonBackdropThumbhash: "sbd",
	}
	seriesOnly := full
	seriesOnly.SeasonPosterPath, seriesOnly.SeasonPosterThumbhash = "", ""
	seriesOnly.SeasonBackdropPath, seriesOnly.SeasonBackdropThumbhash = "", ""
	want := map[string]models.EpisodeParentArtwork{withSeason: full, withoutSeason: seriesOnly}

	items, meta, err := NewFetcher(pool).fetchEpisodeTargetsByContentIDs(ctx, []string{withSeason, withoutSeason}, nil, nil, catalog.AccessFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("episode targets = %d, %v", len(items), err)
	}
	for _, item := range items {
		if item.ParentArtwork == nil || *item.ParentArtwork != want[item.ContentID] {
			t.Errorf("%s section parent artwork = %+v", item.ContentID, item.ParentArtwork)
		}
		if m := meta[item.ContentID].ParentArtwork; m == nil || *m != want[item.ContentID] {
			t.Errorf("%s section meta parent artwork = %+v", item.ContentID, m)
		}
		// The air date and TMDB rating ride on the metadata: the item stays
		// as frozen v1 responses serialize it.
		m := meta[item.ContentID]
		aired := item.ContentID == withSeason
		if aired != (m.EpisodeAirDate != nil && *m.EpisodeAirDate == "2021-06-11") || aired != (item.RatingIMDB != nil && *item.RatingIMDB == 7.9) || aired != (m.RatingTMDB != nil && *m.RatingTMDB == 8.39) {
			t.Errorf("%s air date and ratings = %v, %v, %v", item.ContentID, m.EpisodeAirDate, item.RatingIMDB, m.RatingTMDB)
		}
		if item.ReleaseDate != nil || item.RatingTMDB != nil {
			t.Errorf("%s item carries v2-only fields: %v, %v", item.ContentID, item.ReleaseDate, item.RatingTMDB)
		}
	}

	contexts, err := catalog.NewEpisodeRepository(pool).ListCardContexts(ctx, []string{withSeason, withoutSeason, prefix + "missing"})
	if err != nil || len(contexts) != 2 {
		t.Fatalf("card contexts = %v, %v", contexts, err)
	}
	for id, c := range contexts {
		// Listing cards draw no season backdrop, so their context leaves it out.
		wantListing := want[id]
		wantListing.SeasonBackdropPath, wantListing.SeasonBackdropThumbhash = "", ""
		if c.ParentArtwork != wantListing || c.SeriesID != series || c.SeriesTitle != "Show" || c.StillPath != "episode/still" {
			t.Errorf("%s card context = %+v", id, c)
		}
	}
}
