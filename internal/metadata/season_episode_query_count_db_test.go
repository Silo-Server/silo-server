package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBulkUpsertWithFallback(t *testing.T) {
	t.Run("bulk success", func(t *testing.T) {
		items := []int{1, 2, 3}
		bulkCalls := 0
		singleCalls := 0
		got := bulkUpsertWithFallback(items,
			func(gotItems []int) error {
				bulkCalls++
				if fmt.Sprint(gotItems) != fmt.Sprint(items) {
					t.Fatalf("bulk items = %v, want %v", gotItems, items)
				}
				return nil
			},
			func(error) { t.Fatal("unexpected bulk error callback") },
			func(int) error {
				singleCalls++
				return nil
			},
			func(int, error) { t.Fatal("unexpected row error callback") },
		)
		if fmt.Sprint(got) != fmt.Sprint(items) || bulkCalls != 1 || singleCalls != 0 {
			t.Fatalf("got items %v, bulk calls %d, single calls %d", got, bulkCalls, singleCalls)
		}
	})

	t.Run("fallback partial success preserves order", func(t *testing.T) {
		bulkErr := errors.New("bulk failed")
		rowErr := errors.New("row failed")
		var gotBulkErr error
		var failedRows []int
		got := bulkUpsertWithFallback([]int{3, 1, 2},
			func([]int) error { return bulkErr },
			func(err error) { gotBulkErr = err },
			func(item int) error {
				if item == 1 {
					return rowErr
				}
				return nil
			},
			func(item int, err error) {
				if !errors.Is(err, rowErr) {
					t.Fatalf("row error = %v, want %v", err, rowErr)
				}
				failedRows = append(failedRows, item)
			},
		)
		if !errors.Is(gotBulkErr, bulkErr) || fmt.Sprint(got) != "[3 2]" || fmt.Sprint(failedRows) != "[1]" {
			t.Fatalf("bulk error %v, successes %v, failed rows %v", gotBulkErr, got, failedRows)
		}
	})
}

type seasonEpisodeQueryTracer struct {
	mu      sync.Mutex
	queries []string
}

func (t *seasonEpisodeQueryTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queries = append(t.queries, data.SQL)
	return ctx
}

func (*seasonEpisodeQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func (t *seasonEpisodeQueryTracer) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queries = nil
}

func (t *seasonEpisodeQueryTracer) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.queries)
}

