package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// otherTestsFile is the fake probe's answer for a row another test stored: a
// size no row has, so the pass leaves the row to a scan and never reads it
// again.
var otherTestsFile = containerBitrate{size: -1}

// attachmentBitrateRow is one stored file and the fake probe's answer for it.
type attachmentBitrateRow struct {
	name      string
	container string
	subtitles string
	missing   bool
	stored    int
	probed    containerBitrate
	probeErr  error
	// want is the stored bitrate after the pass.
	want int
	// read is whether the pass should probe the file.
	read bool
}

func resetAttachmentBitrateBackfill(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE attachment_bitrate_backfill_state SET last_media_file_id = 0, retry_media_file_ids = '{}', retry_after_id = 0, completed_at = NULL`); err != nil {
		t.Fatal(err)
	}
}

func insertAttachmentBitrateRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, folderID int, path string, row attachmentBitrateRow) int {
	t.Helper()
	var missingSince *time.Time
	if row.missing {
		now := time.Now()
		missingSince = &now
	}
	var id int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, file_modified_at,
			container, codec_video, bitrate, subtitle_tracks, missing_since, probe_source)
		VALUES ($1, $2, $3, 1000, '2026-01-02T03:04:05Z', $4, 'h264', $5, $6::jsonb, $7, 'local')
		RETURNING id`,
		fmt.Sprintf("attachment-bitrate-%d", time.Now().UnixNano()), folderID, path, row.container, row.stored, row.subtitles, missingSince,
	).Scan(&id); err != nil {
		t.Fatalf("insert media file %s: %v", path, err)
	}
	return id
}

func storedBitrate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int) int {
	t.Helper()
	var bitrate int
	if err := pool.QueryRow(ctx, `SELECT bitrate FROM media_files WHERE id = $1`, id).Scan(&bitrate); err != nil {
		t.Fatal(err)
	}
	return bitrate
}

