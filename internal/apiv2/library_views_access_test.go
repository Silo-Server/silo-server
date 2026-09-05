package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// These tests drive the real v1 seams (handlers.SectionHandler and
// handlers.LibraryCollectionHandler) through the v2 router, so they prove the
// authorization both listeners share rather than a fake's answer.

// scopedViewerDeps wires the real seams behind a viewer whose access policy the
// test controls.
func scopedViewerDeps(t *testing.T, policy *access.Scope, sectionsSvc LibrarySectionService, collectionsSvc LibraryCollectionService) Dependencies {
	t.Helper()
	deps, _ := libraryDeps(t)
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: policy})
	deps.LibrarySections = sectionsSvc
	deps.LibraryCollections = collectionsSvc
	return deps
}

// TestLibraryViewsRefuseLibraryOutsideViewerScope: a profile whose library
// allowlist excludes the library gets not_found from every profile-scoped
// library read before any repository is consulted, so the seams are built on
// nil repositories here and would panic if the check were skipped.
func TestLibraryViewsRefuseLibraryOutsideViewerScope(t *testing.T) {
	policy := &access.Scope{AllowedLibraryIDs: []int{2}, LibrariesRestricted: true}
	h := newTestHandler(t, scopedViewerDeps(t, policy, handlers.NewSectionHandler(nil, nil), handlers.NewLibraryCollectionHandler(nil, nil, nil, 0, nil, nil)))
	for _, path := range []string{
		"/api/v2/library/1/layout",
		"/api/v2/library/1/sections",
		"/api/v2/library/1/sections/continue_watching/items",
		"/api/v2/library/1/collections/c1/items",
	} {
		requireProblem(t, do(t, h, http.MethodGet, path, "", viewerHeaders()), TypeNotFound)
	}
}

func viewerAccessTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var table *string
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('public.library_collection_items')::text`).Scan(&table); err != nil {
		t.Fatalf("check schema: %v", err)
	}
	if table == nil || *table == "" {
		t.Skip("test database has not applied the base schema")
	}
	return pool
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec (%s): %v", sql, err)
	}
}

func seedLibrary(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(), `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() { mustExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, id) })
	return id
}

// TestLibraryViewsAdmitAllowedLibraryDB: the same three section reads answer
// 200 once the allowlist names the library.
func TestLibraryViewsAdmitAllowedLibraryDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	libraryID := seedLibrary(t, pool, fmt.Sprintf("scope-allowed-%d", time.Now().UnixNano()))
	policy := &access.Scope{AllowedLibraryIDs: []int{libraryID}, LibrariesRestricted: true}
	svc := handlers.NewSectionHandler(sections.NewRepository(pool), sections.NewFetcher(pool))
	h := newTestHandler(t, scopedViewerDeps(t, policy, svc, &fakeLibraryViews{}))
	base := fmt.Sprintf("/api/v2/library/%d", libraryID)
	for _, path := range []string{base + "/layout", base + "/sections", base + "/sections/default-recently-added/items"} {
		if rec := do(t, h, http.MethodGet, path, "", viewerHeaders()); rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	policy.AllowedLibraryIDs = []int{libraryID + 1}
	requireProblem(t, do(t, h, http.MethodGet, base+"/layout", "", viewerHeaders()), TypeNotFound)
}

// TestLibraryViewsRefuseUnknownLibraryDB: an unrestricted profile (no library
// allowlist) still gets not_found from every section read of a library id
// that does not exist, instead of a fabricated default layout. The same
// handler answers 200 for a library that does exist, so the refusal is the
// existence check and not the scope check.
func TestLibraryViewsRefuseUnknownLibraryDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	libraryID := seedLibrary(t, pool, fmt.Sprintf("scope-unknown-%d", time.Now().UnixNano()))
	policy := &access.Scope{}
	svc := handlers.NewSectionHandler(sections.NewRepository(pool), sections.NewFetcher(pool))
	svc.FolderRepo = catalogpkg.NewFolderRepository(pool)
	h := newTestHandler(t, scopedViewerDeps(t, policy, svc, &fakeLibraryViews{}))
	if rec := do(t, h, http.MethodGet, fmt.Sprintf("/api/v2/library/%d/layout", libraryID), "", viewerHeaders()); rec.Code != 200 {
		t.Fatalf("existing library: %d %s", rec.Code, rec.Body.String())
	}
	const missing = "/api/v2/library/999999"
	for _, path := range []string{missing + "/layout", missing + "/sections", missing + "/sections/x/items"} {
		requireProblem(t, do(t, h, http.MethodGet, path, "", viewerHeaders()), TypeNotFound)
	}
}

// TestLibraryViewsRefuseHiddenLibrary: an unrestricted profile (no library
// allowlist) that has hidden a library carries it in DisabledLibraryIDs, and
// every profile-scoped library read refuses it as not_found while a library
// the profile has not hidden still answers. The seams are built on nil
// repositories, so the refusal must come from the scope check alone.
func TestLibraryViewsRefuseHiddenLibrary(t *testing.T) {
	policy := &access.Scope{DisabledLibraryIDs: []int{1}}
	h := newTestHandler(t, scopedViewerDeps(t, policy, handlers.NewSectionHandler(nil, nil), handlers.NewLibraryCollectionHandler(nil, nil, nil, 0, nil, nil)))
	for _, path := range []string{
		"/api/v2/library/1/layout",
		"/api/v2/library/1/sections",
		"/api/v2/library/1/sections/continue_watching/items",
		"/api/v2/library/1/collections/c1/items",
		"/api/v2/library/1/user-collections",
	} {
		requireProblem(t, do(t, h, http.MethodGet, path, "", viewerHeaders()), TypeNotFound)
	}
	// The allowed neighbor is answered by the fakes, proving the refusal
	// above was the hidden id and not a blanket one.
	h = newTestHandler(t, scopedViewerDeps(t, policy, &fakeLibraryViews{}, &fakeLibraryViews{}))
	for _, path := range []string{"/api/v2/library/2/layout", "/api/v2/library/2/sections", "/api/v2/library/2/collections/c1/items"} {
		if rec := do(t, h, http.MethodGet, path, "", viewerHeaders()); rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
