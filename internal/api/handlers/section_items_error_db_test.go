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

// A library section whose recommendation read fails answers a 500 problem on
// v2, so the client shows the section's error and retry state; the frozen v1
// endpoint keeps answering it with no items. A section with nothing to
// recommend answers an empty 200 on both.
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

	handler := func(reader sectionRowReader) *SectionHandler {
		fetcher := sections.NewFetcher(pool)
		fetcher.RecommendationReader = reader
		h := NewSectionHandler(sections.NewRepository(pool), fetcher)
		h.FolderRepo = catalog.NewFolderRepository(pool)
		return h
	}
	const sectionID = "default-recommended-for-you"
	v1 := func(reader sectionRowReader) *httptest.ResponseRecorder {
		t.Helper()
		router := chi.NewRouter()
		router.Get("/library/{id}/sections/{sectionId}/items", handler(reader).HandleLibrarySectionItems)
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/library/%d/sections/%s/items", libraryID, sectionID), nil)
		req = req.WithContext(authenticatedRecsContext(req.Context(), nil))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	emptySection := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s; want 200", name, rec.Code, rec.Body.String())
		}
		var body homeSectionItemsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Section.ID != sectionID || len(body.Section.Items) != 0 {
			t.Fatalf("%s = %+v, want the section with no items", name, body.Section)
		}
	}
	failing := sectionRowReader{err: errors.New("recommendation cache read failed")}

	// The service the v2 adapter calls answers the failure as a 500 problem.
	ctx = authenticatedRecsContext(ctx, nil)
	_, err = handler(failing).LibrarySectionItems(ctx, libraryID, sectionID, SectionViewer{Access: catalog.AccessFilter{UserID: 7, ProfileID: "profile-1"}})
	failed := httptest.NewRecorder()
	writeAPIError(failed, err)
	if failed.Code != http.StatusInternalServerError || !strings.Contains(failed.Body.String(), `"internal_error"`) {
		t.Fatalf("failed fetch: %d %s; want a 500 internal_error", failed.Code, failed.Body.String())
	}
	if strings.Contains(failed.Body.String(), "cache read failed") {
		t.Fatalf("failed fetch leaks the cause: %s", failed.Body.String())
	}

	emptySection("v1 failed fetch", v1(failing))
	emptySection("empty row", v1(sectionRowReader{}))
}
