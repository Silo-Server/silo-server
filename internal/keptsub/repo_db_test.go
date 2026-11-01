package keptsub

import (
	"context"
	"errors"
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
			_, _ = pool.Exec(ctx, `DELETE FROM public.embedded_subtitle_track_reservations WHERE media_file_id = $1`, id)
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

func (f *dbFixture) int64(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The usage row follows the rows through every path that changes them. Past
// the store's budget a reservation evicts the least recently used rows, and
// one that can't make room evicts nothing. The rows here are huge and long
// unused, so they rank before every other row, and only a reservation that
// evicts nothing could reach other rows.
func TestKeptTrackBudgetDB(t *testing.T) {
	f := newDBFixture(t)
	ctx := t.Context()
	file, other := f.file(t), f.file(t)
	usage := func() int64 {
		t.Helper()
		got := f.int64(t, `SELECT kept_bytes FROM public.embedded_subtitle_track_usage`)
		if want := f.int64(t, `SELECT COALESCE(SUM(byte_size), 0) FROM public.embedded_subtitle_tracks`); got != want {
			t.Fatalf("usage row holds %d bytes, rows %d", got, want)
		}
		return got
	}
	const huge = int64(1) << 50
	rows := []Track{keptRow(file, 1, "a"), keptRow(file, 2, "b"), keptRow(file, 3, "c")}
	for i := range rows {
		rows[i].Bytes = huge
		if err := f.repo.PutTrack(ctx, rows[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE public.embedded_subtitle_tracks SET updated_at = '1971-01-01'::timestamptz + $3 * interval '1 day'
			WHERE media_file_id = $1 AND embedded_stream_index = $2`, file, rows[i].StreamIndex, i); err != nil {
			t.Fatal(err)
		}
	}
	// A replaced row, a file whose change retires its row, a touch.
	gone := keptRow(other, 1, "d")
	if err := f.repo.PutTrack(ctx, gone); err != nil {
		t.Fatal(err)
	}
	gone.Bytes = 3
	if err := f.repo.PutTrack(ctx, gone); err != nil {
		t.Fatal(err)
	}
	usage()
	if _, err := f.pool.Exec(ctx, `UPDATE public.media_files SET file_size = file_size + 1 WHERE id = $1`, other); err != nil {
		t.Fatal(err)
	}
	if kept, err := f.repo.FileKeptBytes(ctx, file, 2); err != nil || kept != 2*huge {
		t.Fatalf("file's other tracks hold %d bytes, %v; want %d", kept, err, 2*huge)
	}
	// The first is used: now the second is the least recently used.
	if err := f.repo.Touch(ctx, file, 1); err != nil {
		t.Fatal(err)
	}
	total := usage()

	ask := Reservation{MediaFileID: other, StreamIndex: 1, Bytes: huge, FileMax: huge, StoreMax: total + huge, Lease: time.Minute}
	if evicted, err := f.repo.Reserve(ctx, ask); err != nil || len(evicted) != 0 {
		t.Fatalf("within the budget: evicted %v, %v", evicted, err)
	}
	// The reservation counts: a second one past the budget by one row
	// evicts the least recently used row no one is keeping again. The
	// second row is being kept again (a 1-byte reservation), so the third
	// goes.
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.embedded_subtitle_track_reservations VALUES ($1, 2, 1, now() + interval '1 minute')`, file); err != nil {
		t.Fatal(err)
	}
	second := ask
	second.StreamIndex, second.FileMax, second.StoreMax = 2, 2*huge, total+huge+1
	if evicted, err := f.repo.Reserve(ctx, second); err != nil || !slices.Equal(evicted, []string{rows[2].BlobKey}) {
		t.Fatalf("over the budget: evicted %v, %v; want the third row", evicted, err)
	}
	for i, want := range []bool{true, true, false} {
		if got, err := f.repo.Track(ctx, file, rows[i].StreamIndex); err != nil || (got != nil) != want {
			t.Fatalf("track %d kept = %v (%v), want %v", i, got != nil, err, want)
		}
	}
	if q := f.queued(t, file); !slices.Equal(q, []string{rows[2].BlobKey}) {
		t.Fatalf("queued after eviction: %v", q)
	}
	usage()
	// The file's share counts its live reservations, and not expired ones.
	third := second
	third.StreamIndex, third.StoreMax = 3, 1<<62
	if _, err := f.repo.Reserve(ctx, third); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("past the file's share: %v, want ErrNoRoom", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE public.embedded_subtitle_track_reservations SET expires_at = now() WHERE media_file_id = $1`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Reserve(ctx, third); err != nil {
		t.Fatalf("expired reservations still counted: %v", err)
	}
	// Room that evicting every unreserved row wouldn't make: nothing goes.
	full := Reservation{MediaFileID: file, StreamIndex: 9, Bytes: huge, FileMax: 1 << 62, StoreMax: 2*huge - 1, Lease: time.Minute}
	before := usage()
	if _, err := f.repo.Reserve(ctx, full); !errors.Is(err, ErrNoRoom) || usage() != before {
		t.Fatalf("no room: %v, usage %d -> %d", err, before, usage())
	}
	// A kept row ends its reservation.
	if err := f.repo.PutTrack(ctx, keptRow(other, 3, "e")); err != nil {
		t.Fatal(err)
	}
	if n := f.int64(t, `SELECT count(*) FROM public.embedded_subtitle_track_reservations WHERE media_file_id = $1 AND embedded_stream_index = 3`, other); n != 0 {
		t.Fatal("a kept row left its reservation")
	}
}

// Two tracks of one file reserve in turn, so two can't both fit a share
// only one fits: one waiting on the file's lock sees what the holder
// reserved.
func TestKeptTrackReservationsTakeTurnsDB(t *testing.T) {
	f := newDBFixture(t)
	ctx := t.Context()
	file := f.file(t)
	holder, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('embedded_subtitle_track_reservations'), $1)`, file); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `INSERT INTO public.embedded_subtitle_track_reservations VALUES ($1, 1, 40, now() + interval '1 minute')`, file); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.repo.Reserve(ctx, Reservation{MediaFileID: file, StreamIndex: 2, Bytes: 40, FileMax: 60, StoreMax: 1 << 62, Lease: time.Minute})
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); ; {
		select {
		case err := <-done:
			t.Fatalf("a reservation didn't wait for its file's lock: %v", err)
		default:
		}
		if f.int64(t, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND objsubid = 2 AND objid::bigint = $1`, file) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the reservation never waited for its file's lock")
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // polls pg_locks, which has no notification
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNoRoom) {
		t.Fatalf("after the holder reserved: %v, want ErrNoRoom", err)
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
