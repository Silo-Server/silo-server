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
	"bufio"
	"cmp"
	"encoding/base64"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// TestEval aligns every case before and after the joint search and logs
// the tallies of each kind; result.tsv in the corpus holds every case.
func TestEval(t *testing.T) {
	dir := os.Getenv("SUBSYNC_CORPUS")
	if dir == "" {
		t.Skip("SUBSYNC_CORPUS not set")
	}
	speech := loadEvalSpeech(t, filepath.Join(dir, "speech.tsv"))
	jobs := loadEvalJobs(t, filepath.Join(dir, "jobs.tsv"), filepath.Join(dir, "subs"), speech)
	cases := buildEvalCases(jobs, speech)
	if kinds := os.Getenv("SUBSYNC_KINDS"); kinds != "" {
		cases = slices.DeleteFunc(cases, func(c evalCase) bool { return !strings.Contains(","+kinds+",", ","+c.kind+",") })
	}
	started := time.Now()
	outs := runEval(cases)
	t.Logf("aligned %d cases twice in %s", len(cases), time.Since(started).Round(time.Second))
	if err := writeEval(cases, outs, filepath.Join(dir, "result.tsv")); err != nil {
		t.Fatal(err)
	}
	report(t, "window search only (before)", cases, outs, func(o evalOutcome) (bool, subtitles.Timing) {
		return o.baseErr == nil && o.base.Matched(), o.base.Timing
	})
	report(t, "Align (window search, then joint)", cases, outs, func(o evalOutcome) (bool, subtitles.Timing) {
		return o.jointErr == nil && o.joint.Matched(), o.joint.Timing
	})
}

// rawJoint runs the joint search alone.
func rawJoint(windows []mediasample.SpeechLevels, cues []subtitles.SubtitleCue) Alignment {
	signals, cues, search, err := prepare(windows, cues)
	if err != nil {
		return Alignment{}
	}
	a, _ := alignJoint(signals, cues, search)
	return a
}

// runEval aligns each case both ways, on every CPU.
func runEval(cases []evalCase) []evalOutcome {
	out := make([]evalOutcome, len(cases))
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				c := cases[i]
				var o evalOutcome
				o.base = alignWindowsOnly(c.windows, c.cues)
				o.joint, o.jointErr = Align(c.windows, c.cues)
				o.raw = rawJoint(c.windows, c.cues)
				out[i] = o
			}
		}()
	}
	for i := range cases {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
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

// writeEval writes one line per case: its expectation and both results.
func writeEval(cases []evalCase, outs []evalOutcome, path string) error {
	var b strings.Builder
	b.WriteString("kind\tjob\tfile\tstatus\texpect_ms\texpect_scale\tbase_matched\tbase_ms\tbase_scale\tbase_in\tbase_win\tbase_prom\tjoint_ms\tjoint_scale\tjoint_in\tjoint_win\tjoint_mean_prom\tjoint_prom\traw_ms\traw_scale\traw_in\traw_win\traw_prom\tpath\tsource\n")
	for i, c := range cases {
		o := outs[i]
		fmt.Fprintf(&b, "%s\t%d\t%d\t%s\t%d\t%.6f\t%t\t%d\t%.6f\t%d\t%d\t%.2f\t%d\t%.6f\t%d\t%d\t%.2f\t%.2f\t%d\t%.6f\t%d\t%d\t%.2f\t%s\t%s\n",
			c.kind, c.job.id, c.job.file, c.job.status, c.expect.OffsetMS, c.expect.Normalized().Scale,
			o.baseErr == nil && o.base.Matched(), o.base.Timing.OffsetMS, o.base.Timing.Normalized().Scale, o.base.Inliers, o.base.Windows, o.base.Prominence,
			o.joint.Timing.OffsetMS, o.joint.Timing.Normalized().Scale, o.joint.Inliers, o.joint.Windows, o.joint.Prominence, o.joint.JointProminence, o.raw.Timing.OffsetMS, o.raw.Timing.Normalized().Scale, o.raw.Inliers, o.raw.Windows, o.raw.JointProminence, c.job.path, c.source)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// loadEvalSpeech reads speech.tsv into each media file's windows.
func loadEvalSpeech(t *testing.T, path string) map[int][]mediasample.SpeechLevels {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[int][]mediasample.SpeechLevels{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<28)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) != 3 {
			continue
		}
		id, _ := strconv.Atoi(parts[0])
		raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[2], `\n`, ""))
		if err != nil {
			t.Fatalf("speech %d: %v", id, err)
		}
		windows, err := decodeSpeech(raw)
		if err != nil {
			t.Fatalf("speech %d: %v", id, err)
		}
		out[id] = windows
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read speech: %v", err)
	}
	return out
}

// loadEvalJobs reads jobs.tsv with each job's parsed sidecar, skipping
// jobs without speech, a readable sidecar, or cues.
func loadEvalJobs(t *testing.T, path, subs string, speech map[int][]mediasample.SpeechLevels) []*evalJob {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var jobs []*evalJob
	missing := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		p := strings.Split(line, "\t")
		if len(p) != 10 {
			continue
		}
		j := &evalJob{status: p[2], path: unescapeCopy(p[7])}
		j.id, _ = strconv.Atoi(p[0])
		j.file, _ = strconv.Atoi(p[1])
		off, _ := strconv.Atoi(p[4])
		scale, _ := strconv.ParseFloat(p[5], 64)
		j.truth = subtitles.Timing{OffsetMS: off, Scale: scale}
		j.runtime, _ = strconv.ParseFloat(p[9], 64)
		if speech[j.file] == nil {
			missing++
			continue
		}
		raw, err := os.ReadFile(filepath.Join(subs, j.path))
		if err != nil {
			missing++
			continue
		}
		cues, err := subtitles.ParseCuesForFormat(subtitles.SubtitleFormat(p[8]), subtitles.DecodeText(subtitles.SubtitleFormat(p[8]), "", raw))
		if err != nil || len(cues) == 0 {
			missing++
			continue
		}
		j.cues = cues
		jobs = append(jobs, j)
	}
	t.Logf("loaded %d jobs (%d skipped: no speech, file, or cues)", len(jobs), missing)
	return jobs
}

