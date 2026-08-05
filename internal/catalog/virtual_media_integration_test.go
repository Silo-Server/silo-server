package catalog

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newVirtualMediaTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("failed to connect to db: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	// Clean out related tables for isolation.
	for _, statement := range []string{
		"DELETE FROM media_files",
		"TRUNCATE public.episodes CASCADE",
		"DELETE FROM seasons",
		"DELETE FROM media_item_libraries",
		"DELETE FROM library_collection_items",
		"DELETE FROM library_collections",
		"DELETE FROM media_items",
		"DELETE FROM media_folders",
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("reset virtual media test database: %v", err)
		}
	}

	return pool
}

func TestVirtualMediaVariantsUpsert(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	reg := NewVirtualMediaRegistrar(pool)

	if _, err := pool.Exec(ctx, "INSERT INTO media_folders(id,name,type,enabled) VALUES(999,'TestVirtual','mixed',true)"); err != nil {
		t.Fatalf("seed virtual media folder: %v", err)
	}

	in := VirtualMedia{
		LibraryID: "999", MediaType: "movie", Title: "Test Movie", IMDbID: "tt100", TMDBID: "1", Source: "provider-a",
		RuntimeMinutes: 120,
		Variants: []VirtualMediaVariant{
			{VirtualURI: "virtual://movie/tt100?profile=1080p", Resolution: "1080p", CodecVideo: "h264", RuntimeMinutes: 120},
			{VirtualURI: "virtual://movie/tt100?profile=4k", Resolution: "4k", CodecVideo: "hevc", HDR: "hdr10", RuntimeMinutes: 120},
		},
	}
	res, err := reg.UpsertVirtualMedia(ctx, 11, in)
	if err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	// Verify two virtual files were inserted and default container is "virtual"
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1 AND container='virtual'", res.MediaID).Scan(&count); err != nil {
		t.Fatalf("count virtual media files: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 variants, got %d", count)
	}

	// Test variant with supplied container and file size
	inWithSupplied := VirtualMedia{
		LibraryID: "999", MediaType: "movie", Title: "Custom Variant Movie", IMDbID: "tt200", TMDBID: "2", Source: "provider-a",
		RuntimeMinutes: 120,
		Variants: []VirtualMediaVariant{
			{VirtualURI: "virtual://movie/tt200?profile=1080p", Resolution: "1080p", CodecVideo: "h264", Container: "mkv", FileSize: 104857600},
		},
	}
	resSupplied, err := reg.UpsertVirtualMedia(ctx, 11, inWithSupplied)
	if err != nil {
		t.Fatalf("upsert custom variant failed: %v", err)
	}
	var container string
	var fileSize int64
	err = pool.QueryRow(ctx, "SELECT container, file_size FROM media_files WHERE content_id=$1 AND file_path=$2", resSupplied.MediaID, "virtual://movie/tt200?profile=1080p").Scan(&container, &fileSize)
	if err != nil {
		t.Fatalf("failed to query custom variant file: %v", err)
	}
	if container != "virtual" {
		t.Fatalf("expected container 'virtual', got %q", container)
	}
	if fileSize != 104857600 {
		t.Fatalf("expected file_size 104857600, got %d", fileSize)
	}

	// Verify idempotency
	in.Overview = "Updated overview"
	_, err = reg.UpsertVirtualMedia(ctx, 11, in)
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1", res.MediaID).Scan(&count); err != nil {
		t.Fatalf("count updated virtual media files: %v", err)
	}
	if count != 2 {
		t.Fatalf("idempotent upsert should not add duplicates, got %d", count)
	}
	var overview string
	if err := pool.QueryRow(ctx, "SELECT overview FROM media_items WHERE content_id=$1", res.MediaID).Scan(&overview); err != nil {
		t.Fatalf("load updated virtual media overview: %v", err)
	}
	if overview != "Updated overview" {
		t.Fatalf("expected 'Updated overview', got %q", overview)
	}

	// Test episode variants
	series := VirtualMedia{
		LibraryID: "999", MediaType: "series", Title: "Test Series", IMDbID: "tt300", TMDBID: "3", Source: "provider-a",
		Episodes: []VirtualEpisode{
			{
				SeasonNumber: 1, EpisodeNumber: 1, Title: "Ep 1",
				Variants: []VirtualMediaVariant{
					{VirtualURI: "virtual://series/tt300/1/1?profile=1080p", Resolution: "1080p"},
					{VirtualURI: "virtual://series/tt300/1/1?profile=720p", Resolution: "720p"},
				},
			},
		},
	}
	resSeries, err := reg.UpsertVirtualMedia(ctx, 11, series)
	if err != nil {
		t.Fatalf("upsert series failed: %v", err)
	}
	if resSeries.EpisodesUpserted != 1 {
		t.Fatalf("expected 1 episode, got %d", resSeries.EpisodesUpserted)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1", resSeries.MediaID).Scan(&count); err != nil {
		t.Fatalf("count virtual episode files: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 episode variants, got %d", count)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM virtual_media_file_source_claims
		WHERE plugin_installation_id=11 AND source_key='provider-a'`).Scan(&count); err != nil {
		t.Fatalf("count virtual file claims: %v", err)
	}
	if count != 5 {
		t.Fatalf("expected 5 source-scoped file claims, got %d", count)
	}
}

func TestVirtualMediaUpsertAndReconcileShareSourceLock(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(973,'Source Lock','movies',true)`); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	registrar := NewVirtualMediaRegistrar(pool)
	input := VirtualMedia{
		LibraryID: "973", MediaType: "movie", Title: "Source Lock",
		IMDbID: "tt208", TMDBID: "208", Source: "source-lock",
		VirtualURI: "virtual://movie/tt208",
	}

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	if err := lockVirtualMediaSource(ctx, blocker, 11, input.Source); err != nil {
		t.Fatalf("hold source lock: %v", err)
	}
	upsertDone := make(chan error, 1)
	go func() {
		_, err := registrar.UpsertVirtualMedia(ctx, 11, input)
		upsertDone <- err
	}()
	select {
	case err := <-upsertDone:
		_ = blocker.Rollback(ctx)
		t.Fatalf("upsert bypassed source lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release upsert source lock: %v", err)
	}
	select {
	case err := <-upsertDone:
		if err != nil {
			t.Fatalf("upsert after source unlock: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upsert did not resume after source unlock")
	}

	blocker, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reconcile lock holder: %v", err)
	}
	if err := lockVirtualMediaSource(ctx, blocker, 11, input.Source); err != nil {
		t.Fatalf("hold reconcile source lock: %v", err)
	}
	reconcileDone := make(chan error, 1)
	go func() {
		_, err := registrar.ReconcileVirtualMedia(ctx, 11, input.Source, []string{"movie-tmdb-208"}, []int{973})
		reconcileDone <- err
	}()
	select {
	case err := <-reconcileDone:
		_ = blocker.Rollback(ctx)
		t.Fatalf("reconcile bypassed source lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release reconcile source lock: %v", err)
	}
	select {
	case err := <-reconcileDone:
		if err != nil {
			t.Fatalf("reconcile after source unlock: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reconcile did not resume after source unlock")
	}
}

