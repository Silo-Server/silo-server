package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPlaybackDoesNotRepairPQProbeWithoutHDR10PlusCheck(t *testing.T) {
	probedAt := time.Now()
	file := models.MediaFile{
		ProbeSource: "local", ProbeUpdatedAt: &probedAt, Duration: 7200, Container: "matroska",
		CodecVideo: "hevc", Resolution: "2160p", Chapters: []models.MediaChapter{},
		VideoTracks: []models.VideoTrack{{Codec: "hevc", ColorRange: "tv", ColorTransfer: "smpte2084", HDR10PlusUnchecked: true}},
	}
	if NeedsCriticalProbeRepair(&file) {
		t.Fatal("playback would reprobe a PQ file only to check HDR10+, which plays as HDR10 unchecked")
	}
}

func TestVideoTrackJSONKeepsHDR10PlusCheckState(t *testing.T) {
	checked, err := json.Marshal(models.VideoTrack{Codec: "hevc", ColorTransfer: "smpte2084"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(checked), `"hdr10_plus":false`) {
		t.Fatalf("checked track = %s, want an explicit hdr10_plus false", checked)
	}

	var legacy models.VideoTrack
	if err := json.Unmarshal([]byte(`{"codec":"hevc","color_transfer":"smpte2084"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy.HDR10PlusUnchecked {
		t.Fatal("a stored track without hdr10_plus decoded as checked")
	}
	rewritten, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rewritten), "hdr10_plus") {
		t.Fatalf("rewriting an unchecked track = %s, want hdr10_plus left out so the backfill still checks it", rewritten)
	}
}

// Only the primary stream's frame is read, so a second PQ video stream must
// not be stored as unchecked: nothing would ever check it.
func TestConvertProbeDataLeavesOnlyThePrimaryVideoUnchecked(t *testing.T) {
	stream := ffprobeStream{CodecType: "video", CodecName: "hevc", ColorTransfer: "smpte2084"}
	probe := convertProbeData(&ffprobeOutput{Streams: []ffprobeStream{stream, stream}})
	if len(probe.VideoTracks) != 2 {
		t.Fatalf("video tracks = %d, want 2", len(probe.VideoTracks))
	}
	if !probe.VideoTracks[0].HDR10PlusUnchecked || probe.VideoTracks[1].HDR10PlusUnchecked {
		t.Fatalf("unchecked = %v, %v; want only the primary track unchecked",
			probe.VideoTracks[0].HDR10PlusUnchecked, probe.VideoTracks[1].HDR10PlusUnchecked)
	}
}

// commandError runs script and returns the *exec.ExitError it produces, the
// shape a failed ffprobe read returns.
func commandError(t *testing.T, script string) error {
	t.Helper()
	_, err := exec.Command("sh", "-c", script).Output()
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Fatalf("script %q returned %v, want an exit error", script, err)
	}
	return err
}

// hdr10PlusBackfillRow is one stored file and the fake frame read's answer
// for it.
type hdr10PlusBackfillRow struct {
	name     string
	tracks   string
	sideData []ffprobeSideData
	readErr  error
}

func insertHDR10PlusBackfillRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, folderID int, path, tracks string) (int, time.Time) {
	t.Helper()
	probedAt := time.Date(2026, 8, 13, 5, 47, 23, 0, time.UTC)
	modifiedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, file_modified_at,
			container, codec_video, video_tracks, audio_tracks, chapters, probe_source, probe_updated_at)
		VALUES ($1, $2, $3, 1000, $4, 'mkv', 'hevc', $5::jsonb, '[{"codec":"eac3"}]',
			'[{"start":0,"end":60,"title":"Opening","thumbnail_path":"chapters/1.jpg"}]', 'local', $6)
		RETURNING id`,
		fmt.Sprintf("hdr10plus-backfill-%d", time.Now().UnixNano()), folderID, path, modifiedAt, tracks, probedAt,
	).Scan(&fileID); err != nil {
		t.Fatalf("insert media file %s: %v", path, err)
	}
	// The file on disk matches the row, as it does until something changes it.
	if err := os.WriteFile(path, make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modifiedAt, modifiedAt); err != nil {
		t.Fatal(err)
	}
	return fileID, probedAt
}

// The backfill reads the first frame of each unchecked PQ file, sets only
// hdr10_plus and, for an HDR10+ file, video_range_type on its first track,
// and leaves the rest of the row as it found it. A failed read leaves the
// file a candidate; a second pass reads only those.
func TestHDR10PlusBackfillRecordsFrameStatusDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("hdr10plus-backfill-%d", time.Now().UnixNano()))

	rows := hdr10PlusBackfillTestRows(t)
	byPath := make(map[string]hdr10PlusBackfillRow, len(rows))
	ids := make(map[string]int, len(rows))
	var probedAt time.Time
	dir := t.TempDir()
	for _, row := range rows {
		path := dir + "/" + row.name + ".mkv"
		byPath[path] = row
		ids[row.name], probedAt = insertHDR10PlusBackfillRow(t, ctx, pool, folderID, path, row.tracks)
	}

	// A replaced file that no scan has reprobed yet.
	if err := os.WriteFile(dir+"/changed-on-disk.mkv", make([]byte, 2000), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	reads := map[string]int{}
	backfiller := NewHDR10PlusBackfiller(NewFileRepository(pool), "ffprobe")
	backfiller.readFrame = func(_ context.Context, _, filePath, streamSpecifier string) ([]ffprobeSideData, error) {
		row, ok := byPath[filePath]
		if !ok {
			// Another test's row: leave it untouched.
			return nil, errors.New("not this test's file")
		}
		if streamSpecifier != "V:0" {
			t.Errorf("stream specifier = %q, want the primary video V:0", streamSpecifier)
		}
		mu.Lock()
		reads[row.name]++
		mu.Unlock()
		return row.sideData, row.readErr
	}

	if _, err := backfiller.Run(ctx, nil); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	track := func(name string) map[string]any {
		t.Helper()
		return readHDR10PlusBackfillTrack(t, ctx, pool, ids[name])
	}
	checkHDR10PlusBackfillFirstPass(t, track, reads)

	// Everything else in the row stays as it was.
	var chapters string
	var storedProbe time.Time
	if err := pool.QueryRow(ctx, `SELECT chapters::text, probe_updated_at FROM media_files WHERE id = $1`, ids["hdr10plus"]).Scan(&chapters, &storedProbe); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chapters, "chapters/1.jpg") || !storedProbe.Equal(probedAt) {
		t.Fatalf("chapters = %s, probe_updated_at = %v; want both untouched", chapters, storedProbe)
	}

	for name := range reads {
		reads[name] = 0
	}
	if _, err := backfiller.Run(ctx, nil); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	for name, count := range reads {
		want := 0
		if name == "undecodable" || name == "storage-error" || name == "timed-out" || name == "changed-on-disk" {
			want = 1
		}
		if count != want {
			t.Fatalf("second pass read %s %d times, want %d", name, count, want)
		}
	}
}

