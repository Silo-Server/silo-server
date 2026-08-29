package metadata

import (
	"os"
	"strings"
	"testing"
)

func TestArtworkInventoryUpsertPreservesLifecycleState(t *testing.T) {
	parts := strings.Split(artworkInventoryUpsertSQL, "ON CONFLICT (original_path) DO UPDATE SET")
	if len(parts) != 2 {
		t.Fatalf("inventory upsert has %d conflict clauses", len(parts)-1)
	}
	update := parts[1]
	for _, forbidden := range []string{
		"not_before", "next_attempt_at", "last_reference_check_at", "deleted_at",
		"deletion_started_at", "tombstoned_at", "attempt_count", "last_error", "updated_at",
	} {
		if strings.Contains(update, forbidden) {
			t.Fatalf("inventory conflict update mutates lifecycle column %q:\n%s", forbidden, update)
		}
	}
	for _, required := range []string{
		"object_keys", "object_sizes_bytes", "object_content_types", "total_physical_bytes",
		"source_class", "store_generation", "inventory_complete", "last_verified_at",
	} {
		if !strings.Contains(update, required) {
			t.Fatalf("inventory conflict update is missing %q:\n%s", required, update)
		}
	}
}

func TestArtworkAccountingUsesPublishedSnapshotAndDistinctDriftCounters(t *testing.T) {
	if strings.Contains(artworkAccountingStateSQL, "transaction_timestamp") || !strings.Contains(artworkAccountingStateSQL, "SELECT snapshot_at") {
		t.Fatalf("accounting state query does not read the published snapshot: %s", artworkAccountingStateSQL)
	}
	for placeholder, column := range map[string]string{
		"$4": "missing_revisions", "$5": "missing_objects", "$6": "orphan_objects", "$7": "failure_count",
	} {
		if !strings.Contains(artworkAccountingPublishSQL, column+" = "+placeholder) {
			t.Fatalf("accounting publish does not map %s to %s: %s", column, placeholder, artworkAccountingPublishSQL)
		}
	}
}

func TestArtworkAccountingCapacityFailurePreservesMoreSevereHealth(t *testing.T) {
	tests := []struct {
		current string
		want    string
	}{
		{current: "healthy", want: "degraded"},
		{current: "degraded", want: "degraded"},
		{current: "unavailable", want: "unavailable"},
		{current: "wrong_mount", want: "wrong_mount"},
		{current: "empty_rebuilding", want: "empty_rebuilding"},
	}
	for _, test := range tests {
		if got := healthAfterCapacityProbeFailure(test.current); got != test.want {
			t.Errorf("healthAfterCapacityProbeFailure(%q) = %q, want %q", test.current, got, test.want)
		}
	}
}

func TestArtworkSeedAccountingExcludesCollectedAndRetainedSeedsFromReclaimable(t *testing.T) {
	query := artworkStorageTotalSQL()
	for _, want := range []string{
		"source_class = 'seed' AND seed_expires_at <= NOW() AND lifecycle_state IN ('parked', 'pending_gc', 'deleting')",
		"seed_imported_at IS NOT NULL AND seed_expires_at IS NULL",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("seed accounting SQL is missing %q:\n%s", want, query)
		}
	}
}

func TestArtworkSweepSurfaceNamesPinDurableCatalogIdentity(t *testing.T) {
	type pinnedSurface struct {
		table, keys, path, source, thumbhash string
		noUpdatedAt                          bool
	}
	want := map[string]pinnedSurface{
		"item posters":             {"media_items", "content_id", "poster_path", "poster_source_path", "poster_thumbhash", false},
		"item backdrops":           {"media_items", "content_id", "backdrop_path", "backdrop_source_path", "backdrop_thumbhash", false},
		"item logos":               {"media_items", "content_id", "logo_path", "logo_source_path", "", false},
		"localized item posters":   {"media_item_localizations", "content_id,language", "poster_path", "poster_source_path", "poster_thumbhash", false},
		"localized item backdrops": {"media_item_localizations", "content_id,language", "backdrop_path", "backdrop_source_path", "backdrop_thumbhash", false},
		"localized item logos":     {"media_item_localizations", "content_id,language", "logo_path", "logo_source_path", "", false},
		"season posters":           {"seasons", "content_id", "poster_path", "poster_source_path", "poster_thumbhash", false},
		"localized season posters": {"season_localizations", "season_content_id,language", "poster_path", "poster_source_path", "poster_thumbhash", false},
		"episode stills":           {"episodes", "content_id", "still_path", "still_source_path", "still_thumbhash", false},
		"person photos":            {"people", "id", "photo_path", "photo_source_path", "photo_thumbhash", false},
		"collection posters":       {"library_collections", "id", "poster_url", "", "poster_thumbhash", false},
		"collection backdrops":     {"library_collections", "id", "backdrop_url", "", "backdrop_thumbhash", false},
		"user collection posters":  {"user_personal_collections", "user_id,id", "poster_url", "", "poster_thumbhash", false},
		"library posters":          {"media_folders", "id", "poster_path", "", "", true},
	}
	surfaces := artworkSweepSurfaces()
	if len(surfaces) != len(want) {
		t.Fatalf("got %d surfaces, want %d", len(surfaces), len(want))
	}
	seen := make(map[string]bool, len(surfaces))
	for _, surface := range surfaces {
		if seen[surface.name] {
			t.Fatalf("duplicate durable surface name %q", surface.name)
		}
		seen[surface.name] = true
		pinned, ok := want[surface.name]
		if !ok {
			t.Fatalf("unpinned durable surface name %q", surface.name)
		}
		if got := strings.Join(surface.keyColumnNames(), ","); surface.table != pinned.table || got != pinned.keys || surface.pathCol != pinned.path || surface.sourceCol != pinned.source || surface.thumbhashCol != pinned.thumbhash || surface.noUpdatedAt != pinned.noUpdatedAt {
			t.Fatalf("surface %q changed: table=%q keys=%q path=%q source=%q thumbhash=%q no_updated_at=%v", surface.name, surface.table, got, surface.pathCol, surface.sourceCol, surface.thumbhashCol, surface.noUpdatedAt)
		}
	}
}