func TestVirtualMediaUpsertPreservesLocalItemAndAddsIndependentVirtualSource(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	reg := NewVirtualMediaRegistrar(pool)

	if _, err := pool.Exec(ctx, "INSERT INTO media_folders(id,name,type,enabled) VALUES(997,'LocalAndVirtual','movies',true)"); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	const contentID = "movie-tmdb-10"
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items(content_id,type,title,tmdb_id,status)
		VALUES($1,'movie','Authoritative Local Title','10','matched')`, contentID); err != nil {
		t.Fatalf("seed local item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container)
		VALUES($1,997,'/media/authoritative-local.mkv',1024,'mkv')`, contentID); err != nil {
		t.Fatalf("seed local file: %v", err)
	}

	_, err := reg.UpsertVirtualMedia(ctx, 11, VirtualMedia{
		LibraryID: "997", MediaType: "movie", Title: "Plugin Must Not Replace This",
		IMDbID: "tt10", TMDBID: "10", Source: "provider-a",
		VirtualURI: "virtual://movie/tt10",
	})
	if err != nil {
		t.Fatalf("add virtual source to local item: %v", err)
	}

	var (
		title        string
		legacyOwner  *int64
		localFiles   int
		virtualFiles int
		ownsMetadata bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT title,virtual_owner_installation_id FROM media_items WHERE content_id=$1`,
		contentID).Scan(&title, &legacyOwner); err != nil {
		t.Fatalf("load preserved item: %v", err)
	}
	if title != "Authoritative Local Title" || legacyOwner != nil {
		t.Fatalf("local item was claimed or overwritten: title=%q owner=%v", title, legacyOwner)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER(WHERE file_path='/media/authoritative-local.mkv'),
		       count(*) FILTER(WHERE file_path='virtual://movie/tt10')
		FROM media_files WHERE content_id=$1`, contentID).Scan(&localFiles, &virtualFiles); err != nil {
		t.Fatalf("load coexisting files: %v", err)
	}
	if localFiles != 1 || virtualFiles != 1 {
		t.Fatalf("local+virtual coexistence files local=%d virtual=%d", localFiles, virtualFiles)
	}
	if err := pool.QueryRow(ctx, `
		SELECT owns_item_metadata FROM virtual_media_source_claims
		WHERE plugin_installation_id=11 AND source_key='provider-a' AND content_id=$1`,
		contentID).Scan(&ownsMetadata); err != nil {
		t.Fatalf("load virtual source claim: %v", err)
	}
	if ownsMetadata {
		t.Fatal("virtual source incorrectly claimed local item metadata")
	}

	filesDeleted, itemsDeleted, err := (&ItemRepository{pool: pool}).PurgeVirtualPlaybackItems(
		ctx, VirtualPurgeOptions{InstallationID: 11},
	)
	if err != nil {
		t.Fatalf("purge virtual source from local item: %v", err)
	}
	if filesDeleted != 1 || itemsDeleted != 0 {
		t.Fatalf("purge deleted files=%d items=%d, want 1 and 0", filesDeleted, itemsDeleted)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM virtual_media_source_claims
		WHERE plugin_installation_id=11 AND content_id=$1`, contentID).Scan(&virtualFiles); err != nil {
		t.Fatalf("count source claims after purge: %v", err)
	}
	if virtualFiles != 0 {
		t.Fatalf("purge left %d ghost virtual source claims", virtualFiles)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM media_files
		WHERE content_id=$1 AND file_path='/media/authoritative-local.mkv'`, contentID).Scan(&localFiles); err != nil {
		t.Fatalf("count local file after purge: %v", err)
	}
	if localFiles != 1 {
		t.Fatalf("purge removed local file, remaining=%d", localFiles)
	}
}

