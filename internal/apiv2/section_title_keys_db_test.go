package apiv2

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
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

// Recommendation rows read through the real recommendations seam carry the
// key of every title the server words, with its arguments, on the row lists
// and on a row read on its own.
func TestRecommendationRowTitleKeysDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	recs := recommendations.NewRepo(pool)
	genre := seedRecommendationRows(t, pool, recs)
	ratings := catalogpkg.NewRatingsRepo(pool)
	svc := handlers.NewRecommendationsHandler(nil, recommendations.NewReader(recs, ratings, nil, nil), nil, ratings, recs, false)
	svc.Fetcher = sections.NewFetcher(pool)
	deps := scopedViewerDeps(t, &access.Scope{}, &fakeLibraryViews{}, &fakeLibraryViews{})
	deps.Recommendations = svc
	h := newTestHandler(t, deps)

	type row struct {
		Type      string            `json:"type"`
		Title     string            `json:"title"`
		TitleKey  string            `json:"title_key"`
		TitleArgs map[string]string `json:"title_args"`
	}
	// The server's English for each row type, and the key it carries.
	type keyed struct {
		key  string
		args map[string]string
	}
	want := map[[2]string]keyed{
		{"cluster", "For You"}:                               {"home.section.forYou", nil},
		{"popular", "Popular on This Server"}:                {"home.section.popularOnServer", nil},
		{"recently_added", "Recently Added"}:                 {"home.section.recentlyAdded", nil},
		{"top_rated", "Highly Rated in Your Library"}:        {"home.section.highlyRatedInLibrary", nil},
		{"genre_sampler", "Top " + genre}:                    {"home.section.topGenre", map[string]string{"genre": genre}},
		{"similar_users_liked", "Profiles Like You Enjoyed"}: {"home.section.profilesLikeYouEnjoyed", nil},
	}
	check := func(path string, r row) {
		t.Helper()
		w, ok := want[[2]string{r.Type, r.Title}]
		if !ok {
			// Rows another test left in the shared cache, such as other
			// genres, are worded by the same code; only the ones above are
			// asserted.
			return
		}
		if r.TitleKey != w.key || !maps.Equal(r.TitleArgs, w.args) {
			t.Errorf("%s: %s %q has key %q %v, want %q %v", path, r.Type, r.Title, r.TitleKey, r.TitleArgs, w.key, w.args)
		}
	}
	read := func(path string, out any) {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}

	var discover struct{ Items []row }
	read(Prefix+"/recommendations/discover", &discover)
	seen := map[string]bool{}
	for _, r := range discover.Items {
		check("discover", r)
		seen[r.Type] = true
	}
	if !seen["top_rated"] || !seen["recently_added"] {
		t.Fatalf("discover rows %+v; want the Highly Rated and Recently Added rows", discover.Items)
	}
	var main row
	read(Prefix+"/recommendations/for-you/main", &main)
	check("for-you main", main)
	if main.TitleKey == "" {
		t.Errorf("for-you main %q carries no key", main.Title)
	}
	var rest struct{ Items []row }
	read(Prefix+"/recommendations/for-you/rows", &rest)
	for _, r := range rest.Items {
		check("for-you rows", r)
	}
	for _, path := range []string{
		Prefix + "/recommendations/section/top-rated",
		Prefix + "/recommendations/section/recently-added",
		Prefix + "/recommendations/section/genre?key=" + url.QueryEscape(genre),
	} {
		var r row
		read(path, &r)
		if _, ok := want[[2]string{r.Type, r.Title}]; !ok {
			t.Fatalf("%s answered %s %q", path, r.Type, r.Title)
		}
		check(path, r)
	}
}

// seedRecommendationRows seeds a library whose titles fill the Highly Rated and
// Recently Added rows, and a cached genre row over one of them, and returns
// the genre.
func seedRecommendationRows(t *testing.T, pool *pgxpool.Pool, recs *recommendations.Repo) string {
	t.Helper()
	ctx := t.Context()
	suffix := time.Now().UnixNano()
	library := seedLibrary(t, pool, fmt.Sprintf("rec-title-keys-%d", suffix))
	items := catalogpkg.NewItemRepository(pool)
	var ids []string
	// Two rated titles for Highly Rated and an unrated one only Recently
	// Added holds, since Discover shows a title in one row.
	for i := range 3 {
		id := fmt.Sprintf("movie:rec-title-keys-%d-%d", suffix, i)
		if err := items.Upsert(ctx, &models.MediaItem{ContentID: id, Type: "movie", Title: id, Status: "matched"}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mustExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, id) })
		if i < 2 {
			mustExec(t, pool, `UPDATE media_items SET rating_imdb = 8.5 WHERE content_id = $1`, id)
		}
		mustExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library)
		ids = append(ids, id)
	}
	genre := fmt.Sprintf("Drama %d", suffix)
	genreRow := recommendations.RecTypeGenreSamplerPrefix + genre
	if err := recs.UpsertRecommendationCache(ctx, recommendations.GlobalCacheUserID, recommendations.GlobalCacheProfileID, genreRow, "",
		[]recommendations.ScoredItem{{MediaItemID: ids[0]}}, time.Now().Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustExec(t, pool, `DELETE FROM recommendation_cache WHERE rec_type = $1`, genreRow) })
	return genre
}
