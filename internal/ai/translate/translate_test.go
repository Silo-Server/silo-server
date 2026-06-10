package aitranslate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// echoChat translates by upper-casing every value of the indexed JSON payload
// in the user message, mimicking a well-behaved model.
func echoChat(t *testing.T) ChatFn {
	t.Helper()
	return func(_ context.Context, _ string, user string) (string, error) {
		obj, err := extractJSONObject(user)
		if err != nil {
			return "", fmt.Errorf("test chat: %w", err)
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(obj), &m); err != nil {
			return "", fmt.Errorf("test chat decode: %w", err)
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = strings.ToUpper(v)
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}
}

func segments(n int) []Segment {
	out := make([]Segment, n)
	for i := range out {
		out[i] = Segment{ID: strconv.Itoa(i + 1), Text: fmt.Sprintf("line %d", i+1)}
	}
	return out
}

func TestTranslateBatchesAndPreservesIDs(t *testing.T) {
	var batches []int
	out, err := Translate(context.Background(), echoChat(t), Request{
		Segments:  segments(7),
		BatchSize: 3,
	}, func(batch []Segment, done, total int) {
		batches = append(batches, len(batch))
		if total != 7 {
			t.Errorf("total = %d, want 7", total)
		}
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(out) != 7 {
		t.Fatalf("len(out) = %d", len(out))
	}
	for i, s := range out {
		if s.ID != strconv.Itoa(i+1) {
			t.Errorf("ID[%d] = %q, want %d", i, s.ID, i+1)
		}
		if want := strings.ToUpper(fmt.Sprintf("line %d", i+1)); s.Text != want {
			t.Errorf("Text[%d] = %q, want %q", i, s.Text, want)
		}
	}
	if fmt.Sprint(batches) != "[3 3 1]" {
		t.Errorf("batch sizes = %v, want [3 3 1]", batches)
	}
}

func TestTranslateSendsContextNeighborsUntranslated(t *testing.T) {
	var secondBatchMsg string
	call := 0
	chat := func(ctx context.Context, system, user string) (string, error) {
		call++
		if call == 2 {
			secondBatchMsg = user
		}
		return echoChat(t)(ctx, system, user)
	}
	_, err := Translate(context.Background(), chat, Request{
		Segments:         segments(4),
		BatchSize:        2,
		ContextNeighbors: 2,
		TargetName:       "French",
		EntryNoun:        "cues",
	}, nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if !strings.Contains(secondBatchMsg, "context only") ||
		!strings.Contains(secondBatchMsg, "line 1\nline 2") {
		t.Errorf("second batch missing context block:\n%s", secondBatchMsg)
	}
	if !strings.Contains(secondBatchMsg, "Translate these 2 cues into French.") {
		t.Errorf("second batch missing instruction:\n%s", secondBatchMsg)
	}
}

func TestTranslateToleratesCodeFences(t *testing.T) {
	chat := func(_ context.Context, _, _ string) (string, error) {
		return "Sure! Here you go:\n```json\n{\"1\":\"ok\"}\n```", nil
	}
	out, err := Translate(context.Background(), chat, Request{
		Segments:  segments(1),
		BatchSize: 5,
	}, nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if out[0].Text != "ok" {
		t.Errorf("Text = %q", out[0].Text)
	}
}

func TestTranslateRetriesOmittedKeysThenFails(t *testing.T) {
	calls := 0
	chat := func(_ context.Context, _, _ string) (string, error) {
		calls++
		return `{"1":"only one"}`, nil // always omits key "2"
	}
	_, err := Translate(context.Background(), chat, Request{
		Segments:  segments(2),
		BatchSize: 5,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "omitted") {
		t.Fatalf("err = %v, want omission failure", err)
	}
	if calls != maxFormatRetries+1 {
		t.Errorf("calls = %d, want %d", calls, maxFormatRetries+1)
	}
}

func TestTranslateStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	chat := func(_ context.Context, _, _ string) (string, error) {
		calls++
		cancel() // cancel after the first batch completes
		return `{"1":"a"}`, nil
	}
	_, err := Translate(ctx, chat, Request{
		Segments:  segments(3),
		BatchSize: 1,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err = %v, want context cancellation", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}