func TestVirtualMediaSourcesDoNotOverwriteEachOtherAndReconcileIndependently(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	reg := NewVirtualMediaRegistrar(pool)
	if _, err := pool.Exec(ctx, "INSERT INTO media_folders(id,name,type,enabled) VALUES(996,'MultiProvider','movies',true)"); err != nil {
		t.Fatalf("seed folder: %v", err)
	}

	first := VirtualMedia{
		LibraryID: "996", MediaType: "movie", Title: "Provider A Title",
		IMDbID: "tt20", TMDBID: "20", Source: "provider-a",
		VirtualURI: "virtual://movie/tt20?result=a",
	}
	result, err := reg.UpsertVirtualMedia(ctx, 11, first)
	if err != nil {
		t.Fatalf("upsert first provider: %v", err)
	}
	second := first
	second.Title = "Provider B Must Not Replace This"
	second.Source = "provider-b"
	second.VirtualURI = "virtual://movie/tt20?result=b"
	if _, err := reg.UpsertVirtualMedia(ctx, 22, second); err != nil {
		t.Fatalf("upsert second provider: %v", err)
	}

	var (
		title       string
		legacyOwner *int64
		claims      int
		files       int
	)
	if err := pool.QueryRow(ctx, `
		SELECT title,virtual_owner_installation_id FROM media_items WHERE content_id=$1`,
		result.MediaID).Scan(&title, &legacyOwner); err != nil {
		t.Fatalf("load shared item: %v", err)
	}
	if title != first.Title || legacyOwner == nil || *legacyOwner != 11 {
		t.Fatalf("second provider overwrote primary metadata: title=%q owner=%v", title, legacyOwner)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM virtual_media_source_claims WHERE content_id=$1", result.MediaID).Scan(&claims); err != nil {
		t.Fatalf("count virtual source claims: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1", result.MediaID).Scan(&files); err != nil {
		t.Fatalf("count shared virtual files: %v", err)
	}
	if claims != 2 || files != 2 {
		t.Fatalf("multi-provider state claims=%d files=%d, want 2/2", claims, files)
	}

	reconciled, err := reg.ReconcileVirtualMedia(ctx, 11, "provider-a", []string{"movie-tmdb-999"}, []int{996})
	if err != nil {
		t.Fatalf("reconcile first provider: %v", err)
	}
	if reconciled.FilesRemoved != 1 || reconciled.ItemsRemoved != 0 {
		t.Fatalf("reconcile result=%+v, want one file and preserved item", reconciled)
	}
	var remainingOwner int
	if err := pool.QueryRow(ctx, `
		SELECT virtual_owner_installation_id FROM media_files
		WHERE content_id=$1`, result.MediaID).Scan(&remainingOwner); err != nil {
		t.Fatalf("load remaining provider file: %v", err)
	}
	if remainingOwner != 22 {
		t.Fatalf("remaining file owner=%d, want 22", remainingOwner)
	}
	if err := pool.QueryRow(ctx, `
		SELECT virtual_owner_installation_id FROM media_items WHERE content_id=$1`,
		result.MediaID).Scan(&legacyOwner); err != nil {
		t.Fatalf("load compatibility owner: %v", err)
	}
	if legacyOwner == nil || *legacyOwner != 22 {
		t.Fatalf("surviving provider was not promoted after reconciliation: %v", legacyOwner)
	}
}

func TestReplaceCollectionItemsPreservesOtherVirtualSourceClaims(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	reg := NewVirtualMediaRegistrar(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(994,'ClaimedVirtual','movies',true)`); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	result, err := reg.UpsertVirtualMedia(ctx, 11, VirtualMedia{
		LibraryID: "994", MediaType: "movie", Title: "Shared source",
		IMDbID: "tt40", TMDBID: "40", Source: "request",
		VirtualURI: "virtual://movie/tt40",
	})
	if err != nil {
		t.Fatalf("seed request-owned virtual media: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE media_items SET virtual_source='collection'
		WHERE content_id=$1`, result.MediaID); err != nil {
		t.Fatalf("simulate collection scalar ownership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_collections(id,library_id,slug,title,collection_type)
		VALUES('collection-claim-test',994,'claim-test','Claim test','manual');
		INSERT INTO library_collection_libraries(collection_id,library_id)
		VALUES('collection-claim-test',994)`); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_collection_items(collection_id,media_item_id)
		VALUES('collection-claim-test',$1)`, result.MediaID); err != nil {
		t.Fatalf("seed collection membership: %v", err)
	}

	repo := NewLibraryCollectionRepository(pool)
	if err := repo.ReplaceItems(ctx, "collection-claim-test", nil); err != nil {
		t.Fatalf("replace collection items: %v", err)
	}
	var items, files, claims int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM media_items WHERE content_id=$1),
			(SELECT count(*) FROM media_files WHERE content_id=$1),
			(SELECT count(*) FROM virtual_media_source_claims WHERE content_id=$1)`,
		result.MediaID,
	).Scan(&items, &files, &claims); err != nil {
		t.Fatalf("count preserved virtual source: %v", err)
	}
	if items != 1 || files != 1 || claims != 1 {
		t.Fatalf("other virtual source was deleted: items=%d files=%d claims=%d", items, files, claims)
	}
}

func TestCleanupRequestVirtualMediaPreservesOtherPluginClaim(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	reg := NewVirtualMediaRegistrar(pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(993,'RequestClaims','movies',true)`); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	requestMedia := VirtualMedia{
		LibraryID: "993", MediaType: "movie", Title: "Claim cleanup",
		IMDbID: "tt50", TMDBID: "50", Source: "request:movie:tt50",
		VirtualURI: "virtual://movie/tt50?result=request",
	}
	result, err := reg.UpsertVirtualMedia(ctx, 11, requestMedia)
	if err != nil {
		t.Fatalf("seed request claim: %v", err)
	}
	pluginMedia := requestMedia
	pluginMedia.Source = "provider-b"
	pluginMedia.VirtualURI = "virtual://movie/tt50?result=provider"
	if _, err := reg.UpsertVirtualMedia(ctx, 22, pluginMedia); err != nil {
		t.Fatalf("seed plugin claim: %v", err)
	}

	if err := (&ItemRepository{pool: pool}).CleanupRequestVirtualMedia(ctx, "movie", 50, "", "tt50"); err != nil {
		t.Fatalf("cleanup request media: %v", err)
	}
	var requestFiles, pluginFiles, requestClaims, pluginClaims int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND virtual_owner_installation_id=11),
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND virtual_owner_installation_id=22),
		  (SELECT count(*) FROM virtual_media_source_claims WHERE content_id=$1 AND source_key='request:movie:tt50'),
		  (SELECT count(*) FROM virtual_media_source_claims WHERE content_id=$1 AND source_key='provider-b')`,
		result.MediaID,
	).Scan(&requestFiles, &pluginFiles, &requestClaims, &pluginClaims); err != nil {
		t.Fatalf("inspect request cleanup: %v", err)
	}
	if requestFiles != 0 || requestClaims != 0 {
		t.Fatalf("request ownership remained: files=%d claims=%d", requestFiles, requestClaims)
	}
	if pluginFiles != 1 || pluginClaims != 1 {
		t.Fatalf("other plugin ownership was removed: files=%d claims=%d", pluginFiles, pluginClaims)
	}
}

func seedSharedCollectionAndRequestVirtualFile(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	const (
		contentID    = "movie-tmdb-90"
		collectionID = "shared-virtual-claims"
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(989,'SharedClaims','movies',true)`); err != nil {
		t.Fatalf("seed shared collection folder: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_collections(id,library_id,slug,title,collection_type)
		VALUES($1,989,'shared-virtual-claims','Shared virtual claims','manual')`,
		collectionID); err != nil {
		t.Fatalf("seed shared collection row: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_collection_libraries(collection_id,library_id)
		VALUES($1,989)`, collectionID); err != nil {
		t.Fatalf("seed shared collection library: %v", err)
	}
	repo := NewItemRepository(pool)
	created, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, &models.MediaItem{
		ContentID: contentID, Type: "movie", Title: "Shared Claims",
		SortTitle: "Shared Claims", TmdbID: "90", ImdbID: "tt90", Status: "matched",
	}, []int{989}, []VirtualPlaybackVariant{{
		VirtualURI:          "virtual://movie/tt90",
		OwnerInstallationID: 11,
	}})
	if err != nil || !created {
		t.Fatalf("materialize shared collection item: created=%v err=%v", created, err)
	}
	collections := NewLibraryCollectionRepository(pool)
	if err := collections.ReplaceItems(ctx, collectionID, []LibraryCollectionItemInput{{
		MediaItemID: contentID,
	}}); err != nil {
		t.Fatalf("claim collection virtual item: %v", err)
	}
	if _, err := NewVirtualMediaRegistrar(pool).UpsertVirtualMedia(ctx, 11, VirtualMedia{
		LibraryID: "989", MediaType: "movie", Title: "Shared Claims",
		IMDbID: "tt90", TMDBID: "90", Source: "request:movie:tt90",
		VirtualURI: "virtual://movie/tt90",
	}); err != nil {
		t.Fatalf("add shared request claim: %v", err)
	}
	return contentID
}

func TestCleanupRequestVirtualMediaPreservesSharedCollectionFile(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	contentID := seedSharedCollectionAndRequestVirtualFile(t, pool)

	if err := NewItemRepository(pool).CleanupRequestVirtualMedia(ctx, "movie", 90, "", "tt90"); err != nil {
		t.Fatalf("clean shared request claim: %v", err)
	}
	var files, collectionClaims, requestClaims int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files
		   WHERE content_id=$1 AND file_path='virtual://movie/tt90'
		     AND virtual_owner_installation_id=11),
		  (SELECT count(*) FROM virtual_media_file_source_claims
		   WHERE content_id=$1 AND source_key='collection:shared-virtual-claims'),
		  (SELECT count(*) FROM virtual_media_file_source_claims
		   WHERE content_id=$1 AND source_key='request:movie:tt90')`,
		contentID,
	).Scan(&files, &collectionClaims, &requestClaims); err != nil {
		t.Fatalf("inspect request cleanup with collection claim: %v", err)
	}
	if files != 1 || collectionClaims != 1 || requestClaims != 0 {
		t.Fatalf("shared request cleanup files=%d collection_claims=%d request_claims=%d",
			files, collectionClaims, requestClaims)
	}
}

func TestReplaceCollectionItemsPreservesSharedRequestFile(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	contentID := seedSharedCollectionAndRequestVirtualFile(t, pool)

	if err := NewLibraryCollectionRepository(pool).ReplaceItems(ctx, "shared-virtual-claims", nil); err != nil {
		t.Fatalf("remove shared item from collection: %v", err)
	}
	var (
		files            int
		collectionClaims int
		requestClaims    int
		memberships      int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files
		   WHERE content_id=$1 AND file_path='virtual://movie/tt90'
		     AND virtual_owner_installation_id=11),
		  (SELECT count(*) FROM virtual_media_file_source_claims
		   WHERE content_id=$1 AND source_key='collection:shared-virtual-claims'),
		  (SELECT count(*) FROM virtual_media_file_source_claims
		   WHERE content_id=$1 AND source_key='request:movie:tt90'),
		  (SELECT count(*) FROM library_collection_items WHERE media_item_id=$1)`,
		contentID,
	).Scan(&files, &collectionClaims, &requestClaims, &memberships); err != nil {
		t.Fatalf("inspect collection removal with request claim: %v", err)
	}
	if files != 1 || collectionClaims != 0 || requestClaims != 1 || memberships != 0 {
		t.Fatalf("shared collection removal files=%d collection_claims=%d request_claims=%d memberships=%d",
			files, collectionClaims, requestClaims, memberships)
	}
}