// TestPersistSeasonsAndEpisodes_QueryCountIsBounded pins intentional statement
// budgets: persisting 1 episode and 100 episodes both cost 9 statements for a
// canonical write or 13 for a localized write, preventing accidental per-row
// statements and N+1 regressions. A deliberate, reviewed statement-count change
// must update both constants together with this comment.
func TestPersistSeasonsAndEpisodes_QueryCountIsBounded(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &seasonEpisodeQueryTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.episodes')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check episodes table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the base schema")
	}

	for _, testCase := range []struct {
		name           string
		language       string
		wantStatements int
	}{
		{name: "canonical", language: "en", wantStatements: 9},
		{name: "localized", language: "fr", wantStatements: 13},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			queryCounts := make([]int, 0, 2)
			for _, episodeCount := range []int{1, 100} {
				seriesID := fmt.Sprintf("query-count-%s-series-%d-%d", testCase.name, episodeCount, time.Now().UnixNano())
				if _, err := pool.Exec(ctx,
					`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Query Count Series')`,
					seriesID,
				); err != nil {
					t.Fatalf("seed series: %v", err)
				}
				t.Cleanup(func() {
					_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
				})

				itemRepo := newFakeItemRepo()
				itemRepo.items[seriesID] = &models.MediaItem{
					ContentID: seriesID,
					Type:      "series",
					Title:     "Query Count Series",
				}
				seasonRepo := catalog.NewSeasonRepository(pool)
				episodeRepo := catalog.NewEpisodeRepository(pool)
				seasonLocalizationRepo := catalog.NewSeasonLocalizationRepository(pool)
				episodeLocalizationRepo := catalog.NewEpisodeLocalizationRepository(pool)
				existingSeason := &models.Season{
					ContentID:      fmt.Sprintf("query-count-%s-season-%d", testCase.name, episodeCount),
					SeriesID:       seriesID,
					SeasonNumber:   1,
					Title:          "Existing Season",
					MetadataSource: "provider",
				}
				if err := seasonRepo.Upsert(ctx, existingSeason); err != nil {
					t.Fatalf("seed unlinked episode season: %v", err)
				}
				existingEpisode := &models.Episode{
					ContentID:      fmt.Sprintf("query-count-%s-episode-%d", testCase.name, episodeCount),
					SeriesID:       seriesID,
					SeasonID:       existingSeason.ContentID,
					SeasonNumber:   1,
					EpisodeNumber:  1,
					Title:          "Existing Unlinked Episode",
					MetadataSource: "provider",
				}
				if err := episodeRepo.Upsert(ctx, existingEpisode); err != nil {
					t.Fatalf("seed unlinked episode: %v", err)
				}

				service := &MetadataService{
					itemRepo:                itemRepo,
					seasonRepo:              seasonRepo,
					episodeRepo:             episodeRepo,
					seasonLocalizationRepo:  seasonLocalizationRepo,
					episodeLocalizationRepo: episodeLocalizationRepo,
				}
				service.hooks.ensureSeriesEpisodeLinks = func(context.Context, string) error { return nil }

				seasons := make([]SeasonResult, 5)
				for i := range seasons {
					seasons[i] = SeasonResult{SeasonNumber: i + 1, Title: fmt.Sprintf("Season %d", i+1)}
				}
				episodes := make([]EpisodeResult, episodeCount)
				for i := range episodes {
					episodes[i] = EpisodeResult{
						SeasonNumber:  1,
						EpisodeNumber: i + 1,
						Title:         fmt.Sprintf("Episode %d", i+1),
						ProviderIDs:   map[string]string{"tmdb": fmt.Sprintf("%s-tmdb-%d", seriesID, i+1)},
					}
				}

				tracer.reset()
				startedAt := time.Now()
				service.persistSeasonsAndEpisodes(
					ctx,
					itemRepo.items[seriesID],
					nil,
					"en",
					testCase.language,
					seasons,
					episodes,
					MergeFillEmpty,
				)
				queryCount := tracer.count()
				queryCounts = append(queryCounts, queryCount)
				t.Logf("language=%s episodes=%d statements=%d elapsed=%s", testCase.language, episodeCount, queryCount, time.Since(startedAt))

				storedEpisode, err := episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 1, 1)
				if err != nil {
					t.Fatalf("reload unlinked episode: %v", err)
				}
				if got, want := storedEpisode.Title, "Existing Unlinked Episode"; got != want {
					t.Fatalf("unlinked episode title = %q, want %q", got, want)
				}
			}

			if queryCounts[0] != queryCounts[1] {
				t.Fatalf("statement count grows with episode count: 1 episode=%d, 100 episodes=%d", queryCounts[0], queryCounts[1])
			}
			if got := queryCounts[0]; got != testCase.wantStatements {
				t.Fatalf("bounded statement count = %d, want %d", got, testCase.wantStatements)
			}
		})
	}
}

