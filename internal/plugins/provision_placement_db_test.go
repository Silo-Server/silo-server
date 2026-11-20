package plugins

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
)

//nolint:funlen // The test follows a single install through interrupted placement and a later manual removal.
func TestProvisionWriterMetadataPlacementRecoveryDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	repos := NewRepositoryStore(pool)
	if _, err := repos.ReconcileManaged(ctx); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalogService(repos, CatalogServiceOptions{})
	repo, err := catalog.OfficialRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	configs := NewRuntimeConfigStore(pool, instanceStateTestCipher(t))
	installations := NewInstallationStore(pool, configs.cipher)
	root := t.TempDir()
	installer := NewInstaller(installations, InstallerOptions{BaseDir: root})
	anchorID := seedResidentQueryInstallation(t, pool, true)
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("placement-test-%d", time.Now().UnixNano())).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID); err != nil {
			t.Error(err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO library_provider_chains
		(media_folder_id, plugin_installation_id, capability_id, capability_type, content_level, priority, enabled)
		VALUES ($1, $2, 'existing', 'metadata_provider.v1', 'movie', 0, true)`, folderID, anchorID); err != nil {
		t.Fatal(err)
	}

	manifest := packageErrorTestManifest(tmdbID, "1.2.3")
	binary := []byte("verified metadata package")
	manifest.Checksum = sha256Hex(binary)
	manifestJSON, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	staged := &PreparedProvisionPackage{RepositoryID: repo.ID, root: root}
	if err := staged.stageBinary(ctx, &downloadedBinary{
		data: binary, checksum: manifest.Checksum, manifest: manifest, manifestJSON: manifestJSON,
		signature: Signature{State: SignatureStateVerified, SourceRepo: new(tmdbRepoURL), VerifiedAt: new(time.Now())},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := staged.Abort(); err != nil {
			t.Error(err)
		}
	})

	desired := ProvisionPlugin{Version: manifest.Version}
	doc := &ProvisionDocument{APIVersion: 1, Plugins: map[string]ProvisionPlugin{tmdbID: desired}}
	place := func(ctx context.Context, tx pgx.Tx, installationID int) error {
		_, err := tx.Exec(ctx, `INSERT INTO library_provider_chains
			(media_folder_id, plugin_installation_id, capability_id, capability_type, content_level, priority, enabled)
			VALUES ($1, $2, 'fixture', 'metadata_provider.v1', 'movie', 1, false)`, folderID, installationID)
		return err
	}
	initial := NewProvisionWriter(pool, repos, installations, installer, catalog, configs, nil, doc, place)
	t.Cleanup(initial.Close)
	if acquired, err := initial.tryWriter(ctx); !acquired || err != nil {
		t.Fatalf("claim authority: %v, %v", acquired, err)
	}
	if _, err := initial.commit(ctx, []preparedProvision{{pluginID: tmdbID, desired: desired, pkg: staged}}); err != nil {
		t.Fatal(err)
	}
	// Stop this session immediately after the main provisioning commit.
	initial.Close()
	installed, err := installations.ListByPluginID(ctx, tmdbID)
	if err != nil || len(installed) != 1 {
		t.Fatalf("installed package: %v, %v", installed, err)
	}
	id := installed[0].ID
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM plugin_provision_status WHERE plugin_id = $1`, tmdbID); err != nil {
			t.Error(err)
		}
	})
	assertPlacement := func(pending, placed bool) {
		t.Helper()
		var queued bool
		var chainRows int
		err := pool.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM plugin_provision_placement WHERE plugin_installation_id = $1),
			(SELECT count(*) FROM library_provider_chains WHERE plugin_installation_id = $1 AND media_folder_id = $2 AND content_level = 'movie')`, id, folderID).
			Scan(&queued, &chainRows)
		if err != nil || queued != pending || (chainRows == 1) != placed || chainRows > 1 {
			t.Fatalf("placement pending = %v, chain rows = %d, err = %v, want %v, %v", queued, chainRows, err, pending, placed)
		}
	}
	assertPlacement(true, false)

	// A later failure keeps that placement queued without replaying another
	// plugin whose chain was already in place.
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_provision_placement (plugin_installation_id) VALUES ($1)`, anchorID); err != nil {
		t.Fatal(err)
	}
	retryWriter := NewProvisionWriter(pool, repos, installations, installer, catalog, configs, nil, doc, func(ctx context.Context, tx pgx.Tx, id int) error {
		if id == anchorID {
			return nil
		}
		if err := place(ctx, tx, id); err != nil {
			return err
		}
		return errors.New("placement interrupted before commit")
	})
	t.Cleanup(retryWriter.Close)
	if err := retryWriter.StartOffline(ctx); err != nil {
		t.Fatal(err)
	}
	assertPlacement(true, false)
	var anchorQueued bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_provision_placement WHERE plugin_installation_id=$1)`, anchorID).Scan(&anchorQueued); err != nil || anchorQueued {
		t.Fatalf("completed placement still queued = %v, error = %v", anchorQueued, err)
	}
	retryWriter.placeMetadata = place
	checkPlacementBlockedByRestore(t, retryWriter)
	assertPlacement(true, false)
	retryWriter.Close()
	reviewed := NewProvisionWriter(pool, repos, installations, installer, catalog, configs, nil, doc, place)
	t.Cleanup(reviewed.Close)
	if err := reviewed.StartOffline(ctx); err != nil {
		t.Fatal(err)
	}
	assertPlacement(false, true)
	reviewed.Close()

	if _, err := pool.Exec(ctx, `DELETE FROM library_provider_chains WHERE plugin_installation_id = $1 AND media_folder_id = $2`, id, folderID); err != nil {
		t.Fatal(err)
	}
	restart := NewProvisionWriter(pool, repos, installations, installer, catalog, configs, nil, doc, place)
	t.Cleanup(restart.Close)
	if err := restart.StartOffline(ctx); err != nil {
		t.Fatal(err)
	}
	assertPlacement(false, false)
}

func checkPlacementBlockedByRestore(t *testing.T, w *ProvisionWriter) {
	t.Helper()
	ctx := t.Context()
	const hold = "test-restore-before-placement"
	if _, err := w.pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1,$2)`, ProvisionRestoreHoldKey, hold); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := w.pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key=$1 AND value=$2`, ProvisionRestoreHoldKey, hold); err != nil {
			t.Error(err)
		}
	})
	w.tryPlacement(ctx)
	statuses, err := ListProvisionStatus(ctx, w.pool)
	if err != nil || len(statuses) != 1 || statuses[0].State != "held" {
		t.Fatalf("restore during placement: statuses = %v, error = %v", statuses, err)
	}
	if err := AcknowledgeProvisionRestoreHold(ctx, w.pool, hold); err != nil {
		t.Fatal(err)
	}
	w.applyOnline(ctx)
	statuses, err = ListProvisionStatus(ctx, w.pool)
	if err != nil || len(statuses) != 1 || statuses[0].State != "held" {
		t.Fatalf("old writer after acknowledgment: statuses = %v, error = %v", statuses, err)
	}
	if w.retry || w.placementPending {
		t.Fatal("acknowledgment resumed the old writer")
	}
}
