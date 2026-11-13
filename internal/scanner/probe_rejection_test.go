package scanner

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	if err == nil || !IsProbeRejection(ctx, media, err) {
		t.Fatalf("non-zero ffprobe exit = %v, want a rejection", err)
	}

	_, err = ProbeFile(ctx, filepath.Join(t.TempDir(), "missing-ffprobe"), media)
	if err == nil || IsProbeRejection(ctx, media, err) {
		t.Fatalf("missing binary = %v, want a non-rejection error", err)
	}

	garbage := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, garbage, "#!/bin/sh\nprintf 'not json'\n")
	_, err = ProbeFile(ctx, garbage, media)
	if err == nil || IsProbeRejection(ctx, media, err) {
		t.Fatalf("unparseable output = %v, want a non-rejection error", err)
	}

	slow := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, slow, "#!/bin/sh\nexec sleep 30\n")
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = ProbeFile(timeoutCtx, slow, media)
	if err == nil || IsProbeRejection(timeoutCtx, media, err) {
		t.Fatalf("timed-out probe = %v, want a non-rejection error", err)
	}

	if IsProbeRejection(ctx, media, nil) {
		t.Fatal("nil error reported as a rejection")
	}

	// ffprobe can exit 0 on a damaged file after guessing its format: an AVI
	// with a broken header reads as a bare MPEG-4 stream at score 5. A guess
	// (AVPROBE_SCORE_RETRY, 25, or lower) of a format the extension does not
	// name is checked by reading the file with the extension's own demuxer,
	// and counts as a rejection only when that read fails too.
	const invalid = "Invalid data found when processing input"
	avi := filepath.Join(t.TempDir(), "Movie (2019).avi")
	if err := os.WriteFile(avi, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		score       int
		forcedExit  int
		forcedError string
		wideScore   int
		wideFormat  string
		rejection   bool
		failure     bool
	}{
		{"guess the container demuxer refuses", 25, 1, invalid, 25, "m4v", true, true},
		{"guess the container demuxer reads", 5, 0, invalid, 5, "m4v", false, false},
		{"guess a wider read is sure of", 25, 1, invalid, 26, "m4v", false, false},
		// A wider read that finds another format neither confirms the guess
		// nor shows damage.
		{"guess a wider read contradicts", 25, 1, invalid, 26, "mp3", false, true},
		// A read that storage fails says nothing about the file, whichever
		// of the two reads it is.
		{"guess a wider read cannot finish", 25, 1, invalid, -1, "m4v", false, true},
		{"guess the container demuxer cannot finish reading", 25, 1, "Input/output error", -2, "m4v", false, true},
		{"confident reading", 26, 1, invalid, 26, "m4v", false, false},
	} {
		guess := filepath.Join(t.TempDir(), "ffprobe")
		writeFakeTool(t, guess, fmt.Sprintf(`#!/bin/sh
score=%d
format=m4v
for arg; do
	case "$arg" in
	-f)
		echo '%s' >&2
		exit %d
		;;
	-formatprobesize)
		score=%d
		format=%s
		;;
	esac
done
if [ "$score" -eq -1 ]; then
	echo 'Movie.avi: Input/output error' >&2
	exit 1
elif [ "$score" -lt 0 ]; then
	echo 'Movie.avi: Invalid data found when processing input' >&2
	exit 1
fi
printf '%%s\n' '{"format":{"format_name":"'"$format"'","probe_score":'"$score"'},"streams":[{"codec_type":"video","codec_name":"mpeg4","width":640,"height":360}]}'
`, tc.score, tc.forcedError, tc.forcedExit, tc.wideScore, tc.wideFormat))
		probe, err := ProbeFile(ctx, guess, avi)
		if got := err != nil && IsProbeRejection(ctx, avi, err); got != tc.rejection || (err != nil) != tc.failure {
			t.Fatalf("%s: err = %v, rejection = %v, want rejection %v and failure %v", tc.name, err, got, tc.rejection, tc.failure)
		}
		if err == nil && probe.Container != "m4v" {
			t.Fatalf("%s: container = %q, want the guess kept", tc.name, probe.Container)
		}
	}

	// The shell and the dynamic loader exit 126 or 127 when ffprobe itself
	// cannot start, such as a shared library it cannot map.
	for _, code := range []int{126, 127} {
		broken := filepath.Join(t.TempDir(), "ffprobe")
		writeFakeTool(t, broken, fmt.Sprintf("#!/bin/sh\necho 'ffprobe: error while loading shared libraries' >&2\nexit %d\n", code))
		_, err = ProbeFile(ctx, broken, media)
		if err == nil || IsProbeRejection(ctx, media, err) {
			t.Fatalf("ffprobe that could not start (exit %d) = %v, want a non-rejection error", code, err)
		}
	}
}