func TestPersistSeasonsAndEpisodes_NonCanonicalRefreshPreservesBaseAndLocalizations(t *testing.T) {
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

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.episode_localizations')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check episode localizations table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the localization schema")
	}

	seriesID := fmt.Sprintf("localized-bulk-series-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx,
		`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'English Series')`,
		seriesID,
	); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})

	seasonRepo := catalog.NewSeasonRepository(pool)
	episodeRepo := catalog.NewEpisodeRepository(pool)
	seasonLocalizationRepo := catalog.NewSeasonLocalizationRepository(pool)
	episodeLocalizationRepo := catalog.NewEpisodeLocalizationRepository(pool)
	season := &models.Season{
		ContentID:               seriesID + "-season-1",
		SeriesID:                seriesID,
		SeasonNumber:            1,
		Title:                   "English Season",
		DefaultMetadataLanguage: "en",
		Overview:                "English season overview",
		PosterPath:              "artwork/season/base/original.jpg",
		PosterSourcePath:        "tmdb://base-season.jpg",
		PosterThumbhash:         "base-season-thumb",
		MetadataSource:          "provider",
	}
	if err := seasonRepo.Upsert(ctx, season); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	episode := &models.Episode{
		ContentID:               seriesID + "-episode-1",
		SeriesID:                seriesID,
		SeasonID:                season.ContentID,
		SeasonNumber:            1,
		EpisodeNumber:           1,
		Title:                   "English Episode",
		DefaultMetadataLanguage: "en",
		Overview:                "English episode overview",
		StillPath:               "artwork/episode/base/original.jpg",
		StillSourcePath:         "tmdb://base-episode.jpg",
		StillThumbhash:          "base-episode-thumb",
		MetadataSource:          "provider",
	}
	if err := episodeRepo.Upsert(ctx, episode); err != nil {
		t.Fatalf("seed unlinked episode: %v", err)
	}
	if err := seasonLocalizationRepo.Upsert(ctx, &models.SeasonLocalization{
		SeasonContentID:  season.ContentID,
		Language:         "fr",
		Title:            "Ancienne saison",
		Overview:         "Resume manuel de saison",
		PosterPath:       "artwork/season/fr/original.jpg",
		PosterSourcePath: "tmdb://old-fr-season.jpg",
		PosterThumbhash:  "old-fr-thumb",
	}); err != nil {
		t.Fatalf("seed season localization: %v", err)
	}
	if err := episodeLocalizationRepo.Upsert(ctx, &models.EpisodeLocalization{
		EpisodeContentID: episode.ContentID,
		Language:         "fr",
		Title:            "Ancien episode",
		Overview:         "Resume manuel d'episode",
	}); err != nil {
		t.Fatalf("seed episode localization: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE season_localizations SET overview_source = 'manual' WHERE season_content_id = $1 AND language = 'fr'`,
		season.ContentID,
	); err != nil {
		t.Fatalf("mark season overview manual: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE episode_localizations SET overview_source = 'manual' WHERE episode_content_id = $1 AND language = 'fr'`,
		episode.ContentID,
	); err != nil {
		t.Fatalf("mark episode overview manual: %v", err)
	}

	itemRepo := newFakeItemRepo()
	itemRepo.items[seriesID] = &models.MediaItem{ContentID: seriesID, Type: "series", Title: "English Series"}
	service := &MetadataService{
		itemRepo:                itemRepo,
		seasonRepo:              seasonRepo,
		episodeRepo:             episodeRepo,
		seasonLocalizationRepo:  seasonLocalizationRepo,
		episodeLocalizationRepo: episodeLocalizationRepo,
	}
	service.hooks.ensureSeriesEpisodeLinks = func(context.Context, string) error { return nil }
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)

	service.persistSeasonsAndEpisodes(
		ctx,
		itemRepo.items[seriesID],
		map[string]string{"tmdb": "series-123"},
		"en",
		"fr",
		[]SeasonResult{{
			SeasonNumber: 1,
			Title:        "Saison francaise",
			Overview:     "Nouveau resume fournisseur",
			PosterPath:   "tmdb://new-fr-season.jpg",
		}},
		[]EpisodeResult{{
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         "Episode francais",
			Overview:      "Nouveau resume episode fournisseur",
		}},
		MergeReplaceUnlocked,
	)

	storedSeason, err := seasonRepo.GetBySeriesAndNumber(ctx, seriesID, 1)
	if err != nil {
		t.Fatalf("reload base season: %v", err)
	}
	if storedSeason.ContentID != season.ContentID || storedSeason.Title != "English Season" ||
		storedSeason.Overview != "English season overview" || storedSeason.PosterPath != season.PosterPath ||
		storedSeason.PosterSourcePath != season.PosterSourcePath || storedSeason.DefaultMetadataLanguage != "en" {
		t.Fatalf("base season changed during localized refresh: %#v", storedSeason)
	}
	storedEpisode, err := episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 1, 1)
	if err != nil {
		t.Fatalf("reload base episode: %v", err)
	}
	if storedEpisode.ContentID != episode.ContentID || storedEpisode.Title != "English Episode" ||
		storedEpisode.Overview != "English episode overview" || storedEpisode.StillPath != episode.StillPath ||
		storedEpisode.StillSourcePath != episode.StillSourcePath || storedEpisode.DefaultMetadataLanguage != "en" {
		t.Fatalf("base episode changed during localized refresh: %#v", storedEpisode)
	}

	seasonLoc, err := seasonLocalizationRepo.Get(ctx, season.ContentID, "fr")
	if err != nil {
		t.Fatalf("reload season localization: %v", err)
	}
	if seasonLoc.Title != "Saison francaise" || seasonLoc.Overview != "Resume manuel de saison" ||
		seasonLoc.OverviewSource != "manual" || seasonLoc.PosterSourcePath != "tmdb://new-fr-season.jpg" {
		t.Fatalf("season localization = %#v", seasonLoc)
	}
	episodeLoc, err := episodeLocalizationRepo.Get(ctx, episode.ContentID, "fr")
	if err != nil {
		t.Fatalf("reload episode localization: %v", err)
	}
	if episodeLoc.Title != "Episode francais" || episodeLoc.Overview != "Resume manuel d'episode" ||
		episodeLoc.OverviewSource != "manual" {
		t.Fatalf("episode localization = %#v", episodeLoc)
	}

	var localizedJob *EnqueueImageCacheJobInput
	for i := range enqueuer.inputs {
		if enqueuer.inputs[i].TargetType == ImageCacheTargetSeasonLocalization {
			localizedJob = &enqueuer.inputs[i]
			break
		}
	}
	if localizedJob == nil {
		t.Fatalf("localized season image job missing from %#v", enqueuer.inputs)
	}
	if localizedJob.TargetContentID != seriesID || localizedJob.TargetLanguage != "fr" ||
		localizedJob.SeasonNumber == nil || *localizedJob.SeasonNumber != season.SeasonNumber ||
		localizedJob.SourcePath != "tmdb://new-fr-season.jpg" {
		t.Fatalf("localized season image job = %#v", localizedJob)
	}
}
