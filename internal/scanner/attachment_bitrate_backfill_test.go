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
	if _, err := pool.Exec(ctx, `UPDATE attachment_bitrate_backfill_state SET last_media_file_id = 0, retry_media_file_ids = '{}', completed_at = NULL`); err != nil {
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
// after it, without re-reading the files that answered before the stall.
// Files that timed out one at a time are listed together and stall when read
// back to back; that keeps what the retries before it finished, and must not
// hold up the walk over the files after them.
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
	ours := map[string]bool{}
	for _, p := range paths {
		ours[p] = true
	}

	backfiller := NewAttachmentBitrateBackfiller(NewFileRepository(pool), "ffprobe")
	backfiller.workers, backfiller.batch = 1, 2
	// The first batch answers; storage stalls after it.
	timesOut := func(path string) bool { return path != paths[0] && path != paths[1] }
	reads := map[string]int{}
	backfiller.probe = func(_ context.Context, _, filePath string) (containerBitrate, error) {
		if !ours[filePath] {
			return otherTestsFile, nil
		}
		reads[filePath]++
		if timesOut(filePath) {
			return containerBitrate{}, fmt.Errorf("%w after 30s", errAttachmentBitrateProbeTimeout)
		}
		return containerBitrate{kbps: 4047, size: 1000}, nil
	}
	// Batches before the stalled one each listed their timed-out files.
	if result, err := backfiller.Run(ctx, nil); !errors.Is(err, errAttachmentBitrateStorageStalled) || result.Pending != 6 {
		t.Fatalf("Run = %+v, %v; want the stalled-storage stop with 6 files pending", result, err)
	}

	timesOut = func(string) bool { return false }
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

	// The first listed batch answers, then the slow files stall.
	slow := paths[2 : 2+attachmentBitrateBackfillMaxTimeouts]
	var all []int64
	for _, p := range paths {
		all = append(all, int64(ids[p]))
	}
	listed := all[:2+len(slow)]
	if _, err := pool.Exec(ctx, `UPDATE media_files SET bitrate = 8710 WHERE id = ANY($1)`, all); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE attachment_bitrate_backfill_state
		SET retry_media_file_ids = $1, last_media_file_id = $2, completed_at = NULL`, listed, listed[len(listed)-1]); err != nil {
		t.Fatal(err)
	}
	timesOut = func(path string) bool { return slices.Contains(slow, path) }
	result, err := backfiller.Run(ctx, nil)
	if err != nil || result.Done || result.Pending != len(slow) {
		t.Fatalf("Run over a slow retry list = %+v, %v; want the slow files pending", result, err)
	}
	for _, p := range paths {
		want := 4047
		if slices.Contains(slow, p) {
			want = 8710
		}
		if got := storedBitrate(t, ctx, pool, ids[p]); got != want {
			t.Errorf("%s bitrate = %d after a stalled retry, want %d", p, got, want)
		}
	}
	var stillListed []int64
	if err := pool.QueryRow(ctx, `SELECT retry_media_file_ids FROM attachment_bitrate_backfill_state`).Scan(&stillListed); err != nil {
		t.Fatal(err)
	}
	if want := listed[2:]; !slices.Equal(stillListed, want) {
		t.Fatalf("retry list = %v after a stalled retry, want the slow files %v", stillListed, want)
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
