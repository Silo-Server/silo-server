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

// stubExtract writes n fake chunk files into dir and records dir for cleanup
// assertions.
func stubExtract(n int, recordDir *string) func(context.Context, string, int, string, string, int) ([]string, error) {
	return func(_ context.Context, _ string, _ int, dir, _ string, _ int) ([]string, error) {
		*recordDir = dir
		var chunks []string
		for i := 0; i < n; i++ {
			p := filepath.Join(dir, fmt.Sprintf("chunk%05d.wav", i))
			if err := os.WriteFile(p, []byte("RIFF"), 0o644); err != nil {
				return nil, err
			}
			chunks = append(chunks, p)
		}
		return chunks, nil
	}
}

func newTestTranscriber(client *fakeASRClient, chunks int, recordDir *string) *WhisperTranscriber {
	return &WhisperTranscriber{
		client:       client,
		chunkSeconds: 600,
		extract:      stubExtract(chunks, recordDir),
	}
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
	}, func(chunk []SubtitleCue, done, total int) {
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

func TestChunkOrderFromPosition(t *testing.T) {
	if got := fmt.Sprint(chunkOrderFromPosition(4, 0, 600)); got != "[0 1 2 3]" {
		t.Errorf("no playhead: %s", got)
	}
	if got := fmt.Sprint(chunkOrderFromPosition(4, 1900, 600)); got != "[3 0 1 2]" {
		t.Errorf("late playhead: %s", got)
	}
	if got := fmt.Sprint(chunkOrderFromPosition(4, 99999, 600)); got != "[0 1 2 3]" {
		t.Errorf("out-of-range playhead should fall back: %s", got)
	}
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
