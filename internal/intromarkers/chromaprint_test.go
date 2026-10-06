package intromarkers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingFFmpeg writes a fake ffmpeg that saves its arguments, one per line,
// and prints stdout. It returns the binary and a reader for the arguments.
func recordingFFmpeg(t *testing.T, stdout string) (string, func() []string) {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "ffmpeg-args.txt")
	ffmpeg := writeFakeFFmpeg(t, fmt.Sprintf("printf '%%s\\n' \"$@\" > %q\nprintf '%s'", argsPath, stdout))
	return ffmpeg, func() []string {
		data, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatalf("read fake ffmpeg args: %v", err)
		}
		return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
}

// The fingerprint cache outlives releases, so the arguments that produce a
// fingerprint are pinned. They were checked byte for byte against the
// arguments used before the move to mediasample; see
// docs/architecture/media-sampling.md before changing them.
func TestChromaprintExtractorArguments(t *testing.T) {
	ffmpeg, args := recordingFFmpeg(t, `\001\000\000\000`)
	cfg := DefaultConfig(ffmpeg)
	cfg.MaxParallelFFmpeg = 4
	if _, ok, err := NewChromaprintExtractor(cfg).Extract(context.Background(), Candidate{
		FileID: 1, FilePath: "/media/Show/S01E01.mkv", DurationSeconds: 1500,
	}); err != nil || !ok {
		t.Fatalf("Extract = %v, %v", ok, err)
	}
	want := strings.Fields("-hide_banner -nostdin -loglevel warning -threads 1 -ss 0 -i /media/Show/S01E01.mkv -t 375 " +
		"-vn -sn -dn -ac 2 -f chromaprint -fp_format raw -")
	if got := args(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}
}

// Scheduled and admin analysis runs in the background; analysis started from
// playback runs at normal priority.
func TestFingerprintRequestBackground(t *testing.T) {
	candidate := Candidate{FileID: 1, FilePath: "/media/Show/S01E01.mkv", DurationSeconds: 1500}
	if req := fingerprintRequest(context.Background(), candidate, fingerprintWindow{End: 375}); !req.Background {
		t.Fatal("scheduled analysis request is not background")
	}
	if req := fingerprintRequest(WithPlaybackPriority(context.Background()), candidate, fingerprintWindow{End: 375}); req.Background {
		t.Fatal("playback analysis request is background")
	}
}

func TestSilenceBoundaryRefinerArguments(t *testing.T) {
	ffmpeg, args := recordingFFmpeg(t, "")
	segment := Segment{Start: 60, End: 120, Confidence: 0.95, Algorithm: ChapterAlgorithm}
	if _, _, err := NewSilenceBoundaryRefiner(DefaultConfig(ffmpeg)).RefineChapterEnd(context.Background(),
		Candidate{FileID: 1, FilePath: "/media/Show/S01E01.mkv", DurationSeconds: 1200}, segment); err != nil {
		t.Fatal(err)
	}
	want := strings.Fields("-hide_banner -nostdin -loglevel repeat+info -ss 117 -i /media/Show/S01E01.mkv -t 33 " +
		"-vn -sn -dn -af silencedetect=noise=-50dB:duration=0.33 -f null -")
	if got := args(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}
}

func TestChromaprintExtractorPreflight(t *testing.T) {
	listings := func(muxers, help string) string {
		return fmt.Sprintf(`case "$*" in
*-filters*) printf ' ... silencedetect     A->A       Detect silence.\n' ;;
*-muxers*) printf 'Formats:\n ---\n%s' ;;
*muxer=chromaprint*) printf '%s' ;;
esac`, muxers, help)
	}
	supported := writeFakeFFmpeg(t, listings(`  E  chromaprint     Chromaprint\n`, `  -fp_format <int>\n     raw 0 binary raw fingerprint\n`))
	if err := NewChromaprintExtractor(DefaultConfig(supported)).Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight with chromaprint = %v", err)
	}
	missing := writeFakeFFmpeg(t, listings(`  E  matroska        Matroska\n`, ``))
	err := NewChromaprintExtractor(DefaultConfig(missing)).Preflight(context.Background())
	if err == nil || !strings.Contains(err.Error(), "chromaprint muxer") {
		t.Fatalf("Preflight without chromaprint = %v", err)
	}
}