func TestCollectionRemovalCleansFileAfterRequestClaimWasCancelled(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	contentID := seedSharedCollectionAndRequestVirtualFile(t, pool)
	if err := NewItemRepository(pool).CleanupRequestVirtualMedia(ctx, "movie", 90, "", "tt90"); err != nil {
		t.Fatalf("cancel shared request claim: %v", err)
	}
	if err := NewLibraryCollectionRepository(pool).ReplaceItems(ctx, "shared-virtual-claims", nil); err != nil {
		t.Fatalf("remove final collection claim: %v", err)
	}
	var items, files, claims int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_items WHERE content_id=$1),
		  (SELECT count(*) FROM media_files WHERE content_id=$1),
		  (SELECT count(*) FROM virtual_media_source_claims WHERE content_id=$1)`,
		contentID,
	).Scan(&items, &files, &claims); err != nil {
		t.Fatalf("inspect final collection cleanup: %v", err)
	}
	if items != 0 || files != 0 || claims != 0 {
		t.Fatalf("orphaned collection virtual media: items=%d files=%d claims=%d", items, files, claims)
	}
}

func TestDeleteCollectionCleansItsUnsharedVirtualMedia(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	contentID := seedSharedCollectionAndRequestVirtualFile(t, pool)
	if err := NewItemRepository(pool).CleanupRequestVirtualMedia(ctx, "movie", 90, "", "tt90"); err != nil {
		t.Fatalf("cancel shared request claim: %v", err)
	}
	if err := NewLibraryCollectionRepository(pool).Delete(ctx, "shared-virtual-claims"); err != nil {
		t.Fatalf("delete collection: %v", err)
	}
	var items, files, claims, collections int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_items WHERE content_id=$1),
		  (SELECT count(*) FROM media_files WHERE content_id=$1),
		  (SELECT count(*) FROM virtual_media_source_claims WHERE content_id=$1),
		  (SELECT count(*) FROM library_collections WHERE id='shared-virtual-claims')`,
		contentID,
	).Scan(&items, &files, &claims, &collections); err != nil {
		t.Fatalf("inspect collection deletion cleanup: %v", err)
	}
	if items != 0 || files != 0 || claims != 0 || collections != 0 {
		t.Fatalf("orphaned deleted collection state: items=%d files=%d claims=%d collections=%d",
			items, files, claims, collections)
	}
}

