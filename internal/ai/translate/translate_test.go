package aitranslate

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func segments(n int) []Segment {
	out := make([]Segment, n)
	for i := range out {
		out[i] = Segment{ID: strconv.Itoa(i + 1), Text: fmt.Sprintf("line %d", i+1)}
	}
	return out
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
