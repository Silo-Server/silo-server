package trickplay

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
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

func TestExternalDeliveryConfirmationDB(t *testing.T) {
	// A store of its own keeps other tests' revisions out of the claims.
	const store = "local|/trickplay-delivery-test"
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "movie")
	f.reconcile(t)
	job, err := f.repo.ClaimFile(t.Context(), file, "server-a", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	revision, ok, err := f.repo.BeginUpload(t.Context(), file, job.LeaseToken)
	if err != nil || !ok {
		t.Fatalf("begin upload: %v %t", err, ok)
	}
	if ok, err := f.repo.Publish(t.Context(), file, job.LeaseToken, revision, Published{
		Recipe: testRecipe, StoreIdentity: store, Height: 168, Count: 360, SheetBytes: []int{1, 1, 1, 1, 1},
		DeliveryScope: "scope-old",
	}); err != nil || !ok {
		t.Fatalf("publish: %v %t", err, ok)
	}
	served := func(scope string) string {
		t.Helper()
		reader := NewReader(f.pool, identityStore(store), originURLs("/api/v2/artwork/"))
		reader.SetExternalDelivery(scope, originURLs("https://cdn.example/"))
		signed, ok, err := reader.SignedManifest(t.Context(), file)
		if err != nil || !ok {
			t.Fatalf("signed manifest: %v %t", err, ok)
		}
		origin, _, _ := strings.Cut(signed.SheetURLs[0], "trickplay/")
		return origin
	}
	if got := served("scope-old"); got != "https://cdn.example/" {
		t.Fatalf("a revision confirmed at publish was served from %q", got)
	}
	if got := served("scope-a"); got != "/api/v2/artwork/" {
		t.Fatalf("an unconfirmed revision was served from %q", got)
	}

	// Publishing counts as a check, so the revision is due only after its
	// recheck.
	if checks, _ := f.repo.ClaimDeliveryChecks(t.Context(), store, "scope-a", time.Hour, 10); len(checks) != 0 {
		t.Fatal("a revision just checked at publish was claimed")
	}
	checks, err := f.repo.ClaimDeliveryChecks(t.Context(), store, "scope-a", 0, 10)
	if err != nil || !slices.Equal(checks, []DeliveryCheck{{FileID: file, Revision: revision, SheetCount: 5}}) {
		t.Fatalf("claimed %+v, %v", checks, err)
	}
	if again, _ := f.repo.ClaimDeliveryChecks(t.Context(), store, "scope-a", time.Hour, 10); len(again) != 0 {
		t.Fatal("a revision was claimed again before its recheck")
	}
	// A confirmation of a revision replaced meanwhile changes nothing.
	if err := f.repo.RecordDelivery(t.Context(), file, revision+1, store, "scope-a"); err != nil {
		t.Fatal(err)
	}
	if got := served("scope-a"); got != "/api/v2/artwork/" {
		t.Fatalf("a stale confirmation was served from %q", got)
	}
	if err := f.repo.RecordDelivery(t.Context(), file, revision, store, "scope-a"); err != nil {
		t.Fatal(err)
	}
	if got := served("scope-a"); got != "https://cdn.example/" {
		t.Fatalf("a confirmed revision was served from %q", got)
	}

	// A new endpoint configuration has confirmed nothing yet.
	if got := served("scope-b"); got != "/api/v2/artwork/" {
		t.Fatalf("a revision confirmed under another scope was served from %q", got)
	}
	if checks, _ := f.repo.ClaimDeliveryChecks(t.Context(), store, "scope-b", 0, 10); len(checks) != 1 {
		t.Fatalf("claimed %+v under the new scope", checks)
	}
	if checks, _ := f.repo.ClaimDeliveryChecks(t.Context(), store, "scope-a", 0, 10); len(checks) != 0 {
		t.Fatalf("claimed %+v under the confirmed scope", checks)
	}
}
