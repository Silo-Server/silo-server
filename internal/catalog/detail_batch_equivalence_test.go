package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// batchEquivFileFetcher is a FileVersionFetcher (and the optional batch
// fileVersionBatchFetcher) backed by one in-memory map. Serving both the
// per-item GetByContentID and the batched ListByContentIDs from the same
// backing data is exactly how the real scanner.FileRepository behaves: the two
// methods return the same files for a given content ID. This lets the
// equivalence test prove that GetItemDetailsByIDs routes prefetched files into
// buildMediaItemDetail identically to the per-item GetItemDetail path, without
// pulling the scanner package in (which would create an import cycle).
type batchEquivFileFetcher struct {
	files map[string][]*models.MediaFile
}

func (f *batchEquivFileFetcher) GetByContentID(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	return f.files[contentID], nil
}

func (f *batchEquivFileFetcher) GetByEpisodeID(_ context.Context, _ string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (f *batchEquivFileFetcher) ListByContentIDs(_ context.Context, contentIDs []string) (map[string][]*models.MediaFile, error) {
	out := make(map[string][]*models.MediaFile, len(contentIDs))
	for _, id := range contentIDs {
		if files, ok := f.files[id]; ok {
			out[id] = files
		}
	}
	return out, nil
}

// batchEquivWorkSummaryProvider implements both the per-item
// WorkSummaryProvider and the batched WorkSummaryBatchProvider from one backing
// map, the same contract literaryworks.Repository satisfies in production. As
// with files, this proves the batch path's work-summary routing matches the
// per-item path without importing literaryworks (import cycle).
type batchEquivWorkSummaryProvider struct {
	summaries map[string]*WorkSummary
}

func (p *batchEquivWorkSummaryProvider) GetSummaryForContentID(_ context.Context, contentID string, _ AccessFilter) (*WorkSummary, error) {
	return p.summaries[contentID], nil
}

func (p *batchEquivWorkSummaryProvider) ListSummariesForContentIDs(_ context.Context, contentIDs []string, _ AccessFilter) (map[string]*WorkSummary, error) {
	out := make(map[string]*WorkSummary, len(contentIDs))
	for _, id := range contentIDs {
		if s, ok := p.summaries[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func newBatchEquivTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_items')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check media_items table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied base schema")
	}
	return pool
}

func batchEquivExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed (%s): %v", sql, err)
	}
}

