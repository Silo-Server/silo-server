package jellycompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type detailBatchTracer struct {
	mu         sync.Mutex
	statements int
}

func (c *detailBatchTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.statements++
	c.mu.Unlock()
	return ctx
}

func (*detailBatchTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *detailBatchTracer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statements
}

// detailCallCounter counts the detail lookups a handler makes.
type detailCallCounter struct {
	ContentService
	batch, perItem int
}

func (c *detailCallCounter) GetItemDetail(ctx context.Context, session *Session, contentID string, libraryID *int) (*upstreamItemDetail, error) {
	c.perItem++
	return c.ContentService.GetItemDetail(ctx, session, contentID, libraryID)
}

func (c *detailCallCounter) GetItemDetailsByIDs(ctx context.Context, session *Session, contentIDs []string, libraryID *int) (map[string]*upstreamItemDetail, error) {
	c.batch++
	return c.ContentService.GetItemDetailsByIDs(ctx, session, contentIDs, libraryID)
}

// perItemDetails fails every batch, so handlers fetch each detail with
// GetItemDetail: the way NextUp and Ids lookups did before they batched.
type perItemDetails struct{ ContentService }

func (perItemDetails) GetItemDetailsByIDs(context.Context, *Session, []string, *int) (map[string]*upstreamItemDetail, error) {
	return nil, errors.New("batch disabled")
}

// detailBatchFixture serves one viewer's library of four series and three
// movies twice over the production catalog: batched through the real
// content service, and per item through the same service with its batch
// disabled.
type detailBatchFixture struct {
	tracer             *detailBatchTracer
	token              string
	batched, perItem   *detailCallCounter
	batchedH, perItemH http.Handler
	requests           []detailBatchRequest
}

type detailBatchRequest struct {
	name, path   string
	wantPerItem  int // per-item lookups the batched path still makes
	budget       int // statements the batched path may issue
	wantContains []string
	wantAbsent   []string
}

func newDetailBatchFixture(tb testing.TB) *detailBatchFixture {
	tb.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		tb.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		tb.Fatalf("parse test database url: %v", err)
	}
	tracer := &detailBatchTracer{}
	poolConfig.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		tb.Fatalf("connect test database: %v", err)
	}
	tb.Cleanup(pool.Close)

	prefix := fmt.Sprintf("jf-detail-batch-%d-", time.Now().UnixNano())
	userID, movieLibrary := seedDetailBatchLibrary(tb, pool, prefix)
	users := pgstore.NewPostgresProvider(pool)
	engine, err := policy.NewEngine(ctx)
	if err != nil {
		tb.Fatalf("NewEngine: %v", err)
	}
	accessFilter := NewScopeAccessFilter(policy.NewViewerResolver(auth.NewUserRepository(pool), users, nil, policy.NewPDP(engine), access.NewGroupStore(pool)))
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		tb.Fatalf("LoadFromDB: %v", err)
	}
	itemRepo, episodeRepo, seasonRepo := catalog.NewItemRepository(pool), catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool)
	browseRepo, personRepo, folderRepo := catalog.NewBrowseRepository(pool), catalog.NewPersonRepository(pool), catalog.NewFolderRepository(pool)
	detailSvc := catalog.NewDetailService(itemRepo, episodeRepo, seasonRepo, personRepo, scanner.NewFileRepository(pool))
	content := newDirectContentService(browseRepo, itemRepo, seasonRepo, episodeRepo, detailSvc, folderRepo, users, compatAccessFilterResolver(accessFilter), nil)
	content.catalogUserState = cfg.UserDB.Backend != sqliteUserStoreBackend

	codec := NewResourceIDCodec()
	session := Session{Token: prefix + "token", ProfileID: "profile-1", PseudoUserID: PseudoUserID(userID, "profile-1"), StreamAppUserID: userID}
	f := &detailBatchFixture{tracer: tracer, token: session.Token, batched: &detailCallCounter{ContentService: content}, perItem: &detailCallCounter{ContentService: perItemDetails{content}}}
	router := func(content ContentService) http.Handler {
		sessions := NewSessionStore(time.Hour, time.Now)
		if err := sessions.Put(session); err != nil {
			tb.Fatalf("Put session: %v", err)
		}
		return NewRouter(Dependencies{
			Config: cfg, DB: pool, SessionStore: sessions, IDCodec: codec, ContentService: content,
			BrowseRepo: browseRepo, ItemRepo: itemRepo, SeasonRepo: seasonRepo, EpisodeRepo: episodeRepo, PersonRepo: personRepo,
			DetailSvc: detailSvc, FolderRepo: folderRepo, UserStoreProvider: users, AccessFilterFn: accessFilter,
		})
	}
	f.batchedH, f.perItemH = router(f.batched), router(f.perItem)

	ids := make([]string, 0, 7)
	for _, id := range []string{"movie-0", "series-0-season", "series-1-episode-2", "missing", "series-2", "movie-1", "movie-2"} {
		ids = append(ids, codec.EncodeStringID(EncodedIDItem, prefix+id))
	}
	idsPath := "/Users/" + session.PseudoUserID.String() + "/Items?Ids=" + url.QueryEscape(strings.Join(ids, ",")) + "&Fields=MediaSources,People"
	f.requests = []detailBatchRequest{
		{
			name:         "next up with media sources",
			path:         "/Shows/NextUp?UserId=" + session.PseudoUserID.String() + "&Fields=MediaSources,People&Limit=20",
			budget:       45,
			wantContains: []string{"Episode 2 of series 0", "Episode 2 of series 3"},
		},
		{
			name:         "ids with media sources",
			path:         idsPath,
			wantPerItem:  2, // the season and the missing id
			budget:       53,
			wantContains: []string{"Movie 0", "Season 1", "Episode 2 of series 1", "Series 2", "Movie 2", "Lead Actor"},
		},
		{
			name:         "ids in one library",
			path:         idsPath + "&ParentId=" + codec.EncodeIntID(EncodedIDLibrary, int64(movieLibrary)),
			wantPerItem:  4, // everything outside the movie library, all 404
			budget:       62,
			wantContains: []string{"Movie 0", "Movie 2"},
			wantAbsent:   []string{"Season 1", "Episode 2 of series 1", "Series 2"},
		},
	}
	return f
}

