package sections

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLibraryCollectionReferencesDB checks the rows that show a server
// collection: a Home row, a library page row and the starter-pack hero rows a
// template bundle generates are listed, rows showing other collections are
// not, and the per-collection counts match the list. The Home count leaves out
// turned-off Home rows, which no viewer sees; the total keeps them because
// they still block a delete.
func TestLibraryCollectionReferencesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	// One repeatable-read transaction, rolled back at the end, keeps the Home
	// page's row count stable while other packages write rows to the same
	// database.
	tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	ctx := context.WithValue(t.Context(), sectionTransactionKey{}, tx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	prefix := fmt.Sprintf("row-refs-%d", time.Now().UnixNano())
	library := func(name string) int {
		t.Helper()
		var id int
		if err := tx.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix+"-"+name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	kids, films := library("kids"), library("films")
	shown, other, unused := prefix+"-shown", prefix+"-other", prefix+"-unused"

	row := func(id, scope string, libraryID *int, position int, title string, featured, enabled bool, config string) {
		t.Helper()
		exec(`INSERT INTO page_sections(id,scope,library_id,position,section_type,title,featured,config,enabled)
			VALUES($1,$2,$3,$4,'collection',$5,$6,$7::jsonb,$8)`,
			prefix+"-"+id, scope, libraryID, position, title, featured, config, enabled)
	}
	plain := func(collectionID string) string {
		return fmt.Sprintf(`{"library_collection_id":%q}`, collectionID)
	}
	hero := func(collectionID, surface string, libraryID int) string {
		return fmt.Sprintf(`{"library_collection_id":%q,"generated_source":"template_bundle_featured","template_bundle":"franchise_collections","template_id":"tmdb_franchise_placeholder","surface":%q,"library_id":%d}`, collectionID, surface, libraryID)
	}
	row("home", "home", nil, 7, "Shown on Home", false, true, plain(shown))
	row("home-hero", "home", nil, 0, "Starter pack hero", true, true, hero(shown, "home", kids))
	row("home-off", "home", nil, 9, "Off on Home", false, false, plain(shown))
	row("kids-off", "library", &kids, 2, "Turned off", false, false, plain(shown))
	row("kids-filler", "library", &kids, 0, "Other row", false, true, `{}`)
	row("kids-hero", "library", &kids, 1, "Kids hero", true, true, hero(shown, "library", kids))
	row("films", "library", &films, 0, "Films row", false, true, plain(shown))
	row("other-home", "home", nil, 3, "Other collection", false, true, plain(other))
	row("other-films", "library", &films, 1, "Other films", false, true, plain(other))

	var homeRows int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM page_sections WHERE scope = 'home'`).Scan(&homeRows); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(pool)
	refs, err := repo.ListLibraryCollectionReferences(ctx, shown)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		id           string
		scope        string
		libraryID    *int
		position     int
		title        string
		featured     bool
		enabled      bool
		pageRowCount int
	}
	// Home rows come first, then library pages by library, each in page order.
	first, second := kids, films
	if films < kids {
		first, second = films, kids
	}
	byLibrary := map[int][]want{
		kids: {
			{prefix + "-kids-hero", "library", &kids, 1, "Kids hero", true, true, 3},
			{prefix + "-kids-off", "library", &kids, 2, "Turned off", false, false, 3},
		},
		films: {{prefix + "-films", "library", &films, 0, "Films row", false, true, 2}},
	}
	wanted := slices.Concat([]want{
		{prefix + "-home-hero", "home", nil, 0, "Starter pack hero", true, true, homeRows},
		{prefix + "-home", "home", nil, 7, "Shown on Home", false, true, homeRows},
		{prefix + "-home-off", "home", nil, 9, "Off on Home", false, false, homeRows},
	}, byLibrary[first], byLibrary[second])
	if len(refs) != len(wanted) {
		t.Fatalf("got %d references, want %d: %+v", len(refs), len(wanted), refs)
	}
	for i, w := range wanted {
		got := refs[i]
		if got.ID != w.id || got.Scope != w.scope || got.Position != w.position || got.Title != w.title ||
			got.Featured != w.featured || got.Enabled != w.enabled || got.PageRowCount != w.pageRowCount ||
			got.SectionType != SectionCollection || !sameLibrary(got.LibraryID, w.libraryID) {
			t.Errorf("reference %d = %+v (page rows %d), want %+v", i, got.PageSection, got.PageRowCount, w)
		}
	}

	counts, err := repo.CountLibraryCollectionReferencesByID(ctx, []string{shown, other, unused})
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]LibraryCollectionReferenceCount{
		shown: {Home: 2, Total: len(refs)},
		other: {Home: 1, Total: 2},
	}
	if len(counts) != len(wantCounts) || counts[shown] != wantCounts[shown] || counts[other] != wantCounts[other] {
		t.Fatalf("counts = %+v, want %+v (no entry for an unused collection)", counts, wantCounts)
	}
	single, err := repo.CountLibraryCollectionReferences(ctx, shown, "")
	if err != nil {
		t.Fatal(err)
	}
	if single != counts[shown].Total {
		t.Fatalf("CountLibraryCollectionReferences = %d, grouped total %d", single, counts[shown].Total)
	}

	if none, err := repo.ListLibraryCollectionReferences(ctx, unused); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("unused collection references = %v, %v; want an empty list", none, err)
	}
	if empty, err := repo.CountLibraryCollectionReferencesByID(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("counting no collections = %v, %v", empty, err)
	}
}

func sameLibrary(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
