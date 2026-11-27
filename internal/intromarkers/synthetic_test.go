package intromarkers

import (
	"fmt"
	"math"
	"math/rand"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Synthetic loudness traces for the envelope comparator's tests.
//
// A trace is built from parts in the band's own terms: digital silence, a
// music cue with a recognisable shape, and dialogue-like chatter from a noise
// source. The same cue reused across episodes with a little jitter stands in
// for one recording heard through two lossy encodes, which is what a season
// comparison has to match.

// traceBuilder accumulates frames into one trace.
type traceBuilder struct{ levels []byte }

// silence appends digital silence.
func (b *traceBuilder) silence(seconds float64) *traceBuilder {
	for range framesOf(seconds) {
		b.levels = append(b.levels, 1)
	}
	return b
}

// music appends a music-like cue. The same seed and length always give the
// same frames.
func (b *traceBuilder) music(seed int64, seconds float64) *traceBuilder {
	b.levels = append(b.levels, cueLevels(seed, seconds, false)...)
	return b
}

// musicJittered appends the same cue as music with a little noise on every
// frame, standing in for a second encode of the same recording.
func (b *traceBuilder) musicJittered(seed int64, seconds float64, jitterDB float64) *traceBuilder {
	rng := rand.New(rand.NewSource(seed*7 + 1))
	for _, level := range cueLevels(seed, seconds, false) {
		b.levels = append(b.levels, clampLevel(float64(level)+rng.NormFloat64()*jitterDB))
	}
	return b
}

// dialogue appends chatter from a noise source: loud bursts and short pauses,
// unlike any cue.
func (b *traceBuilder) dialogue(seed int64, seconds float64) *traceBuilder {
	b.levels = append(b.levels, cueLevels(seed, seconds, true)...)
	return b
}

// seconds is how long the trace is so far.
func (b *traceBuilder) seconds() float64 { return float64(len(b.levels)) * LevelsHopSeconds }

// build returns the trace's frames.
func (b *traceBuilder) build() []byte { return b.levels }

// framesOf is how many frames a duration holds.
func framesOf(seconds float64) int {
	return int(math.Round(seconds / LevelsHopSeconds))
}

// clampLevel keeps a level in the range the speech output reports.
func clampLevel(level float64) byte {
	if level < 1 {
		return 1
	}
	if level > 100 {
		return 100
	}
	return byte(level + 0.5)
}

// cueLevels builds a cue: carriers over an envelope, with music choosing a
// music-like shape and chatter a burst-and-pause one.
func cueLevels(seed int64, seconds float64, chatter bool) []byte {
	frames := framesOf(seconds)
	rng := rand.New(rand.NewSource(seed))
	base := 40 + rng.Float64()*10
	// The carriers come from the seed too, so two seeds are two recordings.
	f1 := 0.3 + rng.Float64()*0.7
	f2 := 0.9 + rng.Float64()*1.4
	beat := 1.4 + rng.Float64()*1.2
	out := make([]byte, frames)
	for i := range frames {
		t := float64(i) * LevelsHopSeconds
		var level float64
		if !chatter {
			pulse := 0.5 + 0.5*math.Sin(2*math.Pi*beat*t)
			melody := math.Sin(2*math.Pi*f1*t) + 0.6*math.Sin(2*math.Pi*f2*t+1.7)
			level = base + 12*pulse + 8*melody
		} else {
			// Chatter has no shape two seeds share: a random walk with
			// pauses, so two episodes' dialogue correlates no better than
			// chance at any offset.
			if rng.Float64() < 0.15 {
				level = base - 30
			} else {
				level = base + rng.NormFloat64()*7
			}
		}
		out[i] = clampLevel(level)
	}
	return out
}

// syntheticEpisode describes one episode's trace and chapters.
type syntheticEpisode struct {
	fileID   int
	episode  string
	duration float64
	levels   []byte
	// chapters are (title, start, end) triples.
	chapters [][3]any
}

// input turns a synthetic episode into a levels input covering the whole
// trace.
func (ep syntheticEpisode) input() levelsInput {
	chapters := make([]models.MediaChapter, 0, len(ep.chapters))
	for i, c := range ep.chapters {
		chapters = append(chapters, models.MediaChapter{
			Index:        i,
			Title:        c[0].(string),
			StartSeconds: c[1].(float64),
			EndSeconds:   c[2].(float64),
		})
	}
	// A stored envelope covers only the file's analysis window, so the
	// synthetic trace is cut to it as well.
	window := levelsWindow(Candidate{DurationSeconds: ep.duration}, DefaultConfig("ffmpeg"))
	levels := ep.levels
	if end := framesOf(window.End); end < len(levels) {
		levels = levels[:end]
	}
	return levelsInput{
		Candidate: Candidate{
			FileID:          ep.fileID,
			EpisodeID:       ep.episode,
			EpisodeNumber:   ep.fileID,
			SeasonID:        "season",
			DurationSeconds: ep.duration,
			Chapters:        chapters,
		},
		Levels:      levels,
		WindowStart: 0,
	}
}

// describe renders a segment for a failure message.
func describe(segment Segment) string {
	if segment.Algorithm == "" && segment.End == 0 {
		return "nothing"
	}
	return fmt.Sprintf("%.2f-%.2f (%s, %.2f)", segment.Start, segment.End, segment.Algorithm, segment.Confidence)
}

// segmentRange renders a stretch for a failure message.
func segmentRange(start, end float64) string {
	return fmt.Sprintf("%.2f-%.2f", start, end)
}