// The files from issue #1791: real ffprobe refuses zero-byte, random-byte and
// truncated media with a non-zero exit, which must classify as a rejection.
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
	// A fixed seed keeps the bytes the same on every run: freshly random bytes
	// occasionally look like a format ffprobe accepts, which made this flaky.
	random := make([]byte, 256<<10)
	if _, err := rand.NewChaCha8([32]byte{0x19, 0x17, 0x91}).Read(random); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "corrupt.mkv")
	if err := os.WriteFile(corrupt, random, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []string{empty, corrupt}
	if truncated, ok := writeTruncatedMP4(t, dir); ok {
		cases = append(cases, truncated)
	}
	broken, healthy := writeGuessedFormatFiles(t, dir, random[:4096])
	cases = append(cases, broken...)
	for _, path := range cases {
		_, err := ProbeFile(t.Context(), ffprobe, path)
		if err == nil || !IsProbeRejection(t.Context(), path, err) {
			t.Fatalf("%s: probe error = %v, want a rejection", filepath.Base(path), err)
		}
	}
	for _, path := range healthy {
		if _, err := ProbeFile(t.Context(), ffprobe, path); err != nil {
			t.Fatalf("%s: probe error = %v, want a healthy file read", filepath.Base(path), err)
		}
	}
}

// writeGuessedFormatFiles returns the AVI from issue #2862 as rejected: its
// first 4 KiB are overwritten with damage, so ffprobe exits 0 after guessing a
// bare MPEG-4 stream. The healthy files must still probe: a one-frame MPEG-TS
// small enough that ffprobe only guesses at it, the undamaged AVI under an
// .mkv name, which ffprobe identifies with confidence, and a bare 4K MPEG-4
// stream under an .avi name, which ffprobe is sure of only when it reads
// further than its default. It returns nothing when ffmpeg is unavailable.
func writeGuessedFormatFiles(t *testing.T, dir string, damage []byte) (rejected, healthy []string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, nil
	}
	encode := func(name string, args ...string) string {
		path := filepath.Join(dir, name)
		args = append([]string{"-v", "error", "-f", "lavfi"}, append(args, "-y", path)...)
		if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("encode %s: %v: %s", name, err, out)
		}
		return path
	}
	whole, err := os.ReadFile(encode("whole.avi", "-i", "testsrc=duration=6:size=640x360:rate=24", "-c:v", "mpeg4"))
	if err != nil {
		t.Fatal(err)
	}
	mislabeled := filepath.Join(dir, "mislabeled.mkv")
	if err := os.WriteFile(mislabeled, whole, 0o600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken-header.avi")
	if err := os.WriteFile(broken, append(slices.Clone(damage), whole[len(damage):]...), 0o600); err != nil {
		t.Fatal(err)
	}
	oneFrame := encode("one-frame.ts", "-i", "color=black:size=64x64:rate=24", "-frames:v", "1", "-c:v", "mpeg2video", "-f", "mpegts")
	bareStream := encode("bare-stream.avi", "-i", "testsrc2=size=3840x2160:rate=24", "-frames:v", "12", "-c:v", "mpeg4", "-q:v", "2", "-f", "m4v")
	return []string{broken}, []string{oneFrame, mislabeled, bareStream}
}

// writeTruncatedMP4 encodes a short MP4 whose moov atom is written at the end,
// as ffmpeg does by default, and keeps only its first part, so the index is
// missing. It reports false when ffmpeg is unavailable.
func writeTruncatedMP4(t *testing.T, dir string) (string, bool) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", false
	}
	whole := filepath.Join(dir, "whole.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=24",
		"-c:v", "mpeg4", "-y", whole).CombinedOutput(); err != nil {
		t.Fatalf("encode test mp4: %v: %s", err, out)
	}
	data, err := os.ReadFile(whole)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "truncated.mp4")
	if err := os.WriteFile(truncated, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	return truncated, true
}