// TestGetItemDetailsByIDs_MatchesGetItemDetail exercises the REAL
// DetailService.GetItemDetailsByIDs against the REAL per-item GetItemDetail over
// shared in-memory/real repositories, asserting per-id deep equality of the
// returned *ItemDetail.
//
// Unlike the jellycompat handler test (which stubs the content service so both
// paths return the same canned *ItemDetail), this drives the genuinely-batched
// repository code: ItemRepository.GetByIDs vs GetByID, EnsureAccessibleIDs vs
// EnsureAccessible, MediaItemLocalizationRepository.GetByContentIDs vs Get, and
// PersonRepository.ListForItems vs ListForItem all run against a real Postgres.
// Media files and work summaries route through interface fakes that serve the
// batch and per-item method shapes from one backing map (mirroring the real
// FileRepository / literaryworks.Repository), so the file/work-summary
// prefetch wiring is compared too.
//
// Equivalence axes covered, with a mixed movie+movie+series dataset:
//   - access checks (EnsureAccessibleIDs vs EnsureAccessible), incl. an
//     access-filtered R-rated item asserted ABSENT from the batch map and
//     rejected by per-item GetItemDetail;
//   - localization: a movie with an fr localization row (localized title/
//     overview path) alongside a movie that has none (nil short-circuit) and
//     a pending-translation-language case;
//   - credits ordering (ListForItems vs ListForItem) on the first movie;
//   - media files (ListByContentIDs vs GetByContentID) on the first movie;
//   - work summary (ListSummariesForContentIDs vs GetSummaryForContentID) on
//     the series.
func TestGetItemDetailsByIDs_MatchesGetItemDetail(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()

	suffix := time.Now().UnixNano()
	movieA := fmt.Sprintf("batch-equiv-movie-a-%d", suffix)
	movieB := fmt.Sprintf("batch-equiv-movie-b-%d", suffix)
	series := fmt.Sprintf("batch-equiv-series-%d", suffix)
	movieR := fmt.Sprintf("batch-equiv-movie-r-%d", suffix)
	personActor1 := suffix
	personActor2 := suffix + 1
	personDirector := suffix + 2

	t.Cleanup(func() {
		ids := []string{movieA, movieB, series, movieR}
		batchEquivExec(t, pool, `DELETE FROM item_people WHERE content_id = ANY($1)`, ids)
		batchEquivExec(t, pool, `DELETE FROM people WHERE id = ANY($1)`, []int64{personActor1, personActor2, personDirector})
		batchEquivExec(t, pool, `DELETE FROM media_item_localizations WHERE content_id = ANY($1)`, ids)
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
	})

	insertItem := func(contentID, mediaType, title, overview, rating string) {
		batchEquivExec(t, pool, `
			INSERT INTO media_items (content_id, type, title, genres, overview, content_rating, default_metadata_language)
			VALUES ($1, $2, $3, '{}'::text[], $4, $5, 'en')
		`, contentID, mediaType, title, overview, rating)
	}
	// movieA: localized into fr, credited, has files. content_rating PG -> visible.
	insertItem(movieA, "movie", "Movie A", "Movie A overview (en)", "PG")
	// movieB: overview present, default lang en, NO fr localization row -> the
	// pending-translation-language path must fire and match between paths.
	insertItem(movieB, "movie", "Movie B", "Movie B overview (en)", "PG")
	// series: has a work summary, no playable files (series skip the file path).
	insertItem(series, "series", "Series One", "Series overview (en)", "PG")
	// movieR: R-rated -> filtered out by MaxContentRating=PG-13 on BOTH paths.
	insertItem(movieR, "movie", "Movie R", "Movie R overview (en)", "R")

	// fr localization for movieA only.
	batchEquivExec(t, pool, `
		INSERT INTO media_item_localizations (
			content_id, language, title, sort_title, overview, tagline,
			poster_path, poster_source_path, poster_thumbhash,
			backdrop_path, backdrop_source_path, backdrop_thumbhash,
			logo_path, logo_source_path, overview_source, tagline_source
		)
		VALUES ($1, 'fr', 'Film A', '', 'Synopsis du film A (fr)', '',
			'', '', '', '', '', '', '', '', 'manual', 'manual')
	`, movieA)

	// Credits on movieA: two actors (cast) + one director (crew). Seeded with
	// sort_order so ListForItems / ListForItem ordering parity is observable.
	insertPerson := func(id int64, name string) {
		batchEquivExec(t, pool, `INSERT INTO people (id, name) VALUES ($1, $2)`, id, name)
	}
	insertPerson(personActor1, "Lead Actor")
	insertPerson(personActor2, "Supporting Actor")
	insertPerson(personDirector, "The Director")
	insertCredit := func(id, personID int64, kind models.PersonKind, character string, order int) {
		batchEquivExec(t, pool, `
			INSERT INTO item_people (id, person_id, content_id, kind, character, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, id, personID, movieA, int16(kind), character, order)
	}
	insertCredit(personActor1, personActor1, models.PersonKindActor, "Hero", 0)
	insertCredit(personActor2, personActor2, models.PersonKindActor, "Sidekick", 1)
	insertCredit(personDirector, personDirector, models.PersonKindDirector, "", 0)

	fileFetcher := &batchEquivFileFetcher{files: map[string][]*models.MediaFile{
		movieA: {
			{ID: 9001, ContentID: movieA, FilePath: "/media/movie-a-1080p.mkv", Container: "mkv", Resolution: "1080p", Duration: 6000, FileSize: 5_000_000},
			{ID: 9002, ContentID: movieA, FilePath: "/media/movie-a-720p.mkv", Container: "mkv", Resolution: "720p", Duration: 6000, FileSize: 2_000_000},
		},
	}}
	workSummaries := &batchEquivWorkSummaryProvider{summaries: map[string]*WorkSummary{
		series: {
			WorkID: "work-series-1",
			Title:  "Series One Work",
			Formats: []WorkFormatSummary{
				{Type: "series", ContentID: series, LibraryID: 3},
			},
		},
	}}

	newService := func() *DetailService {
		svc := NewDetailService(
			NewItemRepository(pool),
			NewEpisodeRepository(pool),
			NewSeasonRepository(pool),
			NewPersonRepository(pool),
			fileFetcher,
		)
		svc.SetWorkSummaryProvider(workSummaries)
		return svc
	}

	// Single shared access filter for BOTH paths: PG-13 ceiling (so PG items are
	// visible, the R item is not) and an explicit fr presentation language (so
	// the localization path actually runs rather than short-circuiting).
	filter := AccessFilter{
		PresentationLanguage: "fr",
		MaxContentRating:     "PG-13",
		UserID:               1,
		ProfileID:            "profile-1",
	}

	visibleIDs := []string{movieA, movieB, series}
	allIDs := []string{movieA, movieB, series, movieR}

	batch, err := newService().GetItemDetailsByIDs(ctx, allIDs, filter)
	if err != nil {
		t.Fatalf("GetItemDetailsByIDs: %v", err)
	}

	// Access-control parity: the R-rated item must be absent from the batch map
	// and rejected by per-item GetItemDetail, i.e. EnsureAccessibleIDs and
	// EnsureAccessible agree.
	if _, present := batch[movieR]; present {
		t.Fatalf("access-filtered item %s present in batch result", movieR)
	}
	if _, err := newService().GetItemDetail(ctx, movieR, filter); err == nil {
		t.Fatalf("per-item GetItemDetail(%s) succeeded, want access rejection", movieR)
	}

	for _, id := range visibleIDs {
		batchDetail, ok := batch[id]
		if !ok {
			t.Fatalf("id %s missing from batch result", id)
		}
		perItem, err := newService().GetItemDetail(ctx, id, filter)
		if err != nil {
			t.Fatalf("GetItemDetail(%s): %v", id, err)
		}
		if !reflect.DeepEqual(batchDetail, perItem) {
			t.Fatalf("detail mismatch for %s:\nbatch:   %#v\nperItem: %#v", id, batchDetail, perItem)
		}
	}

	// Guard that the distinguishing data actually landed (otherwise the deep
	// equality above could pass vacuously on empty details).
	if got := batch[movieA]; got.Title != "Film A" || got.Overview != "Synopsis du film A (fr)" {
		t.Fatalf("movieA not localized into fr: title=%q overview=%q", got.Title, got.Overview)
	}
	if got := batch[movieA]; len(got.Cast) != 2 || len(got.Crew) != 1 {
		t.Fatalf("movieA credits = %d cast / %d crew, want 2/1", len(got.Cast), len(got.Crew))
	}
	if got := batch[movieA]; got.Cast[0].Character != "Hero" || got.Cast[1].Character != "Sidekick" {
		t.Fatalf("movieA cast order = %q,%q, want Hero,Sidekick", got.Cast[0].Character, got.Cast[1].Character)
	}
	if got := batch[movieA]; len(got.Versions) != 2 {
		t.Fatalf("movieA versions = %d, want 2 (file prefetch not applied)", len(got.Versions))
	}
	if got := batch[movieB]; got.PendingTranslationLanguage != "fr" {
		t.Fatalf("movieB pending translation language = %q, want fr", got.PendingTranslationLanguage)
	}
	if got := batch[series]; got.WorkID != "work-series-1" {
		t.Fatalf("series work summary not applied: WorkID=%q", got.WorkID)
	}
}
