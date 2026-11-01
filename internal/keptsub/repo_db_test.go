package keptsub

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

// These tests share the database with other packages: they make their own
// library and files and remove them after.

type dbFixture struct {
	pool   *pgxpool.Pool
	repo   *PgRepo
	folder int
	files  []int
}

func newDBFixture(t *testing.T) *dbFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	f := &dbFixture{pool: pool, repo: NewPgRepo(pool)}
	if err := pool.QueryRow(t.Context(), `INSERT INTO public.media_folders (type, name) VALUES ('tv', 'kept subtitle test') RETURNING id`).Scan(&f.folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM public.media_folders WHERE id = $1`, f.folder)
		for _, id := range f.files {
			_, _ = pool.Exec(ctx, `DELETE FROM public.blob_gc_queue WHERE prefix LIKE $1`, fmt.Sprintf("subtitles/embedded/%d/%%", id))
		}
		pool.Close()
	})
	return f
}

func (f *dbFixture) file(t *testing.T) int {
	t.Helper()
	var id int
	if err := f.pool.QueryRow(t.Context(), `
		INSERT INTO public.media_files (media_folder_id, file_path, file_size, file_hash, file_modified_at, container)
		VALUES ($1, $2, 1000, 'hash-'||$2, now(), 'matroska') RETURNING id`,
		f.folder, fmt.Sprintf("/kept-subtitle-test/%d.mkv", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.files = append(f.files, id)
	return id
}

func (f *dbFixture) queued(t *testing.T, fileID int) []string {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT prefix FROM public.blob_gc_queue WHERE prefix LIKE $1 ORDER BY prefix`,
		fmt.Sprintf("subtitles/embedded/%d/%%", fileID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func keptRow(fileID, stream int, digit string) Track {
	tags := true
	t := Track{
		MediaFileID: fileID, StreamIndex: stream, ContainerTrackID: "3", Codec: "subrip",
		SourceSize: 1000, SourceModTime: 1_700_000_000_123_456_789, StreamFormat: "matroska",
		StartTime: 1500 * time.Millisecond, SHA256: strings.Repeat(digit, 64), Bytes: 295, Packets: 3,
		Completeness: CompletenessUnknown, Method: MethodCues, HasOverrideTags: &tags,
	}
	t.BlobKey = blobKey(&t)
	return t
}

func TestKeptTrackRowsAndGarbageDB(t *testing.T) {
	f := newDBFixture(t)
	ctx := t.Context()
	file := f.file(t)

	first := keptRow(file, 2, "a")
	if err := f.repo.PutTrack(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err := f.repo.Track(ctx, file, 2)
	if err != nil || got == nil {
		t.Fatalf("track: %v %v", got, err)
	}
	if got.BlobKey != first.BlobKey || got.StartTime != first.StartTime || got.SourceModTime != first.SourceModTime || got.HasOverrideTags == nil || !*got.HasOverrideTags {
		t.Fatalf("read back %+v, wrote %+v", got, first)
	}
	if none, err := f.repo.Track(ctx, file, 3); none != nil || err != nil {
		t.Fatalf("absent track: %v %v", none, err)
	}

	// Replacing a track queues its old stream; rewriting the same one doesn't.
	if err := f.repo.PutTrack(ctx, first); err != nil {
		t.Fatal(err)
	}
	if q := f.queued(t, file); len(q) != 0 {
		t.Fatalf("an unchanged row queued %v", q)
	}
	second := keptRow(file, 2, "b")
	second.HasOverrideTags = nil
	if err := f.repo.PutTrack(ctx, second); err != nil {
		t.Fatal(err)
	}
	if q := f.queued(t, file); !slices.Equal(q, []string{first.BlobKey}) {
		t.Fatalf("queued after a replacement: %v", q)
	}
	live, err := StreamBlobNamespace().Live(ctx, f.pool, []string{first.BlobKey, second.BlobKey})
	if err != nil || live[first.BlobKey] || !live[second.BlobKey] {
		t.Fatalf("live streams = %v, %v", live, err)
	}
	// A stale delete leaves a newer row alone.
	if err := f.repo.DeleteTrack(ctx, file, 2, first.BlobKey); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.repo.Track(ctx, file, 2); got == nil || got.HasOverrideTags != nil {
		t.Fatalf("after a stale delete: %+v", got)
	}
	// Keys keep their shape.
	bad := keptRow(file, 5, "e")
	bad.BlobKey = "subtitles/embedded/elsewhere.mks"
	if err := f.repo.PutTrack(ctx, bad); err == nil {
		t.Fatal("a row with a misshapen key was recorded")
	}
}

// Whichever path removes a file's tracks, their streams are queued.
func TestKeptTracksGoWithTheirFileDB(t *testing.T) {
	f := newDBFixture(t)
	ctx := t.Context()
	file := f.file(t)
	first, second := keptRow(file, 2, "a"), keptRow(file, 3, "b")
	for _, tr := range []Track{first, second} {
		if err := f.repo.PutTrack(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}

	// A rescan that finds the file changed retires its tracks.
	other := keptRow(file, 4, "c")
	if err := f.repo.PutTrack(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE public.media_files SET file_path = file_path || '.moved' WHERE id = $1`, file); err != nil {
		t.Fatal(err)
	}
	if tracks, _ := f.repo.FileTracks(ctx, file); len(tracks) != 3 {
		t.Fatalf("a move retired tracks: %d left", len(tracks))
	}
	if _, err := f.pool.Exec(ctx, `UPDATE public.media_files SET file_size = 2000 WHERE id = $1`, file); err != nil {
		t.Fatal(err)
	}
	if tracks, _ := f.repo.FileTracks(ctx, file); len(tracks) != 0 {
		t.Fatalf("%d tracks outlived a changed file", len(tracks))
	}
	if q := f.queued(t, file); !slices.Equal(q, []string{first.BlobKey, second.BlobKey, other.BlobKey}) {
		t.Fatalf("queued after the change: %v", q)
	}

	// Deleting the file queues its tracks too, and its prefix goes dead.
	gone := f.file(t)
	last := keptRow(gone, 0, "d")
	if err := f.repo.PutTrack(ctx, last); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("subtitles/embedded/%d/", gone)
	if live, _ := FileBlobNamespace().Live(ctx, f.pool, []string{prefix}); !live[prefix] {
		t.Fatal("a live file's prefix reads as dead")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM public.media_files WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	if q := f.queued(t, gone); !slices.Equal(q, []string{last.BlobKey}) {
		t.Fatalf("queued after deleting the file: %v", q)
	}
	if live, _ := FileBlobNamespace().Live(ctx, f.pool, []string{prefix}); live[prefix] {
		t.Fatal("a deleted file's prefix reads as live")
	}
	// The queue admits the file prefix the sweep queues.
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.blob_gc_queue (prefix, not_before) VALUES ($1, now())`, prefix); err != nil {
		t.Fatalf("queue a file prefix: %v", err)
	}
}

// A key queued before its object is stored waits out the grace, and
// queuing it again never brings its deadline forward.
func TestQueueCollectionDB(t *testing.T) {
	f := newDBFixture(t)
	ctx := t.Context()
	file := f.file(t)
	key := keptRow(file, 1, "f").BlobKey
	if err := f.repo.QueueCollection(ctx, key, orphanGrace); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.QueueCollection(ctx, key, time.Hour); err != nil {
		t.Fatal(err)
	}
	var due time.Duration
	if err := f.pool.QueryRow(ctx, `SELECT extract(epoch FROM not_before - now()) * interval '1 second' FROM public.blob_gc_queue WHERE prefix = $1`, key).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if due < orphanGrace-time.Minute || due > orphanGrace {
		t.Fatalf("due in %v, want about %v", due, orphanGrace)
	}
}

// Past the budget the least recently used rows go and their objects are
// queued. The rows here are huge and long unused, so they rank after every
// other row and the budget leaves other rows alone.
func TestKeptTrackBudgetDB(t *testing.T) {
	f := newDBFixture(t)
	ctx := t.Context()
	file := f.file(t)
	const huge = int64(1) << 50
	rows := []Track{keptRow(file, 1, "a"), keptRow(file, 2, "b"), keptRow(file, 3, "c")}
	for i, tr := range rows {
		tr.Bytes = huge
		if err := f.repo.PutTrack(ctx, tr); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE public.embedded_subtitle_tracks SET updated_at = '1971-01-01'::timestamptz + $3 * interval '1 day'
			WHERE media_file_id = $1 AND embedded_stream_index = $2`, file, tr.StreamIndex, i); err != nil {
			t.Fatal(err)
		}
	}
	if kept, err := f.repo.FileKeptBytes(ctx, file, 2); err != nil || kept != 2*huge {
		t.Fatalf("file's other tracks hold %d bytes, %v; want %d", kept, err, 2*huge)
	}

	// The first is used: now the second is the least recently used.
	if err := f.repo.Touch(ctx, file, 1); err != nil {
		t.Fatal(err)
	}
	var others int64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(byte_size), 0) FROM public.embedded_subtitle_tracks WHERE media_file_id <> $1`, file).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if n, err := f.repo.EvictBeyond(ctx, others+3*huge); err != nil || n != 0 {
		t.Fatalf("within the budget: %d evicted, %v", n, err)
	}
	if n, err := f.repo.EvictBeyond(ctx, others+2*huge); err != nil || n != 1 {
		t.Fatalf("over the budget: %d evicted, %v; want 1", n, err)
	}
	for i, want := range []bool{true, false, true} {
		if got, err := f.repo.Track(ctx, file, rows[i].StreamIndex); err != nil || (got != nil) != want {
			t.Fatalf("track %d kept = %v (%v), want %v", i, got != nil, err, want)
		}
	}
	if q := f.queued(t, file); !slices.Equal(q, []string{rows[1].BlobKey}) {
		t.Fatalf("queued after eviction: %v", q)
	}
}

func TestSubtitleTrackClaimsDB(t *testing.T) {
	f := newDBFixture(t)
	ctx := t.Context()
	file := f.file(t)
	a, b := "00000000-0000-4000-8000-00000000000a", "00000000-0000-4000-8000-00000000000b"
	if ok, err := f.repo.Claim(ctx, file, 2, a, time.Minute); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, err := f.repo.Claim(ctx, file, 2, b, time.Minute); err != nil || ok {
		t.Fatalf("a held claim was taken: %v %v", ok, err)
	}
	if ok, _ := f.repo.Claim(ctx, file, 3, b, time.Minute); !ok {
		t.Fatal("another track's claim was refused")
	}
	// Releasing someone else's claim does nothing.
	if err := f.repo.Release(ctx, file, 2, b); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.repo.Claim(ctx, file, 2, b, time.Minute); ok {
		t.Fatal("a claim was released by another token")
	}
	if err := f.repo.Release(ctx, file, 2, a); err != nil {
		t.Fatal(err)
	}
	// A lease that has run out is taken over.
	if ok, _ := f.repo.Claim(ctx, file, 2, b, 0); !ok {
		t.Fatal("a released claim was refused")
	}
	if ok, _ := f.repo.Claim(ctx, file, 2, a, time.Minute); !ok {
		t.Fatal("an expired claim was refused")
	}
}
