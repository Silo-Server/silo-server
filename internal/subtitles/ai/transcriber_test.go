package ai

import (
	"context"
	"errors"
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

func TestTranscribeIncrementalDoesNotRestartAtBeginningAfterProviderFailure(t *testing.T) {
	client := &fakeASRClient{err: context.DeadlineExceeded}
	var starts []float64
	tr := &WhisperTranscriber{
		client: client,
		incrementalExtract: func(_ context.Context, _ string, _ int, dir, _ string, startSec float64, _ int,
			onSegment func(playback.AudioChunk) error) error {
			starts = append(starts, startSec)
			path := filepath.Join(dir, "speech.wav")
			if err := os.WriteFile(path, []byte("RIFF"), 0o600); err != nil {
				return err
			}
			return onSegment(playback.AudioChunk{Path: path, Start: startSec})
		},
		probeOffset: func(context.Context, string, int, string) float64 { return 0 },
	}
	tr.SetExtraction("", 30)
	_, _, err := tr.Transcribe(t.Context(), TranscribeJobRequest{
		FilePath:        "/synthetic.mkv",
		StartPosition:   125,
		DurationSeconds: 600,
		Incremental:     true,
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "at 120.000s") {
		t.Fatalf("error = %v, want original provider failure at the playhead chunk", err)
	}
	if len(starts) != 1 || starts[0] != 120 || len(client.requests) != 1 {
		t.Fatalf("pass starts = %v, ASR calls = %d; provider failure must not restart extraction", starts, len(client.requests))
	}
}
