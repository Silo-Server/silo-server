package intromarkers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPioneerComparison measures the intro comparison on the six Pioneer One
// episodes Blender released, against the reference times the reference engine's
// demo data records for them. It runs only when PIONEER_DIR points at the
// episodes, because the media is a 3 GB download from archive.org and does not
// belong in the repository:
//
//	PIONEER_DIR=/media/pioneer go test ./internal/intromarkers/ -run Pioneer -v
//
// The reference engine (tikkamasalla/introkiller-engine) documents the same
// episodes as its worked example, so these are the numbers to compare an old
// path against a new one on real, lossy, broadcast encodes.
func TestPioneerComparison(t *testing.T) {
	dir := os.Getenv("PIONEER_DIR")
	if dir == "" {
		t.Skip("PIONEER_DIR is not set")
	}
	reference := map[int][2]float64{3: {270.02, 299.17}, 4: {280.41, 299.98}, 5: {187.56, 220.89}, 6: {232.98, 266.60}}
	candidates := pioneerCandidates(t, dir)
	ffmpeg := os.Getenv("FFMPEG_PATH")
	if ffmpeg == "" {
		ffmpeg = "/usr/local/bin/ffmpeg"
	}
	cfg := DefaultConfig(ffmpeg)
	ctx := context.Background()

	t.Run("envelope", func(t *testing.T) {
		ex := NewChromaprintExtractor(cfg)
		var inputs []levelsInput
		for _, candidate := range candidates {
			start := time.Now()
			input, ok, err := ex.ExtractLevels(ctx, candidate)
			if err != nil || !ok {
				t.Fatalf("levels for %d: %v", candidate.FileID, err)
			}
			inputs = append(inputs, input)
			t.Logf("episode %s levels in %.1fs, %d frames", candidate.EpisodeID, time.Since(start).Seconds(), len(input.Levels))
		}
		byID := map[int]levelsInput{}
		for _, input := range inputs {
			byID[input.Candidate.FileID] = input
		}
		segments := CompareLevels(inputs, cfg)
		for id, segment := range segments {
			segments[id] = refineIntroSegment(byID[id], segment, cfg)
		}
		report(t, candidates, segments, reference)
	})

}

// report prints each file's placed intro next to the reference, so a run reads
// as a table of errors rather than a pass or a fail.
func report(t *testing.T, candidates []Candidate, segments map[int]Segment, reference map[int][2]float64) {
	t.Helper()
	for _, candidate := range candidates {
		episode, err := strconv.Atoi(strings.TrimPrefix(candidate.EpisodeID, "e"))
		if err != nil {
			t.Fatalf("episode id %q", candidate.EpisodeID)
		}
		segment, found := segments[candidate.FileID]
		want, known := reference[episode]
		if !found {
			t.Logf("episode %d: no intro (reference %v)", episode, want)
			continue
		}
		line := fmt.Sprintf("episode %d: %.2f-%.2f", episode, segment.Start, segment.End)
		if known {
			startErr := segment.Start - want[0]
			endErr := segment.End - want[1]
			line += fmt.Sprintf(" (reference %.2f-%.2f, start %+.2f, end %+.2f)", want[0], want[1], startErr, endErr)
		}
		t.Logf("%s", line)
	}
}

func pioneerCandidates(t *testing.T, dir string) []Candidate {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".mp4") && !strings.Contains(entry.Name(), "part") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	var candidates []Candidate
	for _, name := range names {
		episode := strings.TrimSuffix(strings.TrimPrefix(name, "Pioneer One E"), ".mp4")
		number, err := strconv.Atoi(episode)
		if err != nil {
			continue
		}
		path := filepath.Join(dir, name)
		duration := probeTestDuration(t, path)
		candidates = append(candidates, Candidate{
			FileID:          number,
			EpisodeID:       fmt.Sprintf("e%02d", number),
			EpisodeNumber:   number,
			SeasonNumber:    1,
			SeasonID:        "pioneer-one",
			FilePath:        path,
			DurationSeconds: duration,
		})
	}
	if len(candidates) < 3 {
		t.Fatalf("found %d episodes in %s", len(candidates), dir)
	}
	return candidates
}
