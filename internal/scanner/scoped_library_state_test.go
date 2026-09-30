package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSyncPresentPathStatePreservesEpisodeProvenanceAndScope(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "path")
	scope := filepath.Join(t.TempDir(), "series_100%\\Δ")
	target := filepath.Join(scope, "episode.mkv")
	outside := scope + "2/other.mkv"
	if _, err := fx.pool.Exec(ctx, `
		UPDATE media_files SET file_path = CASE
			WHEN file_path = $2 THEN $3 ELSE $4 END
		WHERE media_folder_id = $1
	`, fx.folderID, fx.targetPath, target, outside); err != nil {
		t.Fatalf("move fixture paths: %v", err)
	}
	fx.targetPath = target
	fx.unrelatedPath = outside

	runID := fx.episodeID + "-first-scan"
	if _, err := fx.pool.Exec(ctx, `
		INSERT INTO scan_runs (id, media_folder_id, mode, status)
		VALUES ($1, $2, 'library', 'completed')
	`, runID, fx.folderID); err != nil {
		t.Fatalf("seed scan provenance: %v", err)
	}
	// The earlier present version lives outside this scan. An older missing
	// version must not contribute either its date or its scan provenance.
	fx.firstSeen = fx.firstSeen.Add(-time.Hour)
	if _, err := fx.pool.Exec(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, media_folder_id, file_path, file_size,
			created_at, first_seen_scan_run_id, missing_since
		)
		VALUES
			($1, $2, $3, $4, 1024, $5, $6, NULL),
			($1, $2, $3, $7, 1024, $8, NULL, $5),
			($9, NULL, $3, $10, 1024, NOW(), NULL, NULL)
	`, fx.seriesID, fx.episodeID, fx.folderID, outside+"-version", fx.firstSeen, runID,
		outside+"-missing", fx.firstSeen.Add(-time.Hour), fx.seriesID+"-dangling", outside+"-dangling"); err != nil {
		t.Fatalf("seed outside versions and dangling link: %v", err)
	}

	scanner := NewScanner(NewFileRepository(fx.pool), "", nil, 1, false, 0)
	if err := scanner.syncPresentPathState(ctx, fx.folderID, scope); err != nil {
		t.Fatalf("syncPresentPathState: %v", err)
	}
	fx.assertEpisodeRepaired(ctx, t)
	var provenance *string
	if err := fx.pool.QueryRow(ctx, `
		SELECT first_seen_scan_run_id FROM episode_libraries
		WHERE episode_id = $1 AND media_folder_id = $2
	`, fx.episodeID, fx.folderID).Scan(&provenance); err != nil {
		t.Fatalf("read episode provenance: %v", err)
	}
	if provenance == nil || *provenance != runID {
		t.Fatalf("episode provenance = %v, want %s", provenance, runID)
	}
	if fx.hasItemMembership(ctx, t, fx.unrelatedID) {
		t.Fatal("subtree repair restored an unrelated membership")
	}
	var dangling *string
	if err := fx.pool.QueryRow(ctx, `
		SELECT content_id FROM media_files WHERE media_folder_id = $1 AND file_path = $2
	`, fx.folderID, outside+"-dangling").Scan(&dangling); err != nil {
		t.Fatalf("read unrelated dangling link: %v", err)
	}
	if dangling == nil || *dangling != fx.seriesID+"-dangling" {
		t.Fatalf("subtree repair changed unrelated dangling link: %v", dangling)
	}

	// Repairing the same episode again preserves an existing membership's
	// original first-seen data, even if another older version has appeared.
	if _, err := fx.pool.Exec(ctx, `
		UPDATE media_files SET created_at = $1
		WHERE media_folder_id = $2 AND file_path = $3
	`, fx.firstSeen.Add(-time.Hour), fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("age target version: %v", err)
	}
	if err := scanner.syncPresentPathState(ctx, fx.folderID, scope); err != nil {
		t.Fatalf("repeat subtree repair: %v", err)
	}
	fx.assertEpisodeRepaired(ctx, t)
}

func TestScopedLibraryReconciliationKeepsOtherVersionsAndUnrelatedOrphans(t *testing.T) {
	for _, mode := range []string{"relinked", "other version", "protected root"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			fx := seedPresentStateFixture(ctx, t, "cleanup-"+mode)
			scanner := NewScanner(NewFileRepository(fx.pool), "", nil, 1, false, 0)
			if err := scanner.syncPresentFileState(ctx, fx.folderID, fx.targetPath); err != nil {
				t.Fatalf("seed memberships: %v", err)
			}
			existing, err := scanner.fileRepo.GetScanStateByFolderAndPathPrefix(ctx, fx.folderID, fx.targetPath)
			if err != nil {
				t.Fatalf("capture former links: %v", err)
			}
			var protected []string
			switch mode {
			case "relinked":
				// Matches converting a primary file into a local extra: its
				// former IDs are no longer discoverable from the live row.
				_, err = fx.pool.Exec(ctx, `
					UPDATE media_files SET content_id = NULL, episode_id = NULL
					WHERE media_folder_id = $1 AND file_path = $2
				`, fx.folderID, fx.targetPath)
			case "other version":
				_, err = fx.pool.Exec(ctx, `
					INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path, file_size)
					VALUES ($1, $2, $3, $4, 1024)
				`, fx.seriesID, fx.episodeID, fx.folderID, fx.unrelatedPath+"-version")
			case "protected root":
				protected = []string{fx.targetPath}
			}
			if err != nil {
				t.Fatalf("prepare %s: %v", mode, err)
			}
			if mode != "relinked" {
				if _, err := fx.pool.Exec(ctx, `
					UPDATE media_files SET missing_since = NOW() - INTERVAL '2 days'
					WHERE media_folder_id = $1 AND file_path = $2
				`, fx.folderID, fx.targetPath); err != nil {
					t.Fatalf("mark scoped version missing: %v", err)
				}
			}

			removed, deleted, _, err := scanner.reconcileScopedLibraryMemberships(ctx, fx.folderID, fx.targetPath, existing, protected)
			if err != nil {
				t.Fatalf("reconcile scoped membership: %v", err)
			}
			wantRemoved, wantDeleted := 1, 1
			switch mode {
			case "other version":
				wantRemoved, wantDeleted = 0, 0
			case "protected root":
				wantDeleted = 0
			}
			if removed != wantRemoved || deleted != wantDeleted {
				t.Fatalf("removed/deleted = %d/%d, want %d/%d", removed, deleted, wantRemoved, wantDeleted)
			}
			var episodeMembership, unrelatedItem bool
			if err := fx.pool.QueryRow(ctx, `
				SELECT EXISTS(SELECT 1 FROM episode_libraries WHERE episode_id = $1 AND media_folder_id = $2),
				       EXISTS(SELECT 1 FROM media_items WHERE content_id = $3)
			`, fx.episodeID, fx.folderID, fx.unrelatedID).Scan(&episodeMembership, &unrelatedItem); err != nil {
				t.Fatalf("check retained catalog state: %v", err)
			}
			if episodeMembership != (mode == "other version") {
				t.Fatalf("episode membership = %v for %s", episodeMembership, mode)
			}
			if !unrelatedItem || fx.hasItemMembership(ctx, t, fx.unrelatedID) {
				t.Fatal("scoped reconciliation changed an unrelated orphan or its membership")
			}
			if mode == "protected root" {
				removed, deleted, _, err = scanner.reconcileScopedLibraryMemberships(ctx, fx.folderID, fx.targetPath, existing, nil)
				if err != nil || removed != 0 || deleted != 1 {
					t.Fatalf("after root recovery removed/deleted = %d/%d, err = %v, want 0/1", removed, deleted, err)
				}
			}
		})
	}
}

func TestScanSubtreeLeavesUnrelatedStateUntouched(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "scan-subtree")
	root := t.TempDir()
	scope := filepath.Join(root, "Target Show (2020)", "Season 01")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	// Keep the configured root populated while the scanned season is empty.
	writeTestFile(t, filepath.Join(root, "keeper.mkv"), "media")
	target := filepath.Join(scope, "Target Show S01E01.mkv")
	outside := filepath.Join(root, "Other Show (2021)", "other.mkv")
	if _, err := fx.pool.Exec(ctx, `
		UPDATE media_files SET file_path = CASE
			WHEN file_path = $2 THEN $3 ELSE $4 END
		WHERE media_folder_id = $1
	`, fx.folderID, fx.targetPath, target, outside); err != nil {
		t.Fatalf("move fixture paths: %v", err)
	}
	fx.targetPath = target
	fx.unrelatedPath = outside
	scanner := NewScanner(NewFileRepository(fx.pool), "", nil, 1, true, 0)
	if err := scanner.syncPresentFileState(ctx, fx.folderID, target); err != nil {
		t.Fatalf("seed target memberships: %v", err)
	}
	stalePath := filepath.Join(root, "unrelated-stale.mkv")
	danglingID := fx.seriesID + "-dangling"
	if _, err := fx.pool.Exec(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, missing_since)
		VALUES ($1, $2, $3, 1024, NOW() - INTERVAL '2 days'),
		       ($4, $2, $5, 1024, NULL)
	`, fx.unrelatedID, fx.folderID, stalePath, danglingID, outside+"-dangling"); err != nil {
		t.Fatalf("seed unrelated catalog damage: %v", err)
	}
	folder := &models.MediaFolder{ID: fx.folderID, Type: "series", Paths: []string{root}, Enabled: true}
	result, err := scanner.ScanSubtree(ctx, folder, scope)
	if err != nil {
		t.Fatalf("ScanSubtree: %v", err)
	}
	if result.MembershipsRemoved != 1 || result.ItemsDeleted != 1 || result.FilesDeleted != 1 {
		t.Fatalf("subtree cleanup result = %+v, want one membership, item and file removed", result)
	}
	var staleExists, orphanExists bool
	var outsideLink *string
	if err := fx.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM media_files WHERE media_folder_id = $1 AND file_path = $2),
		       EXISTS(SELECT 1 FROM media_items WHERE content_id = $3),
		       (SELECT content_id FROM media_files WHERE media_folder_id = $1 AND file_path = $4)
	`, fx.folderID, stalePath, fx.unrelatedID, outside+"-dangling").Scan(&staleExists, &orphanExists, &outsideLink); err != nil {
		t.Fatalf("read unrelated state: %v", err)
	}
	if !staleExists || !orphanExists || outsideLink == nil || *outsideLink != danglingID {
		t.Fatalf("subtree cleanup changed unrelated rows: stale=%v orphan=%v link=%v", staleExists, orphanExists, outsideLink)
	}
	if fx.hasItemMembership(ctx, t, fx.unrelatedID) {
		t.Fatal("subtree scan repaired an unrelated membership")
	}
}

func TestDeleteMissingInScopeHonorsGraceAndProtectedPaths(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Scoped trash")
	root := fmt.Sprintf("/scoped-trash-%d/movies_100%%", time.Now().UnixNano())
	paths := []string{root + "/old.mkv", root + "/recent.mkv", root + "/offline/old.mkv", root + "2/old.mkv"}
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	for i, path := range paths {
		missing := old
		if i == 1 {
			missing = recent
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, missing_since)
			VALUES ($1, $2, 1024, $3)
		`, folderID, path, missing); err != nil {
			t.Fatalf("seed missing file: %v", err)
		}
	}
	removed, err := NewFileRepository(pool).DeleteMissingInScope(ctx, folderID, root, 24*time.Hour, []string{root + "/offline"})
	if err != nil || removed != 1 {
		t.Fatalf("scoped trash deleted %d, err = %v, want 1", removed, err)
	}
	var remaining []string
	if err := pool.QueryRow(ctx, `
		SELECT array_agg(file_path ORDER BY file_path) FROM media_files WHERE media_folder_id = $1
	`, folderID).Scan(&remaining); err != nil {
		t.Fatalf("read retained missing files: %v", err)
	}
	if len(remaining) != 3 {
		t.Fatalf("remaining paths = %v, want recent, protected and out-of-scope rows", remaining)
	}
}