// hdr10PlusBackfillTestRows are the files TestHDR10PlusBackfillRecordsFrameStatusDB
// seeds: each names its first track and what reading its first frame returns.
func hdr10PlusBackfillTestRows(t *testing.T) []hdr10PlusBackfillRow {
	t.Helper()
	hdr10PlusFrame := []ffprobeSideData{
		{SideDataType: "Mastering display metadata"},
		{SideDataType: "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"},
	}
	staticFrame := hdr10PlusFrame[:1]
	const (
		hdr10Track = `{"codec":"hevc","color_transfer":"smpte2084","video_range":"HDR","video_range_type":"HDR10","future_field":"kept"}`
		dv81Track  = `{"codec":"hevc","color_transfer":"smpte2084","video_range":"DolbyVision","video_range_type":"DOVIWithHDR10","dv_profile":8,"dv_bl_compat_id":1,"dv_config_present":true,"dv_bl_compat_id_present":true}`
		sdrTrack   = `{"codec":"h264","color_transfer":"bt709","video_range_type":"SDR"}`
	)
	return []hdr10PlusBackfillRow{
		{name: "hdr10plus", tracks: "[" + hdr10Track + "]", sideData: hdr10PlusFrame},
		{name: "dv81-hdr10plus", tracks: "[" + dv81Track + "]", sideData: hdr10PlusFrame},
		{name: "plain-hdr10", tracks: `[{"codec":"hevc","color_transfer":"smpte2084"}]`, sideData: staticFrame},
		{name: "undecodable", tracks: "[" + hdr10Track + "]", readErr: commandError(t, "echo 'Invalid data found when processing input' >&2; exit 1")},
		{name: "storage-error", tracks: "[" + hdr10Track + "]", readErr: commandError(t, "echo 'movie.mkv: Input/output error' >&2; exit 1")},
		{name: "timed-out", tracks: "[" + hdr10Track + "]", readErr: fmt.Errorf("%w after 30s", errFrameSideDataTimeout)},
		{name: "sdr", tracks: "[" + sdrTrack + "]", sideData: hdr10PlusFrame},
		{name: "already-checked", tracks: `[{"codec":"hevc","color_transfer":"smpte2084","hdr10_plus":false}]`, sideData: hdr10PlusFrame},
		{name: "changed-on-disk", tracks: "[" + hdr10Track + "]", sideData: hdr10PlusFrame},
	}
}

