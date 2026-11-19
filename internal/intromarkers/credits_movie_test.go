package intromarkers

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// Movie and episode tails are the same kind with different parameters, so
// they never share a key, and a movie tail's key names its sampling.
func TestMovieCreditsTailKeyIsNamespaced(t *testing.T) {
	movie, episode := movieCreditsTailKey(), creditsTailKey()
	if movie.Kind != ArtifactKindCreditsTail || movie.ConfigHash == episode.ConfigHash || movie.ConfigHash == "" {
		t.Fatalf("movie key %+v, episode key %+v", movie, episode)
	}
	if want := "movie;tail=900:0.25;every=3.0;crop=0.90x0.80;width=480;black=20,26,32;format=credits-tail:v1"; movieCreditsTailParams != want {
		t.Fatalf("movie tail params %q, want %q", movieCreditsTailParams, want)
	}
}

// movieKeyframes is a movie tail sampled every three seconds: story, then
// text on black from creditsAt to the end, with a black keyframe just
// before the first text.
func movieKeyframes(duration, creditsAt float64) []creditsKeyframe {
	window := movieTailWindow(Candidate{DurationSeconds: duration})
	var keyframes []creditsKeyframe
	for _, at := range movieTailSamples(window) {
		class := keyframeContent
		switch {
		case at >= creditsAt:
			class = keyframeLettered
		case at >= creditsAt-3:
			class = keyframeBlack
		}
		keyframes = append(keyframes, creditsKeyframe{Seconds: at, Class: class})
	}
	return keyframes
}

func TestPlaceMovieCredits(t *testing.T) {
	const duration = 7200.0
	candidate := Candidate{FileID: 1, DurationSeconds: duration}
	// Credits text from 6603 s; the black keyframe at 6600 s moves the start
	// there, and the last story keyframe is at 6597 s.
	keyframes := movieKeyframes(duration, 6603)
	var asked []fingerprintWindow
	silencesOf := func(silences ...mediasample.Interval) func(fingerprintWindow) ([]mediasample.Interval, error) {
		return func(window fingerprintWindow) ([]mediasample.Interval, error) {
			asked = append(asked, window)
			return silences, nil
		}
	}
	tests := []struct {
		name      string
		silences  func(fingerprintWindow) ([]mediasample.Interval, error)
		wantStart float64
		wantErr   bool
	}{
		{name: "no audio", wantStart: 6600},
		{name: "no silence", silences: silencesOf(), wantStart: 6600},
		{name: "silence between the story and the credits", silences: silencesOf(mediasample.Interval{Start: 6597.5, End: 6599}), wantStart: 6599},
		{name: "silence reaching into the story", silences: silencesOf(mediasample.Interval{Start: 6596, End: 6599}), wantStart: 6600},
		// The audio read starts at 6590 s; a silence starting there may
		// have started earlier, inside the story.
		{name: "silence cut by the start of the read", silences: silencesOf(mediasample.Interval{Start: 6590.01, End: 6599}), wantStart: 6600},
		{name: "silence detection failed", silences: func(fingerprintWindow) ([]mediasample.Interval, error) {
			return nil, errors.New("ffmpeg failed")
		}, wantStart: 6600, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked = nil
			segment, ok, err := placeMovieCredits(candidate, keyframes, tt.silences)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %t", err, tt.wantErr)
			}
			if !ok || segment.Start != tt.wantStart || segment.End != duration ||
				segment.Algorithm != CreditsVideoAlgorithm || segment.Confidence != creditsVideoLetteredConfidence {
				t.Fatalf("segment %+v, %t; want video credits from %.0f s to the end", segment, ok, tt.wantStart)
			}
			if tt.silences != nil && tt.name != "silence detection failed" &&
				(len(asked) != 1 || asked[0] != (fingerprintWindow{Start: 6590, End: 6602})) {
				t.Fatalf("silence windows %+v, want one around the video start", asked)
			}
		})
	}
}

// TestAnalyzeMovieWithRealFFmpeg places the credits of a generated movie: a
// dark, noisy scene until 1100 s, then text bars on black to the end at
// 1260 s, a keyframe a second, with a tone that falls silent from 1098.5 s to
// 1099.8 s. The tail pass samples every three seconds from 945 s, so the last
// story sample is at 1098 s and the first credits sample at 1101 s; the
// silence between them moves the start to 1099.8 s.
func TestAnalyzeMovieWithRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	caps, err := mediasample.LoadCapabilities(ctx, ffmpeg)
	if err != nil {
		t.Skipf("ffmpeg capabilities unavailable: %v", err)
	}
	extractor := NewChromaprintExtractor(DefaultConfig(ffmpeg))
	if err := extractor.PreflightMovieTail(ctx); err != nil {
		t.Skipf("ffmpeg cannot run the movie tail pass: %v", err)
	}
	for _, filter := range []string{"drawbox", "noise", "concat", "aevalsrc", "silencedetect"} {
		if !caps.HasFilter(filter) {
			t.Skipf("ffmpeg lacks the %s filter the clip needs", filter)
		}
	}
	candidate := movieCandidate(1, 1260)
	candidate.FilePath = filepath.Join(t.TempDir(), "movie's cut.mkv")
	scene := "color=c=0x262626:s=320x180:r=2:d=1100,noise=alls=24:allf=t,drawbox=x=250:y=20:w=12:h=12:color=white:t=fill"
	roll := "color=c=black:s=320x180:r=2:d=160,drawbox=x=100:y=75:w=120:h=6:color=white:t=fill"
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", scene, "-f", "lavfi", "-i", roll,
		"-f", "lavfi", "-i", "aevalsrc=if(between(t\\,1098.5\\,1099.8)\\,0\\,0.3*sin(2*PI*330*t)):d=1260:s=8000",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "2", "-shortest", candidate.FilePath)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}

	repo := &fakeIntroRepository{movieCandidates: []Candidate{candidate}}
	analyzer := &Analyzer{
		repo: repo, extractor: extractor, movieSampler: extractor, config: DefaultConfig(ffmpeg),
		node: "node-a", logger: slog.New(slog.DiscardHandler),
	}
	summary, err := analyzer.AnalyzeMovieFile(ctx, 1)
	if err != nil {
		t.Fatalf("AnalyzeMovieFile: %v", err)
	}
	if artifact := repo.artifact(1, ArtifactKindCreditsTail); artifact.Status != mediaartifact.StatusComplete || artifact.ItemCount != 105 {
		t.Fatalf("movie tail artifact %+v, want 105 sampled keyframes", artifact)
	}
	credits := creditsPatches(repo)
	if summary.MovieCreditsMarkersWritten != 1 || len(credits) != 1 || math.Abs(credits[0].Start-1099.8) > 0.1 ||
		credits[0].End != 1260 || credits[0].Algorithm != CreditsVideoAlgorithm {
		t.Fatalf("credits %+v, summary %+v; want video credits from the end of the silence to the end", credits, summary)
	}
}
