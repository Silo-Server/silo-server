package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type fakeItemRefreshFiles struct {
	byContentID map[string][]*models.MediaFile
	byEpisodeID map[string][]*models.MediaFile
	err         error
}

func (f *fakeItemRefreshFiles) GetByContentID(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	return f.byContentID[contentID], f.err
}

func (f *fakeItemRefreshFiles) GetByEpisodeID(_ context.Context, episodeID string) ([]*models.MediaFile, error) {
	return f.byEpisodeID[episodeID], f.err
}

type fakeItemRefreshSeasons struct {
	seasons map[string]*models.Season
}

func (f *fakeItemRefreshSeasons) GetByID(_ context.Context, contentID string) (*models.Season, error) {
	if season, ok := f.seasons[contentID]; ok {
		return season, nil
	}
	return nil, catalog.ErrSeasonNotFound
}

// writeMediaFile creates an empty media file (and its directories) under root.
func writeMediaFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newItemRefreshHandler(root, libraryType string, queue *fakeAutoscanQueue, files *fakeItemRefreshFiles, seasons *fakeItemRefreshSeasons) *AutoscanHandler {
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{{
		ID:      7,
		Name:    "Library",
		Type:    libraryType,
		Enabled: true,
		Paths:   []string{root},
	}}}, queue, NewResourceIDCodec(), nil)
	if files != nil {
		handler.files = files
	}
	if seasons != nil {
		handler.seasons = seasons
	}
	return handler
}