func readHDR10PlusBackfillTrack(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fileID int) map[string]any {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT video_tracks->0 FROM media_files WHERE id = $1`, fileID).Scan(&raw); err != nil {
		t.Fatalf("read media file %d: %v", fileID, err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

// checkHDR10PlusBackfillFirstPass checks what the first pass stored for each
// of hdr10PlusBackfillTestRows, and that it skipped the files it should.
func checkHDR10PlusBackfillFirstPass(t *testing.T, track func(string) map[string]any, reads map[string]int) {
	t.Helper()
	if got := track("hdr10plus"); got["hdr10_plus"] != true || got["video_range_type"] != "HDR10Plus" || got["future_field"] != "kept" {
		t.Fatalf("HDR10+ file = %v, want hdr10_plus true, range HDR10Plus and its other fields kept", got)
	}
	if got := track("dv81-hdr10plus"); got["hdr10_plus"] != true || got["video_range_type"] != "DOVIWithHDR10Plus" {
		t.Fatalf("DV 8.1 file = %v, want hdr10_plus true, range DOVIWithHDR10Plus", got)
	}
	if got := track("plain-hdr10"); got["hdr10_plus"] != false {
		t.Fatalf("plain HDR10 file = %v, want hdr10_plus false", got)
	} else if _, ok := got["video_range_type"]; ok {
		t.Fatalf("plain HDR10 file = %v, want no video_range_type added", got)
	}
	for _, name := range []string{"undecodable", "storage-error", "timed-out", "changed-on-disk"} {
		if _, ok := track(name)["hdr10_plus"]; ok {
			t.Fatalf("%s file = %v, want it left unchecked for the next pass", name, track(name))
		}
	}
	if got := track("already-checked"); got["hdr10_plus"] != false {
		t.Fatalf("already checked file = %v, want it unchanged", got)
	}
	for _, name := range []string{"sdr", "already-checked"} {
		if reads[name] != 0 {
			t.Fatalf("%s file was read %d times, want it skipped", name, reads[name])
		}
	}
}

// A row rewritten while its frame was read keeps what the rewrite stored.
func TestHDR10PlusBackfillLeavesARowRewrittenDuringTheReadDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("hdr10plus-rewrite-%d", time.Now().UnixNano()))
	path := t.TempDir() + "/rewritten.mkv"
	fileID, _ := insertHDR10PlusBackfillRow(t, ctx, pool, folderID, path, `[{"codec":"hevc","color_transfer":"smpte2084","video_range_type":"HDR10"}]`)

	backfiller := NewHDR10PlusBackfiller(NewFileRepository(pool), "ffprobe")
	backfiller.readFrame = func(ctx context.Context, _, filePath, _ string) ([]ffprobeSideData, error) {
		if filePath != path {
			return nil, errors.New("not this test's file")
		}
		// A scan reprobes the file while its frame is being read.
		if _, err := pool.Exec(ctx, `UPDATE media_files SET video_tracks = '[{"codec":"hevc","color_transfer":"smpte2084","video_range_type":"HDR10","bit_depth":10}]' WHERE id = $1`, fileID); err != nil {
			t.Errorf("rewrite row: %v", err)
		}
		return []ffprobeSideData{{SideDataType: "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"}}, nil
	}
	result, err := backfiller.Run(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed < 1 {
		t.Fatalf("result = %+v, want the rewritten row counted as changed", result)
	}
	var kept bool
	if err := pool.QueryRow(ctx, `SELECT video_tracks->0->>'hdr10_plus' IS NULL AND (video_tracks->0->>'bit_depth') = '10' FROM media_files WHERE id = $1`, fileID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if !kept {
		t.Fatal("the backfill wrote over a row a scan rewrote during the read")
	}
}

// Consecutive timeouts mean stalled storage: the pass ends rather than wait
// out every remaining file.
func TestHDR10PlusBackfillStopsWhenStorageStallsDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("hdr10plus-stall-%d", time.Now().UnixNano()))
	dir := t.TempDir()
	paths := map[string]bool{}
	for i := range hdr10PlusBackfillMaxTimeouts + 4 {
		path := fmt.Sprintf("%s/stalled-%d.mkv", dir, i)
		paths[path] = true
		insertHDR10PlusBackfillRow(t, ctx, pool, folderID, path, `[{"codec":"hevc","color_transfer":"smpte2084"}]`)
	}
	timedOut := fmt.Errorf("%w after 30s", errFrameSideDataTimeout)
	backfiller := NewHDR10PlusBackfiller(NewFileRepository(pool), "ffprobe")
	backfiller.workers = 1
	backfiller.readFrame = func(_ context.Context, _, filePath, _ string) ([]ffprobeSideData, error) {
		if !paths[filePath] {
			return nil, errors.New("not this test's file")
		}
		return nil, timedOut
	}
	_, err := backfiller.Run(ctx, nil)
	if !errors.Is(err, errHDR10PlusStorageStalled) {
		t.Fatalf("Run error = %v, want the stalled-storage stop", err)
	}
}

func TestNewHDR10PlusBackfillerNeedsADatabase(t *testing.T) {
	if NewHDR10PlusBackfiller(nil, "ffprobe") != nil || NewHDR10PlusBackfiller(&FileRepository{}, "ffprobe") != nil {
		t.Fatal("a backfiller without a database was built")
	}
}

// Playback's probe repair skips the frame read. It keeps a recorded HDR10+
// status only when the reprobed stream is the one that was checked.
func TestCarryCheckedHDR10PlusKeepsStatusForTheSameStream(t *testing.T) {
	checked := models.VideoTrack{Codec: "hevc", Width: 3840, Height: 2160, ColorRange: "tv", ColorTransfer: "smpte2084", HDR10Plus: true, VideoRangeType: "HDR10Plus"}
	reprobed := func() []models.VideoTrack {
		return []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160, ColorRange: "tv", ColorTransfer: "smpte2084", VideoRangeType: "HDR10", HDR10PlusUnchecked: true}}
	}

	next := reprobed()
	carryCheckedHDR10Plus([]models.VideoTrack{checked}, next)
	if next[0].HDR10PlusUnchecked || !next[0].HDR10Plus || next[0].VideoRangeType != "HDR10Plus" {
		t.Fatalf("same stream = %+v, want the checked HDR10+ status kept", next[0])
	}

	next = reprobed()
	next[0].Width = 1920
	carryCheckedHDR10Plus([]models.VideoTrack{checked}, next)
	if !next[0].HDR10PlusUnchecked || next[0].HDR10Plus {
		t.Fatalf("different stream = %+v, want it left unchecked", next[0])
	}

	// The fields probe repair fills in are not a different stream.
	missingRange := checked
	missingRange.ColorRange = ""
	next = reprobed()
	carryCheckedHDR10Plus([]models.VideoTrack{missingRange}, next)
	if next[0].HDR10PlusUnchecked || !next[0].HDR10Plus {
		t.Fatalf("repaired color range = %+v, want the checked HDR10+ status kept", next[0])
	}

	legacy := false
	legacyDV := models.VideoTrack{Codec: "hevc", Width: 3840, Height: 2160, ColorRange: "tv", ColorTransfer: "smpte2084", DVProfile: 8, DVBLCompatID: 1, DVProvenanceCurrent: &legacy, HDR10Plus: true, VideoRangeType: "DOVIWithHDR10Plus"}
	reprobedDV := func() []models.VideoTrack {
		return []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160, ColorRange: "tv", ColorTransfer: "smpte2084", DVProfile: 8, DVBLCompatID: 1, DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true, VideoRangeType: "DOVIWithHDR10", HDR10PlusUnchecked: true}}
	}
	next = reprobedDV()
	carryCheckedHDR10Plus([]models.VideoTrack{legacyDV}, next)
	if next[0].HDR10PlusUnchecked || !next[0].HDR10Plus || next[0].VideoRangeType != "DOVIWithHDR10Plus" {
		t.Fatalf("repaired DV provenance = %+v, want the checked HDR10+ status kept", next[0])
	}
	next = reprobedDV()
	next[0].DVProfile = 7
	carryCheckedHDR10Plus([]models.VideoTrack{legacyDV}, next)
	if !next[0].HDR10PlusUnchecked {
		t.Fatalf("legacy row, different DV profile = %+v, want it left unchecked", next[0])
	}

	unchecked := checked
	unchecked.HDR10Plus, unchecked.HDR10PlusUnchecked = false, true
	next = reprobed()
	carryCheckedHDR10Plus([]models.VideoTrack{unchecked}, next)
	if !next[0].HDR10PlusUnchecked {
		t.Fatalf("previously unchecked = %+v, want it left unchecked", next[0])
	}
}

func TestPlaybackProbeRepairKeepsCheckedHDR10Plus(t *testing.T) {
	stored := &models.MediaFile{
		ID: 7, FilePath: "/library/hdr10plus.mkv", FileSize: 1_000,
		// No probe source: playback must repair the row before planning.
		CodecVideo: "hevc", Container: "mkv", Duration: 5400, Resolution: "2160p",
		VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160, ColorRange: "tv", ColorTransfer: "smpte2084", HDR10Plus: true, VideoRangeType: "HDR10Plus"}},
	}
	repo := &probeRepairTestRepository{files: map[int]*models.MediaFile{stored.ID: stored}}
	ensurer := &PlaybackProbeEnsurer{
		fileRepo:    repo,
		ffprobePath: "ffprobe",
		probeFile: func(context.Context, string, string) (*ProbeData, error) {
			return &ProbeData{
				Container: "mkv", Duration: 5400, CodecVideo: "hevc", Resolution: "2160p",
				VideoTracks: []VideoTrackInfo{{Codec: "hevc", Width: 3840, Height: 2160, ColorRange: "tv", ColorTransfer: "smpte2084", VideoRangeType: "HDR10", HDR10PlusUnchecked: true}},
			}, nil
		},
	}
	got, err := ensurer.Ensure(context.Background(), stored)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if track := got.VideoTracks[0]; track.HDR10PlusUnchecked || !track.HDR10Plus || track.VideoRangeType != "HDR10Plus" {
		t.Fatalf("repaired track = %+v, want the checked HDR10+ status kept", track)
	}
}

// A stat blocked on stalled storage must not hold up the pass: it times out
// as a retryable file, ends the pass once every slot is stuck, gives way to
// cancellation, and frees its slot only when the syscall returns.
func TestHDR10PlusBackfillBoundsTheOnDiskCheck(t *testing.T) {
	release := make(chan struct{})
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(release) }) })
	backfiller := &HDR10PlusBackfiller{
		readFrame: func(context.Context, string, string, string) ([]ffprobeSideData, error) {
			return []ffprobeSideData{{SideDataType: "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"}}, nil
		},
		statFile: func(string) (fs.FileInfo, error) {
			<-release
			return nil, errors.New("stat after the stall")
		},
		statSlots:   make(chan struct{}, 1),
		statTimeout: 20 * time.Millisecond,
	}
	candidate := hdr10PlusCandidate{id: 1, path: "/stalled/movie.mkv", size: 1000}

	outcome, err := backfiller.backfillFile(context.Background(), candidate)
	if err != nil || outcome != hdr10PlusOutcomeTimedOut {
		t.Fatalf("stalled check = %v, %v; want a timed-out file", outcome, err)
	}
	// The first check still holds the only slot.
	if _, err := backfiller.backfillFile(context.Background(), candidate); !errors.Is(err, errHDR10PlusStorageStalled) {
		t.Fatalf("check with every slot stuck = %v, want the pass to end on stalled storage", err)
	}

	released.Do(func() { close(release) })
	select {
	case backfiller.statSlots <- struct{}{}:
		<-backfiller.statSlots
	case <-time.After(time.Second):
		t.Fatal("the slot was not freed after the stat returned")
	}

	// Cancel the pass while a check is blocked in the kernel.
	started, unblock := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	backfiller.statFile = func(string) (fs.FileInfo, error) {
		close(started)
		<-unblock
		return nil, errors.New("stat after the stall")
	}
	backfiller.statTimeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	if _, err := backfiller.backfillFile(ctx, candidate); !errors.Is(err, context.Canceled) {
		t.Fatalf("check canceled while blocked = %v, want context.Canceled", err)
	}
}