func TestCollectionMaterializationPreservesCanonicalLocalItem(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(992,'CanonicalLocal','movies',true)`); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	const contentID = "movie-tmdb-60"
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items(content_id,type,title,tmdb_id,status)
		VALUES($1,'movie','Keep Local Metadata','60','matched')`, contentID); err != nil {
		t.Fatalf("seed local item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container)
		VALUES($1,992,'/media/local-60.mkv',2048,'mkv')`, contentID); err != nil {
		t.Fatalf("seed local file: %v", err)
	}
	repo := NewItemRepository(pool)
	created, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, &models.MediaItem{
		ContentID: contentID, Type: "movie", Title: "Must Not Replace Local",
		SortTitle: "Must Not Replace Local", TmdbID: "60", ImdbID: "tt60",
		Status: "matched",
	}, []int{992}, []VirtualPlaybackVariant{{
		VirtualURI:          "virtual://movie/tt60?profile=4K+HDR",
		Resolution:          "2160p",
		CodecVideo:          "hevc",
		CodecAudio:          "eac3",
		HDR:                 "hdr10",
		OwnerInstallationID: 11,
	}})
	if err != nil {
		t.Fatalf("materialize canonical local item: %v", err)
	}
	if created {
		t.Fatal("existing canonical local item reported as newly created")
	}
	var title string
	var localFiles, virtualFiles int
	if err := pool.QueryRow(ctx, `
		SELECT title,
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND file_path='/media/local-60.mkv'),
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND file_path='virtual://movie/tt60')
		FROM media_items WHERE content_id=$1`, contentID).Scan(&title, &localFiles, &virtualFiles); err != nil {
		t.Fatalf("inspect canonical local item: %v", err)
	}
	if title != "Keep Local Metadata" || localFiles != 1 || virtualFiles != 1 {
		t.Fatalf("local+virtual result title=%q local=%d virtual=%d", title, localFiles, virtualFiles)
	}
	var (
		resolution string
		videoCodec string
		audioCodec string
		hdr        bool
		owner      int
	)
	if err := pool.QueryRow(ctx, `
		SELECT resolution,codec_video,codec_audio,hdr,virtual_owner_installation_id
		FROM media_files
		WHERE content_id=$1 AND file_path='virtual://movie/tt60?profile=4K+HDR'`,
		contentID,
	).Scan(&resolution, &videoCodec, &audioCodec, &hdr, &owner); err != nil {
		t.Fatalf("inspect collection virtual variant metadata: %v", err)
	}
	if resolution != "2160p" || videoCodec != "hevc" || audioCodec != "eac3" || !hdr || owner != 11 {
		t.Fatalf("variant metadata resolution=%q video=%q audio=%q hdr=%v owner=%d",
			resolution, videoCodec, audioCodec, hdr, owner)
	}
}

func TestCleanupUnreferencedCollectionVirtualItemsIsCandidateScoped(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(991,'FailedSync','movies',true)`); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	repo := NewItemRepository(pool)
	materialize := func(contentID, tmdbID string) {
		t.Helper()
		created, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, &models.MediaItem{
			ContentID: contentID, Type: "movie", Title: contentID,
			SortTitle: contentID, TmdbID: tmdbID, Status: "matched",
		}, []int{991}, []VirtualPlaybackVariant{{
			VirtualURI:          "virtual://movie/tmdb/" + tmdbID,
			OwnerInstallationID: 11,
		}})
		if err != nil || !created {
			t.Fatalf("materialize %s: created=%v err=%v", contentID, created, err)
		}
	}
	materialize("movie-tmdb-70", "70")
	materialize("movie-tmdb-71", "71")

	deleted, err := repo.CleanupUnreferencedCollectionVirtualItems(ctx, []string{"movie-tmdb-70"})
	if err != nil {
		t.Fatalf("cleanup failed sync candidates: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want 1", deleted)
	}
	var removed, preserved int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_items WHERE content_id='movie-tmdb-70'),
		  (SELECT count(*) FROM media_items WHERE content_id='movie-tmdb-71')`,
	).Scan(&removed, &preserved); err != nil {
		t.Fatalf("inspect failed sync cleanup: %v", err)
	}
	if removed != 0 || preserved != 1 {
		t.Fatalf("candidate scope removed=%d preserved=%d", removed, preserved)
	}
}

func TestReleasedEpisodeReconciliationSkipsFutureEpisodes(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(990,'ReleaseSchedule','series',true)`); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	repo := NewItemRepository(pool)
	const seriesID = "series-tvdb-80"
	created, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, &models.MediaItem{
		ContentID: seriesID, Type: "series", Title: "Scheduled Series",
		SortTitle: "Scheduled Series", TvdbID: "80", Status: "matched",
	}, []int{990}, []VirtualPlaybackVariant{{
		VirtualURI:          "virtual://series/tvdb/80",
		OwnerInstallationID: 11,
	}})
	if err != nil || !created {
		t.Fatalf("materialize series: created=%v err=%v", created, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO seasons(content_id,series_id,season_number,title)
		VALUES('season-tvdb-80-1',$1,1,'Season 1')`, seriesID); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes(
			content_id,series_id,season_id,season_number,episode_number,title,air_date
		) VALUES
			('episode-tvdb-80-1-1',$1,'season-tvdb-80-1',1,1,'Released',CURRENT_DATE),
			('episode-tvdb-80-1-2',$1,'season-tvdb-80-1',1,2,'Future',CURRENT_DATE+1)`,
		seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	reconciled, err := repo.ReconcileReleasedCollectionVirtualEpisodes(ctx, 100)
	if err != nil {
		t.Fatalf("reconcile released episodes: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled=%d, want 1", reconciled)
	}
	var released, future int
	if err := pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER(WHERE episode_id='episode-tvdb-80-1-1'),
		  count(*) FILTER(WHERE episode_id='episode-tvdb-80-1-2')
		FROM media_files WHERE content_id=$1 AND probe_source='virtual_collection'`,
		seriesID,
	).Scan(&released, &future); err != nil {
		t.Fatalf("inspect scheduled episode files: %v", err)
	}
	if released != 1 || future != 0 {
		t.Fatalf("released files=%d future files=%d", released, future)
	}
}

func TestVirtualMediaUpsertRemovesStaleVariantsWithinSourceClaim(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	reg := NewVirtualMediaRegistrar(pool)
	if _, err := pool.Exec(ctx, "INSERT INTO media_folders(id,name,type,enabled) VALUES(995,'VariantReconcile','movies',true)"); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	input := VirtualMedia{
		LibraryID: "995", MediaType: "movie", Title: "Variant Reconcile",
		IMDbID: "tt30", TMDBID: "30", Source: "provider-a",
		Variants: []VirtualMediaVariant{
			{VirtualURI: "virtual://movie/tt30?result=a"},
			{VirtualURI: "virtual://movie/tt30?result=b"},
		},
	}
	result, err := reg.UpsertVirtualMedia(ctx, 11, input)
	if err != nil {
		t.Fatalf("initial variant upsert: %v", err)
	}
	input.Variants = input.Variants[:1]
	if _, err := reg.UpsertVirtualMedia(ctx, 11, input); err != nil {
		t.Fatalf("authoritative variant refresh: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1", result.MediaID).Scan(&count); err != nil {
		t.Fatalf("count refreshed variants: %v", err)
	}
	if count != 1 {
		t.Fatalf("stale variant remained: count=%d", count)
	}
}

func TestVirtualMediaUpsertPreservesLocalSeriesEpisodeMetadata(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(984,'LocalSeries','series',true);
		INSERT INTO media_items(content_id,type,title,tvdb_id,status)
		VALUES('series-tvdb-200','series','Local Series','200','matched');
		INSERT INTO seasons(content_id,series_id,season_number,title,metadata_source)
		VALUES('local-season-200-1','series-tvdb-200',1,'Local Season Title','local');
		INSERT INTO episodes(
			content_id,series_id,season_id,season_number,episode_number,title,overview,metadata_source
		) VALUES(
			'local-episode-200-1-1','series-tvdb-200','local-season-200-1',1,1,
			'Local Episode Title','Local overview','local'
		);
		INSERT INTO media_files(
			content_id,episode_id,media_folder_id,file_path,file_size,container
		) VALUES(
			'series-tvdb-200','local-episode-200-1-1',984,'/media/local-series-s01e01.mkv',1024,'mkv'
		)`); err != nil {
		t.Fatalf("seed local series: %v", err)
	}

	_, err := NewVirtualMediaRegistrar(pool).UpsertVirtualMedia(ctx, 11, VirtualMedia{
		LibraryID: "984", MediaType: "series", Title: "Plugin Series", TVDBID: "200",
		Source: "provider-a", Episodes: []VirtualEpisode{{
			SeasonNumber: 1, EpisodeNumber: 1, Title: "Plugin Episode",
			Overview: "Plugin overview", VirtualURI: "virtual://series/tvdb/200/1/1",
		}},
	})
	if err != nil {
		t.Fatalf("add virtual source to local series: %v", err)
	}
	var seasonTitle, episodeTitle, overview, virtualEpisodeID string
	if err := pool.QueryRow(ctx, `
		SELECT s.title,e.title,COALESCE(e.overview,''),vf.episode_id
		FROM seasons s
		JOIN episodes e ON e.season_id=s.content_id
		JOIN media_files vf ON vf.content_id=e.series_id AND vf.file_path='virtual://series/tvdb/200/1/1'
		WHERE s.series_id='series-tvdb-200' AND s.season_number=1 AND e.episode_number=1`,
	).Scan(&seasonTitle, &episodeTitle, &overview, &virtualEpisodeID); err != nil {
		t.Fatalf("inspect local+virtual episode: %v", err)
	}
	if seasonTitle != "Local Season Title" || episodeTitle != "Local Episode Title" || overview != "Local overview" {
		t.Fatalf("plugin overwrote local episode metadata: season=%q episode=%q overview=%q", seasonTitle, episodeTitle, overview)
	}
	if virtualEpisodeID != "local-episode-200-1-1" {
		t.Fatalf("virtual file episode_id=%q, want existing local episode ID", virtualEpisodeID)
	}
}