func TestScopedPathRangesMatchLiteralPathsWithGenericPlans(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "series", "Literal path ranges")
	config := pool.Config()
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	genericPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect generic-plan pool: %v", err)
	}
	t.Cleanup(genericPool.Close)
	root := fmt.Sprintf("/range-%d/Shows_100%%\\Δ", time.Now().UnixNano())
	paths := []string{
		root,
		root + "/episode.mkv",
		root + "/季節/episode.mkv",
		root + "2/episode.mkv",
		root + "0",
		root + "-/episode.mkv",
		filepath.Dir(root) + "/ShowsX100abcΔ/episode.mkv",
		filepath.Dir(root) + "/Shows_100%Δ/episode.mkv",
	}
	for _, path := range paths {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size)
			VALUES ($1, $2, 1024)
		`, folderID, path); err != nil {
			t.Fatalf("seed literal path: %v", err)
		}
	}
	for _, tc := range []struct {
		name, scope string
		want        []string
	}{
		{name: "literal root and descendants", scope: root, want: paths[:3]},
		{name: "trailing separator", scope: root + "/", want: paths[1:3]},
		{name: "filesystem root", scope: string(filepath.Separator), want: paths},
		{name: "exact file", scope: paths[1], want: paths[1:2]},
		{name: "empty scope", scope: root + "/absent", want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, err := NewFileRepository(genericPool).GetScanStateByFolderAndPathPrefix(t.Context(), folderID, tc.scope)
			if err != nil {
				t.Fatalf("read generic-plan path scope: %v", err)
			}
			got := make([]string, 0, len(files))
			for _, file := range files {
				got = append(got, file.FilePath)
			}
			want := slices.Clone(tc.want)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("scope %q paths = %q, want %q", tc.scope, got, want)
			}
		})
	}
}

func TestScopedLibraryReconciliationCleansFormerIdentityAfterRelinking(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "relink-current")
	scanner := NewScanner(NewFileRepository(fx.pool), "", nil, 1, false, 0)
	if err := scanner.syncPresentFileState(ctx, fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("seed original membership: %v", err)
	}
	existing, err := scanner.fileRepo.GetScanStateByFolderAndPathPrefix(ctx, fx.folderID, fx.targetPath)
	if err != nil {
		t.Fatalf("capture original identity: %v", err)
	}
	currentSeries := fx.seriesID + "-current"
	currentEpisode := fx.episodeID + "-current"
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id = $1`, currentSeries)
	})
	if _, err := fx.pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path)
		VALUES ($1, 'series', 'Current Series', 'matched', '{}'::text[], '', '', '')
	`, currentSeries); err != nil {
		t.Fatalf("seed current series: %v", err)
	}
	if _, err := fx.pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, still_path)
		VALUES ($1, $2, 1, 1, 'Current Episode', '')
	`, currentEpisode, currentSeries); err != nil {
		t.Fatalf("seed current episode: %v", err)
	}
	if _, err := fx.pool.Exec(ctx, `
		UPDATE media_files SET content_id = $1, episode_id = $2
		WHERE media_folder_id = $3 AND file_path = $4
	`, currentSeries, currentEpisode, fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("relink file: %v", err)
	}
	if err := scanner.syncPresentPathState(ctx, fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("repair current membership: %v", err)
	}
	removed, deleted, _, err := scanner.reconcileScopedLibraryMemberships(ctx, fx.folderID, fx.targetPath, existing, nil)
	if err != nil || removed != 1 || deleted != 1 {
		t.Fatalf("relink cleanup removed/deleted = %d/%d, err = %v, want 1/1", removed, deleted, err)
	}
	var originalSeries, originalEpisode, currentEpisodeLink bool
	if err := fx.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM media_items WHERE content_id = $1),
		       EXISTS(SELECT 1 FROM episode_libraries WHERE episode_id = $2 AND media_folder_id = $3),
		       EXISTS(SELECT 1 FROM episode_libraries WHERE episode_id = $4 AND media_folder_id = $3)
	`, fx.seriesID, fx.episodeID, fx.folderID, currentEpisode).Scan(&originalSeries, &originalEpisode, &currentEpisodeLink); err != nil {
		t.Fatalf("check relinked catalog: %v", err)
	}
	if originalSeries || originalEpisode || !currentEpisodeLink || !fx.hasItemMembership(ctx, t, currentSeries) {
		t.Fatalf("relink retained incorrect catalog state: oldSeries=%v oldEpisode=%v currentEpisode=%v", originalSeries, originalEpisode, currentEpisodeLink)
	}
	// Reconcile a later disappearance using the original pre-relink snapshot.
	// The live scoped row must supply the new identity IDs for its cleanup.
	if _, err := fx.pool.Exec(ctx, `
		UPDATE media_files SET missing_since = NOW() - INTERVAL '2 days'
		WHERE media_folder_id = $1 AND file_path = $2
	`, fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("make current identity stale: %v", err)
	}
	removed, deleted, _, err = scanner.reconcileScopedLibraryMemberships(ctx, fx.folderID, fx.targetPath, existing, nil)
	if err != nil || removed != 1 || deleted != 1 {
		t.Fatalf("current identity cleanup removed/deleted = %d/%d, err = %v, want 1/1", removed, deleted, err)
	}
	if err := fx.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM episode_libraries WHERE episode_id = $1 AND media_folder_id = $2)
	`, currentEpisode, fx.folderID).Scan(&currentEpisodeLink); err != nil {
		t.Fatalf("read current identity after cleanup: %v", err)
	}
	if currentEpisodeLink || fx.hasItemMembership(ctx, t, currentSeries) {
		t.Fatal("scoped cleanup missed IDs from the current row")
	}
}