// The pass re-probes only present MKV files with ASS subtitles, the releases
// that carry fonts, and writes the attachment-free rate over the row it read.
// Files it could not read, more in a row than a stall takes when they time
// out (an offline library root), neither stop it nor drop out of it: later
// runs read them again until they answer, and then the pass is done for good.
func TestAttachmentBitrateBackfillRecomputesFontHeavyFilesOnceDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	resetAttachmentBitrateBackfill(t, ctx, pool)
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("attachment-bitrate-%d", time.Now().UnixNano()))

	ass := `[{"index":2,"codec":"ass"}]`
	rows := []attachmentBitrateRow{
		{name: "fonts", container: "mkv", subtitles: ass, stored: 8710, probed: containerBitrate{kbps: 4047, size: 1000}, want: 4047, read: true},
		{name: "ssa", container: "mkv", subtitles: `[{"index":2,"codec":"ssa"},{"index":3,"codec":"subrip"}]`, stored: 6322, probed: containerBitrate{kbps: 3100, size: 1000}, want: 3100, read: true},
		{name: "no-attachments", container: "mkv", subtitles: ass, stored: 4047, probed: containerBitrate{kbps: 4047, size: 1000}, want: 4047, read: true},
		// Replaced on disk since the scan: the next scan reprobes it.
		{name: "changed", container: "mkv", subtitles: ass, stored: 8710, probed: containerBitrate{kbps: 4047, size: 2000}, want: 8710, read: true},
		{name: "srt-only", container: "mkv", subtitles: `[{"index":2,"codec":"subrip"}]`, stored: 8710, want: 8710},
		{name: "mp4", container: "mp4", subtitles: ass, stored: 8710, want: 8710},
		{name: "missing", container: "mkv", subtitles: ass, missing: true, stored: 8710, want: 8710},
	}
	for i := range attachmentBitrateBackfillMaxTimeouts + 1 {
		rows = append(rows, attachmentBitrateRow{name: fmt.Sprintf("offline-%02d", i), container: "mkv", subtitles: ass, stored: 8710,
			probed: containerBitrate{kbps: 4047, size: 1000}, probeErr: errors.New("no such file or directory"), want: 8710, read: true})
	}
	dir := t.TempDir()
	byPath := map[string]attachmentBitrateRow{}
	ids := map[string]int{}
	for _, row := range rows {
		path := dir + "/" + row.name + ".mkv"
		byPath[path] = row
		ids[row.name] = insertAttachmentBitrateRow(t, ctx, pool, folderID, path, row)
	}

	var mu sync.Mutex
	reads := map[string]int{}
	offline := true
	backfiller := NewAttachmentBitrateBackfiller(NewFileRepository(pool), "ffprobe")
	backfiller.probe = func(_ context.Context, _, filePath string) (containerBitrate, error) {
		row, ok := byPath[filePath]
		if !ok {
			return otherTestsFile, nil
		}
		mu.Lock()
		defer mu.Unlock()
		reads[row.name]++
		if !offline {
			return row.probed, nil
		}
		return row.probed, row.probeErr
	}
	if result, err := backfiller.Run(ctx, nil); err != nil || result.Updated != 2 || result.Done {
		t.Fatalf("first pass = %+v, %v; want 2 updated and not done while files are unread", result, err)
	}
	for _, row := range rows {
		want := 0
		if row.read {
			want = 1
		}
		if reads[row.name] != want {
			t.Errorf("%s read %d times, want %d", row.name, reads[row.name], want)
		}
		if got := storedBitrate(t, ctx, pool, ids[row.name]); got != row.want {
			t.Errorf("%s bitrate = %d, want %d", row.name, got, row.want)
		}
	}

	// Still offline: only the unread files are read again.
	clear(reads)
	if result, err := backfiller.Run(ctx, nil); err != nil || result.Done {
		t.Fatalf("second pass = %+v, %v; want not done", result, err)
	}
	for _, row := range rows {
		want := 0
		if row.probeErr != nil {
			want = 1
		}
		if reads[row.name] != want {
			t.Errorf("second pass read %s %d times, want only each offline file, once", row.name, reads[row.name])
		}
	}

	offline = false
	clear(reads)
	if result, err := backfiller.Run(ctx, nil); err != nil || !result.Done || result.Updated != attachmentBitrateBackfillMaxTimeouts+1 {
		t.Fatalf("pass after the root returned = %+v, %v; want every offline file corrected and done", result, err)
	}
	for _, row := range rows {
		want := row.want
		if row.probeErr != nil {
			want = row.probed.kbps
		}
		if got := storedBitrate(t, ctx, pool, ids[row.name]); got != want {
			t.Errorf("%s bitrate = %d, want %d", row.name, got, want)
		}
	}

	clear(reads)
	if _, err := backfiller.Run(ctx, nil); err != nil {
		t.Fatalf("last pass: %v", err)
	}
	if len(reads) != 0 {
		t.Fatalf("last pass read %v, want nothing: the pass is done", reads)
	}
}

