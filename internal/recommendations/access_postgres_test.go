package recommendations

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// accessFixture is a catalog of two libraries: kids titles rated G and adult
// titles rated R. Every title is an embedded movie near one axis, the adult
// titles nearer the anchor than any kids title, so an unrestricted ranking
// fills its first places with adult titles.
type accessFixture struct {
	anchor           string
	kids, adults     []string
	kidsLib, adultLb int
	axis             int
}

func newAccessFixture(t *testing.T, pool *pgxpool.Pool, prefix string, axis int) accessFixture {
	t.Helper()
	ctx := t.Context()
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	f := accessFixture{anchor: prefix + "anchor", axis: axis}
	folder := func(name string) int {
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix+name).Scan(&id); err != nil {
			t.Fatalf("insert library: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, id) })
		return id
	}
	f.kidsLib, f.adultLb = folder("kids"), folder("adults")

	seed := func(id string, library int, rating string, age int, distance float32) {
		seedRecoMediaItem(t, pool, id, "movie", "matched")
		if _, err := pool.Exec(ctx, `UPDATE media_items SET content_rating = $2, content_rating_age = $3, poster_path = '/poster.jpg' WHERE content_id = $1`, id, rating, age); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpsertEmbedding(ctx, id, axisVector(axis, map[int]float32{axis + 1: distance}), "test-model", id); err != nil {
			t.Fatal(err)
		}
	}
	seed(f.anchor, f.kidsLib, "G", 0, 0)
	for i := range 12 {
		id := fmt.Sprintf("%sadult-%02d", prefix, i)
		seed(id, f.adultLb, "R", 17, 0.01+float32(i)*0.001)
		f.adults = append(f.adults, id)
	}
	for i := range 8 {
		id := fmt.Sprintf("%skids-%02d", prefix, i)
		seed(id, f.kidsLib, "G", 0, 0.2+float32(i)*0.01)
		f.kids = append(f.kids, id)
	}
	return f
}

// prefixed keeps the IDs that belong to this test, whatever else the shared
// test database holds.
func prefixed(items []ScoredItem, prefix string) []string {
	var ids []string
	for _, item := range items {
		if strings.HasPrefix(item.MediaItemID, prefix) {
			ids = append(ids, item.MediaItemID)
		}
	}
	return ids
}

// A restricted viewer's similar titles are chosen among the titles it can
// see, so its rows are full rather than filtered down after the fact.
func TestFindSimilarAppliesAccessLimitsInSQLPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tfindsim-access-"
	f := newAccessFixture(t, pool, prefix, 2300)
	repo := NewRepo(pool)
	embedding := axisVector(f.axis, nil)

	open, err := repo.FindSimilar(ctx, embedding, []string{f.anchor}, "movie", 5, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := prefixed(open, prefix); len(got) != 5 || !slices.Equal(got, f.adults[:5]) {
		t.Fatalf("unrestricted = %v, want the five nearest adult titles", got)
	}

	for name, filter := range map[string]catalog.AccessFilter{
		"allowed library": {AllowedLibraryIDs: []int{f.kidsLib}},
		"hidden library":  {DisabledLibraryIDs: []int{f.adultLb}},
		"rating ceiling":  {MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}},
	} {
		items, err := repo.FindSimilar(ctx, embedding, []string{f.anchor}, "movie", 5, filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := prefixed(items, prefix)
		if !slices.Equal(got, f.kids[:5]) {
			t.Fatalf("%s: similar = %v, want the five nearest kids titles", name, got)
		}
	}

	none, err := repo.FindSimilar(ctx, embedding, nil, "movie", 5, catalog.AccessFilter{AllowedLibraryIDs: []int{}})
	if err != nil || len(none) != 0 {
		t.Fatalf("no allowed library: %v, %v; want nothing", none, err)
	}

	// Because You Watched ranks the same way, so the restricted row is full.
	engine := NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, nil, config.RecommendationsConfig{DiversityLambda: 1})
	row, err := engine.becauseYouWatched(ctx, f.anchor, 5, map[string]struct{}{}, catalog.AccessFilter{AllowedLibraryIDs: []int{f.kidsLib}})
	if err != nil {
		t.Fatal(err)
	}
	if got := prefixed(row, prefix); len(got) != 5 || slices.ContainsFunc(got, func(id string) bool { return slices.Contains(f.adults, id) }) {
		t.Fatalf("because you watched = %v, want five kids titles", got)
	}
}

