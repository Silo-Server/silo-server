package handlers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// flakyScanQueue fails the first scan it is asked to queue.
type flakyScanQueue struct {
	libraryScanQueuer
	calls  int
	queued []int
}

func (q *flakyScanQueue) EnqueueLibraryScan(_ context.Context, folderID int, _ string) (bool, error) {
	q.calls++
	if q.calls == 1 {
		return false, errors.New("scan queue unavailable")
	}
	q.queued = append(q.queued, folderID)
	return true, nil
}

// TestLibraryRestoredReportsFailedSetupDB: a restore deletes its follow-up
// only when the library's setup succeeded, so a step that fails must be
// returned, not just logged. The other steps still run, and running it again
// completes the failed one without seeding sections twice.
func TestLibraryRestoredReportsFailedSetupDB(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "restored")
	folders := catalog.NewFolderRepository(pool)
	folder, err := folders.Create(ctx, catalog.CreateFolderInput{Paths: []string{path}, Type: "movies", Name: "Restored " + filepath.Base(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folder.ID) })
	queue := &flakyScanQueue{}
	h := &LibraryHandler{folderRepo: folders, SectionRepo: sections.NewRepository(pool), ScanQueue: queue}
	countSections := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM page_sections WHERE library_id = $1`, folder.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := h.LibraryRestored(ctx, folder.ID); err == nil {
		t.Fatal("a failed first scan was reported as success")
	}
	seeded := countSections()
	if seeded == 0 {
		t.Fatal("the other steps did not run")
	}
	if err := h.LibraryRestored(ctx, folder.ID); err != nil {
		t.Fatalf("retry = %v", err)
	}
	if len(queue.queued) != 1 || queue.queued[0] != folder.ID {
		t.Fatalf("queued scans = %v", queue.queued)
	}
	if n := countSections(); n != seeded {
		t.Fatalf("retry seeded sections again: %d, then %d", seeded, n)
	}
}

// insertMetadataProvider adds an enabled plugin installation with one
// metadata provider capability, removed when the test ends.
func insertMetadataProvider(t *testing.T, pool *pgxpool.Pool, pluginID string) int {
	t.Helper()
	ctx := t.Context()
	var id int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled) VALUES ($1, '1.0.0', '/nonexistent', true) RETURNING id`, pluginID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id) })
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata) VALUES ($1, 'metadata_provider.v1', 'meta', '{}')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func chainHas(t *testing.T, pool *pgxpool.Pool, folderID, installationID int) (entries int, has bool) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `SELECT count(*), COALESCE(bool_or(plugin_installation_id = $2), false) FROM library_provider_chains WHERE media_folder_id = $1`, folderID, installationID).Scan(&entries, &has); err != nil {
		t.Fatal(err)
	}
	return entries, has
}

// failingPlacement fails every provider lookup, as a database error would.
func failingPlacement(context.Context, *pgxpool.Pool, int, string, string) (metadata.SeedPlacement, error) {
	return metadata.SeedPlacement{}, errors.New("reading capability metadata failed")
}

// TestRestoredLibraryChainNeedsItsProviderLookupDB: a restored library's
// default chain is built from each provider's manifest placement. When that
// lookup fails, no fallback chain is saved and the restore keeps its
// follow-up; the retry saves the chain from the manifest and clears it.
func TestRestoredLibraryChainNeedsItsProviderLookupDB(t *testing.T) {
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
	suffix := filepath.Base(t.TempDir())
	provider := insertMetadataProvider(t, pool, "test.placement-"+suffix)
	folders := catalog.NewFolderRepository(pool)
	folder, err := folders.Create(ctx, catalog.CreateFolderInput{Paths: []string{filepath.Join(t.TempDir(), "restored")}, Type: "movies", Name: "Placement " + suffix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folder.ID) })
	h := &LibraryHandler{folderRepo: folders, ChainRepo: metadata.NewChainRepository(pool), ScanQueue: &flakyScanQueue{calls: 1}, seedPlacement: failingPlacement}
	svc := backup.New(backup.Options{Pool: pool})
	svc.SetRestoreTargets(backup.RestoreTargets{LibraryCreated: h.LibraryRestored})
	if _, err := pool.Exec(ctx, `INSERT INTO backup_restore_followups (kind, target_id) VALUES ('library_created', $1)`, folder.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM backup_restore_followups WHERE target_id = $1`, folder.ID)
	})
	owed := func() bool {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backup_restore_followups WHERE kind = 'library_created' AND target_id = $1)`, folder.ID).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}

	svc.CompleteFollowUps(ctx)
	if entries, _ := chainHas(t, pool, folder.ID, provider); entries != 0 || !owed() {
		t.Fatalf("after a failed lookup: %d chain entries, follow-up owed %v; want none and owed", entries, owed())
	}
	h.seedPlacement = nil
	svc.CompleteFollowUps(ctx)
	if _, has := chainHas(t, pool, folder.ID, provider); !has || owed() {
		t.Fatalf("after the retry: provider in chain %v, follow-up owed %v", has, owed())
	}
}

// TestRestoredPluginJoinsChainsOnlyFromItsManifestDB: a restored metadata
// provider joins existing library chains at its manifest placement. A failed
// lookup appends nothing and is returned, so the restore retries it.
func TestRestoredPluginJoinsChainsOnlyFromItsManifestDB(t *testing.T) {
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
	suffix := filepath.Base(t.TempDir())
	existing := insertMetadataProvider(t, pool, "test.existing-"+suffix)
	restored := insertMetadataProvider(t, pool, "test.restored-"+suffix)
	folder, err := catalog.NewFolderRepository(pool).Create(ctx, catalog.CreateFolderInput{Paths: []string{filepath.Join(t.TempDir(), "lib")}, Type: "movies", Name: "Chain " + suffix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folder.ID) })
	chains := metadata.NewChainRepository(pool)
	if err := chains.SetChain(ctx, folder.ID, []metadata.ChainEntry{{PluginInstallationID: existing, CapabilityID: "meta", CapabilityType: "metadata_provider.v1", ContentLevel: "movie", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	installations := plugins.NewInstallationStore(pool)
	installation, err := installations.GetByID(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	h := &PluginHandler{installations: installations, chainRepo: chains, seedPlacement: failingPlacement}
	if err := h.addMetadataProvidersToChains(ctx, installation); err == nil {
		t.Fatal("a failed placement lookup was reported as success")
	}
	if entries, has := chainHas(t, pool, folder.ID, restored); has || entries != 1 {
		t.Fatalf("after a failed lookup the chain has %d entries (restored provider %v)", entries, has)
	}
	h.seedPlacement = nil
	if err := h.addMetadataProvidersToChains(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if _, has := chainHas(t, pool, folder.ID, restored); !has {
		t.Fatal("the retry did not add the restored provider")
	}
}
