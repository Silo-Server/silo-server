package sections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sectionMutationFixture struct {
	pool    *pgxpool.Pool
	repo    *Repository
	library int
	app     string
}

func newSectionMutationFixture(t *testing.T) sectionMutationFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	app := fmt.Sprintf("section-cas-%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["application_name"] = app
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := sectionMutationFixture{pool: pool, repo: NewRepository(pool), app: app}
	if err = pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, app).Scan(&f.library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM page_sections WHERE library_id=$1`, f.library)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, f.library)
	})
	return f
}
func (f sectionMutationFixture) section(title string) *PageSection {
	return &PageSection{Scope: "library", LibraryID: new(f.library), Title: title, SectionType: SectionRecentlyAdded, ItemLimit: 20, Enabled: true, Config: json.RawMessage(`{}`)}
}
func (f sectionMutationFixture) create(t *testing.T, title string) *PageSection {
	t.Helper()
	s, err := f.repo.Create(t.Context(), f.section(title))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f sectionMutationFixture) revision(t *testing.T, id string) int64 {
	t.Helper()
	rev, err := f.repo.SectionRevision(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}
func (f sectionMutationFixture) waitForLock(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, f.app).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
	}
	t.Fatal("competing section writer never reached lock wait")
}

func TestSectionMutationCanonicalAndCASDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	a := f.create(t, "A")
	b := f.create(t, "B")
	rows, err := f.repo.ListByScopeAll(t.Context(), "library", &f.library)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID > rows[1].ID {
		t.Fatalf("equal-position IDs not canonical: %+v", rows)
	}
	revision := f.revision(t, a.ID)
	a.Title = "Edited"
	if err = f.repo.UpdateIfRevision(t.Context(), a, revision); err != nil {
		t.Fatal(err)
	}
	if err = f.repo.DeleteIfRevision(t.Context(), a.ID, revision); !errors.Is(err, ErrSectionRevisionMismatch) {
		t.Fatalf("stale delete: %v", err)
	}
	scopeRevision, err := f.repo.ScopeRevision(t.Context(), "library", &f.library)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.ReorderScopeIfRevision(t.Context(), "library", &f.library, []string{a.ID, a.ID}, scopeRevision); !errors.Is(err, ErrSectionOrderMismatch) {
		t.Fatalf("duplicate order: %v", err)
	}
	if current, _ := f.repo.ScopeRevision(t.Context(), "library", &f.library); current != scopeRevision {
		t.Fatal("failed reorder advanced revision")
	}
	if err = f.repo.ReorderScopeIfRevision(t.Context(), "library", &f.library, []string{b.ID, a.ID}, scopeRevision); err != nil {
		t.Fatal(err)
	}
	if err = f.repo.ReorderScopeIfRevision(t.Context(), "library", &f.library, []string{a.ID, b.ID}, scopeRevision); !errors.Is(err, ErrSectionRevisionMismatch) {
		t.Fatalf("stale order: %v", err)
	}
}
func TestSectionMutationLegacyWriterInvalidatesWaitingGuardDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	s := f.create(t, "Before")
	revision := f.revision(t, s.ID)
	locked := make(chan struct{})
	release := make(chan struct{})
	legacyDone := make(chan error, 1)
	go func() {
		legacyDone <- f.repo.mutate(t.Context(), rowMutation(s.ID), func(ctx context.Context) error {
			close(locked)
			<-release
			row := *s
			row.Title = "Legacy wins"
			return f.repo.update(ctx, &row)
		})
	}()
	<-locked
	guardDone := make(chan error, 1)
	go func() {
		row := *s
		row.Title = "Guarded draft"
		guardDone <- f.repo.UpdateIfRevision(t.Context(), &row, revision)
	}()
	f.waitForLock(t)
	close(release)
	if err := <-legacyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-guardDone; !errors.Is(err, ErrSectionRevisionMismatch) {
		t.Fatalf("waiting exact guard: %v", err)
	}
	got, err := f.repo.GetByID(t.Context(), s.ID)
	if err != nil || got.Title != "Legacy wins" {
		t.Fatalf("stale writer replaced state: %+v %v", got, err)
	}
}
func TestSectionMutationOpposingMultiScopeOrdersDB(t *testing.T) {
	first := newSectionMutationFixture(t)
	second := newSectionMutationFixture(t)
	a := first.create(t, "A")
	b := second.create(t, "B")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, entries := range [][]ReorderEntry{{{ID: a.ID, Position: 1}, {ID: b.ID, Position: 2}}, {{ID: b.ID, Position: 3}, {ID: a.ID, Position: 4}}} {
		wg.Go(func() { <-start; results <- first.repo.Reorder(t.Context(), entries) })
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("opposing order transaction: %v", err)
		}
	}
}
func TestSectionMutationReferencesAndOrphanRepairDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	collections := catalog.NewLibraryCollectionRepository(f.pool)
	c, err := collections.Create(t.Context(), catalog.CreateLibraryCollectionInput{LibraryID: f.library, Title: "Referenced", Slug: "referenced", CollectionType: "manual", Visibility: "visible"})
	if err != nil {
		t.Fatal(err)
	}
	s := f.section("Reference")
	s.SectionType = SectionCollection
	s.Config = json.RawMessage(fmt.Sprintf(`{"library_collection_id":%q}`, c.ID))
	s, err = f.repo.Create(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err = collections.Delete(t.Context(), c.ID); !errors.Is(err, catalog.ErrLibraryCollectionInUse) {
		t.Fatalf("legacy delete ignored reference: %v", err)
	}
	bad := f.section("Missing")
	bad.Config = json.RawMessage(`{"library_collection_id":"missing-parent"}`)
	if _, err = f.repo.Create(t.Context(), bad); !errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		t.Fatalf("accepted missing reference: %v", err)
	}

	bad.Config = json.RawMessage(`{"library_collection_id":123}`)
	if _, err = f.repo.Create(t.Context(), bad); !errors.Is(err, ErrInvalidSectionConfig) {
		t.Fatalf("non-string reference bypassed validation: %v", err)
	}
	// Simulate historical corruption without creating another runtime writer.
	if _, err = f.pool.Exec(t.Context(), `UPDATE page_sections SET config='{"library_collection_id":"historical-missing"}' WHERE id=$1`, s.ID); err != nil {
		t.Fatal(err)
	}

	broken, err := f.repo.GetByID(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.RestoreDefaultsWithProfileReset(t.Context(), "library", &f.library, []*PageSection{broken}, false); !errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		t.Fatalf("restored copied historical missing reference: %v", err)
	}
	if _, err = f.repo.GetByID(t.Context(), s.ID); err != nil {
		t.Fatalf("failed copied-reference restore removed original: %v", err)
	}
	if err = f.repo.Delete(t.Context(), s.ID); err != nil {
		t.Fatalf("cannot remove historical orphan: %v", err)
	}
}
func TestSectionMutationResetRollbackAndNoopDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	s := f.create(t, "Original")
	var account int
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, f.app).Scan(&account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account) })
	key := "section_overrides:library:" + strconv.Itoa(f.library)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO user_settings(user_id,key,value) VALUES($1,$2,'draft')`, account, key); err != nil {
		t.Fatal(err)
	}
	revision, _ := f.repo.ScopeRevision(t.Context(), "library", &f.library)
	invalid := f.section("Invalid")
	invalid.Config = json.RawMessage(`invalid-json`)
	if _, err := f.repo.RestoreDefaultsIfRevision(t.Context(), "library", &f.library, []*PageSection{invalid}, revision, true); err == nil {
		t.Fatalf("reset failure: %v", err)
	}
	if _, err := f.repo.GetByID(t.Context(), s.ID); err != nil {
		t.Fatal("failed reset deleted definition")
	}
	var value string
	if err := f.pool.QueryRow(t.Context(), `SELECT value FROM user_settings WHERE user_id=$1 AND key=$2`, account, key).Scan(&value); err != nil || value != "draft" {
		t.Fatalf("failed reset deleted overrides %s %v", value, err)
	}

	// Fail specifically in the final profile-reset DELETE, after valid defaults
	// have replaced definitions in this transaction.
	triggerName := fmt.Sprintf("test_section_reset_%d", f.library)
	_, err := f.pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected profile reset failure'; END; $$;
 CREATE TRIGGER %s BEFORE DELETE ON user_settings FOR EACH ROW WHEN (OLD.user_id=%d) EXECUTE FUNCTION %s()`, triggerName, triggerName, account, triggerName))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrigger := func() {
		_, _ = f.pool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON user_settings; DROP FUNCTION IF EXISTS %s()", triggerName, triggerName))
	}
	t.Cleanup(cleanupTrigger)
	if _, err = f.repo.RestoreDefaultsIfRevision(t.Context(), "library", &f.library, []*PageSection{f.section("Valid replacement")}, revision, true); err == nil {
		t.Fatal("profile reset failure unexpectedly succeeded")
	}
	if original, readErr := f.repo.GetByID(t.Context(), s.ID); readErr != nil || original.Title != "Original" {
		t.Fatalf("profile failure committed definition replacement: %+v %v", original, readErr)
	}
	if err = f.pool.QueryRow(t.Context(), `SELECT value FROM user_settings WHERE user_id=$1 AND key=$2`, account, key).Scan(&value); err != nil || value != "draft" {
		t.Fatalf("profile failure lost override %s %v", value, err)
	}
	if current, _ := f.repo.ScopeRevision(t.Context(), "library", &f.library); current != revision {
		t.Fatal("profile failure committed scope revision")
	}
	cleanupTrigger()
	if _, err := f.repo.RestoreDefaultsIfRevision(t.Context(), "library", &f.library, nil, revision, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM user_settings WHERE user_id=$1 AND key=$2`, account, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reset did not clear overrides: %d %v", count, err)
	}
	revision, _ = f.repo.ScopeRevision(t.Context(), "library", &f.library)
	if err := f.repo.ReorderScopeIfRevision(t.Context(), "library", &f.library, []string{}, revision); err != nil {
		t.Fatal(err)
	}
	if current, _ := f.repo.ScopeRevision(t.Context(), "library", &f.library); current <= revision {
		t.Fatal("guarded empty order did not consume witness")
	}
}

func TestSectionMutationConcurrentCreateManyPositionsDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, prefix := range []string{"first", "second"} {
		wg.Go(func() {
			<-start
			results <- f.repo.CreateMany(t.Context(), []*PageSection{f.section(prefix + "-a"), f.section(prefix + "-b")})
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent batch: %v", err)
		}
	}
	rows, err := f.repo.ListByScopeAll(t.Context(), "library", &f.library)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows", len(rows))
	}
	for i, row := range rows {
		if row.Position != i {
			t.Fatalf("duplicate or skipped append position: %+v", rows)
		}
	}
}