func TestVirtualMetadataOwnershipPromotesSurvivingSource(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO media_folders(id,name,type,enabled) VALUES(983,'OwnerFailover','movies',true)`); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	reg := NewVirtualMediaRegistrar(pool)
	first := VirtualMedia{
		LibraryID: "983", MediaType: "movie", Title: "Primary", IMDbID: "tt201", TMDBID: "201",
		Source: "provider-a", VirtualURI: "virtual://movie/tt201?result=a",
	}
	result, err := reg.UpsertVirtualMedia(ctx, 11, first)
	if err != nil {
		t.Fatalf("upsert primary: %v", err)
	}
	second := first
	second.Source = "provider-b"
	second.Title = "Secondary"
	second.VirtualURI = "virtual://movie/tt201?result=b"
	if _, err := reg.UpsertVirtualMedia(ctx, 22, second); err != nil {
		t.Fatalf("upsert secondary: %v", err)
	}
	if _, err := reg.ReconcileVirtualMedia(ctx, 11, "provider-a", []string{"movie-tmdb-999"}, []int{983}); err != nil {
		t.Fatalf("remove primary: %v", err)
	}
	second.Title = "Promoted Secondary"
	if _, err := reg.UpsertVirtualMedia(ctx, 22, second); err != nil {
		t.Fatalf("refresh promoted secondary: %v", err)
	}
	var title string
	var owner int
	var owns bool
	if err := pool.QueryRow(ctx, `
		SELECT mi.title,mi.virtual_owner_installation_id,claim.owns_item_metadata
		FROM media_items mi
		JOIN virtual_media_source_claims claim ON claim.content_id=mi.content_id
		WHERE mi.content_id=$1 AND claim.plugin_installation_id=22 AND claim.source_key='provider-b'`,
		result.MediaID,
	).Scan(&title, &owner, &owns); err != nil {
		t.Fatalf("inspect promoted owner: %v", err)
	}
	if title != "Promoted Secondary" || owner != 22 || !owns {
		t.Fatalf("survivor was not promoted: title=%q owner=%d owns=%v", title, owner, owns)
	}
}

func TestCollectionMaterializationReconcilesProfilesAndProviders(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(982,'ProfileReconcile','movies',true);
		INSERT INTO library_collections(id,library_id,slug,title,collection_type)
		VALUES('profile-reconcile',982,'profile-reconcile','Profile Reconcile','manual');
		INSERT INTO library_collection_libraries(collection_id,library_id)
		VALUES('profile-reconcile',982)`); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	repo := NewItemRepository(pool)
	item := &models.MediaItem{
		ContentID: "movie-tmdb-202", Type: "movie", Title: "Profiles", SortTitle: "Profiles",
		TmdbID: "202", ImdbID: "tt202", Status: "matched",
	}
	initial := []VirtualPlaybackVariant{
		{VirtualURI: "virtual://movie/tt202?profile=1080p", Resolution: "1080p", OwnerInstallationID: 11},
		{VirtualURI: "virtual://movie/tt202?profile=4K", Resolution: "2160p", OwnerInstallationID: 11},
	}
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, item, []int{982}, initial); err != nil {
		t.Fatalf("materialize profiles: %v", err)
	}
	collections := NewLibraryCollectionRepository(pool)
	if err := collections.ReplaceItems(ctx, "profile-reconcile", []LibraryCollectionItemInput{{MediaItemID: item.ContentID}}); err != nil {
		t.Fatalf("claim initial profiles: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO virtual_media_source_claims(
			plugin_installation_id,source_key,content_id,media_folder_id,owns_item_metadata
		) VALUES(11,'collection',$1,982,false)`, item.ContentID); err != nil {
		t.Fatalf("seed default collection source claim: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO virtual_media_file_source_claims(
			plugin_installation_id,source_key,content_id,media_folder_id,file_path
		) VALUES(11,'collection',$1,982,'virtual://movie/tt202?profile=4K')`, item.ContentID); err != nil {
		t.Fatalf("seed default collection file claim: %v", err)
	}
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, item, []int{982}, initial[:1]); err != nil {
		t.Fatalf("remove profile: %v", err)
	}
	var old4K, defaultClaims int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND file_path='virtual://movie/tt202?profile=4K'),
		  (SELECT count(*) FROM virtual_media_source_claims WHERE content_id=$1 AND source_key='collection')`,
		item.ContentID,
	).Scan(&old4K, &defaultClaims); err != nil {
		t.Fatalf("count removed profile: %v", err)
	}
	if old4K != 0 || defaultClaims != 0 {
		t.Fatalf("removed profile still has files=%d default_claims=%d", old4K, defaultClaims)
	}
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, item, []int{982}, []VirtualPlaybackVariant{{
		VirtualURI: "virtual://movie/tt202", OwnerInstallationID: 22,
	}}); err != nil {
		t.Fatalf("switch provider: %v", err)
	}
	var owner11, owner22 int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER(WHERE virtual_owner_installation_id=11),
		       count(*) FILTER(WHERE virtual_owner_installation_id=22)
		FROM media_files WHERE content_id=$1 AND probe_source='virtual_collection'`, item.ContentID).Scan(&owner11, &owner22); err != nil {
		t.Fatalf("inspect provider switch: %v", err)
	}
	if owner11 != 0 || owner22 != 1 {
		t.Fatalf("provider switch left old=%d new=%d files", owner11, owner22)
	}
}

