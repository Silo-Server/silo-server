package intromarkers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// stalledFFmpeg writes a fake ffmpeg that never finishes, like one whose
// read is stuck on its storage.
func stalledFFmpeg(t *testing.T) string {
	t.Helper()
	return writeFakeFFmpeg(t, "exec sleep 30")
}

// finishesWithinDeadline runs f in the background and fails the test if it
// is still running long after the runs' deadlines: without one, f waits out
// the fake ffmpeg's 30-second sleep.
func finishesWithinDeadline(t *testing.T, name string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s is still running past its ffmpeg deadline", name)
	}
}

// Issue #581: a stalled ffmpeg held intro detection for half an hour. Every
// run intro and credits detection makes now stops at its deadline and fails
// as a timeout, which is not permanent.
func TestAnalysisRunsStopAtTheirDeadline(t *testing.T) {
	cfg := DefaultConfig(stalledFFmpeg(t))
	cfg.attemptTimeout = 200 * time.Millisecond
	extractor := NewChromaprintExtractor(cfg)
	extractor.hardware.Set("none", "")
	candidate := Candidate{FileID: 7, FilePath: "/media/show/e1.mkv", DurationSeconds: 1500, CodecVideo: "h264", CodecAudio: "aac"}
	runs := []struct {
		name string
		run  func(context.Context) error
	}{
		{"intro fingerprint", func(ctx context.Context) error {
			_, _, err := extractor.Extract(ctx, candidate)
			return err
		}},
		{"credits fingerprint", func(ctx context.Context) error {
			_, _, err := extractor.ExtractCredits(ctx, candidate)
			return err
		}},
		{"credits tail", func(ctx context.Context) error {
			_, err := extractor.SampleCreditsTail(ctx, candidate, true)
			return err
		}},
		{"movie tail", func(ctx context.Context) error {
			_, err := extractor.SampleMovieTail(ctx, candidate)
			return err
		}},
		{"movie silences", func(ctx context.Context) error {
			_, err := extractor.SampleMovieSilences(ctx, candidate, fingerprintWindow{Start: 1200, End: 1260})
			return err
		}},
		{"silence refinement", func(ctx context.Context) error {
			_, _, err := NewSilenceBoundaryRefiner(cfg).RefineChapterEnd(ctx, candidate, Segment{Start: 60, End: 120})
			return err
		}},
	}
	for _, tc := range runs {
		var err error
		finishesWithinDeadline(t, tc.name, func() { err = tc.run(t.Context()) })
		if mediasample.Classify(err) != mediasample.ReasonTimeout || !strings.Contains(err.Error(), "did not finish within its 200ms deadline") {
			t.Errorf("%s: error %v, want a timeout at the deadline", tc.name, err)
		}
	}

	// A tail pass planned on hardware bounds its software attempt too.
	extractor.hardware.Set("videotoolbox", "")
	var err error
	finishesWithinDeadline(t, "hardware credits tail", func() { _, err = extractor.SampleCreditsTail(t.Context(), candidate, false) })
	var runErr *mediasample.Error
	if !errors.As(err, &runErr) || len(runErr.Attempts) != 2 {
		t.Fatalf("hardware credits tail error %v, want both attempts", err)
	}
	for _, attempt := range runErr.Attempts {
		if attempt.Reason != mediasample.ReasonTimeout {
			t.Errorf("%s attempt failed as %s, want a timeout", attempt.Decoder, attempt.Reason)
		}
	}
}

