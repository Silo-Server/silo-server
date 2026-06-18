package ai

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// fakeASRClient transcribes by returning canned segments per chunk filename.
type fakeASRClient struct {
	perChunk map[string][]llm.TranscriptionSegment
	language string
	requests []llm.TranscribeRequest
	err      error
}

func (f *fakeASRClient) Transcribe(_ context.Context, req llm.TranscribeRequest) (*llm.Transcription, error) {
	f.requests = append(f.requests, llm.TranscribeRequest{
		Filename: req.Filename, Language: req.Language, Timeout: req.Timeout,
	})
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Transcription{Language: f.language, Segments: f.perChunk[req.Filename]}, nil
}

// stubExtract writes fake chunk files into dir (one per start offset) and
// records dir for cleanup assertions.
func stubExtract(starts []float64, recordDir *string) func(context.Context, string, int, string, string, int) ([]playback.AudioChunk, error) {
	return func(_ context.Context, _ string, _ int, dir, _ string, _ int) ([]playback.AudioChunk, error) {
		*recordDir = dir
		var chunks []playback.AudioChunk
		for i, start := range starts {
			p := filepath.Join(dir, fmt.Sprintf("chunk%05d.wav", i))
			if err := os.WriteFile(p, []byte("RIFF"), 0o644); err != nil {
				return nil, err
			}
			chunks = append(chunks, playback.AudioChunk{Path: p, Start: start})
		}
		return chunks, nil
	}
}

func evenStarts(n int) []float64 {
	starts := make([]float64, n)
	for i := range starts {
		starts[i] = float64(i * 600)
	}
	return starts
}

func newTestTranscriber(client *fakeASRClient, chunks int, recordDir *string) *WhisperTranscriber {
	tr := &WhisperTranscriber{
		client:      client,
		extract:     stubExtract(evenStarts(chunks), recordDir),
		probeOffset: func(context.Context, string, int, string) float64 { return 0 },
	}
	tr.SetExtraction("", 600)
	return tr
}

func TestTranscribeOffsetsTimestampsByChunkStart(t *testing.T) {
	var dir string
	client := &fakeASRClient{
		language: "english",
		perChunk: map[string][]llm.TranscriptionSegment{
			"chunk00000.wav": {{Start: 1, End: 3, Text: " hello"}},
			"chunk00001.wav": {{Start: 2, End: 4, Text: " world"}},
		},
	}
	tr := newTestTranscriber(client, 2, &dir)

	cues, lang, err := tr.Transcribe(context.Background(), TranscribeJobRequest{FilePath: "/x.mkv"}, nil)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if lang != "en" {
		t.Errorf("detected language = %q, want en (normalized from %q)", lang, client.language)
	}
	if len(cues) != 2 {
		t.Fatalf("cues = %d, want 2", len(cues))
	}
	// Chunk 1 segment offset by 600s.
	if cues[1].Start != 602*time.Second || cues[1].End != 604*time.Second {
		t.Errorf("offset cue = %v–%v, want 602s–604s", cues[1].Start, cues[1].End)
	}
	if strings.Join(cues[1].Lines, " ") != "world" {
		t.Errorf("cue text = %q", cues[1].Lines)
	}
	if dir == "" {
		t.Fatal("extract dir not recorded")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("temp dir %s not cleaned up", dir)
	}
}

