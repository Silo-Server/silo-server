package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// testFence stands in for the admin job's fenced transaction: it commits fn
// in a transaction of its own, and refuses once failAfter commits have
// landed, as a fence does after stale recovery marked the job interrupted.
type testFence struct {
	pool      *pgxpool.Pool
	failAfter int
	commits   int
}

var errTestFenced = errors.New("the job is no longer this worker's")

func (f *testFence) run(ctx context.Context, fn func(pgx.Tx) (FencedUpdate, error)) error {
	if f.failAfter > 0 && f.commits >= f.failAfter {
		return errTestFenced
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	f.commits++
	return nil
}

// selectiveSeed is the state a selective restore test backs up, loses, and
// restores.
type selectiveSeed struct {
	libraryID    int
	libraryPath  string
	library2ID   int
	library2Path string
	// groups are the collection's group slugs in the first and second
	// library.
	groups       [2]string
	collectionID string
	collection   string
	items        []string
	artwork      string
	settingKey   string
	secretKey    string
}

func seedSelective(t *testing.T, pool *pgxpool.Pool, settings *catalog.EncryptedSettingsRepo, store blobstore.Store) selectiveSeed {
	t.Helper()
	ctx := t.Context()
	suffix := randomHex(t, 4)
	seed := selectiveSeed{libraryPath: filepath.Join(t.TempDir(), "movies-"+suffix), library2Path: filepath.Join(t.TempDir(), "films-"+suffix),
		groups: [2]string{"one-" + suffix, "two-" + suffix}, collection: "Restore test " + suffix,
		items: []string{"bkrs-" + suffix + "-1", "bkrs-" + suffix + "-2"}, settingKey: "taskmanager.history_keep_per_task", secretKey: "watchsync.simkl.client_secret"}
	for _, dir := range []string{seed.libraryPath, seed.library2Path} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id IN (SELECT media_folder_id FROM media_folder_paths WHERE path = $1)`, dir)
		})
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	folders := catalog.NewFolderRepository(pool)
	inSearch := false
	folder, err := folders.Create(ctx, catalog.CreateFolderInput{Paths: []string{seed.libraryPath}, Type: "movies", Name: "Restore test " + suffix,
		BoxSetsEnabled: true, BoxSetMinMovies: 3, BoxSetsInSearch: &inSearch})
	if err != nil {
		t.Fatal(err)
	}
	seed.libraryID = folder.ID
	folder2, err := folders.Create(ctx, catalog.CreateFolderInput{Paths: []string{seed.library2Path}, Type: "movies", Name: "Restore test 2 " + suffix})
	if err != nil {
		t.Fatal(err)
	}
	seed.library2ID = folder2.ID
	poster := "library-posters/" + strconv.Itoa(folder.ID) + ".jpg"
	exec(`UPDATE media_folders SET poster_path = $2 WHERE id = $1`, folder.ID, poster)
	seed.artwork = "local/movies/" + seed.items[0] + "/abcd1234/poster/original.r1.jpg"
	for _, id := range seed.items {
		exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, id)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, seed.items)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, seed.artwork)
	})
	exec(`UPDATE media_items SET poster_path = $2, poster_source_path = $3 WHERE content_id = $1`, seed.items[0], seed.artwork, "upload://sha256:"+strings.Repeat("ab", 32))
	collections := catalog.NewLibraryCollectionRepository(pool)
	cid := "rs" + suffix
	collectionPoster := "collection-images/" + cid + "/poster/original.abcd.jpg"
	if _, err := collections.Create(ctx, catalog.CreateLibraryCollectionInput{ID: cid, LibraryID: folder.ID, LibraryIDs: []int{folder.ID, folder2.ID},
		Slug: "restore-" + suffix, Title: seed.collection, PosterURL: collectionPoster}); err != nil {
		t.Fatal(err)
	}
	groups := catalog.NewLibraryCollectionGroupRepository(pool)
	for i, libraryID := range []int{folder.ID, folder2.ID} {
		group, err := groups.Create(ctx, catalog.CreateLibraryCollectionGroupInput{LibraryID: libraryID, Name: seed.groups[i], Slug: seed.groups[i]})
		if err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE library_collection_libraries SET group_id = $3 WHERE collection_id = $1 AND library_id = $2`, cid, libraryID, group.ID)
	}
	seed.collectionID = cid
	for i, id := range seed.items {
		if err := collections.AddItem(ctx, cid, id, i); err != nil {
			t.Fatal(err)
		}
	}
	for key, data := range map[string]string{
		poster: "library poster", seed.artwork: "uploaded poster", "local/movies/" + seed.items[0] + "/abcd1234/poster/w500.r1.jpg": "uploaded w500",
		collectionPoster: "collection poster", "collection-images/" + cid + "/poster/w300.abcd.jpg": "collection w300",
	} {
		if err := store.Put(ctx, key, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	setSetting(t, pool, settings, seed.settingKey, "17")
	setSetting(t, pool, settings, seed.secretKey, "simkl-"+suffix)
	return seed
}

func previewItem(t *testing.T, p *RestorePreview, section, id string) PreviewItem {
	t.Helper()
	for _, s := range p.Sections {
		if s.ID != section {
			continue
		}
		for _, item := range s.Items {
			if item.ID == id {
				return item
			}
		}
	}
	t.Fatalf("preview has no %s item %q", section, id)
	return PreviewItem{}
}

func reportOutcomes(r *SelectiveReport) map[string]string {
	out := map[string]string{}
	for _, item := range r.Items {
		out[item.Section+":"+item.ID] = item.Outcome
	}
	return out
}

// selectiveFixture is a backup service over the test database with
// selective restore wired to stand-in write paths.
type selectiveFixture struct {
	pool     *pgxpool.Pool
	svc      *Service
	settings *catalog.EncryptedSettingsRepo
	store    blobstore.Store
	serverID string
	// calls lists every library follow-up run, created the ones that
	// succeeded; failFollowUps makes them fail, as a crash right after an
	// item's commit would leave them undone.
	calls         []int
	created       []int
	failFollowUps bool
}

func newSelectiveFixture(t *testing.T) *selectiveFixture {
	t.Helper()
	pool, dsn := backuptest.Database(t)
	tools := testTools(t)
	masterKey := []byte("selective-restore-test-key-0123456789")
	cipher, err := secret.New(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	keyCheck, err := secret.KeyCheckValue(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &selectiveFixture{pool: pool, store: store, serverID: "selective-" + randomHex(t, 4),
		settings: catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher)}
	f.svc = New(Options{
		Pool: pool, DatabaseURL: dsn, Settings: f.settings, Cipher: cipher, KeyCheck: keyCheck,
		Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: NewStoreDestination(store),
		Locker:    PostgresLocker{Pool: pool},
		ServerID:  func(context.Context) (string, error) { return f.serverID, nil },
		FindTools: func(context.Context) (Tools, error) { return tools, nil },
	})
	f.svc.SetRestoreTargets(RestoreTargets{
		CheckFolder: func(_ context.Context, path string) error { _, err := os.Stat(path); return err },
		ApplySettings: func(ctx context.Context, tx pgx.Tx, values map[string]string) ([]string, func(context.Context), error) {
			err := f.settings.UpdateAtomicWithTx(ctx, tx, func(map[string]string, pgx.Tx) (map[string]string, error) { return values, nil })
			return nil, func(context.Context) {}, err
		},
		LibraryCreated: func(_ context.Context, id int) error {
			f.calls = append(f.calls, id)
			if f.failFollowUps {
				return errors.New("interrupted before the follow-up ran")
			}
			f.created = append(f.created, id)
			return nil
		},
	})
	return f
}

// lose deletes both libraries (and with them the collection), clears the
// item's uploaded poster, and changes both settings.
func (f *selectiveFixture) lose(t *testing.T, seed selectiveSeed) {
	t.Helper()
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{seed.libraryID, seed.library2ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE media_items SET poster_path = '', poster_source_path = '' WHERE content_id = $1`, seed.items[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, seed.settingKey, "3"); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, seed.secretKey, "changed"); err != nil {
		t.Fatal(err)
	}
}

func (f *selectiveFixture) restore(t *testing.T, backupID, digest string, selection map[string][]string, fence *testFence) (*SelectiveReport, error) {
	t.Helper()
	payload, _ := json.Marshal(RestoreRequest{BackupID: backupID, Digest: digest, Selection: selection})
	result, err := f.svc.ExecuteRestore(t.Context(), RestoreRun{Payload: payload, Fenced: fence.run})
	report, _ := result.(*SelectiveReport)
	return report, err
}

func checkPreviewAfterLoss(t *testing.T, preview *RestorePreview, seed selectiveSeed) {
	t.Helper()
	libraryRef := strconv.Itoa(seed.libraryID)
	if item := previewItem(t, preview, SectionLibraries, libraryRef); item.Status != ItemNotOnThisServer || !item.Selectable {
		t.Fatalf("lost library = %+v", item)
	}
	if item := previewItem(t, preview, SectionCollections, collectionItemID(seed.collectionID)); !item.Selectable || len(item.Requires) != 2 || item.Requires[0] != "libraries:"+libraryRef {
		t.Fatalf("collection of lost libraries = %+v, want selectable with its two libraries", item)
	}
	for _, s := range previewItem(t, preview, SectionSettings, "other").Settings {
		if s.Key == seed.secretKey && (!s.Masked || s.BackupValue != "" || s.Status != ItemDiffers) {
			t.Fatalf("encrypted setting in the preview = %+v, want masked and differing", s)
		}
	}
}

// checkRestored verifies the library under its new id with its poster, the
// collection remapped to it with its items and artwork under its own id
// (free again once it was deleted), the item artwork, and both settings.
func (f *selectiveFixture) checkRestored(t *testing.T, seed selectiveSeed, report *SelectiveReport, newLibrary, newLibrary2 int) {
	t.Helper()
	ctx := t.Context()
	if report.LibraryMap[strconv.Itoa(seed.library2ID)] != newLibrary2 {
		t.Fatalf("library map = %v, want %d -> %d", report.LibraryMap, seed.library2ID, newLibrary2)
	}
	if report.LibraryMap[strconv.Itoa(seed.libraryID)] != newLibrary || newLibrary == seed.libraryID {
		t.Fatalf("library map = %v, want %d -> %d", report.LibraryMap, seed.libraryID, newLibrary)
	}
	var poster, collection, collectionPoster, itemSource string
	var boxSets, boxSetsInSearch bool
	var boxSetMin int
	if err := f.pool.QueryRow(ctx, `SELECT poster_path, box_sets_enabled, box_set_min_movies, box_sets_in_search FROM media_folders WHERE id = $1`,
		newLibrary).Scan(&poster, &boxSets, &boxSetMin, &boxSetsInSearch); err != nil {
		t.Fatal(err)
	}
	if !boxSets || boxSetMin != 3 || boxSetsInSearch {
		t.Fatalf("restored box set settings = %v, %d, %v; want true, 3, false", boxSets, boxSetMin, boxSetsInSearch)
	}
	if err := f.pool.QueryRow(ctx, `SELECT id, poster_url FROM library_collections WHERE library_id = $1 AND title = $2`, newLibrary, seed.collection).Scan(&collection, &collectionPoster); err != nil {
		t.Fatalf("restored collection: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT poster_source_path FROM media_items WHERE content_id = $1`, seed.items[0]).Scan(&itemSource); err != nil {
		t.Fatal(err)
	}
	var items int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM library_collection_items WHERE collection_id = $1`, collection).Scan(&items); err != nil {
		t.Fatal(err)
	}
	newPrefix := "collection-images/" + collection + "/poster/"
	switch {
	case poster != "library-posters/"+strconv.Itoa(newLibrary)+".jpg":
		t.Fatalf("restored library poster = %q", poster)
	case collection != seed.collectionID || collectionPoster != newPrefix+"original.abcd.jpg":
		t.Fatalf("restored collection %s poster %q", collection, collectionPoster)
	case items != 2:
		t.Fatalf("restored collection items = %d", items)
	case !strings.HasPrefix(itemSource, "upload://"):
		t.Fatalf("restored item artwork source = %q", itemSource)
	}
	// Each library membership is back in its own group, through its mapped
	// library.
	for i, libraryID := range []int{newLibrary, newLibrary2} {
		var slug string
		if err := f.pool.QueryRow(ctx, `SELECT COALESCE(g.slug, '') FROM library_collection_libraries l
			LEFT JOIN library_collection_groups g ON g.id = l.group_id WHERE l.collection_id = $1 AND l.library_id = $2`, collection, libraryID).Scan(&slug); err != nil || slug != seed.groups[i] {
			t.Fatalf("collection group in library %d = %q, %v; want %q", libraryID, slug, err, seed.groups[i])
		}
	}
	for _, key := range []string{poster, newPrefix + "w300.abcd.jpg"} {
		if _, err := f.store.Stat(ctx, key); err != nil {
			t.Fatalf("restored blob %s: %v", key, err)
		}
	}
	if got, err := f.settings.Get(ctx, seed.settingKey); err != nil || got != "17" {
		t.Fatalf("restored setting = %q, %v", got, err)
	}
	if got, err := f.settings.Get(ctx, seed.secretKey); err != nil || !strings.HasPrefix(got, "simkl-") {
		t.Fatalf("restored encrypted setting = %q, %v", got, err)
	}
	if _, kind, ok := ParseFolderName(report.BeforeRestoreBackupID); !ok || kind != KindBeforeRestore {
		t.Fatalf("before-restore backup id = %q", report.BeforeRestoreBackupID)
	}
}

// pendingFollowUps counts the restore follow-ups still owed for ids.
func (f *selectiveFixture) pendingFollowUps(t *testing.T, ids []int) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM backup_restore_followups WHERE target_id = ANY($1)`, ids).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// checkConflictingBlob restores the item artwork while another object
// already sits at its key: the item fails, the object and the item are left
// as they were, and nothing else is affected.
func (f *selectiveFixture) checkConflictingBlob(t *testing.T, seed selectiveSeed, backupID, digest, artworkRef string) {
	t.Helper()
	ctx := t.Context()
	body, _, err := f.store.Get(ctx, seed.artwork)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := io.ReadAll(body)
	_ = body.Close()
	if err := f.store.Put(ctx, seed.artwork, []byte("someone else's image")); err != nil {
		t.Fatal(err)
	}
	report, err := f.restore(t, backupID, digest, map[string][]string{SectionCollections: {artworkRef}}, &testFence{pool: f.pool})
	if err != nil {
		t.Fatal(err)
	}
	if got := reportOutcomes(report)[SectionCollections+":"+artworkRef]; got != OutcomeFailed {
		t.Fatalf("artwork over a conflicting object = %s (%+v), want failed", got, report.Items)
	}
	var source string
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(poster_source_path, '') FROM media_items WHERE content_id = $1`, seed.items[0]).Scan(&source); err != nil || source != "" {
		t.Fatalf("failed artwork item changed the item: source %q, %v", source, err)
	}
	body, _, err = f.store.Get(ctx, seed.artwork)
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := io.ReadAll(body)
	_ = body.Close()
	if string(kept) != "someone else's image" {
		t.Fatalf("the existing object was overwritten: %q", kept)
	}
	if err := f.store.Put(ctx, seed.artwork, original); err != nil {
		t.Fatal(err)
	}
}

// TestSelectiveRestoreDB backs up two libraries with a poster, an admin
// collection spanning both with a different group in each and uploaded
// artwork, an uploaded item poster, and a plain and an encrypted setting,
// then loses them all and restores them:
//
//   - the preview finds each item on this server, then not on it after the
//     loss, and the collection requires both libraries;
//   - a restore whose source changed since the preview is refused before it
//     takes a backup or writes anything;
//   - an archive blob whose key holds other bytes here fails its item and
//     overwrites nothing;
//   - a restore stopped by its fence after the first library commits, whose
//     follow-up never finished, keeps that library and its owed follow-up;
//     re-running it finishes the follow-up, restores the rest, and reports
//     the library unchanged;
//   - the libraries come back under new ids with their poster, the
//     collection is remapped to them with its items, its group in each
//     library, and its artwork under the new collection id, the settings
//     and the encrypted value are restored, and a before-restore backup is
//     recorded;
//   - running the same restore again changes nothing and runs no follow-up.
func TestSelectiveRestoreDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	seed := seedSelective(t, f.pool, f.settings, f.store)
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	libraryRef, library2Ref := strconv.Itoa(seed.libraryID), strconv.Itoa(seed.library2ID)
	collectionRef, artworkRef := collectionItemID(seed.collectionID), "artwork:"+seed.items[0]+":poster"
	preview, err := f.svc.Preview(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ section, id string }{{SectionLibraries, libraryRef}, {SectionCollections, collectionRef}, {SectionCollections, artworkRef}} {
		if item := previewItem(t, preview, c.section, c.id); item.Status != ItemOnThisServer || item.Selectable {
			t.Fatalf("%s %s before the loss = %+v, want on this server", c.section, c.id, item)
		}
	}
	f.lose(t, seed)
	if preview, err = f.svc.Preview(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	checkPreviewAfterLoss(t, preview, seed)

	selection := map[string][]string{SectionLibraries: {libraryRef, library2Ref}, SectionSettings: {"other"}, SectionCollections: {collectionRef, artworkRef}}
	before, _ := f.svc.folders(ctx, target{Destination: f.svc.opts.Destination}, f.serverID)
	if refused, err := f.restore(t, source.ID, strings.Repeat("0", 64), selection, &testFence{pool: f.pool}); !errors.Is(err, ErrDigestMismatch) || refused.ErrorCode != RestoreErrorSetChanged {
		t.Fatalf("restore with another digest = %v (%+v), want ErrDigestMismatch", err, refused)
	}
	if after, _ := f.svc.folders(ctx, target{Destination: f.svc.opts.Destination}, f.serverID); len(after) != len(before) {
		t.Fatal("a refused restore took a backup")
	}
	f.checkConflictingBlob(t, seed, source.ID, preview.Digest, artworkRef)

	// The fence stops the restore after the before-restore record and the
	// first library's commit, as stale recovery would; that library's
	// follow-up failed, as if the server died right after the commit.
	f.failFollowUps = true
	report, err := f.restore(t, source.ID, preview.Digest, selection, &testFence{pool: f.pool, failAfter: 2})
	if !errors.Is(err, errTestFenced) || len(f.calls) != 1 || len(f.created) != 0 || report.BeforeRestoreBackupID == "" {
		t.Fatalf("fenced restore = %v, follow-ups %v done %v, before-restore %q", err, f.calls, f.created, report.BeforeRestoreBackupID)
	}
	newLibrary := f.calls[0]
	if n := f.pendingFollowUps(t, []int{newLibrary}); n != 1 {
		t.Fatalf("owed follow-ups after the interruption = %d, want 1", n)
	}
	f.failFollowUps = false
	if report, err = f.restore(t, source.ID, preview.Digest, selection, &testFence{pool: f.pool}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		SectionLibraries + ":" + libraryRef: OutcomeUnchanged, SectionLibraries + ":" + library2Ref: OutcomeRestored,
		SectionSettings + ":other": OutcomeRestored, SectionCollections + ":" + collectionRef: OutcomeRestored,
		SectionCollections + ":" + artworkRef: OutcomeRestored,
	}
	outcomes := reportOutcomes(report)
	for k, v := range want {
		if outcomes[k] != v {
			t.Fatalf("re-run outcomes = %v (items %+v), want %v", outcomes, report.Items, want)
		}
	}
	if len(f.created) != 2 || f.created[0] != newLibrary || f.pendingFollowUps(t, f.created) != 0 {
		t.Fatalf("follow-ups done %v (pending %d), want the interrupted library's first", f.created, f.pendingFollowUps(t, f.created))
	}
	f.checkRestored(t, seed, report, newLibrary, f.created[1])

	calls := len(f.calls)
	again, err := f.restore(t, source.ID, preview.Digest, selection, &testFence{pool: f.pool})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range reportOutcomes(again) {
		if v != OutcomeUnchanged {
			t.Fatalf("restoring again: %s %s (%+v)", k, v, again.Items)
		}
	}
	if len(f.calls) != calls {
		t.Fatalf("restoring again ran follow-ups %v", f.calls[calls:])
	}
}

// pagesSeed is the template collections and page sections
// TestTemplateCollectionsAndHomeSectionsDB backs up.
type pagesSeed struct {
	suffix  string
	library int
	// customized was created from a template and then changed by the admin;
	// plain keeps the template's defaults but its description; gone names
	// a template this server's catalog does not have.
	customized, plain, gone string
	// featured shows customized on the home page, recent is a library page
	// section, managed is a home section with the collection it manages,
	// and layout is another home section.
	featured, recent, managed, layout string
	managedCollection                 string
	items                             []string
}

// wirePageTargets gives the fixture the template catalog and section checks
// the server wires, with a stand-in for the post-commit follow-up that
// fails until told otherwise, as an unreachable provider would.
func wirePageTargets(f *selectiveFixture, failSync *bool, synced *[]string) {
	collections := handlers.NewLibraryCollectionHandler(catalog.NewLibraryCollectionRepository(f.pool), nil, nil, nil)
	targets := f.svc.targets
	targets.TemplateTitle = collections.TemplateTitle
	targets.TemplateCollection = func(bundleID, templateID string, libraryID int, libraryType string) (TemplateDefaults, error) {
		title, input, needsSetup, ok, err := collections.TemplateCollectionDefaults(bundleID, templateID, libraryID, libraryType)
		if err != nil {
			return TemplateDefaults{}, err
		}
		if !ok {
			return TemplateDefaults{}, ErrTemplateNotAvailable
		}
		return TemplateDefaults{Title: title, Input: input, NeedsSetup: needsSetup}, nil
	}
	targets.TemplateCollectionCreated = func(_ context.Context, id string) error {
		if *failSync {
			return errors.New("the provider cannot be reached")
		}
		*synced = append(*synced, id)
		return nil
	}
	targets.CheckSection = (&handlers.SectionHandler{}).CheckRestoredSection
	f.svc.SetRestoreTargets(targets)
}

func seedPages(t *testing.T, f *selectiveFixture) pagesSeed {
	t.Helper()
	ctx, pool := t.Context(), f.pool
	s := pagesSeed{suffix: randomHex(t, 4)}
	s.library = testLibrary(t, pool, "pages-"+s.suffix)
	s.items = []string{"bkpg-" + s.suffix + "-1", "bkpg-" + s.suffix + "-2"}
	for _, id := range s.items {
		testMovie(t, pool, id, "Page "+id, "", s.library)
	}
	repo := catalog.NewLibraryCollectionRepository(pool)
	fromTemplate := func(templateID string, edit func(*catalog.CreateLibraryCollectionInput)) string {
		t.Helper()
		defaults, err := f.svc.targets.TemplateCollection("core_defaults", templateID, s.library, "movies")
		if err != nil {
			t.Fatal(err)
		}
		input := defaults.Input
		edit(&input)
		created, err := repo.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	s.customized = fromTemplate("tmdb_popular_movies", func(in *catalog.CreateLibraryCollectionInput) {
		in.Title, in.Featured, in.SortOrder, in.Visibility = "My popular "+s.suffix, !in.Featured, 7, "hidden"
	})
	s.plain = fromTemplate("tmdb_top_rated_movies", func(in *catalog.CreateLibraryCollectionInput) { in.Description = "Backup description" })
	gone, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: s.library, Slug: "gone-" + s.suffix, Title: "Gone template", CollectionType: "tmdb",
		ManagementMode: managementTemplateBundle, ManagementSource: "core_defaults", ManagementKey: TemplateManagementKey("core_defaults", "retired_template", s.library)})
	if err != nil {
		t.Fatal(err)
	}
	s.gone = gone.ID
	group, err := catalog.NewLibraryCollectionGroupRepository(pool).Create(ctx, catalog.CreateLibraryCollectionGroupInput{LibraryID: s.library, Name: "Picks", Slug: "picks-" + s.suffix})
	if err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `UPDATE library_collection_libraries SET group_id = $2 WHERE collection_id = $1`, s.customized, group.ID)
	managed, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: s.library, Slug: "managed-" + s.suffix, Title: "Managed " + s.suffix,
		ManagementMode: managementSection, ManagementSource: "sections", ManagementKey: "managed-" + s.suffix})
	if err != nil {
		t.Fatal(err)
	}
	s.managedCollection = managed.ID
	for i, id := range s.items {
		if err := repo.AddItem(ctx, managed.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	page := func(scope string, library *int, sectionType, title, config string) string {
		t.Helper()
		created, err := sections.NewRepository(pool).Create(ctx, &sections.PageSection{Scope: scope, LibraryID: library, Position: 900, SectionType: sections.SectionType(sectionType),
			Title: title, ItemLimit: 12, Config: json.RawMessage(config), Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	s.featured = page("home", nil, "collection", "Featured "+s.suffix, `{"library_collection_id":"`+s.customized+`"}`)
	s.recent = page("library", &s.library, "recently_added", "Recent "+s.suffix, `{}`)
	s.managed = page("home", nil, "collection", "Managed "+s.suffix, `{"library_collection_id":"`+managed.ID+`"}`)
	s.layout = page("home", nil, "recently_added", "Layout "+s.suffix, `{}`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM page_sections WHERE title LIKE $1`, "%"+s.suffix)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collections WHERE library_id = $1`, s.library)
	})
	return s
}

// losePages deletes the customized template collection with the section
// that shows it, the managed section with its collection, and the layout
// section; renames the library page section; and changes
// the plain template collection after the backup: its title (not a change
// the backup made, so a restore keeps it) and its description back to the
// template's.
func (f *selectiveFixture) losePages(t *testing.T, s pagesSeed) {
	t.Helper()
	for _, id := range []string{s.featured, s.managed, s.layout} {
		dbExec(t, f.pool, `DELETE FROM page_sections WHERE id = $1`, id)
	}
	dbExec(t, f.pool, `DELETE FROM library_collections WHERE id = ANY($1)`, []string{s.customized, s.managedCollection})
	dbExec(t, f.pool, `UPDATE page_sections SET title = 'Renamed', section_type = 'recently_released' WHERE id = $1`, s.recent)
	dbExec(t, f.pool, `UPDATE library_collections SET title = 'Renamed later', description = '' WHERE id = $1`, s.plain)
}

// TestTemplateCollectionsAndHomeSectionsDB backs up template-bundle
// collections and admin page sections, loses some, and restores them on
// the same server:
//
//   - a collection whose template the catalog no longer has cannot be
//     chosen (template_not_available);
//   - a deleted template collection is created from this server's template
//     through the bundle apply's defaults, with the admin's changes and its
//     group, and owes its sync as a follow-up that a failed sync keeps;
//   - an existing template collection takes only the backup's changes that
//     differ, never fields the admin changed since that the backup left at
//     the template's default;
//   - a deleted home section is created with its id and its collection
//     reference mapped to the recreated collection; a renamed and retyped
//     library page section is replaced; a managed section comes back with
//     its collection and items;
//   - restoring again changes nothing.
func TestTemplateCollectionsAndHomeSectionsDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	failSync, synced := true, []string{}
	wirePageTargets(f, &failSync, &synced)
	s := seedPages(t, f)
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	f.losePages(t, s)
	preview, err := f.svc.Preview(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	selection := checkPagesPreview(t, preview, s)
	req := RestoreRequest{BackupID: source.ID, Digest: preview.Digest, Selection: selection}
	report := f.restoreRequest(t, req)
	for _, id := range append(selection[SectionCollections], selection[SectionHomeSections]...) {
		section := SectionCollections
		if strings.HasPrefix(id, "section:") {
			section = SectionHomeSections
		}
		if got := reportItem(t, report, section, id); got.Outcome != OutcomeRestored {
			t.Fatalf("%s = %+v, want restored", id, got)
		}
	}
	recreated := f.checkRestoredPages(t, s)
	if n := countRows(t, f.pool, `SELECT count(*) FROM backup_restore_followups WHERE kind = 'collection_created' AND target_id::text = $1`, recreated); n != 1 {
		t.Fatalf("a failed sync left %d follow-ups, want it kept", n)
	}
	failSync = false
	f.svc.CompleteFollowUps(ctx)
	if !slices.Contains(synced, recreated) || countRows(t, f.pool, `SELECT count(*) FROM backup_restore_followups WHERE target_id::text = $1`, recreated) != 0 {
		t.Fatalf("the retried sync did not finish: synced %v", synced)
	}
	checkChangesNothing(t, f.restoreRequest(t, req))
}

// TestIdentityTwinsRestoreOneToOneDB: a backup with two library page
// sections of one type and title and two collections of one name in one
// library, restored on another server that lost them, creates each as its
// own item (neither matches the other one the restore just created), and a
// second preview finds all of them the same; a section or collection here
// with that title that is neither makes the twins ambiguous instead of one
// of them taking it.
func TestIdentityTwinsRestoreOneToOneDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, pool := t.Context(), f.pool
	wirePageTargets(f, new(bool), &[]string{})
	suffix := randomHex(t, 4)
	library := testLibrary(t, pool, "twins-"+suffix)
	items := []string{"bktw-" + suffix + "-1", "bktw-" + suffix + "-2"}
	for _, id := range items {
		testMovie(t, pool, id, "Twin "+id, "", library)
	}
	title := "Trending " + suffix
	page := func(featured bool, limit int) string {
		t.Helper()
		created, err := sections.NewRepository(pool).Create(ctx, &sections.PageSection{Scope: pageScopeLibrary, LibraryID: &library, Position: 900,
			SectionType: "recently_added", Title: title, Featured: featured, ItemLimit: limit, Config: json.RawMessage(`{}`), Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	repo := catalog.NewLibraryCollectionRepository(pool)
	collection := func(slug, item string) string {
		t.Helper()
		created, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: library, Slug: slug + suffix, Title: "Picks " + suffix})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.AddItem(ctx, created.ID, item, 0); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	twinSections := []string{page(false, 12), page(true, 24)}
	twinCollections := []string{collection("a-", items[0]), collection("b-", items[1])}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM page_sections WHERE title = $1`, title)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collections WHERE library_id = $1`, library)
	})
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `DELETE FROM page_sections WHERE id = ANY($1)`, twinSections)
	dbExec(t, pool, `DELETE FROM library_collections WHERE id = ANY($1)`, twinCollections)
	f.moveToAnotherServer(t, source.ID)
	ids := map[string][]string{SectionHomeSections: {}, SectionCollections: {}}
	for _, id := range twinSections {
		ids[SectionHomeSections] = append(ids[SectionHomeSections], homeSectionItemID(id))
	}
	for _, id := range twinCollections {
		ids[SectionCollections] = append(ids[SectionCollections], collectionItemID(id))
	}
	previewAll := func(want string) *RestorePreview {
		t.Helper()
		preview, err := f.svc.Preview(ctx, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		for section, list := range ids {
			for _, id := range list {
				if got := previewItem(t, preview, section, id); got.Status != want {
					t.Fatalf("%s = %s %s %q, want %s", id, got.Status, got.Reason, got.Message, want)
				}
			}
		}
		return preview
	}
	// A section and a collection here with the twins' title are neither.
	strangers := []string{page(false, 6), collection("c-", items[0])}
	previewAll(ItemAmbiguous)
	dbExec(t, pool, `DELETE FROM page_sections WHERE id = $1`, strangers[0])
	dbExec(t, pool, `DELETE FROM library_collections WHERE id = $1`, strangers[1])
	preview := previewAll(ItemNotOnThisServer)
	report := f.restoreRequest(t, RestoreRequest{BackupID: source.ID, Digest: preview.Digest, Selection: ids})
	for section, list := range ids {
		for _, id := range list {
			if got := reportItem(t, report, section, id); got.Outcome != OutcomeRestored || got.Message != msgCreated {
				t.Fatalf("%s = %+v, want created", id, got)
			}
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM page_sections WHERE title = $1 AND (featured, item_limit) IN ((false, 12), (true, 24))`, title); n != 2 {
		t.Fatalf("%d twin sections here, want both", n)
	}
	if n := countRows(t, pool, `SELECT count(DISTINCT c.id) FROM library_collections c JOIN library_collection_items i ON i.collection_id = c.id
		WHERE c.title = $1 AND i.media_item_id = ANY($2)`, "Picks "+suffix, items); n != 2 {
		t.Fatalf("%d twin collections here, want both", n)
	}
	previewAll(ItemOnThisServer)
}

