package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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

type compatStatementCounter struct {
	mu         sync.Mutex
	statements int
}

func (c *compatStatementCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.statements++
	c.mu.Unlock()
	return ctx
}

func (*compatStatementCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *compatStatementCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statements
}

// statementCountingScopeResolver counts resolutions and the statements they
// issue.
type statementCountingScopeResolver struct {
	next       ScopeResolver
	counter    *compatStatementCounter
	calls      int
	statements int
}

func (r *statementCountingScopeResolver) Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error) {
	before := r.counter.count()
	scope, err := r.next.Resolve(ctx, input)
	r.calls++
	r.statements += r.counter.count() - before
	return scope, err
}

// scopeOnceFixture is a series with one season and one episode in a library
// the viewer may read, served by a compat router over the production
// catalog and viewer resolver.
type scopeOnceFixture struct {
	router   http.Handler
	token    string
	counter  *compatStatementCounter
	resolver *statementCountingScopeResolver
	requests []scopeOnceRequest
}

type scopeOnceRequest struct {
	name, path string
	want       func(body []byte) bool
}

func newScopeOnceFixture(tb testing.TB) *scopeOnceFixture {
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
	counter := &compatStatementCounter{}
	poolConfig.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		tb.Fatalf("connect test database: %v", err)
	}
	tb.Cleanup(pool.Close)
	prefix := fmt.Sprintf("jf-scope-once-%d-", time.Now().UnixNano())
	series, season, episode := prefix+"series", prefix+"season", prefix+"episode"
	userID := seedScopeOnceSeries(tb, pool, prefix)
	itemRepo, episodeRepo, seasonRepo := catalog.NewItemRepository(pool), catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool)

	users := pgstore.NewPostgresProvider(pool)
	store, err := users.ForUser(ctx, userID)
	if err != nil {
		tb.Fatalf("ForUser: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-1", Name: "Viewer"}); err != nil {
		tb.Fatalf("CreateProfile: %v", err)
	}
	engine, err := policy.NewEngine(ctx)
	if err != nil {
		tb.Fatalf("NewEngine: %v", err)
	}
	resolver := &statementCountingScopeResolver{
		next:    policy.NewViewerResolver(auth.NewUserRepository(pool), users, nil, policy.NewPDP(engine), access.NewGroupStore(pool)),
		counter: counter,
	}
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		tb.Fatalf("LoadFromDB: %v", err)
	}
	sessions := NewSessionStore(time.Hour, time.Now)
	session := Session{Token: prefix + "token", ProfileID: "profile-1", PseudoUserID: PseudoUserID(userID, "profile-1"), StreamAppUserID: userID}
	if err := sessions.Put(session); err != nil {
		tb.Fatalf("Put session: %v", err)
	}
	personRepo := catalog.NewPersonRepository(pool)
	codec := NewResourceIDCodec()
	router := NewRouter(Dependencies{
		Config:                cfg,
		SessionStore:          sessions,
		IDCodec:               codec,
		BrowseRepo:            catalog.NewBrowseRepository(pool),
		ItemRepo:              itemRepo,
		SeasonRepo:            seasonRepo,
		EpisodeRepo:           episodeRepo,
		PersonRepo:            personRepo,
		DetailSvc:             catalog.NewDetailService(itemRepo, episodeRepo, seasonRepo, personRepo, scanner.NewFileRepository(pool)),
		FolderRepo:            catalog.NewFolderRepository(pool),
		UserStoreProvider:     users,
		AccessFilterFn:        NewScopeAccessFilter(resolver),
		PlaybackScopeResolver: resolver,
	})

	usersPath := "/Users/" + session.PseudoUserID.String()
	encodedSeries := codec.EncodeStringID(EncodedIDItem, series)
	return &scopeOnceFixture{router: router, token: session.Token, counter: counter, resolver: resolver, requests: []scopeOnceRequest{
		{
			name: "episode detail",
			path: usersPath + "/Items/" + codec.EncodeStringID(EncodedIDItem, episode),
			want: func(body []byte) bool {
				var dto baseItemDTO
				return json.Unmarshal(body, &dto) == nil && dto.Type == "Episode" && dto.SeriesID == encodedSeries && dto.SeasonID == codec.EncodeStringID(EncodedIDSeason, season)
			},
		},
		{
			name: "episode list with media sources",
			path: usersPath + "/Items?ParentId=" + encodedSeries + "&IncludeItemTypes=Episode&Recursive=true&Fields=MediaSources,MediaSourceCount",
			want: func(body []byte) bool {
				var page queryResultDTO
				return json.Unmarshal(body, &page) == nil && len(page.Items) == 1 && page.Items[0].Name == "Pilot"
			},
		},
	}}
}

