package handlers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

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
// so the client shows the section's error and retry state, without leaking
// the cause. A section with nothing to recommend answers with no items.
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

	const sectionID = "default-recommended-for-you"
	viewer := SectionViewer{Access: catalog.AccessFilter{UserID: 7, ProfileID: "profile-1"}}
	ctx = authenticatedRecsContext(ctx, nil)
	items := func(reader sectionRowReader) (SectionView, error) {
		fetcher := sections.NewFetcher(pool)
		fetcher.RecommendationReader = reader
		h := NewSectionHandler(sections.NewRepository(pool), fetcher)
		h.FolderRepo = catalog.NewFolderRepository(pool)
		return h.LibrarySectionItems(ctx, libraryID, sectionID, viewer)
	}

	_, err = items(sectionRowReader{err: errors.New("recommendation cache read failed")})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusInternalServerError || apiErr.Code != "internal_error" {
		t.Fatalf("failed fetch: err = %v; want a 500 internal_error", err)
	}
	if strings.Contains(apiErr.Message, "cache read failed") {
		t.Fatalf("failed fetch leaks the cause: %q", apiErr.Message)
	}

	section, err := items(sectionRowReader{})
	if err != nil {
		t.Fatalf("empty row: %v", err)
	}
	if section.ID != sectionID || len(section.Items) != 0 {
		t.Fatalf("empty row = %+v, want the section with no items", section)
	}
}