// A run's deadline is two minutes plus the running time of the media it
// reads, on each of its attempts.
func TestRunAnalysisDeadlineCoversEveryAttempt(t *testing.T) {
	var remaining []time.Duration
	runner := mediasample.Runner{FFmpegPath: "ffmpeg", HWAccel: "videotoolbox",
		Exec: func(ctx context.Context, _ string, _ []string, _ io.Reader, _, _ io.Writer) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				remaining = append(remaining, 0)
			} else {
				remaining = append(remaining, time.Until(deadline))
			}
			return errors.New("exit status 1")
		}}
	candidate := Candidate{FileID: 7, FilePath: "/media/show/e1.mkv", DurationSeconds: 1500, CodecVideo: "h264", CodecAudio: "aac"}
	req := creditsTailRequest(t.Context(), candidate, fingerprintWindow{Start: 1200, End: 1500}, false)
	req.Attempts = hardwareAttempts("videotoolbox")
	if _, err := runAnalysis(t.Context(), DefaultConfig("ffmpeg"), runner, req); err == nil {
		t.Fatal("runAnalysis succeeded, want the fake's failure")
	}
	want := 7 * time.Minute
	if len(remaining) != 2 {
		t.Fatalf("ran %d attempts, want hardware then software", len(remaining))
	}
	for i, left := range remaining {
		if left <= want-time.Minute || left > want {
			t.Errorf("attempt %d had %s left, want about %s", i+1, left, want)
		}
	}
}

func TestAnalysisDeadline(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	candidate := Candidate{FileID: 7, FilePath: "/media/show/e1.mkv", DurationSeconds: 1500}
	cases := []struct {
		name string
		req  mediasample.Request
		want time.Duration
	}{
		{"intro window", fingerprintRequest(t.Context(), candidate, headWindow(candidate, cfg)), 8*time.Minute + 15*time.Second},
		// Samples every three seconds from 900 reach 1497.
		{"movie tail samples", movieTailRequest(t.Context(), candidate, fingerprintWindow{Start: 900, End: 1500}), 11*time.Minute + 57*time.Second},
		{"no window", mediasample.Request{}, analysisDeadlineBase},
		{"capped", mediasample.Request{Window: &mediasample.Window{DurationSeconds: 48 * 3600}}, maxAnalysisDeadline},
	}
	for _, tc := range cases {
		if got := analysisDeadline(cfg, tc.req); got != tc.want {
			t.Errorf("%s: deadline %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Issue #581: a stalled fingerprint run held its intro group, and the task,
// until an administrator canceled it. The run now fails that file at its
// deadline, the group finishes with the other files and is stored as
// partial, and the log says why.
func TestAnalyzeGroupFailsStalledFingerprintAndFinishes(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `case "$*" in
*stalled.mkv*) exec sleep 30 ;;
esac
printf '\001\000\000\000'`)
	cfg := DefaultConfig(ffmpeg)
	// The deadline also bounds the other files' runs, which start at
	// background priority, so it leaves them room on a loaded CI host.
	cfg.attemptTimeout = 5 * time.Second
	var candidates []Candidate
	for i, name := range []string{"e1.mkv", "stalled.mkv", "e3.mkv"} {
		candidates = append(candidates, Candidate{
			FileID: i + 1, FilePath: "/media/show/" + name, EpisodeID: name, SeasonID: "s1", MediaFolderID: 1, DurationSeconds: 1800,
		})
	}
	repo := &fakeIntroRepository{}
	var logs bytes.Buffer
	analyzer := &Analyzer{repo: repo, extractor: NewChromaprintExtractor(cfg), config: cfg, logger: slog.New(slog.NewTextHandler(&logs, nil))}

	var summary RunSummary
	var err error
	finishesWithinDeadline(t, "group analysis", func() {
		summary, err = analyzer.analyzeGroup(t.Context(), candidateGroup{
			SeasonID: "s1", MediaFolderID: 1, AnalysisGroupKey: candidates[0].AnalysisGroupKey(), Candidates: candidates,
		}, analyzeGroupOptions{persistState: true})
	})
	if err != nil {
		t.Fatalf("analyzeGroup: %v", err)
	}
	if summary.FingerprintExtractionErrors != 1 || summary.FingerprintsComputed != 2 {
		t.Fatalf("extraction errors %d, computed %d; want the stalled file failed and the other two fingerprinted",
			summary.FingerprintExtractionErrors, summary.FingerprintsComputed)
	}
	if len(repo.upsertedStates) != 1 || repo.upsertedStates[0].Status != seasonStatusPartial {
		t.Fatalf("stored states = %+v, want one partial state", repo.upsertedStates)
	}
	if log := logs.String(); !strings.Contains(log, "file_id=2") || !strings.Contains(log, "did not finish within its 5s deadline") {
		t.Fatalf("log does not name the stalled file's timeout:\n%s", log)
	}
}