// seedScopeOnceSeries stores prefix's user, its series library, and a
// series with one season and one episode, and returns the user's id.
func seedScopeOnceSeries(tb testing.TB, pool *pgxpool.Pool, prefix string) int {
	tb.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		tb.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			tb.Fatalf("exec %q: %v", sql, err)
		}
	}
	series, season, episode := prefix+"series", prefix+"season", prefix+"episode"
	var userID, libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`, prefix+"user", prefix+"user@example.test").Scan(&userID); err != nil {
		tb.Fatalf("seed user: %v", err)
	}
	tb.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, TRUE) RETURNING id`, prefix+"library").Scan(&libraryID); err != nil {
		tb.Fatalf("seed library: %v", err)
	}
	tb.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})
	exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Series')`, series)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, series, libraryID)
	if err := catalog.NewSeasonRepository(pool).Upsert(ctx, &models.Season{ContentID: season, SeriesID: series, SeasonNumber: 1, Title: "Season 1"}); err != nil {
		tb.Fatalf("seed season: %v", err)
	}
	if err := catalog.NewEpisodeRepository(pool).Upsert(ctx, &models.Episode{ContentID: episode, SeriesID: series, SeasonID: season, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"}); err != nil {
		tb.Fatalf("seed episode: %v", err)
	}
	exec(`INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $2)`, episode, libraryID)
	return userID
}

func (f *scopeOnceFixture) serve(tb testing.TB, rq scopeOnceRequest) {
	tb.Helper()
	req := httptest.NewRequest(http.MethodGet, rq.path, nil)
	req.Header.Set("X-Emby-Token", f.token)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !rq.want(rec.Body.Bytes()) {
		tb.Fatalf("%s: status %d: %s", rq.name, rec.Code, rec.Body.String())
	}
}

// TestCompatEpisodeItemResolvesScopeOnceDB pins what a Jellyfin request costs
// in access scope resolutions. An episode's detail reads the viewer's scope
// for the episode, its series' artwork, and its season; an episode list with
// media sources reads it for the browse, the episode targets, the details,
// and the file fields. Each request resolves it once, through the production
// viewer resolver.
func TestCompatEpisodeItemResolvesScopeOnceDB(t *testing.T) {
	f := newScopeOnceFixture(t)
	for _, rq := range f.requests {
		before, resolvesBefore := f.counter.count(), f.resolver.calls
		f.resolver.statements = 0
		f.serve(t, rq)
		resolves := f.resolver.calls - resolvesBefore
		t.Logf("%s: %d statements, %d scope resolutions issuing %d of them", rq.name, f.counter.count()-before, resolves, f.resolver.statements)
		if resolves != 1 {
			t.Errorf("%s resolved the access scope %d times, want 1", rq.name, resolves)
		}
	}
}

// BenchmarkCompatScopeOnceRequestsDB times the pinned requests against a
// local database: go test -run '^$' -bench CompatScopeOnce ./internal/jellycompat/
func BenchmarkCompatScopeOnceRequestsDB(b *testing.B) {
	f := newScopeOnceFixture(b)
	// Every request logs a line; keep that I/O out of the timings.
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(logger) })
	for _, rq := range f.requests {
		b.Run(rq.name, func(b *testing.B) {
			before := f.counter.count()
			for b.Loop() {
				f.serve(b, rq)
			}
			b.ReportMetric(float64(f.counter.count()-before)/float64(b.N), "statements/op")
		})
	}
}
