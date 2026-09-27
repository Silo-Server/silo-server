package opslog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	// encoding/json sorts map keys, so the encoding is stable. An error keeps
	// its message rather than encoding as {}.
	want := `{"counts":{"movies":1},"error":"boom","ids":[1,2],"static":{"movies":1},"status":200}`
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

func TestAttrValueError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"plain", errors.New("s3 PutObject failed: AccessDenied"), "s3 PutObject failed: AccessDenied"},
		{"wrapped", fmt.Errorf("upload chapter-images/1/0/original.webp: %w", errors.New("AccessDenied")), "upload chapter-images/1/0/original.webp: AccessDenied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := attrValue(slog.AnyValue(tc.err))
			if got != tc.want {
				t.Fatalf("attrValue(%v) = %#v, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestAttrValueErrorMasksURLCredentials(t *testing.T) {
	presigned := "https://bucket.s3.example.test/chapter-images/1/0/original.webp?X-Amz-Credential=AKIDEXAMPLE&X-Amz-Signature=deadbeef"
	err := fmt.Errorf("upload chapter image: PUT %s: AccessDenied", presigned)
	got, ok := attrValue(slog.AnyValue(err)).(string)
	if !ok {
		t.Fatalf("attrValue(error) = %#v, want a string", got)
	}
	for _, secret := range []string{"X-Amz-Credential", "AKIDEXAMPLE", "X-Amz-Signature", "deadbeef"} {
		if strings.Contains(got, secret) {
			t.Fatalf("attrValue kept %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "https://bucket.s3.example.test/chapter-images/1/0/original.webp") || !strings.Contains(got, "AccessDenied") {
		t.Fatalf("attrValue dropped the useful part of the error: %s", got)
	}
}

func TestAttrValueErrorMasksQueryAfterQuoteInURL(t *testing.T) {
	err := fmt.Errorf(`upload: PUT "https://bucket.s3.example.test/it's<1>.webp?X-Amz-Signature=deadbeef": AccessDenied`)
	got, _ := attrValue(slog.AnyValue(err)).(string)
	if strings.Contains(got, "X-Amz-Signature") || strings.Contains(got, "deadbeef") {
		t.Fatalf("attrValue kept the signature: %s", got)
	}
}
