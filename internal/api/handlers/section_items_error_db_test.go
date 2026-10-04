package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// sectionRowReader answers every recommendation section read with err, or
// with an empty row when err is nil.
type sectionRowReader struct{ err error }

func (r sectionRowReader) SectionForYouMain(context.Context, int, string, catalog.AccessFilter) (*recommendations.ForYouRow, error) {
	return nil, r.err
}

func (r sectionRowReader) SectionForYouFill(context.Context, int, string, catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	return nil, r.err
}

func (r sectionRowReader) SectionBecauseYouWatched(context.Context, int, string, string, []int, catalog.AccessFilter) ([]recommendations.ScoredItem, string, error) {
	return nil, "", r.err
}

func (r sectionRowReader) SectionSimilarUsersLiked(context.Context, int, string, catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	return nil, r.err
}

func (r sectionRowReader) SectionTasteMatchRow(context.Context, int, string, string, catalog.AccessFilter) (*recommendations.ForYouRow, error) {
	return nil, r.err
}

// A library section whose recommendation read fails answers a 500 problem,
// so the client shows the section's error and retry state; a section with
// nothing to recommend still answers an empty 200.
func TestLibrarySectionItemsAnswersAnErrorWhenTheFetchFailsDB(t *testing.T) {
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

	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, "section-error-"+uuid.NewString()).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID) })

	request := func(reader sectionRowReader) *httptest.ResponseRecorder {
		t.Helper()
		fetcher := sections.NewFetcher(pool)
		fetcher.RecommendationReader = reader
		h := NewSectionHandler(sections.NewRepository(pool), fetcher)
		h.FolderRepo = catalog.NewFolderRepository(pool)
		router := chi.NewRouter()
		router.Get("/library/{id}/sections/{sectionId}/items", h.HandleLibrarySectionItems)
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/library/%d/sections/default-recommended-for-you/items", libraryID), nil)
		req = req.WithContext(authenticatedRecsContext(req.Context(), nil))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	failed := request(sectionRowReader{err: errors.New("recommendation cache read failed")})
	if failed.Code != http.StatusInternalServerError || !strings.Contains(failed.Body.String(), `"internal_error"`) {
		t.Fatalf("failed fetch: %d %s; want a 500 internal_error", failed.Code, failed.Body.String())
	}
	if strings.Contains(failed.Body.String(), "cache read failed") {
		t.Fatalf("failed fetch leaks the cause: %s", failed.Body.String())
	}

	empty := request(sectionRowReader{})
	if empty.Code != http.StatusOK {
		t.Fatalf("empty row: %d %s; want 200", empty.Code, empty.Body.String())
	}
	var body homeSectionItemsResponse
	if err := json.Unmarshal(empty.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Section.ID != "default-recommended-for-you" || len(body.Section.Items) != 0 {
		t.Fatalf("empty row = %+v, want the section with no items", body.Section)
	}
}
