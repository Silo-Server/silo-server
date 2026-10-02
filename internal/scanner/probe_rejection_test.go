package scanner

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func writeRejectingFFprobe(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, path, "#!/bin/sh\nexit 1\n")
	return path
}

func TestIsProbeRejectionSeparatesRefusedFilesFromProbeTrouble(t *testing.T) {
	ctx := t.Context()
	media := filepath.Join(t.TempDir(), "S01E03.mkv")
	if err := os.WriteFile(media, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ProbeFile(ctx, writeRejectingFFprobe(t), media)
	if err == nil || !IsProbeRejection(ctx, err) {
		t.Fatalf("non-zero ffprobe exit = %v, want a rejection", err)
	}

	_, err = ProbeFile(ctx, filepath.Join(t.TempDir(), "missing-ffprobe"), media)
	if err == nil || IsProbeRejection(ctx, err) {
		t.Fatalf("missing binary = %v, want a non-rejection error", err)
	}

	garbage := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, garbage, "#!/bin/sh\nprintf 'not json'\n")
	_, err = ProbeFile(ctx, garbage, media)
	if err == nil || IsProbeRejection(ctx, err) {
		t.Fatalf("unparseable output = %v, want a non-rejection error", err)
	}

	slow := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, slow, "#!/bin/sh\nexec sleep 30\n")
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = ProbeFile(timeoutCtx, slow, media)
	if err == nil || IsProbeRejection(timeoutCtx, err) {
		t.Fatalf("timed-out probe = %v, want a non-rejection error", err)
	}

	if IsProbeRejection(ctx, nil) {
		t.Fatal("nil error reported as a rejection")
	}
}