func (f *selectiveFixture) restoreRequest(t *testing.T, req RestoreRequest) *SelectiveReport {
	t.Helper()
	payload, _ := json.Marshal(req)
	result, err := f.svc.ExecuteRestore(t.Context(), RestoreRun{Payload: payload, Fenced: (&testFence{pool: f.pool}).run})
	if err != nil {
		t.Fatal(err)
	}
	return result.(*SelectiveReport)
}

// checkPagesPreview checks the preview after the loss and returns the
// selection that restores the lost items.
func checkPagesPreview(t *testing.T, p *RestorePreview, s pagesSeed) map[string][]string {
	t.Helper()
	if got := previewItem(t, p, SectionCollections, templateCollectionItemID(s.gone)); got.Selectable || got.Reason != ReasonTemplateNotAvailable {
		t.Fatalf("collection of a retired template = %+v", got)
	}
	customized := previewItem(t, p, SectionCollections, templateCollectionItemID(s.customized))
	if !customized.Selectable || customized.Status != ItemNotOnThisServer || !strings.HasPrefix(customized.Label, "Popular Movies in ") ||
		!slices.Equal(customized.Collection.Changes, []string{"title", "visibility", "featured", "sort order"}) {
		t.Fatalf("lost template collection = %+v %+v", customized, customized.Collection)
	}
	plain := previewItem(t, p, SectionCollections, templateCollectionItemID(s.plain))
	if plain.Status != ItemDiffers || !slices.Equal(plain.Collection.Changes, []string{"description"}) {
		t.Fatalf("changed template collection = %+v %+v", plain, plain.Collection)
	}
	if got := previewItem(t, p, SectionHomeSections, homeSectionItemID(s.featured)); !got.Selectable ||
		!slices.Equal(got.Requires, []string{SectionCollections + ":" + templateCollectionItemID(s.customized)}) {
		t.Fatalf("section of a lost collection = %+v", got)
	}
	if got := previewItem(t, p, SectionHomeSections, homeSectionItemID(s.recent)); got.Status != ItemDiffers || !slices.Equal(got.HomeSection.Changes, []string{"type", "title"}) {
		t.Fatalf("renamed and retyped library section = %+v %+v", got, got.HomeSection)
	}
	return map[string][]string{
		SectionCollections:  {templateCollectionItemID(s.customized), templateCollectionItemID(s.plain)},
		SectionHomeSections: {homeSectionItemID(s.featured), homeSectionItemID(s.recent), homeSectionItemID(s.managed), homeSectionItemID(s.layout)},
	}
}

