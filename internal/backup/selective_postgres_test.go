package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
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
		if err := store.Put(ctx, key, testImage(t, data)); err != nil {
			t.Fatal(err)
		}
	}
	setSetting(t, pool, settings, seed.settingKey, "17")
	setSetting(t, pool, settings, config.AccessUnratedContentSettingKey, config.AccessUnratedContentAllow)
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
	// beforeSettings runs ahead of each settings write, before its lock.
	beforeSettings func()
}

func newSelectiveFixture(t *testing.T) *selectiveFixture {
	t.Helper()
	pool, dsn := backuptest.Database(t)
	return newSelectiveFixtureOn(t, pool, dsn)
}

// newSelectiveFixtureOn is newSelectiveFixture with every part on pool.
func newSelectiveFixtureOn(t *testing.T, pool *pgxpool.Pool, dsn string) *selectiveFixture {
	t.Helper()
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
	manifestKey, err := secret.BackupManifestKey(masterKey)
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
		Pool: pool, DatabaseURL: dsn, Settings: f.settings, Cipher: cipher, KeyCheck: keyCheck, ManifestKey: manifestKey,
		Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: NewStoreDestination(store),
		Locker:    PostgresLocker{Pool: pool},
		ServerID:  func(context.Context) (string, error) { return f.serverID, nil },
		FindTools: func(context.Context) (Tools, error) { return tools, nil },
	})
	f.svc.SetRestoreTargets(RestoreTargets{
		CheckFolder:  func(_ context.Context, path string) error { _, err := os.Stat(path); return err },
		TrackArtwork: catalog.NewArtworkRevisionTracker(pool).TrackArtworkRevision,
		ApplySettings: func(ctx context.Context, tx pgx.Tx, values map[string]string, check func(map[string]string) error) ([]string, func(context.Context), error) {
			if f.beforeSettings != nil {
				f.beforeSettings()
			}
			err := f.settings.UpdateAtomicWithTx(ctx, tx, func(stored map[string]string, _ pgx.Tx) (map[string]string, error) {
				if check != nil {
					if err := check(stored); err != nil {
						return nil, err
					}
				}
				return values, nil
			})
			return nil, func(context.Context) {}, err
		},
		LibrarySectionsCreated: func(context.Context, int) error { return nil },
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
	if err := f.settings.Set(ctx, config.AccessUnratedContentSettingKey, config.AccessUnratedContentHide); err != nil {
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
	// Showing unrated titles to rating-capped profiles again would widen
	// access: the stricter live value stays.
	library := previewItem(t, preview, SectionSettings, pageLibrary)
	for _, s := range library.Settings {
		if s.Key == config.AccessUnratedContentSettingKey && s.Status != SettingKeptStricter {
			t.Fatalf("unrated-content setting in the preview = %+v, want kept_stricter", s)
		}
	}
	if library.Selectable || library.Reason != ReasonKeptStricter {
		t.Fatalf("library settings page = %+v, want unselectable, kept_stricter", library)
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
	// The restored artwork's objects are registered with the revision
	// collector under their exact manifest.
	if n := countRows(t, f.pool, `SELECT count(*) FROM artwork_revision_gc_candidates WHERE original_path = $1 AND $1 = ANY(object_keys)`, seed.artwork); n != 1 {
		t.Fatalf("restored artwork has %d revision registrations with its manifest, want 1", n)
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

// checkMissingFilesRestored: item artwork the catalog still shows, whose
// file is gone from storage, previews as differing; restoring it writes the
// file back without touching the catalog, and the next preview finds it
// the same.
func (f *selectiveFixture) checkMissingFilesRestored(t *testing.T, seed selectiveSeed, backupID, artworkRef string) {
	t.Helper()
	ctx := t.Context()
	body, _, err := f.store.Get(ctx, seed.artwork)
	if err != nil {
		t.Fatal(err)
	}
	original, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Delete(ctx, []string{seed.artwork}); err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.Preview(ctx, backupID, false)
	if err != nil {
		t.Fatal(err)
	}
	if item := previewItem(t, preview, SectionCollections, artworkRef); item.Status != ItemDiffers || !item.Selectable || item.Reason != ReasonFilesMissingHere {
		t.Fatalf("artwork whose file is gone = %+v, want differing with %s", item, ReasonFilesMissingHere)
	}
	report, err := f.restore(t, backupID, preview.Digest, map[string][]string{SectionCollections: {artworkRef}}, &testFence{pool: f.pool})
	if err != nil || reportOutcomes(report)[SectionCollections+":"+artworkRef] != OutcomeRestored {
		t.Fatalf("restoring the missing file = %v (%+v)", err, report.Items)
	}
	body, _, err = f.store.Get(ctx, seed.artwork)
	if err != nil {
		t.Fatalf("the file was not written back: %v", err)
	}
	restored, _ := io.ReadAll(body)
	_ = body.Close()
	if !bytes.Equal(restored, original) {
		t.Fatal("the file written back differs from the backup's")
	}
	if preview, err = f.svc.Preview(ctx, backupID, false); err != nil {
		t.Fatal(err)
	}
	if item := previewItem(t, preview, SectionCollections, artworkRef); item.Status != ItemOnThisServer || item.Reason != ReasonSame {
		t.Fatalf("artwork after its file came back = %+v, want the same", item)
	}
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
//
// TestRestoreRechecksAccessSettingsUnderTheLockDB: an access setting an
// admin makes stricter after the restore planned its page, but before the
// settings write takes its lock, keeps the admin's value. The item starts
// again from its read and leaves the setting out.
func TestRestoreRechecksAccessSettingsUnderTheLockDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	const key = config.AuthRefreshTokenExpirySettingKey
	setSetting(t, f.pool, f.settings, key, "7d")
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, key, "30d"); err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.Preview(ctx, source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	page := previewItem(t, preview, SectionSettings, pageSignIn)
	if !page.Selectable {
		t.Fatalf("sign-in page before the edit = %+v, want a stricter value to restore", page)
	}
	edited := false
	f.beforeSettings = func() {
		if !edited {
			edited = true
			if err := f.settings.Set(context.Background(), key, "1d"); err != nil {
				t.Error(err)
			}
		}
	}
	report, err := f.restore(t, source.ID, preview.Digest, map[string][]string{SectionSettings: {pageSignIn}}, &testFence{pool: f.pool})
	if err != nil {
		t.Fatal(err)
	}
	if item := reportItem(t, report, SectionSettings, pageSignIn); item.Outcome != OutcomeLeftOut || item.Reason != ReasonKeptStricter {
		t.Fatalf("sign-in page = %+v, want left out, kept_stricter", item)
	}
	if got, err := f.settings.Get(ctx, key); err != nil || got != "1d" {
		t.Fatalf("%s = %q, %v; want the admin's 1d", key, got, err)
	}
}

// TestRestoreNeverSendsLiveSecretsToARestoredAddressDB: restoring a mail
// server address the backup has no password for clears the password here
// and turns email off, rather than sending this server's password to the
// backup's server; a backup this server cannot verify restores no setting
// that connects to an outside service at all.
func TestRestoreNeverSendsLiveSecretsToARestoredAddressDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	const host, password, enabled = "email.smtp_host", "email.smtp_password", "email.enabled"
	dbExec(t, f.pool, `DELETE FROM server_settings WHERE key = $1`, password)
	setSetting(t, f.pool, f.settings, host, "smtp.backup.example")
	setSetting(t, f.pool, f.settings, enabled, "true")
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, host, "mail.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, password, "the live password"); err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.Preview(ctx, source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	page := previewItem(t, preview, SectionSettings, pageEmail)
	cleared := map[string]bool{}
	for _, s := range page.Settings {
		cleared[s.Key] = s.Status == SettingCleared
	}
	if !page.Selectable || !cleared[password] || !cleared[enabled] {
		t.Fatalf("email page = %+v, want the password cleared and email turned off", page)
	}
	report, err := f.restore(t, source.ID, preview.Digest, map[string][]string{SectionSettings: {pageEmail}}, &testFence{pool: f.pool})
	if err != nil {
		t.Fatal(err)
	}
	if item := reportItem(t, report, SectionSettings, pageEmail); item.Outcome != OutcomeRestored {
		t.Fatalf("email page = %+v", item)
	}
	for key, want := range map[string]string{host: "smtp.backup.example", password: "", enabled: "false"} {
		if got, _ := f.settings.Get(ctx, key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}

	// The same backup under another key: nothing that reaches a service.
	f.unverify(t, source.ID)
	if err := f.settings.Set(ctx, host, "mail.example.com"); err != nil {
		t.Fatal(err)
	}
	unverified, err := f.svc.Preview(ctx, source.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if page := previewItem(t, unverified, SectionSettings, pageEmail); page.Selectable || page.Reason != ReasonUnverifiedBackup {
		t.Fatalf("email page of an unverified backup = %+v, want unselectable, unverified_backup", page)
	}
}

// unverify rewrites the backup's manifest with another key's check value,
// so this server cannot verify it.
func (f *selectiveFixture) unverify(t *testing.T, backupID string) {
	t.Helper()
	ctx := t.Context()
	manifestKey := objectKey(f.serverID, backupID, ManifestName)
	body, _, err := f.store.Get(ctx, manifestKey)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	_ = body.Close()
	m, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	m.KeyCheck = "9999-8888-7777-6666"
	if data, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Put(ctx, manifestKey, data); err != nil {
		t.Fatal(err)
	}
}

// sizedFence is a testFence that records how many bytes of result each
// commit wrote.
type sizedFence struct {
	testFence
	written []int
}

func (f *sizedFence) run(ctx context.Context, fn func(pgx.Tx) (FencedUpdate, error)) error {
	return f.testFence.run(ctx, func(tx pgx.Tx) (FencedUpdate, error) {
		update, err := fn(tx)
		data, _ := json.Marshal(update.Result)
		f.written = append(f.written, len(data))
		return update, err
	})
}

// TestRestoreItemWritesDoNotGrowDB: each item's commit writes a result the
// same size whatever its place in the restore (counts, the library map, the
// latest outcomes), each outcome is a row of its own, and the report the
// restore returns lists every item, its rows removed.
func TestRestoreItemWritesDoNotGrowDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.Preview(ctx, source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 300)
	for i := range ids {
		ids[i] = strconv.Itoa(900000 + i) // not in the backup: left out, each its own item
	}
	fence := &sizedFence{testFence: testFence{pool: f.pool}}
	payload, _ := json.Marshal(RestoreRequest{BackupID: source.ID, Digest: preview.Digest, Selection: map[string][]string{SectionLibraries: ids}})
	result, err := f.svc.ExecuteRestore(ctx, RestoreRun{JobID: "items-" + randomHex(t, 4), Payload: payload, Fenced: fence.run})
	if err != nil {
		t.Fatal(err)
	}
	report := result.(*SelectiveReport)
	if len(report.Items) != len(ids) {
		t.Fatalf("report lists %d items, want %d", len(report.Items), len(ids))
	}
	early, late := fence.written[recentItems+2], fence.written[len(fence.written)-1]
	if late > early+early/4 {
		t.Fatalf("the last item's commit wrote %d bytes, the %dth %d: writes grow with the item's place", late, recentItems+2, early)
	}
	if n := countRows(t, f.pool, `SELECT count(*) FROM backup_restore_items`); n != 0 {
		t.Fatalf("%d outcome rows left after the restore", n)
	}
}

// TestUnverifiedRestoreLeavesCostlyWorkOffDB: a verified backup's preview
// names the options of a library that send its data to outside services or
// run long jobs; restored from a backup this server cannot verify, the
// library is created with them off, and the preview says so.
func TestUnverifiedRestoreLeavesCostlyWorkOffDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	seed := seedSelective(t, f.pool, f.settings, f.store)
	dbExec(t, f.pool, `UPDATE media_folders SET auto_translate_metadata = true, trickplay_enabled = true, intro_detection_enabled = true WHERE id = $1`,
		seed.library2ID)
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	f.lose(t, seed)
	ref := strconv.Itoa(seed.library2ID)
	verified, err := f.svc.Preview(ctx, source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	const costly = "AI translation of metadata, trickplay previews, and intro detection"
	if item := previewItem(t, verified, SectionLibraries, ref); !item.Selectable || item.Message != "Restoring it turns on "+costly+"." {
		t.Fatalf("library of a verified backup = %+v, want its costly options named", item)
	}
	f.unverify(t, source.ID)
	unverified, err := f.svc.Preview(ctx, source.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if item := previewItem(t, unverified, SectionLibraries, ref); !item.Selectable || !strings.HasPrefix(item.Message, "Restored with "+costly+" off") {
		t.Fatalf("library of an unverified backup = %+v, want its costly options off", item)
	}
	report := f.restoreRequest(t, RestoreRequest{BackupID: source.ID, Digest: unverified.Digest,
		Selection: map[string][]string{SectionLibraries: {ref}}, AcceptUnauthenticated: true})
	var translate, trickplay, intro bool
	if err := f.pool.QueryRow(ctx, `SELECT auto_translate_metadata, trickplay_enabled, intro_detection_enabled FROM media_folders WHERE id = $1`,
		report.LibraryMap[ref]).Scan(&translate, &trickplay, &intro); err != nil {
		t.Fatalf("restored library %v: %v (%+v)", report.LibraryMap, err, report.Items)
	}
	if translate || trickplay || intro {
		t.Fatalf("library restored from an unverified backup: translate %v, trickplay %v, intro detection %v; want all off", translate, trickplay, intro)
	}
}

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
	preview, err := f.svc.Preview(ctx, source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ section, id string }{{SectionLibraries, libraryRef}, {SectionCollections, collectionRef}, {SectionCollections, artworkRef}} {
		if item := previewItem(t, preview, c.section, c.id); item.Status != ItemOnThisServer || item.Selectable {
			t.Fatalf("%s %s before the loss = %+v, want on this server", c.section, c.id, item)
		}
	}
	f.checkMissingFilesRestored(t, seed, source.ID, artworkRef)
	f.lose(t, seed)
	if preview, err = f.svc.Preview(ctx, source.ID, false); err != nil {
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
	// The library's set-up and its sections, which wait for the end of the
	// restore.
	if n := f.pendingFollowUps(t, []int{newLibrary}); n != 2 {
		t.Fatalf("owed follow-ups after the interruption = %d, want 2", n)
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
	// library2 is a second library an admin added to both template
	// collections' scope.
	library2 int
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
	s.library2 = testLibrary(t, pool, "pages2-"+s.suffix)
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
		in.LibraryIDs = []int{s.library, s.library2}
	})
	s.plain = fromTemplate("tmdb_top_rated_movies", func(in *catalog.CreateLibraryCollectionInput) {
		in.Description, in.LibraryIDs = "Backup description", []int{s.library, s.library2}
	})
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
	dbExec(t, pool, `UPDATE library_collection_libraries SET group_id = $2 WHERE collection_id = $1 AND library_id = $3`, s.customized, group.ID, s.library)
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
	dbExec(t, f.pool, `DELETE FROM library_collection_libraries WHERE collection_id = $1 AND library_id = $2`, s.plain, s.library2)
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
	preview, err := f.svc.Preview(ctx, source.ID, false)
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

// TestManagedSectionPointedElsewhereIsRestoredDB: a managed section an
// admin pointed at an ordinary collection after the backup differs from the
// backup even when that collection holds the same items; restoring it
// creates the managed collection again, and a second preview finds it the
// same.
func TestManagedSectionPointedElsewhereIsRestoredDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, pool := t.Context(), f.pool
	wirePageTargets(f, new(bool), &[]string{})
	s := seedPages(t, f)
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	repo := catalog.NewLibraryCollectionRepository(pool)
	ordinary, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: s.library, Slug: "ordinary-" + s.suffix, Title: "Ordinary " + s.suffix})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range s.items {
		if err := repo.AddItem(ctx, ordinary.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	dbExec(t, pool, `UPDATE page_sections SET config = jsonb_build_object('library_collection_id', $2::text) WHERE id = $1`, s.managed, ordinary.ID)
	preview, err := f.svc.Preview(ctx, source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	id := homeSectionItemID(s.managed)
	if got := previewItem(t, preview, SectionHomeSections, id); got.Status != ItemDiffers || !slices.Contains(got.HomeSection.Changes, "collection replaced") {
		t.Fatalf("section pointed at an ordinary collection = %+v %+v", got, got.HomeSection)
	}
	report := f.restoreRequest(t, RestoreRequest{BackupID: source.ID, Digest: preview.Digest, Selection: map[string][]string{SectionHomeSections: {id}}})
	if got := reportItem(t, report, SectionHomeSections, id); got.Outcome != OutcomeRestored {
		t.Fatalf("restoring it = %+v", got)
	}
	var shown string
	if err := pool.QueryRow(ctx, `SELECT config->>'library_collection_id' FROM page_sections WHERE id = $1`, s.managed).Scan(&shown); err != nil ||
		shown == ordinary.ID || countRows(t, pool, `SELECT count(*) FROM library_collections WHERE id = $1 AND management_mode = 'section'`, shown) != 1 {
		t.Fatalf("restored section shows %q (%v), want a managed collection", shown, err)
	}
	again, err := f.svc.Preview(ctx, source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := previewItem(t, again, SectionHomeSections, id); got.Status != ItemOnThisServer || got.Reason != ReasonSame {
		t.Fatalf("after the restore = %+v %+v", got, got.HomeSection)
	}
}

// TestUnverifiedSectionCollectionHasNoScheduleDB: a section's carried
// collection that a restore from a backup this server cannot verify creates
// keeps its source but gets no sync schedule, as every collection the
// restore creates (applier.createCollection), and the preview says so.
func TestUnverifiedSectionCollectionHasNoScheduleDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, pool := t.Context(), f.pool
	wirePageTargets(f, new(bool), &[]string{})
	s := seedPages(t, f)
	const source, daily = "https://mdblist.com/lists/someone/list", "30 4 * * *"
	dbExec(t, pool, `UPDATE library_collections SET collection_type = 'mdblist', source_url = $2, sync_schedule = $3 WHERE id = $1`,
		s.managedCollection, source, daily)
	backup, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	// The section now shows another collection, so the restore creates
	// the carried one again.
	repo := catalog.NewLibraryCollectionRepository(pool)
	ordinary, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: s.library, Slug: "ordinary-" + s.suffix, Title: "Ordinary " + s.suffix})
	if err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `UPDATE page_sections SET config = jsonb_build_object('library_collection_id', $2::text) WHERE id = $1`, s.managed, ordinary.ID)
	dbExec(t, pool, `DELETE FROM library_collections WHERE id = $1`, s.managedCollection)
	f.unverify(t, backup.ID)
	preview, err := f.svc.Preview(ctx, backup.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	id := homeSectionItemID(s.managed)
	if got := previewItem(t, preview, SectionHomeSections, id); !got.Selectable || !strings.Contains(got.Message, "A collection it creates gets no sync schedule") {
		t.Fatalf("section of an unverified backup = %+v, want its collection's schedule withheld", got)
	}
	report := f.restoreRequest(t, RestoreRequest{BackupID: backup.ID, Digest: preview.Digest,
		Selection: map[string][]string{SectionHomeSections: {id}}, AcceptUnauthenticated: true})
	got := reportItem(t, report, SectionHomeSections, id)
	if got.Outcome != OutcomeRestored || !slices.Contains(got.Notes, msgScheduleWithheld) {
		t.Fatalf("restoring it = %+v", got)
	}
	var url string
	var schedule *string
	if err := pool.QueryRow(ctx, `SELECT c.source_url, c.sync_schedule FROM page_sections p
		JOIN library_collections c ON c.id = p.config->>'library_collection_id' WHERE p.id = $1`, s.managed).Scan(&url, &schedule); err != nil {
		t.Fatal(err)
	}
	// Kept, in the canonical form the editors store.
	if url != source+"/json" || schedule != nil {
		t.Fatalf("restored collection: source %q, schedule %v; want the source kept and no schedule", url, schedule)
	}
}

// TestRestoreRefusesCollectionSourcesTheEditorWouldDB: a collection whose
// source the editors would not store (an MDBList collection whose list is
// on loopback) is refused by the preview, carried by a section or not.
func TestRestoreRefusesCollectionSourcesTheEditorWouldDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, pool := t.Context(), f.pool
	wirePageTargets(f, new(bool), &[]string{})
	s := seedPages(t, f)
	dbExec(t, pool, `UPDATE library_collections SET collection_type = 'mdblist', source_url = 'http://127.0.0.1:9/lists/a/b',
		source_config = '{"mode":"mdblist_json","url":"http://127.0.0.1:9/lists/a/b"}' WHERE id = $1`, s.managedCollection)
	backup, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `DELETE FROM library_collections WHERE id = $1`, s.managedCollection)
	preview, err := f.svc.Preview(ctx, backup.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := previewItem(t, preview, SectionHomeSections, homeSectionItemID(s.managed)); got.Selectable || got.Reason != ReasonUnreadable ||
		!strings.Contains(got.Message, "not one this server accepts") {
		t.Fatalf("section whose collection's list is on loopback = %+v, want refused", got)
	}
}

// TestManagedCollectionKeysMapLibrariesDB: a section-managed collection's
// management key names its library; restored where the backup's library is
// another one here, the key names that one, and a collection here holding
// the backup's key for the library with the backup's old id is not reused.
func TestManagedCollectionKeysMapLibrariesDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, pool := t.Context(), f.pool
	wirePageTargets(f, new(bool), &[]string{})
	s := seedPages(t, f)
	keyFor := func(library int) string { return "trakt:trending:movie:library:" + strconv.Itoa(library) }
	dbExec(t, pool, `UPDATE library_collections SET management_key = $2 WHERE id = $1`, s.managedCollection, keyFor(s.library))
	backup, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	// Here the backup's library has another id: the library with its old
	// id now has other folders, and a new one has the backup's.
	var folder string
	if err := pool.QueryRow(ctx, `SELECT path FROM media_folder_paths WHERE media_folder_id = $1`, s.library).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `UPDATE media_folder_paths SET path = $2 WHERE media_folder_id = $1`, s.library, folder+"-elsewhere")
	moved, err := catalog.NewFolderRepository(pool).Create(ctx, catalog.CreateFolderInput{Paths: []string{folder}, Type: "movies", Name: "Moved " + s.suffix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, moved.ID) })
	// The section shows another collection, so the restore looks for its
	// own by key.
	repo := catalog.NewLibraryCollectionRepository(pool)
	ordinary, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: s.library, Slug: "ordinary-" + s.suffix, Title: "Ordinary " + s.suffix})
	if err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `UPDATE page_sections SET config = jsonb_build_object('library_collection_id', $2::text) WHERE id = $1`, s.managed, ordinary.ID)
	dbExec(t, pool, `DELETE FROM library_collection_items WHERE collection_id = $1`, s.managedCollection)
	preview, err := f.svc.Preview(ctx, backup.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	id := homeSectionItemID(s.managed)
	report := f.restoreRequest(t, RestoreRequest{BackupID: backup.ID, Digest: preview.Digest, Selection: map[string][]string{SectionHomeSections: {id}}})
	if got := reportItem(t, report, SectionHomeSections, id); got.Outcome != OutcomeRestored {
		t.Fatalf("restoring it = %+v", got)
	}
	var shown, key string
	var library int
	if err := pool.QueryRow(ctx, `SELECT c.id, c.management_key, c.library_id FROM page_sections p
		JOIN library_collections c ON c.id = p.config->>'library_collection_id' WHERE p.id = $1`, s.managed).Scan(&shown, &key, &library); err != nil {
		t.Fatal(err)
	}
	if shown == s.managedCollection || key != keyFor(moved.ID) || library != moved.ID {
		t.Fatalf("restored section shows %s (key %q, library %d), want a new collection in library %d keyed for it", shown, key, library, moved.ID)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM library_collection_items WHERE collection_id = $1`, s.managedCollection); n != 0 {
		t.Fatalf("the other library's collection got %d of the backup's items", n)
	}
}

// TestRestoredLibrarySectionsWaitForTheRestoreDB: a library the restore
// recreates gets its default sections only after the restore wrote its saved
// ones, and only where it wrote none. A default renamed before the backup
// then comes back once, not beside a freshly seeded default.
func TestRestoredLibrarySectionsWaitForTheRestoreDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, pool := t.Context(), f.pool
	wirePageTargets(f, new(bool), &[]string{})
	seed := func(ctx context.Context, id int) error {
		return sections.NewRepository(pool).SeedDefaults(ctx, pageScopeLibrary, &id, sections.DefaultLibrarySectionsForType(&id, "movies"))
	}
	targets := f.svc.targets
	targets.LibrarySectionsCreated = seed
	f.svc.SetRestoreTargets(targets)
	library := testLibrary(t, pool, "seeded-"+randomHex(t, 4))
	if err := seed(ctx, library); err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `UPDATE page_sections SET title = 'Renamed before the backup'
		WHERE id = (SELECT id FROM page_sections WHERE library_id = $1 ORDER BY position LIMIT 1)`, library)
	saved := countRows(t, pool, `SELECT count(*) FROM page_sections WHERE library_id = $1`, library)
	// A template collection of the library, created again before its
	// sections: its follow-up runs before theirs are written.
	defaults, err := f.svc.targets.TemplateCollection("core_defaults", "tmdb_popular_movies", library, "movies")
	if err != nil {
		t.Fatal(err)
	}
	template, err := catalog.NewLibraryCollectionRepository(pool).Create(ctx, defaults.Input)
	if err != nil {
		t.Fatal(err)
	}
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, library)
	preview, err := f.svc.Preview(ctx, source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	selection := map[string][]string{SectionLibraries: {strconv.Itoa(library)}, SectionCollections: {templateCollectionItemID(template.ID)}}
	for _, section := range preview.Sections {
		for _, item := range section.Items {
			if section.ID == SectionHomeSections && item.Selectable && item.HomeSection != nil && item.HomeSection.Scope == pageScopeLibrary {
				selection[SectionHomeSections] = append(selection[SectionHomeSections], item.ID)
			}
		}
	}
	report := f.restoreRequest(t, RestoreRequest{BackupID: source.ID, Digest: preview.Digest, Selection: selection})
	restored := report.LibraryMap[strconv.Itoa(library)]
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, restored) })
	if got := report.Counts[OutcomeRestored]; got != len(selection[SectionLibraries])+len(selection[SectionCollections])+len(selection[SectionHomeSections]) {
		t.Fatalf("restored %d items, want every selected one: %+v", got, report.Items)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM page_sections WHERE library_id = $1`, restored); restored == 0 || n != saved {
		t.Fatalf("restored library %d has %d sections, want the %d saved ones and no seeded defaults beside them", restored, n, saved)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM backup_restore_followups WHERE target_id = $1`, restored); n != 0 {
		t.Fatalf("%d follow-ups left for the restored library", n)
	}
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
		preview, err := f.svc.Preview(ctx, source.ID, false)
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
	if plain.Status != ItemDiffers || !slices.Equal(plain.Collection.Changes, []string{"description", "libraries"}) {
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
		WHERE c.management_key = $1 AND l.library_id = $2`, TemplateManagementKey("core_defaults", "tmdb_popular_movies", s.library), s.library).
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
	for _, c := range []string{id, s.plain} {
		if n := countRows(t, pool, `SELECT count(*) FROM library_collection_libraries WHERE collection_id = $1 AND library_id = ANY($2)`,
			c, []int{s.library, s.library2}); n != 2 {
			t.Fatalf("template collection %s is in %d of its 2 libraries", c, n)
		}
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
		if path.Base(obj.Key) == ManifestName {
			// The copy is a backup of this server now: its manifest names it,
			// signed with this server's key as a backup here writes it.
			m, err := ParseManifest(data)
			if err != nil {
				t.Fatal(err)
			}
			m.ServerID = f.serverID
			if err := m.sign(f.svc.opts.ManifestKey); err != nil {
				t.Fatal(err)
			}
			if data, err = json.Marshal(m); err != nil {
				t.Fatal(err)
			}
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

// TestSelectiveRestoreOnOneConnectionDB: Silo may run with a pool of one
// connection, and a restore item's fenced transaction holds it, so nothing
// an item does may wait on the pool. A restore of every item a preview
// offers, libraries, settings, collections with their template and
// artwork, and page sections, runs on such a pool with nothing cached and
// finishes; any pool use inside an item would hang until the deadline.
func TestSelectiveRestoreOnOneConnectionDB(t *testing.T) {
	_, dsn := backuptest.Database(t)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := newSelectiveFixtureOn(t, pool, dsn)
	wirePageTargets(f, new(bool), &[]string{})
	seed := seedSelective(t, f.pool, f.settings, f.store)
	pages := seedPages(t, f)
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	f.lose(t, seed)
	f.losePages(t, pages)
	// A fresh service: no restore set, settings, or installation cached.
	targets := f.svc.targets
	f.svc = New(f.svc.opts)
	f.svc.SetRestoreTargets(targets)
	preview, err := f.svc.Preview(t.Context(), source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	selection := map[string][]string{}
	for _, section := range preview.Sections {
		for _, item := range section.Items {
			if item.Selectable {
				selection[section.ID] = append(selection[section.ID], item.ID)
			}
		}
	}
	if len(selection[SectionLibraries]) == 0 || len(selection[SectionCollections]) == 0 || len(selection[SectionHomeSections]) == 0 {
		t.Fatalf("selection %v leaves out a section", selection)
	}
	done := make(chan *SelectiveReport, 1)
	go func() {
		report, _ := f.restore(t, source.ID, preview.Digest, selection, &testFence{pool: f.pool})
		done <- report
	}()
	select {
	case report := <-done:
		if report == nil || report.Error != "" || report.Counts[OutcomeFailed] != 0 {
			t.Fatalf("restore on one connection = %+v", report)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("a restore item waited on the pool its own transaction holds")
	}
}