// The taste-seed picker pages only titles the profile can pick.
func TestTasteSeedCandidatesHonourAccessPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttasteseed-access-"
	f := newAccessFixture(t, pool, prefix, 2350)
	repo := NewRepo(pool)
	kids := append([]string{f.anchor}, f.kids...)

	page, err := repo.GetTasteSeedCandidates(ctx, catalog.AccessFilter{AllowedLibraryIDs: []int{f.kidsLib}}, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 5 || slices.ContainsFunc(page, func(id string) bool { return !slices.Contains(kids, id) }) {
		t.Fatalf("allowed-library page = %v, want a full page of kids titles", page)
	}

	for name, filter := range map[string]catalog.AccessFilter{
		"hidden library": {DisabledLibraryIDs: []int{f.adultLb}},
		"G/PG ceiling":   {MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}},
	} {
		all, err := repo.GetTasteSeedCandidates(ctx, filter, 100000, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, id := range f.adults {
			if slices.Contains(all, id) {
				t.Fatalf("%s: picker offers %s", name, id)
			}
		}
		for _, id := range kids {
			if !slices.Contains(all, id) {
				t.Fatalf("%s: picker is missing %s", name, id)
			}
		}
	}

	none, err := repo.GetTasteSeedCandidates(ctx, catalog.AccessFilter{AllowedLibraryIDs: []int{}}, 5, 0)
	if err != nil || len(none) != 0 {
		t.Fatalf("no allowed library: %v, %v; want nothing", none, err)
	}
}

