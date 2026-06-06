package intromarkers

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type chromaprintStartRefiner interface {
	RefineChromaprintStart(ctx context.Context, candidate Candidate, segment Segment) (Segment, bool, error)
}

type DialogueBoundaryRefiner struct {
	config   Config
	readFile func(string) ([]byte, error)
}

var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)

func NewDialogueBoundaryRefiner(config Config) *DialogueBoundaryRefiner {
	return &DialogueBoundaryRefiner{
		config:   config.normalized(),
		readFile: os.ReadFile,
	}
}

func (r *DialogueBoundaryRefiner) RefineChromaprintStart(ctx context.Context, candidate Candidate, segment Segment) (Segment, bool, error) {
	cfg := r.config.normalized()
	if !cfg.DialogueRefinementEnabled || segment.End <= segment.Start || segment.Algorithm != ChromaprintAlgorithm {
		return segment, false, nil
	}
	if err := ctx.Err(); err != nil {
		return segment, false, err
	}
	subtitle, ok := selectDialogueSubtitle(candidate)
	if !ok {
		return segment, false, nil
	}
	readFile := r.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(subtitle.Path)
	if err != nil {
		return segment, false, fmt.Errorf("read dialogue subtitle %q for file %d: %w", subtitle.Path, candidate.FileID, err)
	}
	cues, err := subtitles.ParseCues(data)
	if err != nil {
		return segment, false, fmt.Errorf("parse dialogue subtitle %q for file %d: %w", subtitle.Path, candidate.FileID, err)
	}
	refinedStart, ok := dialogueRefinedStart(cues, segment, cfg)
	if !ok {
		return segment, false, nil
	}
	refined := segment
	refined.Start = refinedStart
	refined.Algorithm = ChromaprintDialogueAlgorithm
	return refined, true, nil
}

func selectDialogueSubtitle(candidate Candidate) (models.ExternalSubtitle, bool) {
	audioLanguage := lang.Canonical(candidate.AudioLanguage)
	bestScore := -1
	var best models.ExternalSubtitle
	for _, subtitle := range candidate.ExternalSubtitles {
		if strings.TrimSpace(subtitle.Path) == "" ||
			!isDialogueSubtitleFormat(subtitle.Format) ||
			subtitle.Forced ||
			subtitle.HearingImpaired {
			continue
		}
		score := 0
		subtitleLanguage := lang.Canonical(subtitle.Language)
		if audioLanguage != "" && subtitleLanguage == audioLanguage {
			score += 100
		} else if audioLanguage == "" && subtitleLanguage == "en" {
			score += 20
		}
		if subtitle.Default {
			score += 10
		}
		if subtitleLanguage != "" {
			score += 1
		}
		if score > bestScore {
			bestScore = score
			best = subtitle
		}
	}
	if bestScore < 0 {
		return models.ExternalSubtitle{}, false
	}
	return best, true
}

func isDialogueSubtitleFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "srt", "subrip", "vtt", "webvtt":
		return true
	default:
		return false
	}
}

func dialogueRefinedStart(cues []subtitles.SubtitleCue, segment Segment, cfg Config) (float64, bool) {
	if segment.End <= segment.Start {
		return 0, false
	}
	windowEnd := math.Min(segment.Start+cfg.DialogueRefinementWindowSeconds, segment.End)
	if windowEnd <= segment.Start {
		return 0, false
	}

	lastDialogueEnd := segment.Start
	for _, cue := range cues {
		if !cueLooksLikeDialogue(cue) {
			continue
		}
		start := cue.Start.Seconds()
		end := cue.End.Seconds()
		if end <= segment.Start {
			continue
		}
		if start > windowEnd {
			break
		}
		if end > lastDialogueEnd {
			lastDialogueEnd = end
		}
	}

	if lastDialogueEnd <= segment.Start {
		return 0, false
	}
	if lastDialogueEnd-segment.Start > cfg.DialogueRefinementMaxShiftSeconds {
		return 0, false
	}
	if segment.End-lastDialogueEnd < cfg.DialogueRefinementMinimumRemainingSeconds {
		return 0, false
	}
	return lastDialogueEnd, true
}

func cueLooksLikeDialogue(cue subtitles.SubtitleCue) bool {
	for _, line := range cue.Lines {
		if lineLooksLikeDialogue(line) {
			return true
		}
	}
	return false
}

func lineLooksLikeDialogue(line string) bool {
	cleaned := strings.TrimSpace(stripSubtitleDialogueMarkup(line))
	if cleaned == "" || strings.Contains(cleaned, "♪") {
		return false
	}
	lower := strings.ToLower(cleaned)
	if isBracketedCue(lower, "[", "]") ||
		isBracketedCue(lower, "(", ")") ||
		isBracketedCue(lower, "{", "}") {
		return false
	}
	for _, r := range cleaned {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func stripSubtitleDialogueMarkup(line string) string {
	line = stripSubtitleASSTags(line)
	line = htmlTagPattern.ReplaceAllString(line, "")
	return strings.TrimSpace(line)
}

func isBracketedCue(line, open, close string) bool {
	return strings.HasPrefix(line, open) && strings.HasSuffix(line, close)
}

func stripSubtitleASSTags(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	inTag := false
	for _, r := range line {
		switch {
		case r == '{':
			inTag = true
		case r == '}':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}