func serveItemRefresh(handler *AutoscanHandler, rawID string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Post("/Items/{id}/Refresh", handler.HandleItemRefresh)
	req := httptest.NewRequest(http.MethodPost, "/Items/"+rawID+"/Refresh?metadataRefreshMode=ValidationOnly&imageRefreshMode=None&replaceAllMetadata=false&replaceAllImages=false", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestItemRefreshScansMovieDirectory(t *testing.T) {
	root := t.TempDir()
	moviePath := writeMediaFile(t, root, "Movie (2020)", "Movie (2020).mkv")
	queue := &fakeAutoscanQueue{}
	files := &fakeItemRefreshFiles{byContentID: map[string][]*models.MediaFile{
		"movie-tmdb-100": {{ID: 1, ContentID: "movie-tmdb-100", FilePath: moviePath}},
	}}
	handler := newItemRefreshHandler(root, "movie", queue, files, nil)

	rec := serveItemRefresh(handler, handler.codec.EncodeStringID(EncodedIDItem, "movie-tmdb-100"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	want := []queuedScan{{libraryID: 7, mode: scantrigger.ModeSubtree, path: filepath.Dir(moviePath), trigger: itemRefreshTrigger}}
	assertQueuedScans(t, queue, want)
}

func TestItemRefreshScansEpisodeDirectory(t *testing.T) {
	root := t.TempDir()
	episodePath := writeMediaFile(t, root, "Show", "Season 01", "Show S01E02.mkv")
	queue := &fakeAutoscanQueue{}
	files := &fakeItemRefreshFiles{byEpisodeID: map[string][]*models.MediaFile{
		"episode-tvdb-200-1-2": {{ID: 2, ContentID: "series-tvdb-200", EpisodeID: "episode-tvdb-200-1-2", SeasonNumber: 1, EpisodeNumber: 2, FilePath: episodePath}},
	}}
	handler := newItemRefreshHandler(root, "tv", queue, files, nil)

	rec := serveItemRefresh(handler, handler.codec.EncodeStringID(EncodedIDItem, "episode-tvdb-200-1-2"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	want := []queuedScan{{libraryID: 7, mode: scantrigger.ModeSubtree, path: filepath.Dir(episodePath), trigger: itemRefreshTrigger}}
	assertQueuedScans(t, queue, want)
}

func TestItemRefreshScansEachDirectoryOfSeriesOnce(t *testing.T) {
	root := t.TempDir()
	s1e1 := writeMediaFile(t, root, "Show", "Season 01", "Show S01E01.mkv")
	s1e2 := writeMediaFile(t, root, "Show", "Season 01", "Show S01E02.mkv")
	s2e1 := writeMediaFile(t, root, "Show", "Season 02", "Show S02E01.mkv")
	queue := &fakeAutoscanQueue{}
	files := &fakeItemRefreshFiles{byContentID: map[string][]*models.MediaFile{
		"series-tvdb-200": {
			{ID: 1, ContentID: "series-tvdb-200", SeasonNumber: 1, FilePath: s1e1},
			{ID: 2, ContentID: "series-tvdb-200", SeasonNumber: 1, FilePath: s1e2},
			{ID: 3, ContentID: "series-tvdb-200", SeasonNumber: 2, FilePath: s2e1},
		},
	}}
	handler := newItemRefreshHandler(root, "tv", queue, files, nil)

	rec := serveItemRefresh(handler, handler.codec.EncodeStringID(EncodedIDItem, "series-tvdb-200"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	want := []queuedScan{
		{libraryID: 7, mode: scantrigger.ModeSubtree, path: filepath.Dir(s1e1), trigger: itemRefreshTrigger},
		{libraryID: 7, mode: scantrigger.ModeSubtree, path: filepath.Dir(s2e1), trigger: itemRefreshTrigger},
	}
	assertQueuedScans(t, queue, want)
}

func TestItemRefreshScansOnlyTheRequestedSeason(t *testing.T) {
	root := t.TempDir()
	s1e1 := writeMediaFile(t, root, "Show", "Season 01", "Show S01E01.mkv")
	s2e1 := writeMediaFile(t, root, "Show", "Season 02", "Show S02E01.mkv")
	queue := &fakeAutoscanQueue{}
	files := &fakeItemRefreshFiles{byContentID: map[string][]*models.MediaFile{
		"series-tvdb-200": {
			{ID: 1, ContentID: "series-tvdb-200", SeasonNumber: 1, FilePath: s1e1},
			{ID: 3, ContentID: "series-tvdb-200", SeasonNumber: 2, FilePath: s2e1},
		},
	}}
	seasons := &fakeItemRefreshSeasons{seasons: map[string]*models.Season{
		"season-tvdb-200-2": {ContentID: "season-tvdb-200-2", SeriesID: "series-tvdb-200", SeasonNumber: 2},
	}}
	handler := newItemRefreshHandler(root, "tv", queue, files, seasons)

	rec := serveItemRefresh(handler, handler.codec.EncodeStringID(EncodedIDSeason, "season-tvdb-200-2"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	want := []queuedScan{{libraryID: 7, mode: scantrigger.ModeSubtree, path: filepath.Dir(s2e1), trigger: itemRefreshTrigger}}
	assertQueuedScans(t, queue, want)
}

func TestItemRefreshScansLibrary(t *testing.T) {
	root := t.TempDir()
	queue := &fakeAutoscanQueue{}
	handler := newItemRefreshHandler(root, "movie", queue, nil, nil)

	rec := serveItemRefresh(handler, handler.codec.EncodeIntID(EncodedIDLibrary, 7))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	want := []queuedScan{{libraryID: 7, mode: scantrigger.ModeLibrary, path: "", trigger: itemRefreshTrigger}}
	assertQueuedScans(t, queue, want)
}

func TestItemRefreshFallsBackToParentForVanishedFile(t *testing.T) {
	root := t.TempDir()
	movieDir := filepath.Join(root, "Movie (2020)")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queue := &fakeAutoscanQueue{}
	files := &fakeItemRefreshFiles{byContentID: map[string][]*models.MediaFile{
		"movie-tmdb-100": {{ID: 1, ContentID: "movie-tmdb-100", FilePath: filepath.Join(movieDir, "Movie (2020).mkv")}},
	}}
	handler := newItemRefreshHandler(root, "movie", queue, files, nil)

	rec := serveItemRefresh(handler, handler.codec.EncodeStringID(EncodedIDItem, "movie-tmdb-100"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	want := []queuedScan{{libraryID: 7, mode: scantrigger.ModeSubtree, path: movieDir, trigger: itemRefreshTrigger}}
	assertQueuedScans(t, queue, want)
}

// TestItemRefreshUnscannableFilesReturnsConflict covers a movie whose file sat
// directly under the library root and has vanished: its parent fallback is a
// library-wide scan, which item refresh drops, so the handler must not claim
// success with nothing queued.
func TestItemRefreshUnscannableFilesReturnsConflict(t *testing.T) {
	root := t.TempDir()
	queue := &fakeAutoscanQueue{}
	files := &fakeItemRefreshFiles{byContentID: map[string][]*models.MediaFile{
		"movie-tmdb-100": {{ID: 1, ContentID: "movie-tmdb-100", FilePath: filepath.Join(root, "Movie (2020).mkv")}},
	}}
	handler := newItemRefreshHandler(root, "movie", queue, files, nil)

	rec := serveItemRefresh(handler, handler.codec.EncodeStringID(EncodedIDItem, "movie-tmdb-100"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(queue.calls) != 0 {
		t.Fatalf("expected no queued scans, got %#v", queue.calls)
	}
}

func TestItemRefreshUnknownItemReturnsNotFound(t *testing.T) {
	root := t.TempDir()
	queue := &fakeAutoscanQueue{}
	handler := newItemRefreshHandler(root, "movie", queue, &fakeItemRefreshFiles{}, &fakeItemRefreshSeasons{})

	for name, rawID := range map[string]string{
		"item without files": handler.codec.EncodeStringID(EncodedIDItem, "movie-tmdb-999"),
		"unknown season":     handler.codec.EncodeStringID(EncodedIDSeason, "season-tvdb-200-9"),
		"not an id":          "not-an-id",
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveItemRefresh(handler, rawID)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if len(queue.calls) != 0 {
		t.Fatalf("expected no queued scans, got %#v", queue.calls)
	}
}

func TestItemRefreshFileLookupErrorReturnsServerError(t *testing.T) {
	root := t.TempDir()
	queue := &fakeAutoscanQueue{}
	handler := newItemRefreshHandler(root, "movie", queue, &fakeItemRefreshFiles{err: errors.New("db down")}, nil)

	rec := serveItemRefresh(handler, handler.codec.EncodeStringID(EncodedIDItem, "movie-tmdb-100"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(queue.calls) != 0 {
		t.Fatalf("expected no queued scans, got %#v", queue.calls)
	}
}

// TestRouter_ItemRefreshRequiresAdminAPIKey proves the route is registered
// (Jellyfin clients previously got a bare 404) and sits behind the same
// admin-API-key check as POST /Library/Media/Updated, including through the
// /emby prefix that compat clients send.
func TestRouter_ItemRefreshRequiresAdminAPIKey(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{
		Config:           cfg,
		SessionStore:     NewSessionStore(time.Hour, time.Now),
		ContentService:   &genresContentService{},
		APIKeyValidator:  &fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_user"}},
		APIKeyUserLoader: &fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "user", Enabled: true}},
	})
	itemID := NewResourceIDCodec().EncodeStringID(EncodedIDItem, "movie-tmdb-100")

	cases := []struct {
		name  string
		path  string
		token string
		want  int
	}{
		{name: "no credentials", path: "/Items/" + itemID + "/Refresh", want: http.StatusUnauthorized},
		{name: "emby prefix", path: "/emby/Items/" + itemID + "/Refresh", want: http.StatusUnauthorized},
		{name: "non-admin key", path: "/Items/" + itemID + "/Refresh", token: "sa_user", want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path+"?metadataRefreshMode=ValidationOnly", nil)
			if tc.token != "" {
				req.Header.Set("X-Emby-Token", tc.token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func assertQueuedScans(t *testing.T, queue *fakeAutoscanQueue, want []queuedScan) {
	t.Helper()
	if len(queue.batches) != 1 {
		t.Fatalf("expected one batch enqueue, got %d", len(queue.batches))
	}
	if len(queue.calls) != len(want) {
		t.Fatalf("expected %d queued scans, got %#v", len(want), queue.calls)
	}
	for i := range want {
		if queue.calls[i] != want[i] {
			t.Fatalf("queued scan %d = %#v, want %#v", i, queue.calls[i], want[i])
		}
	}
}