// Rows are built under the scope the API resolves: an account in an access
// group limited to the kids library gets the kids library only, which the
// profile's own settings do not show.
func TestProfileAccessFilterMatchesTheResolverPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tscope-group-"
	f := newAccessFixture(t, pool, prefix, 2400)
	userID, profileID := newTasteTestAccount(t, pool, prefix)

	groups := access.NewGroupStore(pool)
	group, err := groups.Create(ctx, access.CreateGroupInput{Name: prefix + time.Now().Format(time.RFC3339Nano), LibraryIDs: []int{f.kidsLib}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM access_groups WHERE id = $1`, group.ID) })
	if _, err := pool.Exec(ctx, `UPDATE users SET access_group_id = $2 WHERE id = $1`, userID, group.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_profiles SET max_content_rating = 'PG' WHERE user_id = $1 AND id = $2`, userID, profileID); err != nil {
		t.Fatal(err)
	}

	provider := pgstore.NewPostgresProvider(pool)
	account := &models.User{ID: userID, Role: models.RoleUser, AccessGroupID: &group.ID}
	resolver := access.NewResolver(fixedUsers{account}, provider, nil, groups)
	engine := NewEngine(pool, nil, nil, nil, provider, config.RecommendationsConfig{})

	withoutResolver, err := engine.profileAccessFilter(ctx, userID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	if withoutResolver.AllowedLibraryIDs != nil {
		t.Fatalf("profile-only filter = %v; the access group is invisible to it", withoutResolver.AllowedLibraryIDs)
	}

	filter, err := engine.WithScopeResolver(resolver).profileAccessFilter(ctx, userID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := resolver.Resolve(ctx, access.ResolveInput{UserID: userID, ProfileID: profileID, SkipPINVerification: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(filter.AllowedLibraryIDs, []int{f.kidsLib}) || !slices.Equal(filter.AllowedLibraryIDs, scope.AllowedLibraryIDs) {
		t.Fatalf("build filter libraries = %v, scope = %v; want the group's [%d]", filter.AllowedLibraryIDs, scope.AllowedLibraryIDs, f.kidsLib)
	}
	if filter.MaturityLimits != scope.MaturityLimits || filter.MaxContentRating != "PG" {
		t.Fatalf("build filter maturity = %+v, scope = %+v", filter.MaturityLimits, scope.MaturityLimits)
	}
}

// fixedUsers serves one account to the resolver.
type fixedUsers struct{ user *models.User }

func (f fixedUsers) GetByID(context.Context, int) (*models.User, error) { return f.user, nil }

// An access change that reaches whole accounts marks every profile on them
// stale and leaves other accounts alone.
func TestMarkAccountsStalePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	repo := NewRepo(pool)
	changed, first := newTasteTestAccount(t, pool, "tmark-accounts-changed-")
	second := "second-" + first
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id, user_id, name) VALUES($1, $2, 'second')`, second, changed); err != nil {
		t.Fatal(err)
	}
	other, otherProfile := newTasteTestAccount(t, pool, "tmark-accounts-other-")
	now := time.Now()
	for _, p := range []struct {
		userID  int
		profile string
	}{{changed, first}, {changed, second}, {other, otherProfile}} {
		if err := repo.UpsertTasteProfile(ctx, p.userID, p.profile, axisVector(2450, nil), map[string]int{"favorited": 1}, "", now); err != nil {
			t.Fatal(err)
		}
	}

	marked, err := repo.MarkAccountsStale(ctx, []int{changed})
	if err != nil {
		t.Fatal(err)
	}
	if marked != 2 {
		t.Fatalf("marked %d profiles, want both on the changed account", marked)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM user_taste_profiles WHERE user_id = $1 AND stale_at IS NOT NULL`, other); n != 0 {
		t.Fatalf("other account has %d stale profiles, want 0", n)
	}
}

// Deleting a profile removes its ratings and recommendation state with it,
// and leaves its account's other profiles alone, on the Postgres store and
// through the purge a store outside Postgres relies on.
func TestDeletingAProfilePurgesItsRecommendationRowsPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	repo := NewRepo(pool)
	const prefix = "tpurge-profile-"
	cleanupRecoMediaItems(t, pool, prefix)
	seedRecoMediaItem(t, pool, prefix+"movie", "movie", "matched")
	userID, kept := newTasteTestAccount(t, pool, prefix)
	viaStore, viaPurge := "store-"+kept, "purge-"+kept
	now := time.Now()
	expires := cacheExpiry(now)

	for _, profile := range []string{kept, viaStore, viaPurge} {
		if profile != kept {
			if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id, user_id, name) VALUES($1, $2, $1)`, profile, userID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 5)`, userID, profile, prefix+"movie"); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpsertTasteProfile(ctx, userID, profile, axisVector(2500, nil), map[string]int{"rated_5": 1}, "", now); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpsertTasteClusters(ctx, userID, profile, []TasteCluster{{ClusterIdx: 0, Embedding: axisVector(2500, nil), Label: "Test", MemberCount: 1, TotalWeight: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpsertRecommendationCache(ctx, userID, profile, RecTypeForYouMain, "", []ScoredItem{{MediaItemID: prefix + "movie"}}, expires); err != nil {
			t.Fatal(err)
		}
	}
	// The tables PurgeProfile clears, one row each per seeded profile.
	tables := []string{"user_ratings", "user_taste_profiles", "user_taste_clusters", "recommendation_cache"}
	rowsOf := func(profile string) int {
		t.Helper()
		total := 0
		for _, table := range tables {
			total += countRows(t, pool, fmt.Sprintf(`SELECT count(*) FROM %s WHERE user_id = $1 AND profile_id = $2`, table), userID, profile)
		}
		return total
	}

	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProfile(ctx, viaStore); err != nil {
		t.Fatal(err)
	}
	if err := repo.PurgeProfile(ctx, userID, viaPurge); err != nil {
		t.Fatal(err)
	}
	if n := rowsOf(viaStore); n != 0 {
		t.Fatalf("profile deleted on the Postgres store keeps %d rows", n)
	}
	if n := rowsOf(viaPurge); n != 0 {
		t.Fatalf("purged profile keeps %d rows", n)
	}
	if n := rowsOf(kept); n != len(tables) {
		t.Fatalf("remaining profile has %d rows, want one in each of %d tables", n, len(tables))
	}
}
