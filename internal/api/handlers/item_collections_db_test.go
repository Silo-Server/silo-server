package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// itemCollectionsFixture is a catalog for the "Also in" test: a movie
// library the viewer can open and one it cannot, three movies of one box set
// (one rated above the viewer's limit), and an account with two profiles.
type itemCollectionsFixture struct {
	pool                  *pgxpool.Pool
	prefix                string
	franchise             int64
	shown, other, account int
	title, sequel, mature string
	repo                  *catalog.LibraryCollectionRepository
	provider              *pgstore.PostgresProvider
	store                 userstore.UserStore
	viewer, sibling       string
}

func seedItemCollections(t *testing.T) *itemCollectionsFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	stamp := time.Now().UnixNano()
	f := &itemCollectionsFixture{pool: pool, prefix: fmt.Sprintf("also-in-%d", stamp), franchise: stamp%1_000_000_000 + 2_000_000_000, repo: catalog.NewLibraryCollectionRepository(pool)}
	for i, target := range []*int{&f.shown, &f.other} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled,box_sets_enabled) VALUES('movies',$1,true,true) RETURNING id`, fmt.Sprintf("%s-%d", f.prefix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, f.prefix).Scan(&f.account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM library_collections WHERE slug LIKE $1`, f.prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, f.prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM tmdb_movie_collections WHERE tmdb_collection_id = $1`, f.franchise)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, f.account)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=ANY($1)`, []int{f.shown, f.other})
	})
	f.title, f.sequel, f.mature = f.addMovie(t, "title", "PG", 101), f.addMovie(t, "sequel", "PG", 102), f.addMovie(t, "mature", "R", 103)
	if err := catalog.NewBoxSetRepository(pool).UpsertCollection(ctx, catalog.TMDBMovieCollection{ID: f.franchise, Name: f.prefix + " Saga Collection", MemberTMDBIDs: []int64{101, 102, 103}}); err != nil {
		t.Fatal(err)
	}
	f.provider = pgstore.NewPostgresProvider(pool)
	if f.store, err = f.provider.ForUser(ctx, f.account); err != nil {
		t.Fatal(err)
	}
	f.viewer, f.sibling = f.prefix+"-viewer", f.prefix+"-sibling"
	for _, profile := range []string{f.viewer, f.sibling} {
		if err := f.store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: profile}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// addMovie adds a movie to the shown library; a TMDB id below 200 puts it
