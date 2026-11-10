package apiv2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// A library page row read on its own and the admin section list carry the
// stored title key while the title is the server's own, and lose it once an
// administrator renames the row.
func TestSectionTitleKeysOnRowReadsAndAdminSectionsDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	library := seedLibrary(t, pool, fmt.Sprintf("title-keys-%d", time.Now().UnixNano()))
	t.Cleanup(func() { mustExec(t, pool, `DELETE FROM page_sections WHERE library_id = $1`, library) })
	repo := sections.NewRepository(pool)
	if _, err := repo.RestoreDefaults(t.Context(), "library", &library, sections.DefaultLibrarySectionsForType(&library, "movies")); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListByScopeAll(t.Context(), "library", &library)
	if err != nil {
		t.Fatal(err)
	}
	var rowID string
	for _, row := range rows {
		if row.Title == "Top Rated Movies" {
			rowID = row.ID
		}
	}
	if rowID == "" {
		t.Fatal("no Top Rated Movies row among the restored defaults")
	}

	svc := handlers.NewSectionHandler(repo, sections.NewFetcher(pool))
	svc.FolderRepo = catalogpkg.NewFolderRepository(pool)
	deps := scopedViewerDeps(t, &access.Scope{}, svc, &fakeLibraryViews{})
	deps.AdminSections = svc
	h := newTestHandler(t, deps)
	admin := bearer(adminToken)
	itemsPath := fmt.Sprintf("/api/v2/library/%d/sections/%s/items", library, rowID)
	listPath := fmt.Sprintf("%s/admin/sections?scope=library&library_id=%d", Prefix, library)
	sectionPath := Prefix + "/admin/sections/" + rowID

	type titled struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		TitleKey string `json:"title_key"`
	}
	read := func(path string, headers map[string]string) []byte {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, "", headers)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	keys := func() (items, listed, single string) {
		t.Helper()
		var row titled
		if err := json.Unmarshal(read(itemsPath, viewerHeaders()), &row); err != nil {
			t.Fatal(err)
		}
		var list struct{ Items []titled }
		if err := json.Unmarshal(read(listPath, admin), &list); err != nil {
			t.Fatal(err)
		}
		listed = "<missing>"
		for _, s := range list.Items {
			if s.ID == rowID {
				listed = s.TitleKey
			}
		}
		var one titled
		if err := json.Unmarshal(read(sectionPath, admin), &one); err != nil {
			t.Fatal(err)
		}
		return row.TitleKey, listed, one.TitleKey
	}

	const key = "home.section.topRatedMovies"
	if items, listed, single := keys(); items != key || listed != key || single != key {
		t.Fatalf("default row keys: items %q, admin list %q, admin read %q; want %q", items, listed, single, key)
	}

	editor := do(t, h, http.MethodGet, sectionPath, "", admin)
	renamed := do(t, h, http.MethodPatch, sectionPath, `{"title":"Best Films"}`, with(admin, "If-Match", editor.Header().Get("ETag")))
	if renamed.Code != 200 {
		t.Fatalf("rename: %d %s", renamed.Code, renamed.Body.String())
	}
	var patched titled
	if err := json.Unmarshal(renamed.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Title != "Best Films" || patched.TitleKey != "" {
		t.Fatalf("rename answered %+v; want the typed title without a key", patched)
	}
	if items, listed, single := keys(); items != "" || listed != "" || single != "" {
		t.Fatalf("renamed row keys: items %q, admin list %q, admin read %q; want none", items, listed, single)
	}
}
