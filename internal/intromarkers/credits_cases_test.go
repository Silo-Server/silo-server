package intromarkers

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// Replay of the frame-checked validation corpus in testdata/credits_cases.txt.
//
// Each case is one real file whose tail was decoded and whose keyframes were
// classified by hand against frames: the stretch of the tail holding every
// text run, plus the audio match the season comparison found and the silences
// ffmpeg reported. "want" is the credits segment a person confirmed on the
// video, algorithm and confidence included, so a change to the placement rules
// is measured against real files instead of reasoning about the thresholds.

// loadCreditsCases reads the corpus.
func loadCreditsCases(t *testing.T) []creditsCase {
	t.Helper()
	file, err := os.Open("testdata/credits_cases.txt")
	if err != nil {
		t.Fatalf("opening the credits corpus: %v", err)
	}
	defer file.Close()

	var (
		cases    []creditsCase
		current  *creditsCase
		silences []mediasample.Interval
	)
	flush := func() {
		if current == nil {
			return
		}
		current.evidence.Silences = silences
		cases = append(cases, *current)
		current, silences = nil, nil
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "case":
			flush()
			current = &creditsCase{
				name:  fields[1],
				movie: len(fields) > 2 && fields[2] == "movie",
				candidate: Candidate{
					FileID:    1,
					EpisodeID: "ep1",
				},
			}
		case "end":
			flush()
		default:
			if current == nil {
				t.Fatalf("line %q outside a case", line)
			}
			applyCaseLine(t, current, line, fields, &silences)
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the credits corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("the credits corpus is empty")
	}
	return cases
}

// applyCaseLine applies one corpus line to its case.
func applyCaseLine(t *testing.T, c *creditsCase, line string, fields []string, silences *[]mediasample.Interval) {
	t.Helper()
	seconds := func(i int) float64 {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			t.Fatalf("case %s: %q: %v", c.name, line, err)
		}
		return value
	}
	switch fields[0] {
	case "duration":
		c.candidate.DurationSeconds = seconds(1)
	case "window":
		c.window = seconds(1)
	case "intro_end":
		end := seconds(1)
		c.candidate.IntroEnd = &end
	case "audio":
		c.evidence.Audio = &creditsAudio{
			Start:         seconds(1),
			End:           seconds(2),
			Confirmations: int(seconds(3)),
			Confidence:    seconds(4),
		}
		if c.evidence.Audio.Confirmations >= creditsMinimumConfirmations && c.evidence.Audio.Confidence == 0 {
			t.Fatalf("case %s: a strong audio match needs its confidence", c.name)
		}
	case "silence":
		*silences = append(*silences, mediasample.Interval{Start: seconds(1), End: seconds(2)})
	case "want":
		c.want = &Segment{}
		if fields[1] == "none" {
			c.want = nil
			c.wantNone = true
			return
		}
		c.want.Start, c.want.End = seconds(1), seconds(2)
		c.want.Algorithm = fields[3]
		c.want.Confidence = seconds(4)
	case "keyframes":
		parsed, err := parseKeyframes(strings.Join(fields[1:], " "))
		if err != nil {
			t.Fatalf("case %s: %v", c.name, err)
		}
		c.evidence.Keyframes = parsed
	default:
		t.Fatalf("case %s: unknown line %q", c.name, line)
	}
}

func TestCreditsCasesFromValidation(t *testing.T) {
	for _, c := range loadCreditsCases(t) {
		t.Run(c.name, func(t *testing.T) {
			limits := creditsLimitsFor(c.movie)
			got, ok := combineCredits(c.candidate, c.evidence, limits)
			switch {
			case c.wantNone:
				if ok {
					t.Fatalf("placed %s, want none", describe(got))
				}
			case !ok:
				t.Fatalf("placed nothing, want %s %s %.2f", segmentRange(c.want.Start, c.want.End), c.want.Algorithm, c.want.Confidence)
			case got.Algorithm != c.want.Algorithm ||
				absFloat(got.Start-c.want.Start) > 0.01 ||
				absFloat(got.End-c.want.End) > 0.01 ||
				absFloat(got.Confidence-c.want.Confidence) > 0.001:
				t.Fatalf("placed %s, want %s %s %.2f", describe(got), segmentRange(c.want.Start, c.want.End), c.want.Algorithm, c.want.Confidence)
			}
		})
	}
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