// seedDetailBatchLibrary stores prefix's user and profile, a series library
// holding four series of two episodes each with the first one watched, and a
// movie library holding three movies, all with artwork and two credits, and
// returns the user's and the movie library's ids.
func seedDetailBatchLibrary(tb testing.TB, pool *pgxpool.Pool, prefix string) (int, int) {
	tb.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		tb.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			tb.Fatalf("exec %q: %v", sql, err)
		}
	}
	var userID, seriesLibrary, movieLibrary int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`, prefix+"user", prefix+"user@example.test").Scan(&userID); err != nil {
		tb.Fatalf("seed user: %v", err)
	}
	tb.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	for _, library := range []struct {
		kind string
		id   *int
	}{{"series", &seriesLibrary}, {"movies", &movieLibrary}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, TRUE) RETURNING id`, library.kind, prefix+library.kind).Scan(library.id); err != nil {
			tb.Fatalf("seed library: %v", err)
		}
	}
	tb.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id = ANY($1)`, []int{seriesLibrary, movieLibrary})
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int{seriesLibrary, movieLibrary})
		_, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE name LIKE $1`, prefix+"%")
	})
	people := [2]int64{}
	for i, name := range []string{"Lead Actor", "Director"} {
		people[i] = time.Now().UnixNano() + int64(i)
		exec(`INSERT INTO people (id, name, photo_path) VALUES ($1, $2, $3)`, people[i], prefix+name, "tmdb/people/"+name+"/original.jpg")
	}
	// artworkAndCredits gives an item a poster, a backdrop, and both people,
	// so detail responses carry image tags and People.
	artworkAndCredits := func(contentID string) {
		exec(`UPDATE media_items SET poster_path = 'local/' || $1 || '/aa/poster/original.r.webp', backdrop_path = 'local/' || $1 || '/bb/backdrop/original.r.webp' WHERE content_id = $1`, contentID)
		exec(`INSERT INTO item_people (id, person_id, content_id, kind, sort_order) VALUES ($1, $2, $3, $4, 0), ($1 + 1, $5, $3, $6, 1)`,
			time.Now().UnixNano(), people[0], contentID, int16(models.PersonKindActor), people[1], int16(models.PersonKindDirector))
	}
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		tb.Fatalf("ForUser: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-1", Name: "Viewer"}); err != nil {
		tb.Fatalf("CreateProfile: %v", err)
	}
	seasons, episodes, files := catalog.NewSeasonRepository(pool), catalog.NewEpisodeRepository(pool), scanner.NewFileRepository(pool)
	addFile := func(file models.MediaFile) {
		tb.Helper()
		file.Container, file.FileSize = "mkv", 1<<30
		if _, err := files.Upsert(ctx, file); err != nil {
			tb.Fatalf("seed file: %v", err)
		}
	}
	watchedAt := time.Now().Add(-time.Hour)
	for i := range 4 {
		series := fmt.Sprintf("%sseries-%d", prefix, i)
		exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', $2)`, series, fmt.Sprintf("Series %d", i))
		exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, series, seriesLibrary)
		artworkAndCredits(series)
		if err := seasons.Upsert(ctx, &models.Season{ContentID: series + "-season", SeriesID: series, SeasonNumber: 1, Title: "Season 1"}); err != nil {
			tb.Fatalf("seed season: %v", err)
		}
		for number := 1; number <= 2; number++ {
			episode := fmt.Sprintf("%s-episode-%d", series, number)
			if err := episodes.Upsert(ctx, &models.Episode{ContentID: episode, SeriesID: series, SeasonID: series + "-season", SeasonNumber: 1, EpisodeNumber: number, Title: fmt.Sprintf("Episode %d of series %d", number, i)}); err != nil {
				tb.Fatalf("seed episode: %v", err)
			}
			exec(`INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $2)`, episode, seriesLibrary)
			addFile(models.MediaFile{EpisodeID: episode, MediaFolderID: seriesLibrary, FilePath: "/" + episode + ".mkv", Duration: 1500})
		}
		exec(`INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, completed, updated_at) VALUES ($1, 'profile-1', $2, 0, TRUE, $3)`,
			userID, series+"-episode-1", watchedAt.Add(time.Duration(i)*time.Minute))
	}
	for i := range 3 {
		movie := fmt.Sprintf("%smovie-%d", prefix, i)
		exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $2)`, movie, fmt.Sprintf("Movie %d", i))
		exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, movie, movieLibrary)
		artworkAndCredits(movie)
		addFile(models.MediaFile{ContentID: movie, MediaFolderID: movieLibrary, FilePath: "/" + movie + ".mkv", Duration: 6000})
	}
	exec(`INSERT INTO media_item_backdrops (content_id, ordinal, path, source_path) VALUES ($1, 1, 'local/' || $1 || '/cc/backdrop/original.r1.webp', 'file:///media/fanart-1.jpg')`, prefix+"movie-0")
	return userID, movieLibrary
}

func (f *detailBatchFixture) serve(tb testing.TB, h http.Handler, path string) []byte {
	tb.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Emby-Token", f.token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		tb.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// TestCompatDetailBatchesMatchPerItemDB pins a NextUp page that asks for
// detail fields, and Ids lists, to one batched detail fetch with per-item
// lookups only for what the batch leaves out, and to a statement budget. Each
// response must be byte for byte the one per-item lookups build.
func TestCompatDetailBatchesMatchPerItemDB(t *testing.T) {
	f := newDetailBatchFixture(t)
	for _, rq := range f.requests {
		before := f.tracer.count()
		batched := f.serve(t, f.batchedH, rq.path)
		batchedStatements := f.tracer.count() - before
		before = f.tracer.count()
		perItem := f.serve(t, f.perItemH, rq.path)
		perItemStatements := f.tracer.count() - before
		t.Logf("%s: %d statements batched, %d per item", rq.name, batchedStatements, perItemStatements)

		for _, want := range rq.wantContains {
			if !bytes.Contains(batched, []byte(want)) {
				t.Errorf("%s: response lacks %q: %s", rq.name, want, batched)
			}
		}
		for _, absent := range rq.wantAbsent {
			if bytes.Contains(batched, []byte(absent)) {
				t.Errorf("%s: response has %q: %s", rq.name, absent, batched)
			}
		}
		if !bytes.Equal(batched, perItem) {
			t.Errorf("%s: batched response differs from per-item:\nbatched:  %s\nper item: %s", rq.name, batched, perItem)
		}
		if f.batched.batch != 1 || f.batched.perItem != rq.wantPerItem {
			t.Errorf("%s: %d batch and %d per-item lookups, want 1 and %d", rq.name, f.batched.batch, f.batched.perItem, rq.wantPerItem)
		}
		if batchedStatements > rq.budget || batchedStatements >= perItemStatements {
			t.Errorf("%s: batched path issued %d statements (budget %d), per-item %d", rq.name, batchedStatements, rq.budget, perItemStatements)
		}
		f.batched.batch, f.batched.perItem = 0, 0
	}
}

// BenchmarkCompatDetailBatchesDB times the pinned requests both ways against
// a local database: go test -run '^$' -bench CompatDetailBatches ./internal/jellycompat/
func BenchmarkCompatDetailBatchesDB(b *testing.B) {
	f := newDetailBatchFixture(b)
	// Every request logs a line; keep that I/O out of the timings.
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(logger) })
	for _, rq := range f.requests {
		for _, path := range []struct {
			name string
			h    http.Handler
		}{{"batched", f.batchedH}, {"per item", f.perItemH}} {
			b.Run(rq.name+"/"+path.name, func(b *testing.B) {
				before := f.tracer.count()
				for b.Loop() {
					f.serve(b, path.h, rq.path)
				}
				b.ReportMetric(float64(f.tracer.count()-before)/float64(b.N), "statements/op")
			})
		}
	}
}