func TestArtworkInventoryReferenceSQLCoversEveryLifecycleSurface(t *testing.T) {
	query := artworkInventoryReferenceSQL()
	branches := strings.Split(query, " UNION ALL ")
	if want := len(artworkSweepSurfaces()) + 1; len(branches) != want {
		t.Fatalf("inventory union has %d branches, want %d", len(branches), want)
	}
	for _, want := range []string{
		"FROM media_items",
		"FROM media_item_localizations",
		"FROM seasons",
		"FROM season_localizations",
		"FROM episodes",
		"FROM people",
		"FROM library_collections",
		"FROM user_personal_collections",
		"FROM media_folders",
		"FROM user_profiles",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("inventory reference SQL is missing %q:\n%s", want, query)
		}
	}
}

func TestArtworkInventoryBackfillAlsoIncludesLifecycleRows(t *testing.T) {
	query := artworkInventoryEnumerationSQL()
	for _, want := range []string{
		"FROM artwork_revision_gc_candidates WHERE tombstoned_at IS NULL",
		"FROM user_profiles",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("inventory backfill is missing %q", want)
		}
	}
}

func TestLegacyPrefixCleanupChecksEveryLiveArtworkSurface(t *testing.T) {
	query := artworkLegacyPrefixReferencedSQL()
	for _, surface := range artworkSweepSurfaces() {
		if !strings.Contains(query, "FROM "+surface.table) {
			t.Fatalf("legacy-prefix proof omits %q (%s):\n%s", surface.name, surface.table, query)
		}
	}
	for _, want := range []string{"FROM user_profiles", `path LIKE $1 ESCAPE '\'`} {
		if !strings.Contains(query, want) {
			t.Fatalf("legacy-prefix proof is missing %q:\n%s", want, query)
		}
	}
	if got, want := artworkLegacyPrefixPattern(`legacy/%_folder\part/`), `legacy/\%\_folder\\part/%`; got != want {
		t.Fatalf("legacy-prefix pattern = %q, want %q", got, want)
	}
}

func TestArtworkLibraryAccountingSQLPinsAttributionAndNonAdditiveSharing(t *testing.T) {
	query := artworkLibraryAccountingSQL()
	for _, want := range []string{
		"JOIN media_item_libraries mil ON mil.content_id = mi.content_id",
		"JOIN media_item_libraries mil ON mil.content_id = se.series_id",
		"JOIN media_item_libraries mil ON mil.content_id = ep.series_id",
		"JOIN item_people ip ON ip.person_id = p.id",
		"SELECT mf.id, mf.poster_path",
		"SELECT lc.library_id, lc.poster_url",
		"count(*) AS library_count",
		"rs.library_count = 1 AND NOT rs.server_shared",
		"rs.library_count > 1 OR rs.server_shared",
		"rs.library_count = 1 AND NOT rs.server_shared AND lr.reconstructible",
		"LIKE '/%'",
		"FROM user_personal_collections",
		"FROM user_profiles",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("library accounting SQL is missing %q:\n%s", want, query)
		}
	}
}

func TestArtworkInventoryMigrationPinsExactObjectAndJobState(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/20260825220849_artwork_inventory_accounting.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	columnsEnd := strings.Index(sql, "ADD COLUMN tombstoned_at timestamptz;")
	generatedStart := strings.Index(sql, "ADD COLUMN lifecycle_state text GENERATED ALWAYS")
	if columnsEnd < 0 || generatedStart <= columnsEnd {
		t.Fatal("generated lifecycle_state column is not added in a later ALTER TABLE statement")
	}
	for _, want := range []string{
		"object_sizes_bytes bigint[]",
		"object_content_types text[]",
		"total_physical_bytes bigint",
		"store_generation text",
		"inventory_complete boolean",
		"last_reference_check_at timestamptz",
		"tombstoned_at timestamptz",
		"lifecycle_state text GENERATED ALWAYS",
		"dry_run boolean",
		"checkpoint jsonb",
		"admin_jobs_active_artwork_storage_idx",
		"ON public.admin_jobs ((TRUE))",
		"artwork_legacy_prefix_gc_candidates",
		"coverage_limited boolean",
		"failure_count bigint",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration is missing %q", want)
		}
	}
}

