package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// LLMTranslator translates subtitle cues with an OpenAI-compatible chat model.
//
// Cues are translated in batches. Each batch is sent as a JSON object keyed by
// cue number; the model must return the same keys with translated values. Only
// the text is sent to the model — timestamps never leave the server — so timing
// alignment is structurally guaranteed. A few preceding source cues are
// included as untranslated context so the model can keep scene continuity
// across batch boundaries.
type LLMTranslator struct {
	client           *Client
	batchSize        int
	contextNeighbors int
	maxRetries       int
}

// NewLLMTranslator builds a translator. batchSize and contextNeighbors fall back
// to sane defaults when non-positive.
func NewLLMTranslator(client *Client, batchSize, contextNeighbors int) *LLMTranslator {
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
		maxRetries:       2,
	}
}

// Translate implements Translator.
func (t *LLMTranslator) Translate(ctx context.Context, req TranslateRequest, onBatch func(batch []SubtitleCue, done, total int)) ([]SubtitleCue, error) {
	total := len(req.Cues)
	if total == 0 {
		return nil, fmt.Errorf("no cues to translate")
	}

	// Preserve timing by copying input cues and only replacing Lines.
	out := make([]SubtitleCue, total)
	copy(out, req.Cues)

	srcName := languageDisplayName(req.SourceLanguage)
	tgtName := languageDisplayName(req.TargetLanguage)
	system := translationSystemPrompt(srcName, tgtName)

	for start := 0; start < total; start += t.batchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+t.batchSize, total)

		contextStart := max(0, start-t.contextNeighbors)
		translated, err := t.translateBatch(ctx, system, tgtName, req.Cues[contextStart:start], req.Cues[start:end])
		if err != nil {
			return nil, fmt.Errorf("translate cues %d-%d: %w", start+1, end, err)
		}
		for i, lines := range translated {
			out[start+i].Lines = lines
		}
		if onBatch != nil {
			onBatch(out[start:end], end, total)
		}
	}

	return out, nil
}

func (t *LLMTranslator) translateBatch(ctx context.Context, system, targetName string, contextCues, batch []SubtitleCue) ([][]string, error) {
	texts := make([]string, len(batch))
	for i, c := range batch {
		texts[i] = strings.Join(c.Lines, "\n")
	}
	payload, err := buildIndexedJSON(texts)
	if err != nil {
		return nil, err
	}

	var user strings.Builder
	if len(contextCues) > 0 {
		user.WriteString("Preceding lines for context only — do not translate or include them in your output:\n")
		for _, c := range contextCues {
			user.WriteString(strings.Join(c.Lines, " "))
			user.WriteByte('\n')
		}
		user.WriteByte('\n')
	}
	fmt.Fprintf(&user, "Translate these %d cues into %s. Respond with only the JSON object:\n%s", len(batch), targetName, payload)

	messages := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user.String()},
	}

	var lastErr error
	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := t.client.chat(ctx, messages, true)
		if err != nil {
			return nil, err // transport/API errors are already retried inside chat
		}

		obj, err := extractJSONObject(content)
		if err != nil {
			lastErr = err
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(obj), &m); err != nil {
			lastErr = fmt.Errorf("decode translation JSON: %w", err)
			continue
		}

		out := make([][]string, len(batch))
		complete := true
		for i := range batch {
			v, ok := m[strconv.Itoa(i+1)]
			if !ok {
				complete = false
				break
			}
			out[i] = splitCueLines(v)
		}
		if !complete {
			lastErr = fmt.Errorf("model omitted one or more cues")
			continue
		}
		return out, nil
	}

	return nil, fmt.Errorf("invalid model response after %d attempts: %w", t.maxRetries+1, lastErr)
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

// buildIndexedJSON renders texts as a JSON object {"1":..., "2":...} keyed by
// 1-based cue number, escaping each value safely. It is built by hand rather
// than json.Marshal'ing a map so the keys stay in numeric order — that reads
// more naturally for the model than the lexicographic order Go emits for maps
// ("1","10","11",...,"2"). Correctness doesn't depend on order (results are
// mapped back by key), but ordered input gives the model better scene context.
func buildIndexedJSON(texts []string) (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, text := range texts {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(strconv.Itoa(i + 1))
		if err != nil {
			return "", err
		}
		val, err := json.Marshal(text)
		if err != nil {
			return "", err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.String(), nil
}

// extractJSONObject pulls the first balanced-looking JSON object out of a model
// reply, tolerating ``` code fences and surrounding prose.
func extractJSONObject(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return "", fmt.Errorf("no JSON object found in model response")
	}
	return s[start : end+1], nil
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
