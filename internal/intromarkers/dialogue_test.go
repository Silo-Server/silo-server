package intromarkers

import (
	"context"
	"math"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDialogueBoundaryRefinerMovesStartPastEarlyDialogue(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	segment := Segment{
		Start:      322.014,
		End:        363.465,
		Confidence: 0.85,
		Algorithm:  ChromaprintAlgorithm,
	}
	candidate := Candidate{
		FileID:        636600,
		AudioLanguage: "eng",
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/episode.en.srt", Language: "en", Format: "srt"},
		},
	}
	refiner := &DialogueBoundaryRefiner{
		config: cfg,
		readFile: func(path string) ([]byte, error) {
			return []byte(swatS05E02IntroLeadInSRT), nil
		},
	}

	refined, ok, err := refiner.RefineChromaprintStart(context.Background(), candidate, segment)
	if err != nil {
		t.Fatalf("RefineChromaprintStart returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected dialogue refinement to apply")
	}
	if math.Abs(refined.Start-330.852) > 0.001 {
		t.Fatalf("refined start = %.3f, want 330.852", refined.Start)
	}
	if refined.End != segment.End {
		t.Fatalf("refined end = %.3f, want %.3f", refined.End, segment.End)
	}
	if refined.Algorithm != ChromaprintDialogueAlgorithm {
		t.Fatalf("algorithm = %q, want %q", refined.Algorithm, ChromaprintDialogueAlgorithm)
	}
}

func TestDialogueBoundaryRefinerIgnoresMusicCues(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	segment := Segment{Start: 100, End: 150, Confidence: 0.85, Algorithm: ChromaprintAlgorithm}
	candidate := Candidate{
		FileID:        1,
		AudioLanguage: "en",
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/episode.en.srt", Language: "en", Format: "srt"},
		},
	}
	refiner := &DialogueBoundaryRefiner{
		config: cfg,
		readFile: func(path string) ([]byte, error) {
			return []byte("1\n00:01:40,000 --> 00:01:48,000\n♪ Opening theme ♪\n\n"), nil
		},
	}

	refined, ok, err := refiner.RefineChromaprintStart(context.Background(), candidate, segment)
	if err != nil {
		t.Fatalf("RefineChromaprintStart returned error: %v", err)
	}
	if ok {
		t.Fatalf("music cue should not refine segment, got %+v", refined)
	}
}

func TestDialogueBoundaryRefinerKeepsMinimumRemainingDuration(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	segment := Segment{Start: 100, End: 112, Confidence: 0.85, Algorithm: ChromaprintAlgorithm}
	candidate := Candidate{
		FileID:        1,
		AudioLanguage: "en",
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/episode.en.srt", Language: "en", Format: "srt"},
		},
	}
	refiner := &DialogueBoundaryRefiner{
		config: cfg,
		readFile: func(path string) ([]byte, error) {
			return []byte("1\n00:01:40,000 --> 00:01:45,000\nStill talking.\n\n"), nil
		},
	}

	refined, ok, err := refiner.RefineChromaprintStart(context.Background(), candidate, segment)
	if err != nil {
		t.Fatalf("RefineChromaprintStart returned error: %v", err)
	}
	if ok {
		t.Fatalf("refinement should not leave too-short intro, got %+v", refined)
	}
}

func TestSelectDialogueSubtitlePrefersAudioLanguage(t *testing.T) {
	candidate := Candidate{
		AudioLanguage: "eng",
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/episode.es.srt", Language: "es", Format: "srt"},
			{Path: "/episode.en.srt", Language: "en", Format: "srt"},
		},
	}

	subtitle, ok := selectDialogueSubtitle(candidate)
	if !ok {
		t.Fatal("expected subtitle selection")
	}
	if subtitle.Path != "/episode.en.srt" {
		t.Fatalf("selected %q, want English sidecar", subtitle.Path)
	}
}

const swatS05E02IntroLeadInSRT = `142
00:05:11,006 --> 00:05:13,051
Okay.

143
00:05:13,095 --> 00:05:15,358
I'm in.

144
00:05:15,402 --> 00:05:19,144
Never be in a hurry to die.

145
00:05:19,188 --> 00:05:20,972
Weapons are nice,
but we need intel.

146
00:05:21,016 --> 00:05:22,539
We don't know where Delfina is

147
00:05:22,583 --> 00:05:24,106
or how many people
are holding her.

148
00:05:24,149 --> 00:05:25,499
It won't be easy.

149
00:05:25,542 --> 00:05:27,196
Senor Novak has
friends everywhere.

150
00:05:27,239 --> 00:05:28,806
I got an idea
of where we can start.

151
00:05:28,850 --> 00:05:30,852
Follow me.

152
00:06:20,771 --> 00:06:22,643
Oh, keep screaming, chica.
`