func TestArtworkSeedAdoptionMigrationPinsLifecycleAndSerialization(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/20260825233000_artwork_seed_adoption.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, want := range []string{
		"seed_imported_at timestamptz", "seed_expires_at timestamptz", "source_class IN",
		"'seed'", "adoption_index_bytes bigint", "last_seed_import_at timestamptz",
		"branding_bytes bigint", "branding_objects bigint",
		"legacy_upload_bytes bigint", "legacy_upload_objects bigint",
		"'artwork_storage_import'", "artwork_revision_seed_expiry_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("seed migration is missing %q", want)
		}
	}
}

func TestArtworkResilientDeliveryMigrationPinsLossAndRebuildState(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/20260826000100_artwork_resilient_delivery.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, want := range []string{
		"missing_at TIMESTAMPTZ", "repair_state TEXT", "protected_loss_at TIMESTAMPTZ",
		"store_health TEXT", "missing_bytes BIGINT", "repair_pending_bytes BIGINT",
		"rebuild_generation TEXT", "rebuild_surface_name TEXT", "rebuild_enqueued_at TIMESTAMPTZ",
		"artwork_storage_alerts",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("resilient-delivery migration is missing %q", want)
		}
	}
}

// reconstructibleRemoteArtworkSource only classifies a source's scheme; it is
// the gate that decides which sources are worth probing, not proof that one is
// retrievable. revalidateTargets fetches every source it lets through (see
// TestRevalidateTargetsProtectsUnreachableRemoteSource).
func TestReconstructibleRemoteArtworkSourceClassifiesSchemes(t *testing.T) {
	if !strings.Contains(nonProviderImageSchemesSQL, "'embedded://%'") {
		t.Fatalf("SQL protected-source list omits embedded artwork: %s", nonProviderImageSchemesSQL)
	}
	if !strings.Contains(artworkReconstructibleSQL("source_path"), nonProviderImageSchemesSQL) {
		t.Fatal("accounting SQL does not use the shared protected-source list")
	}
	for _, source := range []string{
		"https://images.example/poster.jpg",
		"tmdb://poster/abc",
		"plugin://metadata/poster/abc",
	} {
		if !reconstructibleRemoteArtworkSource(source) {
			t.Fatalf("provider/plugin source %q was not reconstructible", source)
		}
	}
	for _, source := range []string{"", "://broken", "file:///media/poster.jpg", "upload://poster", "generated://poster", "embedded://cover", "library-artwork://opaque"} {
		if reconstructibleRemoteArtworkSource(source) {
			t.Fatalf("protected/locally-validated source %q was treated as remote reconstructible", source)
		}
	}
}

func TestSafePurgeMetricsCountPhysicalRevisionsOnce(t *testing.T) {
	metrics := calculatePurgePlanMetrics([]artworkPurgeTarget{
		{path: "reclaimable", bytes: 100},
		{path: "reclaimable", bytes: 100},
		{path: "partly-protected", bytes: 200},
		{path: "partly-protected", bytes: 200, protected: true},
		{path: "shared", bytes: 300, shared: true},
		{path: "shared", bytes: 300, shared: true, protected: true},
	})
	if metrics.transitionedReferences != 3 || metrics.queuedRevisions != 2 {
		t.Fatalf("transition metrics = %#v", metrics)
	}
	if metrics.pendingBytes != 300 || metrics.reclaimableBytes != 100 {
		t.Fatalf("byte metrics = %#v", metrics)
	}
	if metrics.sharedRevisions != 1 || metrics.protectedRevisions != 1 {
		t.Fatalf("retention metrics = %#v", metrics)
	}
}

func TestPurgePlanFingerprintIsStableAcrossCheckpointResume(t *testing.T) {
	firstID, resumedID := 42, 42
	targets := []artworkPurgeTarget{{
		surfaceName: "item posters", keys: []string{"movie-1"}, path: "stored/original.rev.webp",
		source: "plugin://poster/1", fallback: "plugin://poster/1", bytes: 123,
	}}
	first := purgePlanFingerprint(ArtworkPurgeRequest{Scope: ArtworkPurgeScope{LibraryID: &firstID}, Mode: ArtworkPurgeModeSafeMaterialized}, targets)
	resumed := purgePlanFingerprint(ArtworkPurgeRequest{Scope: ArtworkPurgeScope{LibraryID: &resumedID}, Mode: ArtworkPurgeModeSafeMaterialized}, restoreCheckpointTargets(checkpointTargets(targets)))
	if first != resumed {
		t.Fatalf("equivalent decoded plans fingerprint differently: %s != %s", first, resumed)
	}
	changed := append([]artworkPurgeTarget(nil), targets...)
	changed[0].bytes++
	if purgePlanFingerprint(ArtworkPurgeRequest{Scope: ArtworkPurgeScope{LibraryID: &resumedID}, Mode: ArtworkPurgeModeSafeMaterialized}, changed) == first {
		t.Fatal("checkpoint mutation did not change plan fingerprint")
	}
}