func TestTranscribeProcessesChunksPlayheadFirst(t *testing.T) {
	var dir string
	client := &fakeASRClient{
		language: "ja",
		perChunk: map[string][]llm.TranscriptionSegment{
			"chunk00000.wav": {{Start: 0, End: 1, Text: "a"}},
			"chunk00001.wav": {{Start: 0, End: 1, Text: "b"}},
			"chunk00002.wav": {{Start: 0, End: 1, Text: "c"}},
		},
	}
	tr := newTestTranscriber(client, 3, &dir)

	var chunkOrder []string
	cues, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{
		FilePath: "/x.mkv", StartPosition: 1300, // inside chunk 2
	}, func(chunk []SubtitleCue, _ string, done, total int) {
		if total != 3 {
			t.Errorf("total = %d, want 3", total)
		}
		chunkOrder = append(chunkOrder, strings.Join(chunk[0].Lines, ""))
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got := strings.Join(chunkOrder, ""); got != "cab" {
		t.Errorf("chunk processing order = %q, want cab (playhead-first, wrapping)", got)
	}
	if len(cues) != 3 {
		t.Errorf("cues = %d, want 3", len(cues))
	}
}

func TestTranscribePassesHintAndChunkSizedTimeout(t *testing.T) {
	var dir string
	client := &fakeASRClient{
		language: "fr",
		perChunk: map[string][]llm.TranscriptionSegment{
			"chunk00000.wav": {{Start: 0, End: 1, Text: "bonjour"}},
		},
	}
	tr := newTestTranscriber(client, 1, &dir)

	_, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{FilePath: "/x.mkv", LanguageHint: "fr"}, nil)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	req := client.requests[0]
	if req.Language != "fr" {
		t.Errorf("hint = %q, want fr", req.Language)
	}
	if want := 1800 * time.Second; req.Timeout != want {
		t.Errorf("timeout = %v, want %v (3x chunk duration)", req.Timeout, want)
	}
}

func TestSetExtractionClampsChunkSecondsToNearestBound(t *testing.T) {
	tr := &WhisperTranscriber{}

	tr.SetExtraction("", 1)
	if got := int(tr.chunkSeconds.Load()); got != minASRChunkSeconds {
		t.Fatalf("low clamp = %d, want %d", got, minASRChunkSeconds)
	}

	tr.SetExtraction("", defaultASRChunkSeconds+1)
	if got := int(tr.chunkSeconds.Load()); got != defaultASRChunkSeconds {
		t.Fatalf("high clamp = %d, want %d", got, defaultASRChunkSeconds)
	}

	tr.SetExtraction("", 45)
	if got := int(tr.chunkSeconds.Load()); got != 45 {
		t.Fatalf("in-range chunk seconds = %d, want 45", got)
	}
}

func TestTranscribeUsesRequestChunkSecondsOverride(t *testing.T) {
	var dir string
	var extractedChunkSeconds int
	client := &fakeASRClient{
		language: "en",
		perChunk: map[string][]llm.TranscriptionSegment{
			"chunk00000.wav": {{Start: 0, End: 1, Text: "hi"}},
		},
	}
	tr := &WhisperTranscriber{
		client: client,
		extract: func(_ context.Context, _ string, _ int, d, _ string, chunkSeconds int) ([]playback.AudioChunk, error) {
			dir = d
			extractedChunkSeconds = chunkSeconds
			p := filepath.Join(d, "chunk00000.wav")
			if err := os.WriteFile(p, []byte("RIFF"), 0o644); err != nil {
				return nil, err
			}
			return []playback.AudioChunk{{Path: p, Start: 0}}, nil
		},
		probeOffset: func(context.Context, string, int, string) float64 { return 0 },
	}
	tr.SetExtraction("", 600)

	_, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{
		FilePath:     "/x.mkv",
		ChunkSeconds: 10,
	}, nil)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if extractedChunkSeconds != minASRChunkSeconds {
		t.Fatalf("extract chunk seconds = %d, want clamped override %d", extractedChunkSeconds, minASRChunkSeconds)
	}
	if want := time.Duration(minASRChunkSeconds*asrChunkTimeoutFactor) * time.Second; client.requests[0].Timeout != want {
		t.Fatalf("timeout = %v, want %v", client.requests[0].Timeout, want)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("temp dir %s not cleaned up", dir)
	}
}

