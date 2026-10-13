package trickplay

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests share the database with other packages. They create their own
// libraries, remove them after, and never rely on the queue being empty
// beyond their own rows: every claim names a fixture file.

const testStore = "local|/trickplay-test"

type fixture struct {
	pool    *pgxpool.Pool
	repo    *Repository
	folders []int
	files   []int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{pool: pool, repo: NewRepository(pool)}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM public.media_folders WHERE id = ANY($1)`, f.folders)
		for _, id := range f.files {
			_, _ = pool.Exec(ctx, `DELETE FROM public.blob_gc_queue WHERE prefix LIKE $1`, fmt.Sprintf("trickplay/%d/%%", id))
		}
		pool.Close()
	})
	return f
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// library adds a library of libraryType with trickplay on or off.
func (f *fixture) library(t *testing.T, libraryType string, enabled bool) int {
	t.Helper()
	var id int
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO public.media_folders (type, name, trickplay_enabled) VALUES ($1, 'trickplay test', $2) RETURNING id`,
		libraryType, enabled).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.folders = append(f.folders, id)
	return id
}

// file adds a probed video file.
func (f *fixture) file(t *testing.T, folder int, name string) int {
	t.Helper()
	var id int
	if err := f.pool.QueryRow(t.Context(), `
		INSERT INTO public.media_files (media_folder_id, file_path, file_size, file_hash, duration, container, codec_video,
			probe_updated_at, video_tracks)
		VALUES ($1, $2, 1000000, 'hash-'||$2, 3600, 'matroska', 'h264', now(),
			'[{"codec":"h264","width":1920,"height":1080,"aspect_ratio":"16:9","bit_depth":8}]'::jsonb)
		RETURNING id`, folder, "/trickplay-test/"+name+fmt.Sprint(time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.files = append(f.files, id)
	return id
}

type rowState struct {
	state     string
	failures  int
	due       bool
	revision  *int64
	working   *int64
	lastError string
	version   int
}

func (f *fixture) row(t *testing.T, fileID int) (rowState, bool) {
	t.Helper()
	var r rowState
	err := f.pool.QueryRow(t.Context(), `
		SELECT state, failure_count, available_at <= now(), revision, work_revision, last_error, recipe_version
		FROM public.media_file_trickplay WHERE media_file_id = $1`, fileID).
		Scan(&r.state, &r.failures, &r.due, &r.revision, &r.working, &r.lastError, &r.version)
	if err != nil {
		return rowState{}, false
	}
	return r, true
}

func (f *fixture) queued(t *testing.T, fileID int) []string {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT prefix FROM public.blob_gc_queue WHERE prefix LIKE $1 ORDER BY prefix`,
		fmt.Sprintf("trickplay/%d/%%", fileID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			t.Fatal(err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

var testRecipe = Recipe{Width: 300, IntervalMS: 10000}

func (f *fixture) reconcile(t *testing.T) ReconcileStats {
	t.Helper()
	stats, err := f.repo.Reconcile(t.Context(), testRecipe, testStore, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

// generate claims fileID, uploads, and publishes, returning the revision.
func (f *fixture) generate(t *testing.T, fileID int, owner string) int64 {
	t.Helper()
	job, err := f.repo.ClaimFile(t.Context(), fileID, owner, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim %d: %v %v", fileID, job, err)
	}
	owner = job.LeaseToken
	revision, ok, err := f.repo.BeginUpload(t.Context(), fileID, owner)
	if err != nil || !ok {
		t.Fatalf("begin upload: %v %t", err, ok)
	}
	published, err := f.repo.Publish(t.Context(), fileID, owner, revision, Published{
		Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 360, SheetBytes: []int{200_000, 180_000, 150_000, 60_000, 30_000},
	})
	if err != nil || !published {
		t.Fatalf("publish: %v %t", err, published)
	}
	return revision
}

func TestReconcileQueuesOptedInVideoFilesDB(t *testing.T) {
	f := newFixture(t)
	movies := f.library(t, "Movies", true)
	off := f.library(t, "movies", false)
	books := f.library(t, "audiobooks", true)
	queuedFile := f.file(t, movies, "a")
	missing := f.file(t, movies, "missing")
	unprobed := f.file(t, movies, "unprobed")
	noVideo := f.file(t, movies, "novideo")
	f.exec(t, `UPDATE public.media_files SET missing_since = now() WHERE id = $1`, missing)
	f.exec(t, `UPDATE public.media_files SET probe_updated_at = NULL WHERE id = $1`, unprobed)
	f.exec(t, `UPDATE public.media_files SET video_tracks = '[]' WHERE id = $1`, noVideo)
	offFile := f.file(t, off, "off")
	bookFile := f.file(t, books, "book")

	f.reconcile(t)
	if row, ok := f.row(t, queuedFile); !ok || row.state != statePending || !row.due || row.version != AlgorithmVersion {
		t.Fatalf("opted-in file: %+v %t", row, ok)
	}
	for _, id := range []int{missing, unprobed, noVideo, offFile, bookFile} {
		if _, ok := f.row(t, id); ok {
			t.Errorf("file %d should not be queued", id)
		}
	}
	// Idempotent.
	f.reconcile(t)
	if row, _ := f.row(t, queuedFile); row.state != statePending {
		t.Fatalf("second pass changed the row: %+v", row)
	}
}

func TestClaimPublishAndServeDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "tv", true), "episode")
	f.reconcile(t)

	job, err := f.repo.ClaimFile(t.Context(), file, "server-a", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	if job.DurationSeconds != 3600 || job.Container != "matroska" || job.Codec != "h264" || len(job.VideoTracks) != 1 || job.VideoTracks[0].AspectRatio != "16:9" {
		t.Fatalf("job %+v", job)
	}
	if again, _ := f.repo.ClaimFile(t.Context(), file, "server-b", time.Minute); again != nil {
		t.Fatal("a leased file was claimed twice")
	}
	if ok, err := f.repo.Heartbeat(t.Context(), file, "server-b", time.Minute); err != nil || ok {
		t.Fatal("another server renewed the lease")
	}
	revision, ok, err := f.repo.BeginUpload(t.Context(), file, job.LeaseToken)
	if err != nil || !ok {
		t.Fatalf("begin upload: %v %t", err, ok)
	}
	if manifests, _ := f.repo.Manifests(t.Context(), []int{file}, testStore); len(manifests) != 0 {
		t.Fatal("nothing is served before publishing")
	}
	if ok, _ := f.repo.Publish(t.Context(), file, "server-b", revision, Published{Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 360, SheetBytes: []int{1}}); ok {
		t.Fatal("another server published")
	}
	if ok, err := f.repo.Publish(t.Context(), file, job.LeaseToken, revision, Published{
		Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 360, SheetBytes: []int{180_000, 200_000, 150_000, 60_000, 30_000},
	}); err != nil || !ok {
		t.Fatalf("publish: %v %t", err, ok)
	}
	manifests, err := f.repo.Manifests(t.Context(), []int{file}, testStore)
	if err != nil {
		t.Fatal(err)
	}
	want := Manifest{FileID: file, Revision: revision, Width: 300, Height: 168, TileColumns: 10, TileRows: 8, IntervalMS: 10000,
		ThumbnailCount: 360, SheetCount: 5, Bandwidth: 2000}
	if manifests[file] != want {
		t.Fatalf("manifest %+v, want %+v", manifests[file], want)
	}
	if other, _ := f.repo.Manifests(t.Context(), []int{file}, "s3|elsewhere"); len(other) != 0 {
		t.Fatal("sheets in another store were served")
	}

	// Regenerating keeps serving the old revision until the new one publishes,
	// then queues the old one for deletion.
	if n, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil || n != 1 {
		t.Fatalf("regenerate: %d %v", n, err)
	}
	if manifests, _ := f.repo.Manifests(t.Context(), []int{file}, testStore); manifests[file].Revision != revision {
		t.Fatal("the old revision stopped serving before the new one published")
	}
	next := f.generate(t, file, "server-b")
	if manifests, _ := f.repo.Manifests(t.Context(), []int{file}, testStore); manifests[file].Revision != next {
		t.Fatal("the new revision is not served")
	}
	if got := f.queued(t, file); !slices.Equal(got, []string{revisionPrefix(file, revision)}) {
		t.Fatalf("queued %v, want the displaced revision", got)
	}
}

func TestServingFollowsTheFileDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "movie")
	f.reconcile(t)
	f.generate(t, file, "server-a")
	serves := func() bool {
		manifests, err := f.repo.Manifests(t.Context(), []int{file}, testStore)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := manifests[file]
		return ok
	}
	// A re-probe that rounds the duration differently, or a hash computed
	// later, is the same file.
	f.exec(t, `UPDATE public.media_files SET duration = 3602, file_hash = NULL WHERE id = $1`, file)
	if !serves() {
		t.Fatal("the same file stopped serving")
	}
	f.reconcile(t)
	if row, _ := f.row(t, file); row.state != stateReady {
		t.Fatalf("the same file was requeued: %+v", row)
	}
	// A replaced file is not.
	f.exec(t, `UPDATE public.media_files SET file_size = 2000000 WHERE id = $1`, file)
	if serves() {
		t.Fatal("sheets of the replaced file are served")
	}
	f.reconcile(t)
	if row, _ := f.row(t, file); row.state != statePending || !row.due {
		t.Fatalf("the replaced file was not requeued: %+v", row)
	}
}

func TestReconcileRequeuesOtherRecipesAndStoresDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	file, newer := f.file(t, folder, "a"), f.file(t, folder, "newer")
	f.reconcile(t)
	f.generate(t, file, "server-a")
	f.generate(t, newer, "server-a")
	// A newer algorithm made this one: an older server leaves it alone.
	f.exec(t, `UPDATE public.media_file_trickplay SET recipe_version = $2, published_recipe = 'v99/x' WHERE media_file_id = $1`, newer, AlgorithmVersion+1)

	if _, err := f.repo.Reconcile(t.Context(), Recipe{Width: 320, IntervalMS: 10000}, testStore, 1000); err != nil {
		t.Fatal(err)
	}
	if row, _ := f.row(t, file); row.state != statePending {
		t.Fatalf("a changed width did not requeue: %+v", row)
	}
	if row, _ := f.row(t, newer); row.state != stateReady {
		t.Fatalf("an older server requeued a newer algorithm's row: %+v", row)
	}
	if job, _ := f.repo.ClaimFile(t.Context(), newer, "server-a", time.Minute); job != nil {
		t.Fatal("an older server claimed a newer algorithm's row")
	}
}

func TestExpiredLeaseIsReclaimedAsAFailureDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "crash")
	f.reconcile(t)
	job, err := f.repo.ClaimFile(t.Context(), file, "server-a", time.Minute)
	if err != nil || job == nil {
		t.Fatal("claim")
	}
	revision, _, _ := f.repo.BeginUpload(t.Context(), file, job.LeaseToken)
	// server-a dies: its lease runs out.
	f.exec(t, `UPDATE public.media_file_trickplay SET lease_expires_at = now() - interval '1 second' WHERE media_file_id = $1`, file)
	if ok, _ := f.repo.Heartbeat(t.Context(), file, job.LeaseToken, time.Minute); ok {
		t.Fatal("an expired lease was renewed")
	}
	stats := f.reconcile(t)
	if stats.Reclaimed < 1 {
		t.Fatalf("stats %+v", stats)
	}
	row, _ := f.row(t, file)
	if row.state != statePending || row.failures != 1 || row.due || row.working != nil || row.lastError != "lease expired" {
		t.Fatalf("reclaimed row %+v, want pending, one failure, backing off", row)
	}
	if got := f.queued(t, file); !slices.Equal(got, []string{revisionPrefix(file, revision)}) {
		t.Fatalf("queued %v, want the abandoned revision", got)
	}
	if ok, _ := f.repo.Publish(t.Context(), file, job.LeaseToken, revision, Published{Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 1, SheetBytes: []int{1}}); ok {
		t.Fatal("the dead server published after losing its lease")
	}
}

func TestFinishOutcomesDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	failed, unusable, released := f.file(t, folder, "failed"), f.file(t, folder, "unusable"), f.file(t, folder, "released")
	f.reconcile(t)
	for _, tt := range []struct {
		file    int
		outcome Outcome
		delay   time.Duration
		want    rowState
	}{
		{failed, Failed, 0, rowState{state: statePending, failures: 1, lastError: "boom"}},
		{unusable, Unusable, 0, rowState{state: stateUnusable, failures: 1, due: true, lastError: "boom"}},
		{released, Released, time.Minute, rowState{state: statePending, failures: 0, lastError: "boom"}},
	} {
		job, err := f.repo.ClaimFile(t.Context(), tt.file, "server-a", time.Minute)
		if err != nil || job == nil {
			t.Fatal("claim")
		}
		if ok, err := f.repo.Finish(t.Context(), tt.file, job.LeaseToken, tt.outcome, "boom", tt.delay); err != nil || !ok {
			t.Fatalf("finish: %v %t", err, ok)
		}
		row, _ := f.row(t, tt.file)
		row.version, row.revision, row.working = 0, nil, nil
		if row != tt.want {
			t.Errorf("outcome %d: row %+v, want %+v", tt.outcome, row, tt.want)
		}
	}
	// An unusable file is retried once it changes.
	f.exec(t, `UPDATE public.media_files SET file_size = 5 WHERE id = $1`, unusable)
	f.reconcile(t)
	if row, _ := f.row(t, unusable); row.state != statePending || row.failures != 0 {
		t.Fatalf("a replaced unusable file was not requeued: %+v", row)
	}
}

func TestClaimsDoNotOverlapDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	var files []int
	for i := range 12 {
		files = append(files, f.file(t, folder, fmt.Sprintf("parallel-%d", i)))
	}
	f.reconcile(t)
	var mu sync.Mutex
	claimed := map[int]string{}
	var wg sync.WaitGroup
	for worker := range 6 {
		wg.Go(func() {
			owner := fmt.Sprintf("server-%d", worker)
			for {
				job, err := f.repo.Claim(t.Context(), owner, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if job == nil {
					return
				}
				mu.Lock()
				if previous, dup := claimed[job.FileID]; dup {
					t.Errorf("file %d claimed by %s and %s", job.FileID, previous, owner)
				}
				claimed[job.FileID] = owner
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	for _, file := range files {
		if _, ok := claimed[file]; !ok {
			t.Errorf("file %d was never claimed", file)
		}
	}
}

func TestOptOutAndFileDeletionQueueSheetsDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	kept, deleted := f.file(t, folder, "kept"), f.file(t, folder, "deleted")
	f.reconcile(t)
	keptRevision := f.generate(t, kept, "server-a")
	deletedRevision := f.generate(t, deleted, "server-a")

	f.exec(t, `DELETE FROM public.media_files WHERE id = $1`, deleted)
	if got := f.queued(t, deleted); !slices.Equal(got, []string{revisionPrefix(deleted, deletedRevision)}) {
		t.Fatalf("deleting the file queued %v", got)
	}
	f.exec(t, `UPDATE public.media_folders SET trickplay_enabled = false WHERE id = $1`, folder)
	stats := f.reconcile(t)
	if _, ok := f.row(t, kept); ok || stats.Removed < 1 {
		t.Fatalf("opting out kept the row (stats %+v)", stats)
	}
	if got := f.queued(t, kept); !slices.Equal(got, []string{revisionPrefix(kept, keptRevision)}) {
		t.Fatalf("opting out queued %v", got)
	}
}

func TestBlobNamespaceLiveDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "live")
	f.reconcile(t)
	published := f.generate(t, file, "server-a")
	if _, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil {
		t.Fatal(err)
	}
	job, err := f.repo.ClaimFile(t.Context(), file, "server-b", time.Minute)
	if err != nil || job == nil {
		t.Fatal("claim")
	}
	working, _, _ := f.repo.BeginUpload(t.Context(), file, job.LeaseToken)
	dead := newRevision()
	ns := BlobNamespace()
	prefixes := []string{revisionPrefix(file, published), revisionPrefix(file, working), revisionPrefix(file, dead)}
	live, err := ns.Live(t.Context(), f.pool, prefixes)
	if err != nil {
		t.Fatal(err)
	}
	if !live[prefixes[0]] || !live[prefixes[1]] || live[prefixes[2]] {
		t.Fatalf("live %v", live)
	}
	for _, key := range []string{SheetKey(file, published, 0), prefixes[0]} {
		if group, ok := ns.Group(key); !ok || group != prefixes[0] {
			t.Errorf("group %q = %q %t", key, group, ok)
		}
	}
	for _, key := range []string{"trickplay/12/", "trickplay/012/5/0.5.jpg", "trickplay/12/0/0.0.jpg", "chapter-images/12/0/w300.webp"} {
		if _, ok := ns.Group(key); ok {
			t.Errorf("%q grouped", key)
		}
	}
}