var episodeTag = regexp.MustCompile(`S\d+E\d+`)

// titleOf names the title a sidecar belongs to: its file name up to the
// year, bracket, or episode tag, so other releases and versions in other
// folders compare equal.
func titleOf(path string) string {
	name := strings.NewReplacer(".", " ", "_", " ").Replace(strings.ToLower(filepath.Base(path)))
	if i := strings.IndexAny(name, "([{"); i > 0 {
		name = name[:i]
	}
	if loc := episodeTag.FindStringIndex(strings.ToUpper(name)); loc != nil {
		name = name[:loc[0]]
	}
	if loc := yearTag.FindStringIndex(name); loc != nil && loc[0] > 0 {
		name = name[:loc[0]]
	}
	return strings.TrimSpace(strings.Trim(name, " .-_"))
}

// yearTag is a release year standing alone in a file name.
var yearTag = regexp.MustCompile(`\b(19|20)\d\d\b`)

// unescapeCopy undoes the escaping of COPY TO STDOUT text format.
func unescapeCopy(s string) string {
	return strings.NewReplacer(`\\`, `\`, `\t`, "\t", `\n`, "\n").Replace(s)
}

// transformCues returns cues c' with c = k*c' + d, so a truth m = s*c + o
// becomes m = s*k*c' + (s*d + o).
func transformCues(cues []subtitles.SubtitleCue, k, d float64) []subtitles.SubtitleCue {
	out := make([]subtitles.SubtitleCue, 0, len(cues))
	for _, c := range cues {
		start := (c.Start.Seconds() - d) / k
		end := (c.End.Seconds() - d) / k
		if start < 0 {
			continue
		}
		out = append(out, subtitles.SubtitleCue{Start: seconds(start), End: seconds(end), Lines: c.Lines})
	}
	return out
}

// buildEvalCases makes the known and nomatch cases from production jobs,
// a shifted case per known job, and a negative per media file.
func buildEvalCases(jobs []*evalJob, speech map[int][]mediasample.SpeechLevels) []evalCase {
	r := rand.New(rand.NewPCG(7, 11))
	var cases []evalCase
	var known []*evalJob
	for _, j := range jobs {
		w := speech[j.file]
		switch j.status {
		case "no_match":
			cases = append(cases, evalCase{kind: "nomatch", job: j, cues: j.cues, windows: w})
		case "synced", "already_synced":
			known = append(known, j)
			cases = append(cases, evalCase{kind: "known", job: j, cues: j.cues, expect: j.truth, windows: w})
		}
	}
	// Shifted: every known pair under a random offset and, for a third, a
	// framerate conversion.
	ratios := []float64{25 / 23.976, 23.976 / 25, 24 / 23.976, 23.976 / 24, 25.0 / 24, 24.0 / 25}
	for _, j := range known {
		k := 1.0
		if r.IntN(3) == 0 {
			k = ratios[r.IntN(len(ratios))]
		}
		d := (r.Float64()*2 - 1) * 120
		s := j.truth.Normalized().Scale
		expect := subtitles.Timing{Scale: s * k, OffsetMS: int(math.Round((s*d + float64(j.truth.OffsetMS)/1000) * 1000))}
		cases = append(cases, evalCase{kind: "shifted", job: j, cues: transformCues(j.cues, k, d), expect: expect, windows: speech[j.file]})
	}
	// Negatives: each file's speech against another file's subtitle,
	// preferably from the same folder (another episode of the season), else
	// the title closest in runtime.
	byDir := map[string][]*evalJob{}
	for _, j := range jobs {
		byDir[filepath.Dir(j.path)] = append(byDir[filepath.Dir(j.path)], j)
	}
	byRuntime := slices.Clone(jobs)
	slices.SortFunc(byRuntime, func(a, b *evalJob) int { return cmp.Compare(a.runtime, b.runtime) })
	seen := map[int]bool{}
	for i, j := range byRuntime {
		if seen[j.file] {
			continue
		}
		seen[j.file] = true
		var other *evalJob
		ep := episodeTag.FindString(strings.ToUpper(filepath.Base(j.path)))
		for _, o := range byDir[filepath.Dir(j.path)] {
			// Another version of the same title is no negative.
			if oep := episodeTag.FindString(strings.ToUpper(filepath.Base(o.path))); o.file != j.file && ep != "" && oep != "" && oep != ep {
				other = o
				break
			}
		}
		if other == nil {
			for step := 1; step < 20 && other == nil; step++ {
				for _, idx := range []int{i + step, i - step} {
					if idx >= 0 && idx < len(byRuntime) && byRuntime[idx].file != j.file &&
						titleOf(byRuntime[idx].path) != titleOf(j.path) {
						other = byRuntime[idx]
						break
					}
				}
			}
		}
		if other == nil {
			continue
		}
		cases = append(cases, evalCase{kind: "negative", job: j, cues: other.cues, windows: speech[j.file], source: other.path})
	}
	return cases
}