func TestTranscribeAllSilentChunksFailsClearly(t *testing.T) {
	var dir string
	client := &fakeASRClient{language: "en", perChunk: map[string][]llm.TranscriptionSegment{}}
	tr := newTestTranscriber(client, 2, &dir)

	_, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{FilePath: "/x.mkv"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no speech") {
		t.Fatalf("err = %v, want no-speech failure", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("temp dir %s not cleaned up on failure", dir)
	}
}

func TestWrapCueText(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"short line", []string{"short line"}},
		{"", nil},
		{
			"this sentence is long enough that it needs to wrap onto a second line",
			[]string{"this sentence is long enough that it", "needs to wrap onto a second line"},
		},
	}
	for _, c := range cases {
		got := wrapCueText(c.text, 42, 2)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("wrapCueText(%q) = %q, want %q", c.text, got, c.want)
		}
	}

	// Overflow beyond two lines is absorbed, never dropped.
	long := strings.Repeat("word ", 40)
	got := wrapCueText(strings.TrimSpace(long), 42, 2)
	if len(got) != 2 {
		t.Fatalf("lines = %d, want 2", len(got))
	}
	if joined := strings.Join(got, " "); strings.Count(joined, "word") != 40 {
		t.Errorf("overflow dropped words: %d/40", strings.Count(joined, "word"))
	}
}

func TestCuesFromSegmentsCapsWordlessSegmentDuration(t *testing.T) {
	// Without word timings, Whisper segment ends stretch wall-to-wall across
	// silence (a 0.8s line reported as 30s); cap rather than linger.
	cues := cuesFromSegments([]llm.TranscriptionSegment{
		{Start: 10, End: 40, Text: "Days like today."},
	}, 0)
	if len(cues) != 1 {
		t.Fatalf("cues = %d, want 1", len(cues))
	}
	if want := time.Duration(maxCueSeconds * float64(time.Second)); cues[0].End-cues[0].Start != want {
		t.Errorf("capped duration = %v, want %v", cues[0].End-cues[0].Start, want)
	}
}

func wordSeq(startAt, dur, gap float64, words ...string) []llm.TranscriptionWord {
	out := make([]llm.TranscriptionWord, 0, len(words))
	at := startAt
	for _, w := range words {
		out = append(out, llm.TranscriptionWord{Start: at, End: at + dur, Text: " " + w})
		at += dur + gap
	}
	return out
}

func TestCuesFromWordsEndsCueWhenSpeechStops(t *testing.T) {
	// One segment whose reported end (60s) is far past the last word (12.4s):
	// the cue must end at the words, not the segment.
	words := wordSeq(10, 0.4, 0.1, "Are", "you", "okay?")
	cues := cuesFromSegments([]llm.TranscriptionSegment{
		{Start: 10, End: 60, Text: " Are you okay?", Words: words},
	}, 0)
	if len(cues) != 1 {
		t.Fatalf("cues = %d, want 1", len(cues))
	}
	if cues[0].Start != 10*time.Second {
		t.Errorf("start = %v, want 10s", cues[0].Start)
	}
	// Last word ends at 11.3s; the minimum-duration stretch may pad slightly,
	// but nothing close to the segment's reported 60s.
	if cues[0].End > 12*time.Second {
		t.Errorf("end = %v, want ~11.3s (last word), not segment end", cues[0].End)
	}
}

func TestCuesFromWordsSplitsAtPause(t *testing.T) {
	words := append(wordSeq(0, 0.4, 0.1, "First", "thought"),
		wordSeq(5, 0.4, 0.1, "second", "thought")...)
	cues := cuesFromWords(words, 0)
	if len(cues) != 2 {
		t.Fatalf("cues = %d, want 2 (split at 4s pause)", len(cues))
	}
	if cues[1].Start != 5*time.Second {
		t.Errorf("second cue start = %v, want 5s", cues[1].Start)
	}
}

func TestCuesFromWordsSplitsParagraphAtSentencesAndCapacity(t *testing.T) {
	// A paragraph-length segment (the "465-char single cue" failure) must
	// split into readable cues. Sentence ends close a cue once a line's worth
	// of text has accumulated; capacity closes one regardless.
	var words []llm.TranscriptionWord
	for i := 0; i < 6; i++ {
		words = append(words, wordSeq(float64(i)*3, 0.3, 0.1,
			"this", "sentence", "keeps", "going", "and", "going", "until", "it", "stops.")...)
	}
	cues := cuesFromWords(words, 0)
	if len(cues) < 4 {
		t.Fatalf("cues = %d, want the paragraph split into several", len(cues))
	}
	maxRunes := cueMaxLineLength * cueMaxLines
	for i, c := range cues {
		text := strings.Join(c.Lines, " ")
		if got := len([]rune(text)); got > maxRunes {
			t.Errorf("cue %d has %d runes, over capacity %d: %q", i, got, maxRunes, text)
		}
		if dur := c.End - c.Start; dur > time.Duration(maxCueSeconds*float64(time.Second))+time.Second {
			t.Errorf("cue %d duration %v exceeds max", i, dur)
		}
	}
}

