package pgstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// allowanceFixture is one account with a primary profile ("parent"), a
// PG-limited profile ("kid") and an unlimited one ("teen"), six R-rated titles
// in one library, an episode of the series, and three collections: a manual
// one the parent created (live), a smart one the parent created, and a manual
// one the teen created.
type allowanceFixture struct {
	pool    *pgxpool.Pool
	store   *PostgresUserStore
	allow   *TitleAllowances
	userID  int
	library int
	prefix  string
	episode string
	live    string
	smart   string
	teens   string
}

func (f allowanceFixture) id(suffix string) string { return f.prefix + "-" + suffix }

// manager is the primary profile writing at the PIN revision it was created
// with.
func (f allowanceFixture) manager() userstore.AllowanceManager {
	return userstore.AllowanceManager{UserID: f.userID, ProfileID: "parent"}
}

var allowanceFixtureTitles = map[string]string{
	"movie-allowed":    "movie",
	"movie-collection": "movie",
	"movie-smart":      "movie",
	"movie-other":      "movie",
	"movie-hidden":     "movie",
	"series-allowed":   "series",
	"audiobook-live":   "audiobook",
}

func seedAllowanceFixture(t *testing.T) allowanceFixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	f := allowanceFixture{pool: pool, prefix: fmt.Sprintf("allow-%d", time.Now().UnixNano())}
	f.userID = seedTestUser(t, pool, f.prefix)
	f.episode = f.id("episode")
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, f.prefix,
	).Scan(&f.library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, f.userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM episodes WHERE content_id LIKE $1`, f.prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, f.prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, f.library)
	})
	for suffix, kind := range allowanceFixtureTitles {
		mustExec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age)
			VALUES($1,$2,$1,'released','{}','R',18)`, f.id(suffix), kind)
		mustExec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, f.id(suffix), f.library)
	}
	mustExec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number) VALUES($1,$2,1,1)`,
		f.episode, f.id("series-allowed"))

	f.store = newStore(pool, f.userID)
	f.allow = NewTitleAllowances(pool)
	// The first profile created is the primary one.
	for _, p := range []userstore.Profile{
		{ID: "parent", Name: "Parent"},
		{ID: "kid", Name: "Kid", MaxContentRating: "PG"},
		{ID: "teen", Name: "Teen"},
	} {
		if err := f.store.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	collection := func(creator, kind, member string) string {
		t.Helper()
		c, err := f.store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: creator, Name: creator + kind + member, CollectionType: kind})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.AddCollectionItem(ctx, c.ID, f.id(member), 0); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	f.live = collection("parent", "manual", "movie-collection")
	// A live collection allows only the movies and series it holds.
	if err := f.store.AddCollectionItem(ctx, f.live, f.id("audiobook-live"), 1); err != nil {
		t.Fatal(err)
	}
	f.smart = collection("parent", "smart", "movie-smart")
	f.teens = collection("teen", "manual", "movie-other")
	return f
}

func (f allowanceFixture) policyRevision(t *testing.T) int64 {
	t.Helper()
	var r int64
	if err := f.pool.QueryRow(context.Background(), `SELECT access_policy_revision FROM users WHERE id = $1`, f.userID).Scan(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

// allowAll writes the kid's allowances: two titles and all three collections.
func (f allowanceFixture) allowAll(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"movie-allowed", "series-allowed"} {
		if changed, err := f.allow.SetTitleAllowance(ctx, f.manager(), "kid", f.id(id), true); err != nil || !changed {
			t.Fatalf("SetTitleAllowance(%s) = %v, %v", id, changed, err)
		}
	}
	if changed, err := f.allow.SetTitleAllowance(ctx, f.manager(), "kid", f.id("movie-allowed"), true); err != nil || changed {
		t.Fatalf("repeating an allowance = %v, %v; want no change", changed, err)
	}
	for _, id := range []string{f.live, f.smart, f.teens} {
		if _, err := f.allow.SetCollectionAllowance(ctx, f.manager(), "kid", id, true); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTitleAllowancesLiftProfileLimitsDB follows an allowance from its rows
// to what a limited profile sees: the profile read gathers direct allowances
// and the titles of live-allowed collections (a manual collection the primary
// profile created) and nothing from any other collection, and the catalog
// filter then admits an allowed movie and the episodes of an allowed series
// above the profile's ceiling while still hiding every other title above it.
// Skips unless SILO_TEST_DATABASE_URL names a migrated database.
func TestTitleAllowancesLiftProfileLimitsDB(t *testing.T) {
	f := seedAllowanceFixture(t)
	ctx := context.Background()

	before := f.policyRevision(t)
	f.allowAll(t)
	if got := f.policyRevision(t); got != before+5 {
		t.Fatalf("access_policy_revision = %d, want %d: each changing write advances it once and a repeat does not", got, before+5)
	}

	kid, err := f.store.GetProfile(ctx, "kid")
	if err != nil || kid == nil {
		t.Fatalf("GetProfile(kid) = %v, %v", kid, err)
	}
	want := []string{f.id("movie-allowed"), f.id("movie-collection"), f.id("series-allowed")}
	slices.Sort(want)
	if !slices.Equal(kid.AllowedTitleIDs, want) {
		t.Fatalf("AllowedTitleIDs = %v, want %v (no smart collection, no collection another profile created)", kid.AllowedTitleIDs, want)
	}

	t.Run("only the parent's manual collection is live", func(t *testing.T) {
		collections, err := f.allow.ListCollectionAllowances(ctx, f.userID, "kid")
		if err != nil {
			t.Fatal(err)
		}
		live := map[string]bool{}
		for _, c := range collections {
			live[c.CollectionID] = c.Live
		}
		if !live[f.live] || live[f.smart] || live[f.teens] || len(live) != 3 {
			t.Fatalf("collection allowances live = %v", live)
		}
	})

	t.Run("the catalog admits allowed titles and episodes only", func(t *testing.T) {
		limits := access.MaturityLimits{MaxContentRating: kid.MaxContentRating}.WithAllowedTitles(kid.AllowedTitleIDs)
		requested := []string{f.episode}
		for suffix := range allowanceFixtureTitles {
			requested = append(requested, f.id(suffix))
		}
		visible, err := catalog.NewLibraryItemRepository(f.pool).FilterAccessibleContentIDs(ctx, requested, []int{f.library}, nil, limits)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{f.id("movie-allowed"), f.id("movie-collection"), f.id("series-allowed"), f.episode} {
			if !visible[id] {
				t.Errorf("%s is allowed and must pass the PG ceiling", id)
			}
		}
		for _, suffix := range []string{"movie-smart", "movie-other", "movie-hidden", "audiobook-live"} {
			if visible[f.id(suffix)] {
				t.Errorf("%s is not allowed and must stay hidden", suffix)
			}
		}
	})

	t.Run("copying a collection's items allows its movies and series once", func(t *testing.T) {
		ids, err := f.allow.CollectionItemIDs(ctx, f.userID, f.live)
		if err != nil || len(ids) != 2 {
			t.Fatalf("CollectionItemIDs = %v, %v; want the movie and the audiobook", ids, err)
		}
		ids = append(ids, f.id("movie-smart"))
		// movie-collection is allowed through the collection but not as a
		// title, so it and movie-smart are new; the audiobook never is.
		for pass, want := range []int{2, 0} {
			added, err := f.allow.AllowTitles(ctx, f.manager(), "kid", ids)
			if err != nil || added != want {
				t.Fatalf("AllowTitles pass %d = %d, %v; want %d", pass, added, err, want)
			}
		}
	})

	t.Run("a write after the manager's PIN changed is refused", func(t *testing.T) {
		if _, err := f.pool.Exec(ctx, `UPDATE user_profiles SET pin_revision = pin_revision + 1 WHERE user_id = $1 AND id = 'parent'`, f.userID); err != nil {
			t.Fatal(err)
		}
		changed, err := f.allow.SetTitleAllowance(ctx, f.manager(), "kid", f.id("movie-hidden"), true)
		if !errors.Is(err, userstore.ErrAllowanceManagerChanged) || changed {
			t.Fatalf("SetTitleAllowance with a stale PIN revision = %v, %v; want ErrAllowanceManagerChanged", changed, err)
		}
		var rows int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM profile_title_allowances WHERE user_id = $1 AND content_id = $2`, f.userID, f.id("movie-hidden")).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("refused write left %d rows (%v)", rows, err)
		}
	})

	t.Run("deleting the profile removes its allowances", func(t *testing.T) {
		if err := f.store.DeleteProfile(ctx, "kid"); err != nil {
			t.Fatal(err)
		}
		var left int
		if err := f.pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM profile_title_allowances WHERE user_id = $1)
			     + (SELECT count(*) FROM profile_collection_allowances WHERE user_id = $1)`, f.userID).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Fatalf("%d allowance rows outlived their profile", left)
		}
	})
}