// in the franchise's box set.
func (f *itemCollectionsFixture) addMovie(t *testing.T, name, rating string, tmdb int64) string {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	id := f.prefix + "-" + name
	exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age,tmdb_id) VALUES($1,'movie',$1,'matched','{}',$2,$3,$4)`,
		id, rating, map[string]int{"G": 0, "PG": 8, "R": 17}[rating], strconv.FormatInt(tmdb, 10))
	exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, f.shown)
	if tmdb < 200 {
		exec(`INSERT INTO media_item_tmdb_collections(content_id,checked_tmdb_id,tmdb_collection_id) VALUES($1,$2,$3)`, id, tmdb, f.franchise)
	}
	return id
}

// viewerScope is the viewer profile reading with the shown library only and
// a PG limit.
func (f *itemCollectionsFixture) viewerScope(t *testing.T) (context.Context, ItemViewer) {
	ctx := access.SetScope(t.Context(), access.Scope{AllowedLibraryIDs: []int{f.shown}, LibrariesRestricted: true, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}})
	filter := AccessFilterFromContext(ctx, "")
	filter.UserID, filter.ProfileID = f.account, f.viewer
	return ctx, ItemViewer{Access: filter, ProfileID: f.viewer}
}

// server creates a server collection in one library; source is its
// source_config JSON, or empty for none.
func (f *itemCollectionsFixture) server(t *testing.T, name, kind, visibility string, library int, source string, members ...string) string {
	t.Helper()
	input := catalog.CreateLibraryCollectionInput{LibraryID: library, LibraryIDs: []int{library}, Slug: f.prefix + "-" + name, Title: name, CollectionType: kind, Visibility: visibility}
	if source != "" {
		input.SourceConfig = json.RawMessage(source)
	}
	if kind == "smart" {
		input.QueryDefinition = json.RawMessage(`{"media_scope":"movie"}`)
	}
	c, err := f.repo.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range members {
		if err := f.repo.AddItem(t.Context(), c.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	return c.ID
}

// personal creates a manual personal collection owned by owner.
func (f *itemCollectionsFixture) personal(t *testing.T, owner, name string, shared bool, members ...string) string {
	t.Helper()
	c, err := f.store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: owner, Name: name, CollectionType: "manual", IsShared: shared})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range members {
		if err := f.store.AddCollectionItem(t.Context(), c.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	return c.ID
}

func (f *itemCollectionsFixture) service() *ItemCollections {
	itemRepo := catalog.NewItemRepository(f.pool)
	library := NewLibraryCollectionHandler(f.repo, nil, itemRepo, nil)
	library.FolderRepo = catalog.NewFolderRepository(f.pool)
	library.Executor = &catalog.QueryExecutor{Pool: f.pool}
	personal := NewCollectionHandler(f.provider)
	personal.Executor = &catalog.QueryExecutor{Pool: f.pool}
	// Another profile may see only G-rated titles, less than the viewer.
	owners := gOnlyOwners{}
	personal.CollectionOwners = owners
	browseRepo := catalog.NewBrowseRepository(f.pool)
	resolver := catalog.NewCatalogResolver(browseRepo, itemRepo).WithUserStoreProvider(f.provider).WithPersonalCollectionAccess(owners)
	return &ItemCollections{
		Cards:   &CollectionRowCards{Library: library, Personal: personal},
		Items:   itemRepo,
		BoxSets: resolver,
		Browser: NewCatalogHandler(resolver, NewItemsHandler(browseRepo, itemRepo, nil, nil, nil, nil, f.provider, nil, nil)),
	}
}

// gOnlyOwners limits every collection owner to G-rated titles.
type gOnlyOwners struct{}

func (gOnlyOwners) OwnerFilter(ctx context.Context, _ int, _ string) (catalog.AccessFilter, error) {
	return AccessFilterFromContext(access.SetScope(ctx, access.Scope{MaturityLimits: access.MaturityLimits{MaxContentRating: "G"}}), ""), nil
}

// TestItemCollectionsDB pins which collections a title's page lists under
// "Also in": the visible server collections and the profile's visible
// personal collections that store the title, server collections first, each
// group by title. A hidden server collection, one in no library the viewer
// can open, a smart collection (also one made smart after storing titles),
// another profile's unshared collection, another profile's shared one when
// its owner may not see the title, and the synced copy of the box set the
// page already shows are left out. A title the viewer cannot see is not
// found, whatever holds it.
func TestItemCollectionsDB(t *testing.T) {
	f := seedItemCollections(t)
	zebra := f.server(t, "Zebra picks", "manual", "visible", f.shown, "", f.title)
	ghibli := f.server(t, "Ghibli", "manual", "visible", f.shown, "", f.title, f.sequel)
	otherFranchise := f.server(t, "Other franchise", "manual", "visible", f.shown, `{"mode":"tmdb_collection","collection_id":1}`, f.title)
	f.server(t, "Hidden", "manual", "hidden", f.shown, "", f.title)
	f.server(t, "Elsewhere", "manual", "visible", f.other, "", f.title)
	f.server(t, "Rules", "smart", "visible", f.shown, "")
	f.server(t, "Saga copy", "manual", "visible", f.shown, fmt.Sprintf(`{"mode":"tmdb_collection","collection_id":%d}`, f.franchise), f.title, f.sequel)
	f.server(t, "Lacks it", "manual", "visible", f.shown, "", f.sequel)
	converted := f.server(t, "Made smart", "manual", "visible", f.shown, "", f.title)
	if _, err := f.pool.Exec(t.Context(), `UPDATE library_collections SET collection_type='smart', query_definition='{"media_scope":"movie"}' WHERE id=$1`, converted); err != nil {
		t.Fatal(err)
	}
	f.server(t, "Mature pick", "manual", "visible", f.shown, "", f.mature)
	own := f.personal(t, f.viewer, "My picks", false, f.title)
	// The sibling may see only G titles, so its shared collection's own page
	// does not list this PG title.
	f.personal(t, f.sibling, "Family night", true, f.title)
	f.personal(t, f.sibling, "Sibling's private", false, f.title)
	f.personal(t, f.viewer, "Without it", false, f.sequel)

	svc := f.service()
	viewerCtx, v := f.viewerScope(t)

	got, err := svc.ItemCollections(viewerCtx, v, f.account, f.title)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{ghibli, otherFranchise, zebra, own}; !slices.Equal(cardIDs(got), want) {
		t.Errorf("Also in = %v, want %v", cardIDs(got), want)
	}
	if len(got.Preview) != 0 {
		t.Errorf("a title in several collections previews %v, want none", previewIDs(got))
	}

	_, err = svc.ItemCollections(viewerCtx, v, f.account, f.mature)
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusNotFound {
		t.Errorf("a title the viewer cannot see answers %v, want 404", err)
	}

	episode := f.prefix + "-episode"
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,'episode',$1,'matched','{}','PG',8)`, episode); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, episode, f.shown); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ItemCollections(viewerCtx, v, f.account, episode)
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusNotFound {
		t.Errorf("an episode answers %v, want 404", err)
	}
}