func TestEnforceMinCueDurationsClampsToNextCue(t *testing.T) {
	cues := []SubtitleCue{
		{Start: 0, End: 200 * time.Millisecond, Lines: []string{"What?"}},
		{Start: 600 * time.Millisecond, End: 2 * time.Second, Lines: []string{"LSD."}},
		{Start: 3 * time.Second, End: 3200 * time.Millisecond, Lines: []string{"Oh."}},
	}
	enforceMinCueDurations(cues)
	if cues[0].End != 600*time.Millisecond {
		t.Errorf("cue 0 end = %v, want clamped to next cue start (600ms)", cues[0].End)
	}
	if cues[1].End != 2*time.Second {
		t.Errorf("cue 1 end = %v, want unchanged", cues[1].End)
	}
	if cues[2].End != 4*time.Second {
		t.Errorf("cue 2 end = %v, want stretched to 1s minimum", cues[2].End)
	}
}

func TestCuesFromSegmentsGuardsDegenerateTimes(t *testing.T) {
	cues := cuesFromSegments([]llm.TranscriptionSegment{
		{Start: 5, End: 5, Text: "zero duration"},
		{Start: 1, End: 2, Text: "   "},
	}, 0)
	if len(cues) != 1 {
		t.Fatalf("cues = %d, want 1 (whitespace dropped)", len(cues))
	}
	if cues[0].End <= cues[0].Start {
		t.Errorf("degenerate cue not given a minimum duration: %v–%v", cues[0].Start, cues[0].End)
	}
}

func TestChunkOrderForPosition(t *testing.T) {
	chunks := []playback.AudioChunk{{Start: 0}, {Start: 600.06}, {Start: 1200.13}, {Start: 1800.2}}
	if got := fmt.Sprint(chunkOrderForPosition(chunks, 0)); got != "[0 1 2 3]" {
		t.Errorf("no playhead: %s", got)
	}
	if got := fmt.Sprint(chunkOrderForPosition(chunks, 1900)); got != "[3 0 1 2]" {
		t.Errorf("late playhead: %s", got)
	}
	if got := fmt.Sprint(chunkOrderForPosition(chunks, 99999)); got != "[3 0 1 2]" {
		t.Errorf("beyond-last playhead starts at the final chunk: %s", got)
	}
}

