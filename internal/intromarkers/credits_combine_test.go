package intromarkers

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

var fixtureClasses = map[byte]keyframeClass{'c': keyframeContent, 'b': keyframeBlack, 'L': keyframeLettered, 'C': keyframeCard, 'm': keyframeMostlyBlack}

// parseKeyframes reads space-separated "<seconds><class>" tokens such as
// "2599.472L".
func parseKeyframes(spec string) ([]creditsKeyframe, error) {
	var out []creditsKeyframe
	for _, token := range strings.Fields(spec) {
		class, ok := fixtureClasses[token[len(token)-1]]
		if !ok {
			return nil, fmt.Errorf("keyframe %q has no class", token)
		}
		seconds, err := strconv.ParseFloat(token[:len(token)-1], 64)
		if err != nil {
			return nil, fmt.Errorf("keyframe %q: %w", token, err)
		}
		out = append(out, creditsKeyframe{Seconds: seconds, Class: class})
	}
	return out, nil
}

func formatFixtureSeconds(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}

// creditsCase is one validated file from testdata/credits_cases.txt.
type creditsCase struct {
	name      string
	movie     bool
	candidate Candidate
	window    float64
	evidence  creditsEvidence
	want      *Segment
}

func loadCreditsCases(t *testing.T) []creditsCase {
	t.Helper()
	f, err := os.Open("testdata/credits_cases.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1<<20)
	number := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatalf("bad number %q: %v", s, err)
		}
		return v
	}
	var cases []creditsCase
	var current *creditsCase
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if fields[0] == "case" {
			cases = append(cases, creditsCase{name: fields[1], movie: fields[2] == "movie"})
			current = &cases[len(cases)-1]
			continue
		}
		switch fields[0] {
		case "duration":
			current.candidate.DurationSeconds = number(fields[1])
		case "window":
			current.window = number(fields[1])
		case "intro_end":
			end := number(fields[1])
			current.candidate.IntroEnd = &end
		case "audio":
			current.evidence.Audio = &creditsAudio{
				Start: number(fields[1]), End: number(fields[2]),
				Confirmations: int(number(fields[3])), Confidence: number(fields[4]),
			}
		case "silence":
			current.evidence.Silences = append(current.evidence.Silences, mediasample.Interval{Start: number(fields[1]), End: number(fields[2])})
		case "want":
			if fields[1] != "none" {
				current.want = &Segment{Start: number(fields[1]), End: number(fields[2]), Algorithm: fields[3], Confidence: number(fields[4])}
			}
		case "keyframes":
			if current.evidence.Keyframes, err = parseKeyframes(strings.Join(fields[1:], " ")); err != nil {
				t.Fatal(err)
			}
		case "end":
			current = nil
		default:
			t.Fatalf("unknown fixture line %q", fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

// The validated files where earlier rules cut story, or dropped real
// credits, place credits as frame-checked.
func TestCombineCreditsValidatedCases(t *testing.T) {
	cases := loadCreditsCases(t)
	if len(cases) != 8 {
		t.Fatalf("loaded %d cases, want 8", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limits := creditsLimitsFor(tc.movie)
			if got := limits.windowStart(tc.candidate.DurationSeconds); math.Abs(got-tc.window) > 0.001 {
				t.Fatalf("window starts at %.3f, fixture has %.3f", got, tc.window)
			}
			got, ok := combineCredits(tc.candidate, tc.evidence, limits)
			assertCredits(t, got, ok, tc.want)
		})
	}
}

func assertCredits(t *testing.T, got Segment, ok bool, want *Segment) {
	t.Helper()
	if want == nil {
		if ok {
			t.Fatalf("placed %+v, want no credits", got)
		}
		return
	}
	if !ok {
		t.Fatalf("placed no credits, want %+v", *want)
	}
	if math.Abs(got.Start-want.Start) > 0.001 || math.Abs(got.End-want.End) > 0.001 ||
		got.Algorithm != want.Algorithm || math.Abs(got.Confidence-want.Confidence) > 1e-9 {
		t.Fatalf("placed %+v, want %+v", got, *want)
	}
}

func TestCombineCreditsRules(t *testing.T) {
	const duration = 1500.0 // tail window from 1050 s
	story := func(end float64) string { return every(1050, end, 1, "c") }
	strong := func(start, end float64) *creditsAudio {
		return &creditsAudio{Start: start, End: end, Confirmations: 3, Confidence: 0.90}
	}
	weak := func(start, end float64) *creditsAudio {
		return &creditsAudio{Start: start, End: end, Confirmations: 1}
	}
	credits := every(1420, 1495, 1, "L")
	segment := func(start, end float64, algorithm string, confidence float64) *Segment {
		return &Segment{Start: start, End: end, Algorithm: algorithm, Confidence: confidence}
	}
	tests := []struct {
		name     string
		audio    *creditsAudio
		spec     string
		silences []mediasample.Interval
		introEnd float64
		want     *Segment
	}{
		{
			name: "video alone: text on black to the end of the file",
			spec: story(1419) + credits,
			want: segment(1420, duration, CreditsVideoAlgorithm, 0.60),
		},
		{
			name: "video alone: mostly cards rate lower",
			spec: story(1419) + every(1420, 1495, 1, "C"),
			want: segment(1420, duration, CreditsVideoAlgorithm, 0.55),
		},
		{
			name: "video alone: the start moves back over black before the text",
			spec: story(1405) + every(1406, 1419, 1, "b") + credits,
			want: segment(1406, duration, CreditsVideoAlgorithm, 0.60),
		},
		{
			name: "video alone: not over black more than 10 s before",
			spec: story(1400) + " 1408b" + credits,
			want: segment(1420, duration, CreditsVideoAlgorithm, 0.60),
		},
		{
			name:     "video alone: a silence between story and credits marks the cut",
			spec:     story(1400) + credits,
			silences: []mediasample.Interval{{Start: 1405, End: 1415}},
			want:     segment(1415, duration, CreditsVideoAlgorithm, 0.60),
		},
		{
			name:     "video alone: a silence reaching into the story is ignored",
			spec:     story(1400) + credits,
			silences: []mediasample.Interval{{Start: 1398, End: 1410}, {Start: 1412}},
			want:     segment(1420, duration, CreditsVideoAlgorithm, 0.60),
		},
		{
			name: "video alone: the last cluster of runs",
			spec: story(1099) + every(1100, 1130, 1, "L") + every(1131, 1419, 1, "c") + credits,
			want: segment(1420, duration, CreditsVideoAlgorithm, 0.60),
		},
		{
			name: "video alone: credits ending over 120 s before the end are a scene",
			spec: story(1149) + every(1150, 1250, 1, "L") + every(1251, 1499, 1, "c"),
		},
		{
			name:  "strong audio grows over an overlapping run",
			audio: strong(1430, duration),
			spec:  story(1419) + credits,
			want:  segment(1420, duration, CreditsAudioVideoAlgorithm, 0.95),
		},
		{
			name:  "strong audio starts at a later run after only story",
			audio: strong(1400, duration),
			spec:  story(1429) + every(1430, 1495, 1, "L"),
			want:  segment(1430, duration, CreditsAudioVideoAlgorithm, 0.95),
		},
		{
			name:  "strong audio starts at a later run after dark story",
			audio: strong(1400, duration),
			spec:  story(1409) + every(1410, 1429, 1, "m") + every(1430, 1495, 1, "L"),
			want:  segment(1430, duration, CreditsAudioVideoAlgorithm, 0.95),
		},
		{
			name: "video alone: mostly black keyframes do not start the credits",
			spec: story(1405) + every(1406, 1419, 1, "m") + credits,
			want: segment(1420, duration, CreditsVideoAlgorithm, 0.60),
		},
		{
			name:  "strong audio keeps its start when black leads into the run",
			audio: strong(1400, duration),
			spec:  story(1409) + every(1410, 1429, 1, "b") + every(1430, 1495, 1, "L"),
			want:  segment(1400, duration, CreditsAudioAlgorithm, 0.90),
		},
		{
			name:  "strong audio keeps its start when the run begins over 60 s later",
			audio: strong(1360, duration),
			spec:  story(1429) + every(1430, 1495, 1, "L"),
			want:  segment(1360, duration, CreditsAudioAlgorithm, 0.90),
		},
		{
			name:  "strong audio without video must reach the end of the file",
			audio: strong(1300, 1400),
			spec:  story(1499),
		},
		{
			name:  "a later text run to the end beats audio that stops early",
			audio: strong(1200, 1230),
			spec:  story(1419) + credits,
			want:  segment(1420, duration, CreditsVideoAlgorithm, 0.60),
		},
		{
			name:  "credits followed by a scene keep an early end",
			audio: strong(1400, 1440),
			spec:  story(1401) + every(1402, 1442, 1, "L") + every(1443, 1499, 1, "c"),
			want:  segment(1402, 1442, CreditsAudioVideoAlgorithm, 0.95),
		},
		{
			name:  "weak audio alone places nothing",
			audio: weak(1420, duration),
			spec:  story(1499),
		},
		{
			name:  "weak audio with an overlapping run",
			audio: weak(1425, duration),
			spec:  story(1419) + credits,
			want:  segment(1420, duration, CreditsAudioVideoAlgorithm, 0.65),
		},
		{
			name:     "credits cannot start before the intro ends",
			spec:     story(1419) + credits,
			introEnd: 1450,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := Candidate{DurationSeconds: duration}
			if tt.introEnd > 0 {
				candidate.IntroEnd = &tt.introEnd
			}
			evidence := creditsEvidence{Audio: tt.audio, Keyframes: keyframes(t, tt.spec), Silences: tt.silences}
			got, ok := combineCredits(candidate, evidence, creditsLimitsFor(false))
			assertCredits(t, got, ok, tt.want)
		})
	}
}

func TestCreditsAudioFor(t *testing.T) {
	profile := creditsProfile()
	match := func(length float64, confirmations int, consistent bool) seasonMatch {
		return seasonMatch{Segment: Segment{Start: 1000, End: 1000 + length}, Confirmations: confirmations, SeasonConsistent: consistent}
	}
	tests := []struct {
		name         string
		match        seasonMatch
		strong, weak bool
		confidence   float64
	}{
		{"season-consistent", match(60, 2, true), true, false, 0.90},
		{"not season-consistent", match(60, 5, false), true, false, 0.65},
		{"short and season-consistent", match(16, 3, true), true, false, 0.90},
		{"short and not season-consistent", match(16, 3, false), false, false, 0},
		{"one confirmation", match(60, 1, true), false, true, 0},
	}
	for _, tt := range tests {
		audio := creditsAudioFor(tt.match, profile)
		if audio.strong() != tt.strong || audio.weak() != tt.weak || audio.Confidence != tt.confidence {
			t.Errorf("%s: %+v strong=%t weak=%t, want strong=%t weak=%t confidence %.2f",
				tt.name, audio, audio.strong(), audio.weak(), tt.strong, tt.weak, tt.confidence)
		}
	}
}