// TestItemCollectionsPreviewDB pins the row a title's page shows when one
// collection holds it: up to six of that collection's other titles as its
// page lists them, which for another profile's collection is only what its
// owner may see too. A collection holding only the title previews nothing.
func TestItemCollectionsPreviewDB(t *testing.T) {
	f := seedItemCollections(t)
	var members []string
	for i := range 8 {
		members = append(members, f.addMovie(t, fmt.Sprintf("member-%d", i), "PG", int64(300+i)))
	}
	// Collection order: member-0, the title, the mature movie, member-1…7.
	ordered := append([]string{members[0], f.title, f.mature}, members[1:]...)
	only := f.server(t, "Only home", "manual", "visible", f.shown, "", ordered...)
	lone := f.addMovie(t, "lone", "PG", 400)
	f.server(t, "Just one", "manual", "visible", f.shown, "", lone)
	solo := f.addMovie(t, "solo", "G", 401)
	gentle := f.addMovie(t, "gentle", "G", 402)
	family := f.personal(t, f.sibling, "Family night", true, members[2], gentle, f.mature, solo, members[3])

	svc := f.service()
	viewerCtx, v := f.viewerScope(t)

	for _, tc := range []struct {
		name, title  string
		wantCards    []string
		wantPreviews []string
	}{
		{"server collection", f.title, []string{only}, members[:6]},
		{"another profile's collection", solo, []string{family}, []string{gentle}},
		{"collection of the title alone", lone, nil, nil},
	} {
		got, err := svc.ItemCollections(viewerCtx, v, f.account, tc.title)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.wantCards != nil && !slices.Equal(cardIDs(got), tc.wantCards) {
			t.Errorf("%s: cards = %v, want %v", tc.name, cardIDs(got), tc.wantCards)
		}
		if !slices.Equal(previewIDs(got), tc.wantPreviews) {
			t.Errorf("%s: preview = %v, want %v", tc.name, previewIDs(got), tc.wantPreviews)
		}
	}
}

// TestItemCollectionsPreviewLibraryDB pins that a server collection spanning
// two libraries previews only the titles of the library its card opens,
// as Explore all lists them, however the title's page was opened.
func TestItemCollectionsPreviewLibraryDB(t *testing.T) {
	f := seedItemCollections(t)
	here := f.addMovie(t, "here", "PG", 500)
	there := f.prefix + "-there"
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,'movie',$1,'matched','{}','PG',8)`, there); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, there, f.other); err != nil {
		t.Fatal(err)
	}
	c, err := f.repo.Create(t.Context(), catalog.CreateLibraryCollectionInput{LibraryID: f.shown, LibraryIDs: []int{f.shown, f.other}, Slug: f.prefix + "-spanning", Title: "Spanning", CollectionType: "manual", Visibility: "visible"})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{there, f.title, here} {
		if err := f.repo.AddItem(t.Context(), c.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}

	ctx := access.SetScope(t.Context(), access.Scope{AllowedLibraryIDs: []int{f.shown, f.other}, LibrariesRestricted: true, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}})
	filter := AccessFilterFromContext(ctx, "")
	filter.UserID, filter.ProfileID = f.account, f.viewer
	other := f.other
	// The page was opened from the other library, whose titles the card's
	// library does not hold.
	filter.PresentationLibraryID = &other
	got, err := f.service().ItemCollections(ctx, ItemViewer{Access: filter, ProfileID: f.viewer}, f.account, f.title)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cards) != 1 || got.Cards[0].LibraryID == nil {
		t.Fatalf("cards = %+v, want the spanning collection with its library", got.Cards)
	}
	want := []string{here}
	if *got.Cards[0].LibraryID == f.other {
		want = []string{there}
	}
	if !slices.Equal(previewIDs(got), want) {
		t.Errorf("preview in library %d = %v, want %v", *got.Cards[0].LibraryID, previewIDs(got), want)
	}
}

func cardIDs(v ItemCollectionsView) []string {
	var ids []string
	for _, card := range v.Cards {
		ids = append(ids, card.ID)
	}
	return ids
}

func previewIDs(v ItemCollectionsView) []string {
	var ids []string
	for _, item := range v.Preview {
		ids = append(ids, item.ContentID)
	}
	return ids
}
