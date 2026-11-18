//go:build subsynceval

package subsync

// Offline evaluation of Align against real files. Not part of the normal
// test run: it needs a corpus exported from a deployment.
//
//	SUBSYNC_CORPUS=/path go test -tags subsynceval -run TestEval -v ./internal/subtitles/subsync/
//
// The corpus holds jobs.tsv (job id, media file id, status, confidence,
// result offset ms, result scale, sha256, path, format, runtime), speech.tsv
// (media file id, config hash, base64 speech-levels:v1 payload, as COPY
// TO STDOUT writes it), and subs/ with each sidecar at its path.
//
// CI never builds this file; after changing the package, check it still
// compiles with go vet -tags subsynceval ./internal/subtitles/subsync/.

import (
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type evalJob struct {
	id, file int
	status   string
	truth    subtitles.Timing
	path     string
	runtime  float64
	cues     []subtitles.SubtitleCue
}

type evalCase struct {
	kind    string // known, shifted, negative, nomatch
	job     *evalJob
	cues    []subtitles.SubtitleCue
	expect  subtitles.Timing
	windows []mediasample.SpeechLevels
	// source is the sidecar the cues came from, for a negative.
	source string
}

// evalOutcome is one case aligned before (base: window search only) and
// after (joint: Align).
type evalOutcome struct {
	base, joint Alignment
	// raw is the joint search's own result, matched or not.
	raw               Alignment
	baseErr, jointErr error
}

// sameTiming reports whether two corrections place every cue within
// tolerance over the runtime.
func sameTiming(a, b subtitles.Timing, runtime float64, tolerance time.Duration) bool {
	for _, at := range []float64{0, runtime / 2, runtime} {
		d := seconds(at)
		if diff := a.Apply(d) - b.Apply(d); diff > tolerance || diff < -tolerance {
			return false
		}
	}
	return true
}

// report logs, per kind of case, how many decide matched rightly or
// wrongly, matched without a timing to compare (production no_match), or
// did not match.
func report(t *testing.T, name string, cases []evalCase, outs []evalOutcome, decide func(evalOutcome) (bool, subtitles.Timing)) {
	type tally struct{ n, right, wrong, unverified, none int }
	tallies := map[string]*tally{}
	for i, c := range cases {
		key := c.kind
		if c.kind == "known" {
			key += "/" + c.job.status
		}
		tl := tallies[key]
		if tl == nil {
			tl = &tally{}
			tallies[key] = tl
		}
		tl.n++
		ok, timing := decide(outs[i])
		switch {
		case !ok:
			tl.none++
		case c.kind == "negative":
			tl.wrong++
		case c.kind == "nomatch":
			tl.unverified++ // production rejected it; no timing to compare
		case sameTiming(timing, c.expect, c.job.runtime, 300*time.Millisecond):
			tl.right++
		default:
			tl.wrong++
		}
	}
	keys := make([]string, 0, len(tallies))
	for k := range tallies {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	t.Logf("== %s", name)
	for _, k := range keys {
		tl := tallies[k]
		t.Logf("  %-22s n=%5d right=%5d (%5.1f%%) wrong=%5d (%5.1f%%) unverified=%5d (%5.1f%%) none=%5d", k, tl.n,
			tl.right, 100*float64(tl.right)/float64(tl.n), tl.wrong, 100*float64(tl.wrong)/float64(tl.n),
			tl.unverified, 100*float64(tl.unverified)/float64(tl.n), tl.none)
	}
}

var episodeTag = regexp.MustCompile(`S\d+E\d+`)

// yearTag is a release year standing alone in a file name.
var yearTag = regexp.MustCompile(`\b(19|20)\d\d\b`)
