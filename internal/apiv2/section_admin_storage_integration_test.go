package apiv2

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestAdminSectionTransportSnapshotAndRestoreDB(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			pool := viewerAccessTestPool(t)
			library := seedLibrary(t, pool, fmt.Sprintf("admin-section-transport-%d", time.Now().UnixNano()))
			repo := sections.NewRepository(pool)
			row, err := repo.Create(t.Context(), &sections.PageSection{Scope: "library", LibraryID: &library, Title: "Editor original", SectionType: sections.SectionRecentlyAdded, ItemLimit: 20, Enabled: true, Config: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			var provider userstore.UserStoreProvider = pgstore.NewPostgresProvider(pool)
			if backend == "sqlite" {
				provider = userdb.NewSQLiteProvider(nil)
			}
			service := handlers.NewSectionHandler(repo, nil)
			service.StoreProvider = notifications.WrapUserStoreProvider(provider, &notifications.System{})
			deps, _ := libraryDeps(t)
			deps.AdminSections = service
			h := newTestHandler(t, deps)
			auth := bearer(adminToken)
			path := Prefix + "/admin/sections/" + row.ID
			editor := do(t, h, http.MethodGet, path, "", auth)
			if editor.Code != 200 || editor.Header().Get("ETag") == "" {
				t.Fatalf("editor: %d %s", editor.Code, editor.Body.String())
			}
			row.Title = "Concurrent legacy edit"
			if err := repo.Update(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			stale := do(t, h, http.MethodPatch, path, `{"title":"Stale draft"}`, with(auth, "If-Match", editor.Header().Get("ETag")))
			if stale.Code != 412 {
				t.Fatalf("stale editor: %d %s", stale.Code, stale.Body.String())
			}
			current, err := repo.GetByID(t.Context(), row.ID)
			if err != nil || current.Title != row.Title {
				t.Fatalf("stale draft overwrote row: %+v %v", current, err)
			}
			query := fmt.Sprintf("?scope=library&library_id=%d", library)
			orderPath := Prefix + "/admin/sections/order" + query
			defaultsPath := Prefix + "/admin/sections/defaults" + query
			order := do(t, h, http.MethodGet, orderPath, "", auth)
			if order.Code != 200 || order.Header().Get("ETag") == "" {
				t.Fatalf("order: %d %s", order.Code, order.Body.String())
			}
			row.Title = "Another concurrent edit"
			if err := repo.Update(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			stale = do(t, h, http.MethodPut, defaultsPath, `{"reset_profiles":false}`, with(auth, "If-Match", order.Header().Get("ETag")))
			if stale.Code != 412 {
				t.Fatalf("stale restore: %d %s", stale.Code, stale.Body.String())
			}
			order = do(t, h, http.MethodGet, orderPath, "", auth)
			if order.Code != 200 {
				t.Fatal(order.Body.String())
			}
			before, err := repo.ScopeRevision(t.Context(), "library", &library)
			if err != nil {
				t.Fatal(err)
			}
			restored := do(t, h, http.MethodPut, defaultsPath, `{"reset_profiles":true}`, with(auth, "If-Match", order.Header().Get("ETag")))
			if backend == "sqlite" {
				if restored.Code != 501 {
					t.Fatalf("unsupported reset: %d %s", restored.Code, restored.Body.String())
				}
				after, err := repo.ScopeRevision(t.Context(), "library", &library)
				if err != nil || after != before {
					t.Fatalf("unsupported reset changed scope: %d -> %d %v", before, after, err)
				}
				if _, err := repo.GetByID(t.Context(), row.ID); err != nil {
					t.Fatal("unsupported reset removed original row")
				}
			} else {
				if restored.Code != 200 {
					t.Fatalf("wrapped postgres reset: %d %s", restored.Code, restored.Body.String())
				}
				if _, err := repo.GetByID(t.Context(), row.ID); !errors.Is(err, sections.ErrSectionNotFound) {
					t.Fatalf("successful reset retained original definition: %v", err)
				}
				defaults, err := repo.ListByScopeAll(t.Context(), "library", &library)
				if err != nil || len(defaults) == 0 {
					t.Fatalf("successful reset has no defaults: %v", err)
				}
			}
		})
	}
}
