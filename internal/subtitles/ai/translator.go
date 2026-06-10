package ai

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	aitranslate "github.com/Silo-Server/silo-server/internal/ai/translate"
)

// LLMTranslator translates subtitle cues with an OpenAI-compatible chat model
// via the shared aitranslate batch protocol. Only the text is sent to the
// model — timestamps never leave the server — so timing alignment is
// structurally guaranteed. A few preceding source cues are included as
// untranslated context so the model can keep scene continuity across batch
// boundaries.
type LLMTranslator struct {
	client           *llm.Client
	batchSize        int
	contextNeighbors int
}

// NewLLMTranslator builds a translator. batchSize and contextNeighbors fall
// back to sane defaults when non-positive.
func NewLLMTranslator(client *llm.Client, batchSize, contextNeighbors int) *LLMTranslator {
	if batchSize <= 0 {
		batchSize = 40
	}
	if contextNeighbors < 0 {
		contextNeighbors = 0
	}
	return &LLMTranslator{
		client:           client,
		batchSize:        batchSize,
		contextNeighbors: contextNeighbors,
	}
}

// Translate implements Translator.
func (t *LLMTranslator) Translate(ctx context.Context, req TranslateRequest, onBatch func(batch []SubtitleCue, done, total int)) ([]SubtitleCue, error) {
	if t.client == nil {
		return nil, fmt.Errorf("translator client is nil")
	}
	if strings.TrimSpace(req.TargetLanguage) == "" {
		return nil, fmt.Errorf("target language is required")
	}
	total := len(req.Cues)
	if total == 0 {
		return nil, fmt.Errorf("no cues to translate")
	}

	// Preserve timing by copying input cues and only replacing Lines.
	out := make([]SubtitleCue, total)
	copy(out, req.Cues)

	segments := make([]aitranslate.Segment, total)
	for i, c := range req.Cues {
		segments[i] = aitranslate.Segment{ID: strconv.Itoa(i + 1), Text: strings.Join(c.Lines, "\n")}
	}

	srcName := aitranslate.LanguageDisplayName(req.SourceLanguage)
	tgtName := aitranslate.LanguageDisplayName(req.TargetLanguage)

	chat := func(ctx context.Context, system, user string) (string, error) {
		messages := []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		}
		return t.client.Chat(ctx, messages, true)
	}

	translated, err := aitranslate.Translate(ctx, chat, aitranslate.Request{
		Segments:         segments,
		SystemPrompt:     translationSystemPrompt(srcName, tgtName),
		TargetName:       tgtName,
		EntryNoun:        "cues",
		BatchSize:        t.batchSize,
		ContextNeighbors: t.contextNeighbors,
	}, func(batch []aitranslate.Segment, done, totalSegs int) {
		// Batches are sequential ranges, so this batch covers [done-len, done).
		start := done - len(batch)
		for i, seg := range batch {
			out[start+i].Lines = splitCueLines(seg.Text)
		}
		if onBatch != nil {
			onBatch(out[start:done], done, totalSegs)
		}
	})
	if err != nil {
		return nil, err
	}

	// Map the full result too: covers callers without an onBatch callback.
	for i, seg := range translated {
		out[i].Lines = splitCueLines(seg.Text)
	}
	return out, nil
}

func translationSystemPrompt(srcName, tgtName string) string {
	src := srcName
	if src == "" {
		src = "the source language"
	}
	return fmt.Sprintf(
		"You are a professional subtitle translator. Translate subtitle cues from %s into %s. "+
			"Produce natural, idiomatic %s that preserves meaning, tone, register, and proper nouns. "+
			"You receive a JSON object whose keys are cue numbers and whose values are the source text. "+
			"Respond with ONLY a JSON object using the exact same keys, where each value is the translation of that cue. "+
			"Preserve line breaks within a cue as \\n. Do not add, remove, merge, split, reorder, or renumber cues, "+
			"and do not output anything except the JSON object.",
		src, tgtName, tgtName,
	)
}

func splitCueLines(v string) []string {
	lines := strings.Split(v, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