func TestCollectionVirtualFilesAreScopedToLibrary(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES
			(981,'Movies A','movies',true),(980,'Movies B','movies',true)`); err != nil {
		t.Fatalf("seed folders: %v", err)
	}
	repo := NewItemRepository(pool)
	item := &models.MediaItem{
		ContentID: "movie-tmdb-203", Type: "movie", Title: "Two Libraries", SortTitle: "Two Libraries",
		TmdbID: "203", ImdbID: "tt203", Status: "matched",
	}
	variant := []VirtualPlaybackVariant{{VirtualURI: "virtual://movie/tt203", OwnerInstallationID: 11}}
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, item, []int{981}, variant); err != nil {
		t.Fatalf("materialize first library: %v", err)
	}
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, item, []int{980}, variant); err != nil {
		t.Fatalf("materialize second library: %v", err)
	}
	var count, distinctFolders int
	if err := pool.QueryRow(ctx, `
		SELECT count(*),count(DISTINCT media_folder_id)
		FROM media_files
		WHERE content_id=$1 AND file_path='virtual://movie/tt203' AND virtual_owner_installation_id=11`,
		item.ContentID,
	).Scan(&count, &distinctFolders); err != nil {
		t.Fatalf("inspect library-scoped files: %v", err)
	}
	if count != 2 || distinctFolders != 2 {
		t.Fatalf("virtual files count=%d folders=%d, want 2/2", count, distinctFolders)
	}
}

func TestCollectionClaimsAndCleanupAreScopedToLibrary(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES
			(976,'Claim Scope A','movies',true),(975,'Claim Scope B','movies',true);
		INSERT INTO library_collections(id,library_id,slug,title,collection_type) VALUES
			('claim-scope-a',976,'claim-scope-a','Claim Scope A','manual'),
			('claim-scope-b',975,'claim-scope-b','Claim Scope B','manual');
		INSERT INTO library_collection_libraries(collection_id,library_id) VALUES
			('claim-scope-a',976),('claim-scope-b',975)`); err != nil {
		t.Fatalf("seed scoped collections: %v", err)
	}
	repo := NewItemRepository(pool)
	item := &models.MediaItem{
		ContentID: "movie-tmdb-206", Type: "movie", Title: "Scoped Claims",
		SortTitle: "Scoped Claims", TmdbID: "206", ImdbID: "tt206", Status: "matched",
	}
	variant := []VirtualPlaybackVariant{{
		VirtualURI: "virtual://movie/tt206", OwnerInstallationID: 11,
	}}
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, item, []int{976}, variant); err != nil {
		t.Fatalf("materialize first library: %v", err)
	}
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, item, []int{975}, variant); err != nil {
		t.Fatalf("materialize second library: %v", err)
	}
	collections := NewLibraryCollectionRepository(pool)
	if err := collections.ReplaceItems(ctx, "claim-scope-a", []LibraryCollectionItemInput{{MediaItemID: item.ContentID}}); err != nil {
		t.Fatalf("claim first collection: %v", err)
	}
	if err := collections.ReplaceItems(ctx, "claim-scope-b", []LibraryCollectionItemInput{{MediaItemID: item.ContentID}}); err != nil {
		t.Fatalf("claim second collection: %v", err)
	}
	var claimsA, claimsB int
	if err := pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER(WHERE source_key='collection:claim-scope-a' AND media_folder_id=976),
		  count(*) FILTER(WHERE source_key='collection:claim-scope-b' AND media_folder_id=975)
		FROM virtual_media_file_source_claims WHERE content_id=$1`, item.ContentID,
	).Scan(&claimsA, &claimsB); err != nil {
		t.Fatalf("inspect scoped collection claims: %v", err)
	}
	if claimsA != 1 || claimsB != 1 {
		t.Fatalf("scoped claims first=%d second=%d, want 1/1", claimsA, claimsB)
	}
	var crossed int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM virtual_media_file_source_claims
		WHERE content_id=$1 AND (
		  (source_key='collection:claim-scope-a' AND media_folder_id<>976)
		  OR (source_key='collection:claim-scope-b' AND media_folder_id<>975)
		)`, item.ContentID).Scan(&crossed); err != nil {
		t.Fatalf("inspect crossed collection claims: %v", err)
	}
	if crossed != 0 {
		t.Fatalf("collections claimed %d virtual files outside their libraries", crossed)
	}

	if err := collections.ReplaceItems(ctx, "claim-scope-a", nil); err != nil {
		t.Fatalf("remove first collection item: %v", err)
	}
	var filesA, filesB, owner int
	var ownerSource string
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND media_folder_id=976),
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND media_folder_id=975),
		  virtual_owner_installation_id,virtual_source
		FROM media_items WHERE content_id=$1`, item.ContentID,
	).Scan(&filesA, &filesB, &owner, &ownerSource); err != nil {
		t.Fatalf("inspect scoped collection cleanup: %v", err)
	}
	if filesA != 0 || filesB != 1 || owner != 11 || ownerSource != "collection:claim-scope-b" {
		t.Fatalf("cleanup files=%d/%d owner=%d source=%q", filesA, filesB, owner, ownerSource)
	}
}

func TestReleasedEpisodeReconciliationMaintainsCollectionClaims(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(979,'EpisodeClaims','series',true);
		INSERT INTO library_collections(id,library_id,slug,title,collection_type)
		VALUES('episode-claims',979,'episode-claims','Episode Claims','manual');
		INSERT INTO library_collection_libraries(collection_id,library_id)
		VALUES('episode-claims',979)`); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	repo := NewItemRepository(pool)
	const seriesID = "series-tvdb-204"
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, &models.MediaItem{
		ContentID: seriesID, Type: "series", Title: "Episode Claims", SortTitle: "Episode Claims",
		TvdbID: "204", Status: "matched",
	}, []int{979}, []VirtualPlaybackVariant{{VirtualURI: "virtual://series/tvdb/204", OwnerInstallationID: 11}}); err != nil {
		t.Fatalf("materialize series: %v", err)
	}
	if err := NewLibraryCollectionRepository(pool).ReplaceItems(ctx, "episode-claims", []LibraryCollectionItemInput{{MediaItemID: seriesID}}); err != nil {
		t.Fatalf("claim series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO virtual_media_source_claims(
			plugin_installation_id,source_key,content_id,media_folder_id,owns_item_metadata
		) VALUES(11,'collection',$1,979,false)`, seriesID); err != nil {
		t.Fatalf("seed default collection episode source claim: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO virtual_media_file_source_claims(
			plugin_installation_id,source_key,content_id,media_folder_id,file_path
		) VALUES(11,'collection',$1,979,'virtual://series/tvdb/204')`, seriesID); err != nil {
		t.Fatalf("seed default collection episode file claim: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO seasons(content_id,series_id,season_number,title)
		VALUES('season-tvdb-204-1',$1,1,'Season 1')`, seriesID); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes(content_id,series_id,season_id,season_number,episode_number,title,air_date)
		VALUES('episode-tvdb-204-1-1',$1,'season-tvdb-204-1',1,1,'Released',CURRENT_DATE)`, seriesID); err != nil {
		t.Fatalf("seed released episode: %v", err)
	}
	if err := repo.MaterializeVirtualPlaybackEpisodes(ctx, seriesID); err != nil {
		t.Fatalf("materialize released episode: %v", err)
	}
	const episodePath = "virtual://series/tvdb/204/1/1"
	var files, claims, defaultClaims int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND file_path=$2),
		  (SELECT count(*) FROM virtual_media_file_source_claims WHERE content_id=$1 AND file_path=$2 AND source_key='collection:episode-claims'),
		  (SELECT count(*) FROM virtual_media_file_source_claims WHERE content_id=$1 AND file_path=$2 AND source_key='collection')`,
		seriesID, episodePath,
	).Scan(&files, &claims, &defaultClaims); err != nil {
		t.Fatalf("inspect released episode claims: %v", err)
	}
	if files != 1 || claims != 1 || defaultClaims != 1 {
		t.Fatalf("released episode files=%d claims=%d default_claims=%d, want 1/1/1", files, claims, defaultClaims)
	}
	if _, err := pool.Exec(ctx, `UPDATE episodes SET air_date=CURRENT_DATE+1 WHERE content_id='episode-tvdb-204-1-1'`); err != nil {
		t.Fatalf("move episode to future: %v", err)
	}
	if err := repo.MaterializeVirtualPlaybackEpisodes(ctx, seriesID); err != nil {
		t.Fatalf("remove future episode: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files WHERE content_id=$1 AND file_path=$2),
		  (SELECT count(*) FROM virtual_media_file_source_claims WHERE content_id=$1 AND file_path=$2)`,
		seriesID, episodePath,
	).Scan(&files, &claims); err != nil {
		t.Fatalf("inspect removed episode claims: %v", err)
	}
	if files != 0 || claims != 0 {
		t.Fatalf("future episode left files=%d claims=%d", files, claims)
	}
}

func TestFailedCollectionCleanupRemovesVirtualAdditionsFromExistingItem(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES
			(978,'Local Movies','movies',true),(977,'Virtual Movies','movies',true);
		INSERT INTO media_items(content_id,type,title,tmdb_id,status)
		VALUES('movie-tmdb-205','movie','Existing Local','205','matched');
		INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container)
		VALUES('movie-tmdb-205',978,'/media/existing-local.mkv',1024,'mkv');
		INSERT INTO media_item_libraries(content_id,media_folder_id)
		VALUES('movie-tmdb-205',978)`); err != nil {
		t.Fatalf("seed existing item: %v", err)
	}
	repo := NewItemRepository(pool)
	if _, err := repo.MaterializeVirtualPlaybackItemWithVariants(ctx, &models.MediaItem{
		ContentID: "movie-tmdb-205", Type: "movie", Title: "Existing Local",
		SortTitle: "Existing Local", TmdbID: "205", ImdbID: "tt205", Status: "matched",
	}, []int{977}, []VirtualPlaybackVariant{{VirtualURI: "virtual://movie/tt205", OwnerInstallationID: 11}}); err != nil {
		t.Fatalf("materialize existing item: %v", err)
	}
	if _, err := repo.CleanupUnreferencedCollectionVirtualItems(ctx, []string{"movie-tmdb-205"}); err != nil {
		t.Fatalf("cleanup failed collection effects: %v", err)
	}
	var localFiles, virtualFiles, localLinks, virtualLinks int
	if err := pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER(WHERE file_path='/media/existing-local.mkv'),
		  count(*) FILTER(WHERE file_path='virtual://movie/tt205'),
		  (SELECT count(*) FROM media_item_libraries WHERE content_id='movie-tmdb-205' AND media_folder_id=978),
		  (SELECT count(*) FROM media_item_libraries WHERE content_id='movie-tmdb-205' AND media_folder_id=977)
		FROM media_files WHERE content_id='movie-tmdb-205'`,
	).Scan(&localFiles, &virtualFiles, &localLinks, &virtualLinks); err != nil {
		t.Fatalf("inspect failed collection cleanup: %v", err)
	}
	if localFiles != 1 || virtualFiles != 0 || localLinks != 1 || virtualLinks != 0 {
		t.Fatalf("cleanup local=%d virtual=%d local_links=%d virtual_links=%d", localFiles, virtualFiles, localLinks, virtualLinks)
	}
}