// The files from issue #1791: real ffprobe refuses zero-byte and random-byte
// media with a non-zero exit, which must classify as a rejection.
func TestIsProbeRejectionWithRealFFprobe(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.mkv")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 256<<10)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "corrupt.mkv")
	if err := os.WriteFile(corrupt, random, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{empty, corrupt} {
		_, err := ProbeFile(t.Context(), ffprobe, path)
		if err == nil || !IsProbeRejection(t.Context(), err) {
			t.Fatalf("%s: probe error = %v, want a rejection", filepath.Base(path), err)
		}
	}
}

func TestScannerProbeFileReportsRejection(t *testing.T) {
	media := filepath.Join(t.TempDir(), "S01E04.mkv")
	if err := os.WriteFile(media, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Scanner{ffprobePath: writeRejectingFFprobe(t)}
	probe, source, rejected := s.probeFile(t.Context(), media)
	if probe != nil || source != "local" || !rejected {
		t.Fatalf("probeFile = (%v, %q, %v), want a local rejection", probe, source, rejected)
	}

	// No ffprobe configured: nothing ran, so nothing was rejected.
	s = &Scanner{}
	if _, _, rejected := s.probeFile(t.Context(), media); rejected {
		t.Fatal("scanner without ffprobe reported a rejection")
	}
}

func TestApplyProbeDataClearsProbeFailure(t *testing.T) {
	failedAt := time.Now().UTC()
	mf := models.MediaFile{ProbeFailedAt: &failedAt}
	applyProbeData(&mf, completeProbeRepairTestData(), "local")
	if mf.ProbeFailedAt != nil || mf.ProbeUpdatedAt == nil {
		t.Fatalf("after a successful probe ProbeFailedAt=%v ProbeUpdatedAt=%v", mf.ProbeFailedAt, mf.ProbeUpdatedAt)
	}

	var marked models.MediaFile
	markProbeRejected(&marked)
	if marked.ProbeFailedAt == nil || !marked.ProbeRejected() {
		t.Fatal("markProbeRejected did not mark an unprobed file as rejected")
	}
}

func TestPlaybackProbeEnsurerRecordsAndClearsProbeRejection(t *testing.T) {
	file := &models.MediaFile{ID: 7, FilePath: "/library/show/S01E03.mkv"}
	repo := &probeRepairTestRepository{files: map[int]*models.MediaFile{file.ID: file}}
	ensurer := &PlaybackProbeEnsurer{
		fileRepo:    repo,
		ffprobePath: writeRejectingFFprobe(t),
		timeout:     5 * time.Second,
	}

	got, err := ensurer.Ensure(t.Context(), file)
	if err != nil {
		t.Fatalf("Ensure error = %v, want the rejection reported on the row", err)
	}
	if got == nil || !got.ProbeRejected() {
		t.Fatalf("Ensure returned %+v, want a rejected file", got)
	}
	if repo.markCalls != 1 || repo.files[file.ID].ProbeFailedAt == nil {
		t.Fatalf("mark calls = %d, stored ProbeFailedAt = %v", repo.markCalls, repo.files[file.ID].ProbeFailedAt)
	}

	// The file is replaced with a readable one: the next repair probes it,
	// and the successful probe clears the mark.
	ensurer.probeFile = func(context.Context, string, string) (*ProbeData, error) {
		return completeProbeRepairTestData(), nil
	}
	got, err = ensurer.Ensure(t.Context(), repo.files[file.ID])
	if err != nil {
		t.Fatalf("Ensure after replacement = %v", err)
	}
	if got.ProbeFailedAt != nil || got.ProbeRejected() || got.ProbeUpdatedAt == nil {
		t.Fatalf("after a successful probe ProbeFailedAt=%v ProbeUpdatedAt=%v", got.ProbeFailedAt, got.ProbeUpdatedAt)
	}
	if stored := repo.files[file.ID]; stored.ProbeFailedAt != nil {
		t.Fatalf("stored ProbeFailedAt = %v, want cleared", stored.ProbeFailedAt)
	}
}

func TestPlaybackProbeEnsurerKeepsProbedRowsOnRejection(t *testing.T) {
	probedAt := time.Now().UTC()
	// Probed once, but missing chapters, so it is due for a repair probe.
	file := &models.MediaFile{
		ID: 8, FilePath: "/library/show/S01E01.mkv", ProbeSource: "local", ProbeUpdatedAt: &probedAt,
		Duration: 1200, Container: "mkv", CodecVideo: "h264", Resolution: "1080p",
		VideoTracks: []models.VideoTrack{{Codec: "h264", ColorRange: "tv"}},
	}
	repo := &probeRepairTestRepository{files: map[int]*models.MediaFile{file.ID: file}}
	ensurer := &PlaybackProbeEnsurer{fileRepo: repo, ffprobePath: writeRejectingFFprobe(t), timeout: 5 * time.Second}

	got, err := ensurer.EnsureProbeOnly(t.Context(), file)
	if err == nil {
		t.Fatal("a failed repair of a probed row should still report the probe error")
	}
	if got.ProbeRejected() || repo.markCalls != 0 {
		t.Fatalf("probed row was marked rejected (mark calls %d)", repo.markCalls)
	}
}

// The scan records ffprobe's rejection on the row, re-records it for rows that
// predate the column, and clears it once the file is replaced with one that
// probes.
func TestScanRecordsAndClearsProbeRejectionPostgres(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required")
	}
	ctx := t.Context()
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Probe rejection")
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
	moviePath := filepath.Join(root, "Broken Movie (2020).mp4")
	if err := os.WriteFile(moviePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 1, false, 0)

	readState := func() (failedAt, probedAt *time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`SELECT probe_failed_at, probe_updated_at FROM media_files WHERE file_path = $1`, moviePath,
		).Scan(&failedAt, &probedAt); err != nil {
			t.Fatalf("read probe state: %v", err)
		}
		return failedAt, probedAt
	}

	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if failedAt, probedAt := readState(); failedAt == nil || probedAt != nil {
		t.Fatalf("after rejected probe: probe_failed_at=%v probe_updated_at=%v", failedAt, probedAt)
	}
	file, err := s.fileRepo.GetByPath(ctx, moviePath)
	if err != nil || !file.ProbeRejected() {
		t.Fatalf("stored file rejected=%v err=%v", file != nil && file.ProbeRejected(), err)
	}

	// A row written before probe failures were recorded is marked by the
	// next scan even though nothing else about it changes.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET probe_failed_at = NULL WHERE file_path = $1`, moviePath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if failedAt, _ := readState(); failedAt == nil {
		t.Fatal("rescan did not record the rejection on an unmarked row")
	}

	video, err := os.ReadFile("testdata/test.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moviePath, video, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan after replacement: %v", err)
	}
	if failedAt, probedAt := readState(); failedAt != nil || probedAt == nil {
		t.Fatalf("after replacement: probe_failed_at=%v probe_updated_at=%v", failedAt, probedAt)
	}
}

// MarkProbeFailed never overrides a successful probe.
func TestMarkProbeFailedSkipsProbedRowsPostgres(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := t.Context()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Probe rejection guard")
	repo := NewFileRepository(pool)
	probedAt := time.Now().UTC()
	probed, err := repo.Upsert(ctx, models.MediaFile{
		MediaFolderID: folderID, FilePath: filepath.Join(t.TempDir(), "probed.mkv"), FileSize: 10,
		ProbeSource: "local", ProbeUpdatedAt: &probedAt, CodecVideo: "h264",
		SubtitleTracks: []models.SubtitleTrack{}, ExternalSubtitles: []models.ExternalSubtitle{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkProbeFailed(ctx, probed.ID); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, probed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProbeFailedAt != nil {
		t.Fatalf("probed row marked failed at %v", got.ProbeFailedAt)
	}
}
