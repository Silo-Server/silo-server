package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
	if _, err := pool.Exec(ctx, `UPDATE attachment_bitrate_backfill_state SET last_media_file_id = 0, completed_at = NULL`); err != nil {
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
// Once it has covered every file it is done for good.
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
		{name: "unreadable", container: "mkv", subtitles: ass, stored: 8710, probeErr: errors.New("ffprobe failed"), want: 8710, read: true},
		{name: "srt-only", container: "mkv", subtitles: `[{"index":2,"codec":"subrip"}]`, stored: 8710, want: 8710},
		{name: "mp4", container: "mp4", subtitles: ass, stored: 8710, want: 8710},
		{name: "missing", container: "mkv", subtitles: ass, missing: true, stored: 8710, want: 8710},
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
	backfiller := NewAttachmentBitrateBackfiller(NewFileRepository(pool), "ffprobe")
	backfiller.probe = func(_ context.Context, _, filePath string) (containerBitrate, error) {
		row, ok := byPath[filePath]
		if !ok {
			return containerBitrate{}, errors.New("not this test's file")
		}
		mu.Lock()
		reads[row.name]++
		mu.Unlock()
		return row.probed, row.probeErr
	}

	result, err := backfiller.Run(ctx, nil)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if result.Updated != 2 {
		t.Fatalf("updated = %d, want 2 (%+v)", result.Updated, result)
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

	clear(reads)
	if _, err := backfiller.Run(ctx, nil); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(reads) != 0 {
		t.Fatalf("second pass read %v, want nothing: the pass is done", reads)
	}
}

// A pass that stops on stalled storage keeps what it finished and resumes
// after it, without re-reading the files before the stall.
func TestAttachmentBitrateBackfillResumesAfterStorageStallDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	resetAttachmentBitrateBackfill(t, ctx, pool)
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("attachment-bitrate-stall-%d", time.Now().UnixNano()))
	dir := t.TempDir()
	var paths []string
	ids := map[string]int{}
	for i := range attachmentBitrateBackfillMaxFailures + 4 {
		path := fmt.Sprintf("%s/file-%02d.mkv", dir, i)
		paths = append(paths, path)
		ids[path] = insertAttachmentBitrateRow(t, ctx, pool, folderID, path, attachmentBitrateRow{
			container: "mkv", subtitles: `[{"index":2,"codec":"ass"}]`, stored: 8710,
		})
	}
	ours := map[string]bool{}
	for _, p := range paths {
		ours[p] = true
	}

	backfiller := NewAttachmentBitrateBackfiller(NewFileRepository(pool), "ffprobe")
	backfiller.workers, backfiller.batch = 1, 2
	stalled := true
	reads := map[string]int{}
	backfiller.probe = func(_ context.Context, _, filePath string) (containerBitrate, error) {
		if !ours[filePath] {
			return containerBitrate{}, errors.New("not this test's file")
		}
		reads[filePath]++
		// The first batch answers; storage stalls after it.
		if stalled && filePath != paths[0] && filePath != paths[1] {
			return containerBitrate{}, fmt.Errorf("%w after 30s", errAttachmentBitrateProbeTimeout)
		}
		return containerBitrate{kbps: 4047, size: 1000}, nil
	}
	if _, err := backfiller.Run(ctx, nil); !errors.Is(err, errAttachmentBitrateStorageStalled) {
		t.Fatalf("Run error = %v, want the stalled-storage stop", err)
	}

	stalled = false
	clear(reads)
	if _, err := backfiller.Run(ctx, nil); err != nil {
		t.Fatalf("resumed pass: %v", err)
	}
	if reads[paths[0]] != 0 || reads[paths[1]] != 0 {
		t.Fatalf("resumed pass re-read the batch finished before the stall: %v", reads)
	}
	for _, p := range paths {
		if got := storedBitrate(t, ctx, pool, ids[p]); got != 4047 {
			t.Fatalf("%s bitrate = %d after the resumed pass, want 4047", p, got)
		}
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