func TestPurgeVirtualPlaybackItemsRemovesOwnerZeroClaimsFromLocalItem(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(972,'Owner Zero Purge','movies',true);
		INSERT INTO media_items(content_id,type,title,tmdb_id,status)
		VALUES('movie-tmdb-209','movie','Owner Zero Purge','209','matched');
		INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container)
		VALUES('movie-tmdb-209',972,'/media/owner-zero-local.mkv',1024,'mkv')`); err != nil {
		t.Fatalf("seed local item: %v", err)
	}
	if _, err := NewVirtualMediaRegistrar(pool).Upsert(ctx, VirtualMedia{
		LibraryID: "972", MediaType: "movie", Title: "Owner Zero Purge",
		IMDbID: "tt209", TMDBID: "209", Source: "generic-source",
		VirtualURI: "virtual://movie/tt209",
	}); err != nil {
		t.Fatalf("add generic virtual source: %v", err)
	}
	files, items, err := NewItemRepository(pool).PurgeVirtualPlaybackItems(ctx, VirtualPurgeOptions{})
	if err != nil {
		t.Fatalf("purge generic virtual source: %v", err)
	}
	if files != 1 || items != 0 {
		t.Fatalf("purge files=%d items=%d, want 1/0", files, items)
	}
	var localFiles, sourceClaims, fileClaims int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files WHERE content_id='movie-tmdb-209'),
		  (SELECT count(*) FROM virtual_media_source_claims WHERE content_id='movie-tmdb-209'),
		  (SELECT count(*) FROM virtual_media_file_source_claims WHERE content_id='movie-tmdb-209')`,
	).Scan(&localFiles, &sourceClaims, &fileClaims); err != nil {
		t.Fatalf("inspect generic purge: %v", err)
	}
	if localFiles != 1 || sourceClaims != 0 || fileClaims != 0 {
		t.Fatalf("generic purge local=%d source_claims=%d file_claims=%d", localFiles, sourceClaims, fileClaims)
	}
}

func TestScopedPurgePreservesSharedLegacyFileAndPromotesOwner(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled)
		VALUES(971,'Shared Legacy Purge','movies',true);
		INSERT INTO media_items(
			content_id,type,title,tmdb_id,status,
			virtual_owner_installation_id,virtual_source,virtual_last_seen_at
		) VALUES('movie-tmdb-210','movie','Shared Legacy Purge','210','matched',11,'provider-a',NOW());
		INSERT INTO media_item_libraries(content_id,media_folder_id)
		VALUES('movie-tmdb-210',971);
		INSERT INTO media_files(
			content_id,media_folder_id,file_path,file_size,container,
			probe_source,virtual_owner_installation_id
		) VALUES('movie-tmdb-210',971,'virtual://movie/tt210',0,'virtual','virtual',0);
		INSERT INTO virtual_media_source_claims(
			plugin_installation_id,source_key,content_id,media_folder_id,owns_item_metadata
		) VALUES
			(11,'provider-a','movie-tmdb-210',971,true),
			(22,'provider-b','movie-tmdb-210',971,false);
		INSERT INTO virtual_media_file_source_claims(
			plugin_installation_id,source_key,content_id,media_folder_id,file_path
		) VALUES
			(11,'provider-a','movie-tmdb-210',971,'virtual://movie/tt210'),
			(22,'provider-b','movie-tmdb-210',971,'virtual://movie/tt210')`); err != nil {
		t.Fatalf("seed shared legacy file: %v", err)
	}
	files, items, err := NewItemRepository(pool).PurgeVirtualPlaybackItems(ctx, VirtualPurgeOptions{InstallationID: 11})
	if err != nil {
		t.Fatalf("purge first legacy owner: %v", err)
	}
	if files != 0 || items != 0 {
		t.Fatalf("scoped purge files=%d items=%d, want shared file preserved", files, items)
	}
	var fileCount, claims11, claims22, owner int
	var source string
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files WHERE content_id='movie-tmdb-210'),
		  (SELECT count(*) FROM virtual_media_source_claims WHERE content_id='movie-tmdb-210' AND plugin_installation_id=11),
		  (SELECT count(*) FROM virtual_media_source_claims WHERE content_id='movie-tmdb-210' AND plugin_installation_id=22),
		  virtual_owner_installation_id,virtual_source
		FROM media_items WHERE content_id='movie-tmdb-210'`,
	).Scan(&fileCount, &claims11, &claims22, &owner, &source); err != nil {
		t.Fatalf("inspect shared legacy purge: %v", err)
	}
	if fileCount != 1 || claims11 != 0 || claims22 != 1 || owner != 22 || source != "provider-b" {
		t.Fatalf("shared purge file=%d claims=%d/%d owner=%d source=%q", fileCount, claims11, claims22, owner, source)
	}
}

// Installation-scoped purges must not delete rows belonging to another
// installation. Ownership currently lives on media_items (one owner per
// content ID); this test pins that invariant until per-file ownership is
// introduced.
func TestPurgeVirtualPlaybackItemsKeepsOtherInstallation(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	const contentID = "movie-tmdb-purge-owner-b"
	if _, err := pool.Exec(ctx, `INSERT INTO media_folders(id,name,type,enabled) VALUES(998,'PurgeTest','mixed',true)`); err != nil {
		t.Fatalf("seed purge test folder: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,virtual_owner_installation_id,virtual_source) VALUES($1,'movie','Purge Test',22,'provider-b')`, contentID); err != nil {
		t.Fatalf("seed other-installation item: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container,probe_source) VALUES($1,998,'virtual://movie/purge-owner-b',0,'virtual','virtual')`, contentID); err != nil {
		t.Fatalf("seed other-installation virtual item: %v", err)
	}

	files, items, err := (&ItemRepository{pool: pool}).PurgeVirtualPlaybackItems(ctx, VirtualPurgeOptions{InstallationID: 11})
	if err != nil {
		t.Fatalf("scoped purge failed: %v", err)
	}
	if files != 0 || items != 0 {
		t.Fatalf("purge of installation 11 removed files=%d items=%d owned by installation 22", files, items)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE file_path='virtual://movie/purge-owner-b'").Scan(&count); err != nil {
		t.Fatalf("check preserved file: %v", err)
	}
	if count != 1 {
		t.Fatalf("other installation's virtual file count=%d, want 1", count)
	}
}
