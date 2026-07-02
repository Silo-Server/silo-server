package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminJobCreator struct {
	mu       sync.Mutex
	refreshs []adminjob.LibraryRefreshRequest
}

func (f *fakeAdminJobCreator) Create(context.Context, adminjob.CreateJobInput) (*models.AdminJob, error) {
	return &models.AdminJob{}, nil
}

func (f *fakeAdminJobCreator) CreateLibraryRefresh(_ context.Context, _ int, req adminjob.LibraryRefreshRequest, _ string) (*models.AdminJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshs = append(f.refreshs, req)
	return &models.AdminJob{}, nil
}

func (f *fakeAdminJobCreator) libraryRefreshes() []adminjob.LibraryRefreshRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]adminjob.LibraryRefreshRequest(nil), f.refreshs...)
}

// TestHandleUpdateLibraryLanguageChangeQueuesRefresh covers the trigger half
// of issue #211: changing a library's metadata language must enqueue a
// library metadata refresh so existing items re-fetch in the new language,
// while an update that does not change the language must not enqueue one.
func TestHandleUpdateLibraryLanguageChangeQueuesRefresh(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled, metadata_language)
		VALUES ('movies', 'Lang Change Test', true, 'zh')
		RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	jobs := &fakeAdminJobCreator{}
	h := NewLibraryHandler(catalog.NewFolderRepository(pool), nil, nil, pool, nil)
	h.JobRepo = jobs

	doUpdate := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/libraries/"+strconv.Itoa(folderID), strings.NewReader(body))
		chiCtx := chi.NewRouteContext()
		chiCtx.URLParams.Add("id", strconv.Itoa(folderID))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chiCtx))
		rec := httptest.NewRecorder()
		h.HandleUpdateLibrary(rec, req)
		return rec
	}

	// An update that does not touch the language must not enqueue a refresh.
	if rec := doUpdate(`{"name":"Lang Change Test Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename update status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	if got := jobs.libraryRefreshes(); len(got) != 0 {
		t.Fatalf("refresh jobs after rename = %d, want 0", len(got))
	}

	// Changing the metadata language must enqueue a refresh for this library.
	if rec := doUpdate(`{"metadata_language":"da"}`); rec.Code != http.StatusOK {
		t.Fatalf("language update status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	refreshes := jobs.libraryRefreshes()
	if len(refreshes) != 1 {
		t.Fatalf("refresh jobs after language change = %d, want 1", len(refreshes))
	}
	if refreshes[0].LibraryID != folderID {
		t.Errorf("refresh library id = %d, want %d", refreshes[0].LibraryID, folderID)
	}

	// Re-submitting the same language is a no-op update and must not enqueue.
	if rec := doUpdate(`{"metadata_language":"da"}`); rec.Code != http.StatusOK {
		t.Fatalf("same-language update status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	if got := jobs.libraryRefreshes(); len(got) != 1 {
		t.Fatalf("refresh jobs after same-language update = %d, want still 1", len(got))
	}
}