func TestTranscribeIncrementalStreamsPlayheadFirstAcrossTwoPasses(t *testing.T) {
	var trace []string
	var starts []float64
	client := &fakeASRClient{
		language: "en",
		perChunk: map[string][]llm.TranscriptionSegment{
			"inc1200.wav": {{Start: 0, End: 1, Text: "pivot"}},
			"inc1800.wav": {{Start: 0, End: 1, Text: "tail"}},
			"inc0.wav":    {{Start: 0, End: 1, Text: "head"}},
			"inc600.wav":  {{Start: 0, End: 1, Text: "middle"}},
		},
	}
	tr := &WhisperTranscriber{
		client: client,
		incrementalExtract: func(_ context.Context, _ string, _ int, dir, _ string, startSec float64, _ int,
			onSegment func(playback.AudioChunk) error) error {
			starts = append(starts, startSec)
			segmentStarts := []float64{1200, 1800}
			if startSec == 0 {
				segmentStarts = []float64{0, 600, 1200}
			}
			for _, segStart := range segmentStarts {
				name := fmt.Sprintf("inc%.0f.wav", segStart)
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, []byte("RIFF"), 0o644); err != nil {
					return err
				}
				trace = append(trace, fmt.Sprintf("segment:%.0f", segStart))
				if err := onSegment(playback.AudioChunk{Path: p, Start: segStart}); err != nil {
					return err
				}
				trace = append(trace, fmt.Sprintf("after:%.0f", segStart))
			}
			return nil
		},
		probeOffset: func(context.Context, string, int, string) float64 { return 0 },
	}
	tr.SetExtraction("", 600)

	var progress []string
	cues, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{
		FilePath:        "/x.mkv",
		StartPosition:   1300,
		DurationSeconds: 2400,
		Incremental:     true,
	}, func(chunk []SubtitleCue, _ string, done, total int) {
		progress = append(progress, fmt.Sprintf("%d/%d", done, total))
		trace = append(trace, "chunk:"+strings.Join(chunk[0].Lines, " "))
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got := fmt.Sprint(starts); got != "[1200 0]" {
		t.Fatalf("incremental pass starts = %s, want [1200 0]", got)
	}
	var requestOrder []string
	for _, req := range client.requests {
		requestOrder = append(requestOrder, req.Filename)
	}
	if got := strings.Join(requestOrder, ","); got != "inc1200.wav,inc1800.wav,inc0.wav,inc600.wav" {
		t.Fatalf("request order = %s", got)
	}
	if got := strings.Join(progress, ","); got != "1/4,2/4,3/4,4/4" {
		t.Fatalf("progress = %s, want chunk totals", got)
	}
	if chunkIdx, nextSegmentIdx := traceIndexString(trace, "chunk:pivot"), traceIndexString(trace, "segment:1800"); chunkIdx < 0 || nextSegmentIdx < 0 || chunkIdx > nextSegmentIdx {
		t.Fatalf("first onChunk did not fire before the next extracted segment: %v", trace)
	}
	if len(cues) != 4 {
		t.Fatalf("returned cues = %d, want complete transcript", len(cues))
	}
}

func TestTranscribeIncrementalFallsBackToStartWhenSeekedPassProducesNoAudio(t *testing.T) {
	client := &fakeASRClient{
		language: "en",
		perChunk: map[string][]llm.TranscriptionSegment{
			"head.wav": {{Start: 1, End: 2, Text: "head"}},
		},
	}
	tr := &WhisperTranscriber{
		client: client,
		incrementalExtract: func(_ context.Context, _ string, _ int, dir, _ string, startSec float64, _ int,
			onSegment func(playback.AudioChunk) error) error {
			if startSec > 0 {
				return fmt.Errorf("ffmpeg produced no audio chunks for track 0")
			}
			p := filepath.Join(dir, "head.wav")
			if err := os.WriteFile(p, []byte("RIFF"), 0o644); err != nil {
				return err
			}
			return onSegment(playback.AudioChunk{Path: p, Start: 0})
		},
		probeOffset: func(context.Context, string, int, string) float64 { return 0 },
	}
	tr.SetExtraction("", 600)

	cues, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{
		FilePath:        "/x.mkv",
		StartPosition:   1300,
		DurationSeconds: 1800,
		Incremental:     true,
	}, nil)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if len(cues) != 1 || strings.Join(cues[0].Lines, " ") != "head" {
		t.Fatalf("fallback cues = %#v, want head cue from start pass", cues)
	}
	if len(client.requests) != 1 || client.requests[0].Filename != "head.wav" {
		t.Fatalf("ASR requests = %#v, want only fallback chunk", client.requests)
	}
}

func TestTranscribeIncrementalDoesNotDoubleApplyAudioOffsetAfterSeek(t *testing.T) {
	client := &fakeASRClient{
		language: "en",
		perChunk: map[string][]llm.TranscriptionSegment{
			"seek.wav": {{Start: 1, End: 2, Text: "seek"}},
			"head.wav": {{Start: 1, End: 2, Text: "head"}},
		},
	}
	tr := &WhisperTranscriber{
		client: client,
		incrementalExtract: func(_ context.Context, _ string, _ int, dir, _ string, startSec float64, _ int,
			onSegment func(playback.AudioChunk) error) error {
			name := "seek.wav"
			chunkStart := 600.0
			if startSec == 0 {
				name = "head.wav"
				chunkStart = 0
			}
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte("RIFF"), 0o644); err != nil {
				return err
			}
			if err := onSegment(playback.AudioChunk{Path: p, Start: chunkStart}); err != nil {
				return err
			}
			if startSec == 0 {
				stop := filepath.Join(dir, "seek.wav")
				if err := os.WriteFile(stop, []byte("RIFF"), 0o644); err != nil {
					return err
				}
				return onSegment(playback.AudioChunk{Path: stop, Start: 600})
			}
			return nil
		},
		probeOffset: func(context.Context, string, int, string) float64 { return 1.25 },
	}
	tr.SetExtraction("", 600)

	cues, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{
		FilePath:        "/x.mkv",
		StartPosition:   600,
		DurationSeconds: 1200,
		Incremental:     true,
	}, nil)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("cues = %d, want seek and head", len(cues))
	}
	if want := 601 * time.Second; cues[0].Start != want {
		t.Fatalf("seek cue start = %v, want %v without re-adding audio offset", cues[0].Start, want)
	}
	if want := time.Duration(2.25 * float64(time.Second)); cues[1].Start != want {
		t.Fatalf("head cue start = %v, want %v with audio offset", cues[1].Start, want)
	}
}

