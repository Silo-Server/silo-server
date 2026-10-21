package catalog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// statementCountTracer counts the statements a pool sends.
type statementCountTracer struct{ statements atomic.Int64 }

func (t *statementCountTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	t.statements.Add(1)
	return ctx
}

func (*statementCountTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestPersonalCollectionCollageListReadsSkipSmartQueriesDB pins what a list
// read of smart collections costs: it serves each viewer's stored collage
// with one statement however many smart collections are listed, and never
// runs their queries. Their sources are read in the background, when a
// collection has no collage for the viewer yet or its collage is older than
// SmartRefreshInterval. Set SILO_TEST_DATABASE_URL to a migrated database to
// run it.
func TestPersonalCollectionCollageListReadsSkipSmartQueriesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	tracer := &statementCountTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("smart-collage-reads-%d", time.Now().UnixNano())
	library := seedCollagePosterLibrary(t, pool, prefix)
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	profile := prefix + "-owner"
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'owner')`, profile, userID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	genre := prefix + "-genre"
	poster := func(name string) string { return fmt.Sprintf("test/%s-%s/poster/original.webp", prefix, name) }
	addTitle := func(name string) {
		t.Helper()
		contentID := prefix + "-" + name
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, sort_title, genres, poster_path)
			VALUES ($1, 'movie', $2, $2, ARRAY[$3], $4)
		`, contentID, name, genre, poster(name)); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		})
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, library); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
	}
	for _, name := range []string{"b", "c", "d", "e"} {
		addTitle(name)
	}

	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM user_personal_collections WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "collection-images/%"+prefix+"%")
	})
	// Each collection has its own definition, so none shares another's read.
	const smartCount = 8
	smart := make([]PersonalCollectionDefinition, 0, smartCount)
	for i := range smartCount {
		query := fmt.Sprintf(`{"match":"any","groups":[{"match":"any","rules":[{"field":"genre","op":"is","value":%q},{"field":"genre","op":"is","value":"%s-unused-%d"}]}],"sort":{"field":"title","order":"asc"}}`, genre, prefix, i)
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: profile, Name: fmt.Sprintf("Smart %d", i), CollectionType: "smart", QueryDefinition: query})
		if err != nil {
			t.Fatalf("create smart collection: %v", err)
		}
		smart = append(smart, PersonalCollectionDefinition{ID: c.ID, CollectionType: "smart", QueryDefinition: query})
	}
	viewer := AccessFilter{UserID: userID, ProfileID: profile}
	gen := &fakeCollageGenerator{}
	collages := NewPersonalCollectionCollages(pool, gen)
	for _, c := range smart {
		if err := prepareCollage(ctx, collages, userID, c, viewer); err != nil {
			t.Fatalf("prepare %s: %v", c.ID, err)
		}
	}
	first := CollectionCollageKey([]string{poster("b"), poster("c"), poster("d"), poster("e")})

	t.Run("a list read serves stored collages with one statement", func(t *testing.T) {
		before := tracer.statements.Load()
		posters := collages.Posters(ctx, userID, smart, viewer)
		if got := tracer.statements.Load() - before; got != 1 {
			t.Fatalf("listing %d smart collections sent %d statements, want 1", smartCount, got)
		}
		for _, c := range smart {
			if posters[c.ID].CollageKey != first {
				t.Fatalf("poster of %s = %+v, want collage %s", c.ID, posters[c.ID], first)
			}
		}
	})

	t.Run("a new match reaches the collage in the background once it is due", func(t *testing.T) {
		addTitle("a")
		if got := collages.Posters(ctx, userID, smart[:1], viewer)[smart[0].ID]; got.CollageKey != first {
			t.Fatalf("a read before the refresh is due served %+v, want the stored collage %s", got, first)
		}
		collages.SmartRefreshInterval = 0
		want := CollectionCollageKey([]string{poster("a"), poster("b"), poster("c"), poster("d")})
		deadline := time.Now().Add(10 * time.Second)
		for collages.Posters(ctx, userID, smart[:1], viewer)[smart[0].ID].CollageKey != want {
			if time.Now().After(deadline) {
				t.Fatalf("the collage never moved to %s", want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("a changed definition does not serve the old definition's collage", func(t *testing.T) {
		changed := smart[1]
		changed.QueryDefinition = strings.Replace(changed.QueryDefinition, `"asc"`, `"desc"`, 1)
		if got, ok := collages.Posters(ctx, userID, []PersonalCollectionDefinition{changed}, viewer)[changed.ID]; ok {
			t.Fatalf("changed definition served %+v before its collage was built", got)
		}
	})
}

// prepareCollage builds, in the foreground, the collage the viewer described
// by access sees for c, unless it is already stored.
func prepareCollage(ctx context.Context, p *PersonalCollectionCollages, userID int, c PersonalCollectionDefinition, access AccessFilter) error {
	if !p.enabled() {
		return nil
	}
	if IsLiveQueryType(c.CollectionType) {
		return p.refreshSmart(ctx, userID, c, access)
	}
	sources, err := p.ListSources(ctx, userID, []PersonalCollectionDefinition{c}, access)
	if err != nil {
		return err
	}
	return p.collageSet(userID).prepare(ctx, c.ID, sources[c.ID])
}