// checkRestoredPages checks what the restore wrote and returns the
// recreated template collection's id.
func (f *selectiveFixture) checkRestoredPages(t *testing.T, s pagesSeed) string {
	t.Helper()
	ctx, pool := t.Context(), f.pool
	var id, title, visibility, slug string
	var featured, fromTemplate bool
	var sortOrder int
	if err := pool.QueryRow(ctx, `SELECT c.id, c.title, c.visibility, c.featured, c.sort_order, c.poster_from_template, COALESCE(g.slug, '')
		FROM library_collections c JOIN library_collection_libraries l ON l.collection_id = c.id
		LEFT JOIN library_collection_groups g ON g.id = l.group_id
		WHERE c.management_key = $1`, TemplateManagementKey("core_defaults", "tmdb_popular_movies", s.library)).
		Scan(&id, &title, &visibility, &featured, &sortOrder, &fromTemplate, &slug); err != nil {
		t.Fatalf("recreated template collection: %v", err)
	}
	defaults, err := f.svc.targets.TemplateCollection("core_defaults", "tmdb_popular_movies", s.library, "movies")
	if err != nil {
		t.Fatal(err)
	}
	if id == s.customized || title != "My popular "+s.suffix || visibility != "hidden" || featured == defaults.Input.Featured || sortOrder != 7 ||
		fromTemplate != defaults.Input.PosterFromTemplate || slug != "picks-"+s.suffix {
		t.Fatalf("recreated %s: title %q visibility %q featured %v sort %d from template %v group %q", id, title, visibility, featured, sortOrder, fromTemplate, slug)
	}
	var plainTitle, plainDescription string
	if err := pool.QueryRow(ctx, `SELECT title, description FROM library_collections WHERE id = $1`, s.plain).Scan(&plainTitle, &plainDescription); err != nil ||
		plainTitle != "Renamed later" || plainDescription != "Backup description" {
		t.Fatalf("existing template collection: title %q description %q, %v", plainTitle, plainDescription, err)
	}
	var config string
	if err := pool.QueryRow(ctx, `SELECT config::text FROM page_sections WHERE id = $1`, s.featured).Scan(&config); err != nil || sectionCollectionID(json.RawMessage(config)) != id {
		t.Fatalf("recreated home section config %s (%v), want collection %s", config, err, id)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM page_sections WHERE id = $1 AND title = $2 AND section_type = 'recently_added'`, s.recent, "Recent "+s.suffix); n != 1 {
		t.Fatal("the renamed library section was not replaced")
	}
	var managed string
	if err := pool.QueryRow(ctx, `SELECT config->>'library_collection_id' FROM page_sections WHERE id = $1`, s.managed).Scan(&managed); err != nil ||
		managed == s.managedCollection || countRows(t, pool, `SELECT count(*) FROM library_collections WHERE id = $1 AND management_mode = 'section'`, managed) != 1 ||
		countRows(t, pool, `SELECT count(*) FROM library_collection_items WHERE collection_id = $1`, managed) != len(s.items) {
		t.Fatalf("managed section's collection %q, %v", managed, err)
	}
	return id
}

func dbExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func testLibrary(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	folder, err := catalog.NewFolderRepository(pool).Create(t.Context(), catalog.CreateFolderInput{Paths: []string{dir}, Type: "movies", Name: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folder.ID) })
	return folder.ID
}

func testMovie(t *testing.T, pool *pgxpool.Pool, id, title, tmdb string, library int) {
	t.Helper()
	dbExec(t, pool, `INSERT INTO media_items (content_id, type, title, year, tmdb_id, status) VALUES ($1, 'movie', $2, 2001, $3, 'matched')`, id, title, tmdb)
	if library > 0 {
		dbExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, id) })
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func reportItem(t *testing.T, r *SelectiveReport, section, id string) ReportItem {
	t.Helper()
	for _, item := range r.Items {
		if item.Section == section && item.ID == id {
			return item
		}
	}
	t.Fatalf("report has no %s item %s: %+v", section, id, r.Items)
	return ReportItem{}
}

// moveToAnotherServer gives the database another server id and puts the
// backup where that server finds it, as a backup brought over from the
// original server would be.
func (f *selectiveFixture) moveToAnotherServer(t *testing.T, backupID string) {
	t.Helper()
	ctx := t.Context()
	from := folderPrefix(f.serverID, backupID)
	f.serverID = "elsewhere-" + randomHex(t, 4)
	objects, _, err := f.store.List(ctx, from, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objects {
		body, _, err := f.store.Get(ctx, obj.Key)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.Put(ctx, folderPrefix(f.serverID, backupID)+strings.TrimPrefix(obj.Key, from), data); err != nil {
			t.Fatal(err)
		}
	}
}

// checkChangesNothing requires a re-run's report to show nothing restored
// or failed.
func checkChangesNothing(t *testing.T, again *SelectiveReport) {
	t.Helper()
	for _, item := range again.Items {
		if item.Outcome == OutcomeRestored || item.Outcome == OutcomeFailed {
			t.Fatalf("restoring again: %s %s", item.ID, item.Outcome)
		}
	}
}
