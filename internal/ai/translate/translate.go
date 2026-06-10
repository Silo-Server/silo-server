// Package aitranslate implements generic batched text translation over an
// OpenAI-compatible chat model. Callers hand it ordered text segments and a
// domain-specific system prompt; it owns the wire protocol (indexed-JSON
// batches the model must echo back key-for-key), context carryover between
// batches, and retries on malformed model output. Subtitle cues and metadata
// descriptions both translate through this one implementation.
package aitranslate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Segment is one unit of translatable text. ID is the caller's identifier and
// is never sent to the model (batches are keyed by position), so any stable
// string works.
type Segment struct {
	ID   string
	Text string
}

// ChatFn performs one chat completion. Wiring this as a function keeps the
// package independent of the client and trivially testable.
type ChatFn func(ctx context.Context, system, user string) (string, error)

// Request describes one translation run.
type Request struct {
	Segments     []Segment
	SystemPrompt string // domain-specific; must demand same-keys JSON output
	// TargetName is the human-readable target language name used in the
	// per-batch user message.
	TargetName string
	// EntryNoun names the units in the user message ("cues", "descriptions");
	// empty defaults to "entries".
	EntryNoun string
	// BatchSize is the number of segments per chat request (required, > 0).
	BatchSize int
	// ContextNeighbors is how many preceding source segments are included,
	// untranslated, for continuity across batch boundaries.
	ContextNeighbors int
}

// maxFormatRetries is how many times a batch is re-asked after the model
// returns unparsable or incomplete JSON. Transport/API errors are retried
// inside the chat client and are not part of this count.
const maxFormatRetries = 2

// Translate translates req.Segments in order, preserving count and IDs and
// only rewriting Text. onBatch, when non-nil, is called after each batch with
// that batch's translated segments and overall progress (done/total
// segments). Implementations must honor ctx cancellation between batches.
func Translate(ctx context.Context, chat ChatFn, req Request, onBatch func(batch []Segment, done, total int)) ([]Segment, error) {
	if chat == nil {
		return nil, fmt.Errorf("chat function is nil")
	}
	if req.BatchSize <= 0 {
		return nil, fmt.Errorf("invalid batch size: %d", req.BatchSize)
	}
	total := len(req.Segments)
	if total == 0 {
		return nil, fmt.Errorf("no segments to translate")
	}
	noun := req.EntryNoun
	if noun == "" {
		noun = "entries"
	}

	// Preserve IDs by copying input segments and only replacing Text.
	out := make([]Segment, total)
	copy(out, req.Segments)

	for start := 0; start < total; start += req.BatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+req.BatchSize, total)

		contextStart := max(0, start-req.ContextNeighbors)
		translated, err := translateBatch(ctx, chat, req, noun, req.Segments[contextStart:start], req.Segments[start:end])
		if err != nil {
			return nil, fmt.Errorf("translate %s %d-%d: %w", noun, start+1, end, err)
		}
		for i, text := range translated {
			out[start+i].Text = text
		}
		if onBatch != nil {
			onBatch(out[start:end], end, total)
		}
	}

	return out, nil
}

func translateBatch(ctx context.Context, chat ChatFn, req Request, noun string, contextSegs, batch []Segment) ([]string, error) {
	texts := make([]string, len(batch))
	for i, s := range batch {
		texts[i] = s.Text
	}
	payload, err := buildIndexedJSON(texts)
	if err != nil {
		return nil, err
	}

	var user strings.Builder
	if len(contextSegs) > 0 {
		user.WriteString("Preceding lines for context only — do not translate or include them in your output:\n")
		for _, s := range contextSegs {
			user.WriteString(strings.ReplaceAll(s.Text, "\n", " "))
			user.WriteByte('\n')
		}
		user.WriteByte('\n')
	}
	target := req.TargetName
	if target == "" {
		target = "the target language"
	}
	fmt.Fprintf(&user, "Translate these %d %s into %s. Respond with only the JSON object:\n%s", len(batch), noun, target, payload)

	var lastErr error
	for attempt := 0; attempt <= maxFormatRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := chat(ctx, req.SystemPrompt, user.String())
		if err != nil {
			return nil, err // transport/API errors are already retried inside the client
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

		out := make([]string, len(batch))
		complete := true
		for i := range batch {
			v, ok := m[strconv.Itoa(i+1)]
			if !ok {
				complete = false
				break
			}
			out[i] = v
		}
		if !complete {
			lastErr = fmt.Errorf("model omitted one or more %s", noun)
			continue
		}
		return out, nil
	}

	return nil, fmt.Errorf("invalid model response after %d attempts: %w", maxFormatRetries+1, lastErr)
}

// buildIndexedJSON renders texts as a JSON object {"1":..., "2":...} keyed by
// 1-based position, escaping each value safely. It is built by hand rather
// than json.Marshal'ing a map so the keys stay in numeric order — that reads
// more naturally for the model than the lexicographic order Go emits for maps
// ("1","10","11",...,"2"). Correctness doesn't depend on order (results are
// mapped back by key), but ordered input gives the model better context.
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

// extractJSONObject pulls the first balanced-looking JSON object out of a
// model reply, tolerating ``` code fences and surrounding prose.
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