func TestScopedLibraryReconciliationWithNoIDsKeepsUnrelatedEpisodeState(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "empty-ids")
	scanner := NewScanner(NewFileRepository(fx.pool), "", nil, 1, false, 0)
	if err := scanner.syncPresentFileState(ctx, fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("seed unrelated episode membership: %v", err)
	}
	if _, err := fx.pool.Exec(ctx, `
		UPDATE media_files SET missing_since = NOW() - INTERVAL '2 days'
		WHERE media_folder_id = $1 AND file_path = $2
	`, fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("make unrelated membership stale: %v", err)
	}
	removed, deleted, _, err := scanner.reconcileScopedLibraryMemberships(ctx, fx.folderID, fx.targetPath+"-absent", nil, nil)
	if err != nil || removed != 0 || deleted != 0 {
		t.Fatalf("empty scope removed/deleted = %d/%d, err = %v, want 0/0", removed, deleted, err)
	}
	var episodeMembership bool
	if err := fx.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM episode_libraries WHERE episode_id = $1 AND media_folder_id = $2)
	`, fx.episodeID, fx.folderID).Scan(&episodeMembership); err != nil {
		t.Fatalf("read unrelated stale episode membership: %v", err)
	}
	if !episodeMembership || !fx.hasItemMembership(ctx, t, fx.seriesID) {
		t.Fatal("empty scoped reconciliation removed unrelated stale memberships")
	}
}
