package metadata

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The repository lists the matched items a scan scope touches with the
// folders of all their files, and stores fingerprints per target.
func TestLocalSidecarRepositoryDB(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	repo := NewLocalSidecarRepository(pool)
	nonce := time.Now().UnixNano()
	root := fmt.Sprintf("/lsr-%d", nonce)
	folderID := insertTestFolder(t, pool, "mixed")

	series := fmt.Sprintf("series-lsr-%d", nonce)
	movie := fmt.Sprintf("movie-lsr-%d", nonce)
	pending := fmt.Sprintf("pending-lsr-%d", nonce)
	gone := fmt.Sprintf("gone-lsr-%d", nonce)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{series, movie, pending, gone})
		_, _ = pool.Exec(context.Background(), `DELETE FROM local_sidecar_fingerprints WHERE content_id LIKE $1`, fmt.Sprintf("%%-lsr-%d%%", nonce))
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	for id, status := range map[string]string{series: "matched", movie: "matched", pending: "pending_match", gone: "matched"} {
		itemType := "movie"
		if id == series {
			itemType = "series"
		}
		exec(`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, $2, $1, $3, '{}'::text[])`, id, itemType, status)
	}
	file := func(contentID, path, observedRoot string, missing bool) {
		t.Helper()
		exec(`INSERT INTO media_files (content_id, media_folder_id, file_path, observed_root_path, file_size, missing_since)
			VALUES ($1, $2, $3, $4, 1, CASE WHEN $5 THEN now() END)`, contentID, folderID, root+path, root+observedRoot, missing)
	}
	file(series, "/Show/Season 01/Show S01E01.mkv", "/Show", false)
	file(series, "/Show/Season 02/Show S02E01.mkv", "/Show", false)
	file(movie, "/Film/Film.mkv", "/Film", false)
	file(pending, "/Show/Season 01/Other.mkv", "/Show", false)
	file(gone, "/Show/Season 01/Gone.mkv", "/Show", true)
	exec(`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1, $2, 1)`, series+"-s1", series)
	exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number) VALUES ($1, $2, 1, 1)`, series+"-e1", series)

	// A scope inside one season still reports every folder of the series.
	contents, err := repo.ListScopeContents(ctx, folderID, root+"/Show/Season 01")
	if err != nil {
		t.Fatalf("ListScopeContents: %v", err)
	}
	if len(contents) != 1 || contents[0].ContentID != series || contents[0].Type != "series" || contents[0].FolderID != folderID ||
		contents[0].File != root+"/Show/Season 01/Show S01E01.mkv" {
		t.Fatalf("scope contents = %+v, want only the matched series", contents)
	}
	dirs := slices.Clone(contents[0].Dirs)
	slices.Sort(dirs)
	if want := []string{root + "/Show", root + "/Show/Season 01", root + "/Show/Season 02"}; !slices.Equal(dirs, want) {
		t.Fatalf("series folders = %v, want %v", dirs, want)
	}
	if contents, err = repo.ListContents(ctx, movie); err != nil || len(contents) != 1 || !slices.Equal(contents[0].Dirs, []string{root + "/Film"}) {
		t.Fatalf("ListContents(movie) = %+v, %v", contents, err)
	}

	if id, err := repo.ContentIDOfFile(ctx, folderID, root+"/Film/Film.mkv"); err != nil || id != movie {
		t.Fatalf("ContentIDOfFile = %q, %v; want %s", id, err, movie)
	}
	if id, err := repo.ContentIDOfFile(ctx, folderID, root+"/Film/Other.mkv"); err != nil || id != "" {
		t.Fatalf("ContentIDOfFile(unknown) = %q, %v; want none", id, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if id, err := repo.ContentIDOfFile(canceled, folderID, root+"/Film/Film.mkv"); err == nil {
		t.Fatalf("ContentIDOfFile with a failed query = %q, nil; want the error", id)
	}

	seasons, episodes, err := repo.SeriesChildren(ctx, series)
	if err != nil || seasons[1] != series+"-s1" || episodes[episodeResultKey{1, 1}] != series+"-e1" {
		t.Fatalf("SeriesChildren = %v %v, %v", seasons, episodes, err)
	}

	otherFolderID := insertTestFolder(t, pool, "series")
	absent := fmt.Sprintf("absent-lsr-%d", nonce)
	records := []localSidecarRecord{
		{FolderID: folderID, TargetType: RefreshTargetItem, ContentID: series, ItemID: series, Fingerprint: "fp1", FolderDigest: "d1", ProviderIDs: map[string]string{"tvdb": "1"}},
		{FolderID: otherFolderID, TargetType: RefreshTargetItem, ContentID: series, ItemID: series, Fingerprint: "other"},
		{FolderID: folderID, TargetType: RefreshTargetEpisode, ContentID: series + "-e1", ItemID: series, Fingerprint: "ep1"},
		{FolderID: folderID, TargetType: RefreshTargetItem, ContentID: gone, ItemID: gone, Fingerprint: "gone"},
		// An item deleted before its record is stored.
		{FolderID: folderID, TargetType: RefreshTargetItem, ContentID: absent, ItemID: absent, Fingerprint: "absent"},
	}
	if err := repo.Upsert(ctx, records); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// A baseline never replaces a stored record, and is stored where none is.
	if err := repo.Upsert(ctx, []localSidecarRecord{
		{FolderID: folderID, TargetType: RefreshTargetItem, ContentID: series, ItemID: series, Fingerprint: "late baseline", Baseline: true},
		{FolderID: folderID, TargetType: RefreshTargetItem, ContentID: pending, ItemID: pending, Fingerprint: "pending baseline", FolderDigest: "md", Baseline: true},
	}); err != nil {
		t.Fatalf("Upsert baselines: %v", err)
	}
	if got, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{series, pending}); err != nil || got[series].Fingerprint != "fp1" || got[pending].Fingerprint != "pending baseline" {
		t.Fatalf("records after baselines = %+v, %v; want the series record kept and the new baseline stored", got, err)
	}
	// The folder digest advances only while the fingerprint is the one given.
	if err := repo.AdvanceFolderDigest(ctx, folderID, pending, "stale", "md2"); err != nil {
		t.Fatalf("AdvanceFolderDigest: %v", err)
	}
	if err := repo.AdvanceFolderDigest(ctx, folderID, pending, "pending baseline", "md3"); err != nil {
		t.Fatalf("AdvanceFolderDigest: %v", err)
	}
	if got, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{pending}); err != nil || got[pending].FolderDigest != "md3" {
		t.Fatalf("pending record = %+v, %v; want only the matching advance applied", got, err)
	}
	// A conditional write replaces the record only while it still holds the
	// fingerprint the writer read.
	stale, current := "stale", "pending baseline"
	if err := repo.Upsert(ctx, []localSidecarRecord{
		{FolderID: folderID, TargetType: RefreshTargetItem, ContentID: pending, ItemID: pending, Fingerprint: "lost", ifFingerprint: &stale},
	}); err != nil {
		t.Fatalf("Upsert conditional: %v", err)
	}
	if got, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{pending}); err != nil || got[pending].Fingerprint != "pending baseline" {
		t.Fatalf("pending record after a stale conditional write = %+v, %v; want it kept", got, err)
	}
	// Nor a record that moved to another item since (a rename cascades into
	// item_id).
	if err := repo.Upsert(ctx, []localSidecarRecord{
		{FolderID: folderID, TargetType: RefreshTargetItem, ContentID: pending, ItemID: movie, Fingerprint: "lost", ifFingerprint: &current},
	}); err != nil {
		t.Fatalf("Upsert conditional: %v", err)
	}
	if got, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{pending}); err != nil || got[pending].Fingerprint != "pending baseline" {
		t.Fatalf("pending record after a write for another owner = %+v, %v; want it kept", got, err)
	}
	if err := repo.Upsert(ctx, []localSidecarRecord{
		{FolderID: folderID, TargetType: RefreshTargetItem, ContentID: pending, ItemID: pending, Fingerprint: "won", FolderDigest: "md4", ifFingerprint: &current},
	}); err != nil {
		t.Fatalf("Upsert conditional: %v", err)
	}
	if got, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{pending}); err != nil || got[pending].Fingerprint != "won" || got[pending].FolderDigest != "md4" {
		t.Fatalf("pending record after a matching conditional write = %+v, %v; want it replaced", got, err)
	}
	if stored, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{absent}); err != nil || len(stored) != 0 {
		t.Fatalf("record of a deleted item = %+v, %v; want none", stored, err)
	}
	// Deleting an item drops its records.
	exec(`DELETE FROM media_items WHERE content_id = $1`, gone)
	if stored, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{gone}); err != nil || len(stored) != 0 {
		t.Fatalf("records after deleting the item = %+v, %v; want none", stored, err)
	}
	records[0].Fingerprint, records[0].ProviderIDs = "fp2", map[string]string{"tvdb": "2"}
	if err := repo.Upsert(ctx, records[:1]); err != nil {
		t.Fatalf("Upsert again: %v", err)
	}
	items, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{series, movie})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := items[series]; len(items) != 1 || got.Fingerprint != "fp2" || got.FolderDigest != "d1" || !maps.Equal(got.ProviderIDs, map[string]string{"tvdb": "2"}) {
		t.Fatalf("items = %+v, want the replaced series record only", items)
	}
	if episodesStored, err := repo.Get(ctx, folderID, RefreshTargetEpisode, []string{series + "-e1"}); err != nil || episodesStored[series+"-e1"].Fingerprint != "ep1" {
		t.Fatalf("episode records = %+v, %v", episodesStored, err)
	}
	// Each library keeps its own record, and deleting a library drops its rows.
	if other, err := repo.Get(ctx, otherFolderID, RefreshTargetItem, []string{series}); err != nil || other[series].Fingerprint != "other" {
		t.Fatalf("other library's record = %+v, %v", other, err)
	}
	exec(`DELETE FROM media_folders WHERE id = $1`, otherFolderID)
	if other, err := repo.Get(ctx, otherFolderID, RefreshTargetItem, []string{series}); err != nil || len(other) != 0 {
		t.Fatalf("deleted library's records = %+v, %v", other, err)
	}

	// A content ID rename moves the records with the item.
	renamed := series + "-renamed"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, renamed)
	})
	exec(`SELECT silo_rename_content_ids($1::text[], $2::text[])`, []string{series}, []string{renamed})
	if moved, err := repo.Get(ctx, folderID, RefreshTargetItem, []string{renamed}); err != nil || moved[renamed].Fingerprint != "fp2" || moved[renamed].ItemID != renamed {
		t.Fatalf("records after rename = %+v, %v", moved, err)
	}
	if episode, err := repo.Get(ctx, folderID, RefreshTargetEpisode, []string{series + "-e1"}); err != nil || episode[series+"-e1"].ItemID != renamed {
		t.Fatalf("episode record after the series rename = %+v, %v", episode, err)
	}

	// An item's lock excludes every other session until it is released, and
	// leaves other items free.
	tryLock := func(contentID string) bool {
		t.Helper()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		defer conn.Release()
		key := localSidecarLockKey(folderID, contentID)
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
			t.Fatalf("try lock: %v", err)
		}
		if locked {
			if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key); err != nil {
				t.Fatalf("unlock: %v", err)
			}
		}
		return locked
	}
	repo.lockWait = 200 * time.Millisecond
	unlock, locked, err := repo.LockItem(ctx, folderID, movie)
	if err != nil || !locked {
		t.Fatalf("LockItem = %v, %v; want the lock", locked, err)
	}
	if tryLock(movie) {
		t.Fatal("another session took a held item lock")
	}
	// A check on another node waits, then reports the lock as held. (Another
	// repository has its own slots, as another node's process does; with a
	// small test pool this one's only slot is taken.)
	other := NewLocalSidecarRepository(pool)
	other.lockWait = 200 * time.Millisecond
	if _, again, err := other.LockItem(ctx, folderID, movie); err != nil || again {
		t.Fatalf("second LockItem = %v, %v; want it reported as held after the wait", again, err)
	}
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND NOT granted
			  AND ((classid::bigint << 32) | objid::bigint) = $1`, localSidecarLockKey(folderID, movie)).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("sessions left queued after the wait ran out = %d, %v; want none", queued, err)
	}
	if !tryLock(renamed) {
		t.Fatal("one item's lock blocked another item")
	}
	// A waiter gets the lock once the holder releases it.
	acquired := make(chan bool, 1)
	other.lockWait = time.Minute
	go func() {
		release, ok, err := other.LockItem(ctx, folderID, movie)
		if err == nil && ok {
			release()
		}
		acquired <- ok && err == nil
	}()
	// Release only once the waiter is queued on the lock.
	waitingKey := localSidecarLockKey(folderID, movie)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM pg_locks
				WHERE locktype = 'advisory' AND NOT granted
				  AND ((classid::bigint << 32) | objid::bigint) = $1)`, waitingKey).Scan(&waiting); err != nil {
			t.Fatalf("reading lock waits: %v", err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second check never waited for the item lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	unlock()
	if !<-acquired {
		t.Fatal("a check waiting for the item lock did not get it after unlock")
	}
	if !tryLock(movie) {
		t.Fatal("the item lock was still held after unlock")
	}

	// With a one-connection pool the lock takes a connection of its own, so
	// the pool's connection stays free for the re-apply.
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("one-connection pool: %v", err)
	}
	t.Cleanup(single.Close)
	singleRepo := NewLocalSidecarRepository(single)
	singleRepo.lockWait = 200 * time.Millisecond
	release, locked, err := singleRepo.LockItem(ctx, folderID, movie)
	if err != nil || !locked {
		t.Fatalf("LockItem on a one-connection pool = %v, %v; want the lock", locked, err)
	}
	if _, err := singleRepo.Get(ctx, folderID, RefreshTargetItem, []string{movie}); err != nil {
		t.Fatalf("query while the lock is held: %v", err)
	}
	if tryLock(movie) {
		t.Fatal("another session took the one-connection pool's item lock")
	}
	release()
	if !tryLock(movie) {
		t.Fatal("the one-connection pool's item lock was still held after unlock")
	}
}