// ffprobe also exits non-zero when it cannot read the file at all. That is a
// permissions or storage problem, not a damaged file, so it is not a
// rejection and playback keeps the retryable answer.
func TestIsProbeRejectionIgnoresFilesTheServerCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of their mode")
	}
	ctx := t.Context()
	locked := filepath.Join(t.TempDir(), "S01E06.mkv")
	if err := os.WriteFile(locked, []byte("valid bytes the server may not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

	_, err := ProbeFile(ctx, writeRejectingFFprobe(t), locked)
	if err == nil || IsProbeRejection(ctx, locked, err) {
		t.Fatalf("unreadable file = %v, want a non-rejection", err)
	}
	if ffprobe, lookErr := exec.LookPath("ffprobe"); lookErr == nil {
		_, err := ProbeFile(ctx, ffprobe, locked)
		if err == nil || IsProbeRejection(ctx, locked, err) {
			t.Fatalf("real ffprobe on an unreadable file = %v, want a non-rejection", err)
		}
	}

	missing := filepath.Join(t.TempDir(), "gone.mkv")
	_, err = ProbeFile(ctx, writeRejectingFFprobe(t), missing)
	if err == nil || IsProbeRejection(ctx, missing, err) {
		t.Fatalf("vanished file = %v, want a non-rejection", err)
	}

	s := &Scanner{ffprobePath: writeRejectingFFprobe(t)}
	if _, _, rejected := s.probeFile(ctx, locked); rejected {
		t.Fatal("scanner reported an unreadable file as rejected")
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

func TestPlaybackProbeEnsurerRecordsAndClearsProbeRejection(t *testing.T) {
	media := filepath.Join(t.TempDir(), "S01E03.mkv")
	if err := os.WriteFile(media, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 7, FilePath: media}
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

// The playback-time repair leaves a file it cannot read unmarked, so the start
// answer stays the retryable source_metadata_incomplete.
func TestPlaybackProbeEnsurerDoesNotMarkUnreadableAccess(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "S01E07.mkv")
	file := &models.MediaFile{ID: 9, FilePath: missing}
	repo := &probeRepairTestRepository{files: map[int]*models.MediaFile{file.ID: file}}
	ensurer := &PlaybackProbeEnsurer{fileRepo: repo, ffprobePath: writeRejectingFFprobe(t), timeout: 5 * time.Second}

	got, err := ensurer.EnsureProbeOnly(t.Context(), file)
	if err == nil {
		t.Fatal("a probe that could not read the file should report its error")
	}
	if got.ProbeRejected() || repo.markCalls != 0 {
		t.Fatalf("unreadable access was marked as a damaged file (mark calls %d)", repo.markCalls)
	}
}

// writeStderrFFprobe is a fake ffprobe that prints stderr and exits 1.
func writeStderrFFprobe(t *testing.T, stderr string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, path, "#!/bin/sh\ncat >&2 <<'EOF'\n"+stderr+"\nEOF\nexit 1\n")
	return path
}

// ffprobe can open a file and still fail to read it partway through, as on a
// flaky network mount. Its error output names the operating-system failure;
// that is a storage problem, not damaged content.
func TestIsProbeRejectionIgnoresReadFailuresFFprobeReports(t *testing.T) {
	ctx := t.Context()
	media := filepath.Join(t.TempDir(), "S01E08.mkv")
	if err := os.WriteFile(media, []byte("readable header bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, stderr := range []string{
		"S01E08.mkv: Input/output error",
		"[matroska,webm @ 0x1] Read error at pos. 1048576 (0x100000)\nS01E08.mkv: Connection timed out",
		"S01E08.mkv: Stale file handle",
	} {
		_, err := ProbeFile(ctx, writeStderrFFprobe(t, stderr), media)
		if err == nil || IsProbeRejection(ctx, media, err) {
			t.Fatalf("stderr %q: %v, want a non-rejection", stderr, err)
		}
	}
	for _, stderr := range []string{
		"S01E08.mkv: Invalid data found when processing input",
		"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x1] moov atom not found\nS01E08.mkv: Invalid data found when processing input",
	} {
		_, err := ProbeFile(ctx, writeStderrFFprobe(t, stderr), media)
		if err == nil || !IsProbeRejection(ctx, media, err) {
			t.Fatalf("stderr %q: %v, want a rejection", stderr, err)
		}
	}
}

// The readability check covers the end of the file, where an MP4 written
// without faststart keeps its index, as well as the start.
func TestProbeInputReadableReadsBothEnds(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large.mp4")
	if err := os.WriteFile(large, make([]byte, 3*probeInputReadableBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := probeInputReadable(large); err != nil {
		t.Fatalf("readable file: %v", err)
	}
	empty := filepath.Join(dir, "empty.mp4")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := probeInputReadable(empty); err != nil {
		t.Fatalf("an empty file is readable, got %v", err)
	}
	if err := probeInputReadable(dir); err == nil {
		t.Fatal("a directory reported as a readable media file")
	}
}

// rejectingProbeFunc runs a fake ffprobe that exits 1, so the error is the
// real *exec.ExitError IsProbeRejection classifies, after calling before.
func rejectingProbeFunc(t *testing.T, before func()) func(context.Context, string, string) (*ProbeData, error) {
	t.Helper()
	ffprobe := writeRejectingFFprobe(t)
	return func(ctx context.Context, _ string, path string) (*ProbeData, error) {
		if before != nil {
			before()
		}
		return ProbeFile(ctx, ffprobe, path)
	}
}

// stubProbeReadable replaces the storage check and its bounds for one test.
func stubProbeReadable(t *testing.T, check func(string) error, timeout time.Duration, slots int) {
	t.Helper()
	prevCheck, prevTimeout, prevSlots := probeReadableCheck, probeReadableTimeout, probeReadableSlots
	probeReadableCheck, probeReadableTimeout, probeReadableSlots = check, timeout, make(chan struct{}, slots)
	t.Cleanup(func() {
		probeReadableCheck, probeReadableTimeout, probeReadableSlots = prevCheck, prevTimeout, prevSlots
	})
}

// A storage check stuck in the kernel (a stalled mount) must not hold the
// caller past its bound, and the failure is then not blamed on the file.
func TestIsProbeRejectionBoundsAStalledStorageCheck(t *testing.T) {
	ctx := t.Context()
	media := filepath.Join(t.TempDir(), "S01E10.mkv")
	if err := os.WriteFile(media, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	stubProbeReadable(t, func(string) error { <-release; return nil }, 50*time.Millisecond, 1)
	defer close(release)

	_, err := ProbeFile(ctx, writeRejectingFFprobe(t), media)
	started := time.Now()
	if IsProbeRejection(ctx, media, err) {
		t.Fatal("a storage check that never answered was treated as a rejection")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("IsProbeRejection blocked for %v, want it bounded by the check timeout", elapsed)
	}

	// The stalled check still holds its slot, so the next one is skipped
	// immediately instead of queueing behind it.
	started = time.Now()
	if IsProbeRejection(ctx, media, err) {
		t.Fatal("a skipped storage check was treated as a rejection")
	}
	if elapsed := time.Since(started); elapsed > 40*time.Millisecond {
		t.Fatalf("saturated check waited %v, want an immediate skip", elapsed)
	}
}

// The caller's context bounds the check as well as the timeout.
func TestIsProbeRejectionAbandonsStorageCheckWithContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	stubProbeReadable(t, func(string) error { <-release; return nil }, time.Minute, 1)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)
	started := time.Now()
	if err := boundedProbeInputReadable(ctx, "unused"); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("abandoned check = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("check outlived its context by %v", elapsed)
	}
}

// Within the bound, the check's own answer decides as before, and a finished
// check frees its slot.
func TestBoundedProbeInputReadablePassesThroughTheCheck(t *testing.T) {
	stubProbeReadable(t, probeInputReadable, time.Minute, 1)
	dir := t.TempDir()
	readable := filepath.Join(dir, "readable.mkv")
	if err := os.WriteFile(readable, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := boundedProbeInputReadable(t.Context(), readable); err != nil {
			t.Fatalf("readable file: %v", err)
		}
	}
	if err := boundedProbeInputReadable(t.Context(), filepath.Join(dir, "missing.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file = %v, want os.ErrNotExist", err)
	}
}