func TestTranscribeIncrementalKeepsProgressIndeterminateWhenDurationUnknown(t *testing.T) {
	client := &fakeASRClient{
		language: "en",
		perChunk: map[string][]llm.TranscriptionSegment{
			"head.wav": {{Start: 0, End: 1, Text: "head"}},
		},
	}
	tr := &WhisperTranscriber{
		client: client,
		incrementalExtract: func(_ context.Context, _ string, _ int, dir, _ string, _ float64, _ int,
			onSegment func(playback.AudioChunk) error) error {
			p := filepath.Join(dir, "head.wav")
			if err := os.WriteFile(p, []byte("RIFF"), 0o644); err != nil {
				return err
			}
			return onSegment(playback.AudioChunk{Path: p, Start: 0})
		},
		probeOffset: func(context.Context, string, int, string) float64 { return 0 },
	}
	tr.SetExtraction("", 600)

	var gotDone, gotTotal int
	_, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{
		FilePath:    "/x.mkv",
		Incremental: true,
	}, func(_ []SubtitleCue, _ string, done, total int) {
		gotDone = done
		gotTotal = total
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if gotDone != 1 || gotTotal != 0 {
		t.Fatalf("progress = %d/%d, want 1/0 for indeterminate total", gotDone, gotTotal)
	}
}

// Cue timing must use the muxer-reported chunk start (not index*chunkSeconds)
// plus the probed audio start offset, or sync drifts on long files and
// delayed-audio containers.
func TestTranscribeUsesExactChunkStartsAndAudioOffset(t *testing.T) {
	var dir string
	client := &fakeASRClient{
		language: "en",
		perChunk: map[string][]llm.TranscriptionSegment{
			"chunk00000.wav": {{Start: 1, End: 2, Text: "first"}},
			"chunk00001.wav": {{Start: 1, End: 2, Text: "second"}},
		},
	}
	tr := &WhisperTranscriber{
		client: client,
		// The second chunk really starts at 600.5s, not 600s.
		extract:     stubExtract([]float64{0, 600.5}, &dir),
		probeOffset: func(context.Context, string, int, string) float64 { return 1.25 },
	}
	tr.SetExtraction("", 600)

	cues, _, err := tr.Transcribe(context.Background(), TranscribeJobRequest{FilePath: "/x.mkv"}, nil)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if want := time.Duration(2.25 * float64(time.Second)); cues[0].Start != want {
		t.Errorf("cue 0 start = %v, want %v (1s segment + 1.25s audio offset)", cues[0].Start, want)
	}
	if want := time.Duration(602.75 * float64(time.Second)); cues[1].Start != want {
		t.Errorf("cue 1 start = %v, want %v (600.5s chunk + 1s segment + 1.25s offset)", cues[1].Start, want)
	}
}

func traceIndexString(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

func TestNormalizeDetectedLanguage(t *testing.T) {
	cases := map[string]string{
		"english":  "en",
		"en":       "en",
		"eng":      "en",
		"JAPANESE": "ja",
		"":         "",
		"klingon":  "",
	}
	for in, want := range cases {
		if got := normalizeDetectedLanguage(in); got != want {
			t.Errorf("normalizeDetectedLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
