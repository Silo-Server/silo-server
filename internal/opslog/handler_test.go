package opslog

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
)

// TestHandlerSnapshotsValuesTheCallerOwns logs a map and a slice, then changes
// both the way a caller may once the log call returns. The consumer encodes
// the entry later on its own goroutine, so the entry must not share them.
func TestHandlerSnapshotsValuesTheCallerOwns(t *testing.T) {
	t.Parallel()
	writer := &recordingWriter{}
	logger := slog.New(NewHandler(slog.DiscardHandler, writer, slog.LevelInfo, "node-a"))

	counts := map[string]int{"movies": 1}
	ids := []int{1, 2}
	logger.With("static", counts).InfoContext(context.Background(), "probe: snapshot",
		"counts", counts, "ids", ids, "error", errors.New("boom"), "status", 200)
	counts["movies"] = 2
	counts["shows"] = 3
	ids[0] = 9

	writer.mu.Lock()
	entry := writer.entries[0]
	writer.mu.Unlock()
	got, err := json.Marshal(entry.Attrs)
	if err != nil {
		t.Fatal(err)
	}
	// encoding/json sorts map keys, so the encoding is stable.
	want := `{"counts":{"movies":1},"error":{},"ids":[1,2],"static":{"movies":1},"status":200}`
	if string(got) != want {
		t.Fatalf("attrs = %s, want %s", got, want)
	}
}

// TestHandlerKeepsAValueItCannotEncode checks that a value encoding/json
// rejects is kept as text instead of emptying the entry's attrs.
func TestHandlerKeepsAValueItCannotEncode(t *testing.T) {
	t.Parallel()
	writer := &recordingWriter{}
	logger := slog.New(NewHandler(slog.DiscardHandler, writer, slog.LevelInfo, "node-a"))

	logger.InfoContext(context.Background(), "probe: unencodable", "ch", make(chan int), "status", 200)

	writer.mu.Lock()
	entry := writer.entries[0]
	writer.mu.Unlock()
	if s, ok := entry.Attrs["ch"].(string); !ok || s == "" {
		t.Fatalf("attrs[ch] = %#v, want its text form", entry.Attrs["ch"])
	}
	if entry.Attrs["status"] != int64(200) {
		t.Fatalf("attrs[status] = %#v, want 200", entry.Attrs["status"])
	}
}