// A pass that stops on stalled storage keeps what it finished and resumes
// after it, without re-reading the files that answered before the stall. A
// root whose reads keep stalling holds up neither the walk over the files
// after it nor the files listed after its own.
func TestAttachmentBitrateBackfillResumesAfterStorageStallDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	resetAttachmentBitrateBackfill(t, ctx, pool)
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("attachment-bitrate-stall-%d", time.Now().UnixNano()))
	dir := t.TempDir()
	var paths []string
	ids := map[string]int{}
	for i := range attachmentBitrateBackfillMaxTimeouts + 4 {
		path := fmt.Sprintf("%s/file-%02d.mkv", dir, i)
		paths = append(paths, path)
		ids[path] = insertAttachmentBitrateRow(t, ctx, pool, folderID, path, attachmentBitrateRow{
			container: "mkv", subtitles: `[{"index":2,"codec":"ass"}]`, stored: 8710,
		})
	}
	// failing maps a path to how its read fails; others answer.
	failing := map[string]error{}
	timeout := fmt.Errorf("%w after 30s", errProbeTimeout)
	for _, p := range paths[2:] {
		failing[p] = timeout
	}
	ours := map[string]bool{}
	for _, p := range paths {
		ours[p] = true
	}
	backfiller := NewAttachmentBitrateBackfiller(NewFileRepository(pool), "ffprobe")
	backfiller.workers, backfiller.batch = 1, 2
	reads := map[string]int{}
	backfiller.probe = func(_ context.Context, _, filePath string) (containerBitrate, error) {
		if !ours[filePath] {
			return otherTestsFile, nil
		}
		reads[filePath]++
		if err := failing[filePath]; err != nil {
			return containerBitrate{}, err
		}
		return containerBitrate{kbps: 4047, size: 1000}, nil
	}
	check := func(step string, wantPending int, corrected ...string) {
		t.Helper()
		var listed []int64
		if err := pool.QueryRow(ctx, `SELECT retry_media_file_ids FROM attachment_bitrate_backfill_state`).Scan(&listed); err != nil {
			t.Fatal(err)
		}
		if len(listed) != wantPending {
			t.Errorf("%s: %d files listed, want %d", step, len(listed), wantPending)
		}
		for _, p := range paths {
			want := 8710
			if slices.Contains(corrected, p) {
				want = 4047
			}
			if got := storedBitrate(t, ctx, pool, ids[p]); got != want {
				t.Errorf("%s: %s bitrate = %d, want %d", step, p, got, want)
			}
		}
	}

	// The walk stalls in its fifth batch; it lists that batch and the three
	// before it and moves on, so the next run starts after them.
	if _, err := backfiller.Run(ctx, nil); !errors.Is(err, errAttachmentBitrateStorageStalled) {
		t.Fatalf("first run error = %v, want the stalled-storage stop", err)
	}
	check("stalled walk", 8, paths[0], paths[1])

	// The listed files' root still stalls, one file after it is gone for
	// now, and the last answers: the walk reaches both.
	hung := paths[2:10]
	delete(failing, paths[11])
	failing[paths[10]] = errors.New("no such file or directory")
	if result, err := backfiller.Run(ctx, nil); err != nil || result.Done {
		t.Fatalf("run past a hung root = %+v, %v; want the walk finished and files pending", result, err)
	}
	check("walk past a hung root", 9, paths[0], paths[1], paths[11])

	// The gone file is back, listed in one batch behind the hung files. The
	// stall stops that batch before it reaches the file; the next run starts
	// there and reads it.
	delete(failing, paths[10])
	if _, err := pool.Exec(ctx, `UPDATE attachment_bitrate_backfill_state SET retry_after_id = 0`); err != nil {
		t.Fatal(err)
	}
	backfiller.batch = 20
	clear(reads)
	for range 2 {
		if _, err := backfiller.Run(ctx, nil); err != nil {
			t.Fatalf("retry past a hung root: %v", err)
		}
	}
	if reads[paths[10]] != 1 {
		t.Fatalf("file listed behind the hung root read %d times, want once, by the second run", reads[paths[10]])
	}
	check("retry past a hung root", len(hung), paths[0], paths[1], paths[10], paths[11])

	clear(failing)
	clear(reads)
	if result, err := backfiller.Run(ctx, nil); err != nil || !result.Done {
		t.Fatalf("run after the root returned = %+v, %v; want done", result, err)
	}
	check("root returned", 0, paths...)
	if reads[paths[0]] != 0 || reads[paths[1]] != 0 || reads[paths[10]] != 0 || reads[paths[11]] != 0 {
		t.Fatalf("last run re-read files corrected before it: %v", reads)
	}
}

// The backfill's header read must see attachment sizes, which ffprobe prints
// only when the stream entries ask for them. The same encode muxed with and
// without a 4 MB attachment gives the expected rate independently.
func TestProbeContainerBitrateExcludesAttachedFont(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()
	font := filepath.Join(dir, "font.ttf")
	payload := make([]byte, 4<<20)
	for i := range payload {
		payload[i] = byte(i * 7919 >> 3)
	}
	if err := os.WriteFile(font, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	plain, withFont := filepath.Join(dir, "plain.mkv"), filepath.Join(dir, "fonts.mkv")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
		}
	}
	run("-f", "lavfi", "-i", "testsrc=duration=4:size=320x240:rate=24", "-c:v", "libx264", "-b:v", "800k", plain)
	run("-i", plain, "-attach", font, "-metadata:s:t", "mimetype=font/ttf", "-c", "copy", withFont)

	want, err := probeContainerBitrate(context.Background(), "ffprobe", plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := probeContainerBitrate(context.Background(), "ffprobe", withFont)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(withFont)
	if err != nil {
		t.Fatal(err)
	}
	if got.size != info.Size() {
		t.Fatalf("size = %d, want the file's %d", got.size, info.Size())
	}
	// The attachment alone would add about 8 Mbps over 4 s.
	if diff := got.kbps - want.kbps; want.kbps <= 0 || diff < -10 || diff > 10 {
		t.Fatalf("bitrate with font = %d kbps, without = %d kbps; want them within 10 kbps", got.kbps, want.kbps)
	}
}
