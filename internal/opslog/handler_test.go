package opslog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
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
	for _, scheme := range []string{"http", "https", "HTTP", "HTTPS", "hTtP", "hTtPs"} {
		t.Run(scheme, func(t *testing.T) {
			presigned := scheme + "://synthetic-user:synthetic-password@bucket.s3.example.test/chapter-images/1/0/original.webp?X-Amz-Credential=AKIDEXAMPLE&X-Amz-Signature=deadbeef#private-fragment"
			err := fmt.Errorf("upload chapter image: PUT %s: AccessDenied", presigned)
			got, ok := attrValue(slog.AnyValue(err)).(string)
			if !ok {
				t.Fatalf("attrValue(error) = %#v, want a string", got)
			}
			for _, secret := range []string{"synthetic-user", "synthetic-password", "X-Amz-Credential", "AKIDEXAMPLE", "X-Amz-Signature", "deadbeef", "private-fragment"} {
				if strings.Contains(got, secret) {
					t.Fatalf("attrValue kept %q: %s", secret, got)
				}
			}
			if !strings.Contains(got, strings.ToLower(scheme)+"://bucket.s3.example.test/chapter-images/1/0/original.webp") || !strings.Contains(got, "AccessDenied") {
				t.Fatalf("attrValue dropped the useful part of the error: %s", got)
			}
		})
	}
}

func TestAttrValueErrorMasksQueryAfterQuoteInURL(t *testing.T) {
	err := fmt.Errorf(`upload: PUT "https://bucket.s3.example.test/it's<1>.webp?X-Amz-Signature=deadbeef": AccessDenied`)
	got, _ := attrValue(slog.AnyValue(err)).(string)
	if strings.Contains(got, "X-Amz-Signature") || strings.Contains(got, "deadbeef") {
		t.Fatalf("attrValue kept the signature: %s", got)
	}
}

func TestHandlerMasksNestedCredentialAssignments(t *testing.T) {
	writer := &recordingWriter{}
	logger := slog.New(NewHandler(slog.DiscardHandler, writer, slog.LevelInfo, "fixture"))
	logger.WarnContext(t.Context(), "fixture rejected", "error", errors.New("rpc error: code = Unauthenticated desc = api_key=FAKE_FIXTURE_SECRET rejected"))
	got, ok := writer.entries[0].Attrs["error"].(string)
	if !ok || strings.Contains(got, "FAKE_FIXTURE_SECRET") || !strings.Contains(got, "api_key=[REDACTED]") || !strings.Contains(got, "Unauthenticated") {
		t.Fatalf("captured error = %#v, want redacted credential and preserved diagnosis", writer.entries[0].Attrs["error"])
	}
}

// TestHandlerRecordsErrorText checks that an error attr is stored as its
// text. Errors from errors.New and fmt.Errorf have no exported fields, so a
// JSON encode would store {} and hide why the logged operation failed.
func TestHandlerRecordsErrorText(t *testing.T) {
	t.Parallel()
	writer := &recordingWriter{}
	logger := slog.New(NewHandler(slog.DiscardHandler, writer, slog.LevelInfo, "node-a"))

	wrapped := fmt.Errorf("loading overlay summaries: %w", errors.New("connection refused"))
	logger.With("static_err", errors.New("static")).ErrorContext(context.Background(), "probe: error", "error", wrapped)

	writer.mu.Lock()
	entry := writer.entries[0]
	writer.mu.Unlock()
	if got := entry.Attrs["error"]; got != "loading overlay summaries: connection refused" {
		t.Fatalf("attrs[error] = %#v, want the error text", got)
	}
	if got := entry.Attrs["static_err"]; got != "static" {
		t.Fatalf("attrs[static_err] = %#v, want the error text", got)
	}
}

type nilPtrErr struct{ msg string }

func (e *nilPtrErr) Error() string { return e.msg }

// TestHandlerRecordsTypedNilError checks that a nil pointer satisfying error
// is recorded instead of panicking when Error dereferences the receiver.
func TestHandlerRecordsTypedNilError(t *testing.T) {
	t.Parallel()
	writer := &recordingWriter{}
	logger := slog.New(NewHandler(slog.DiscardHandler, writer, slog.LevelInfo, "node-a"))

	var err *nilPtrErr
	logger.ErrorContext(context.Background(), "probe: error", "error", err)

	writer.mu.Lock()
	entry := writer.entries[0]
	writer.mu.Unlock()
	if got := entry.Attrs["error"]; got != "<nil *opslog.nilPtrErr>" {
		t.Fatalf("attrs[error] = %#v, want a nil placeholder", got)
	}
}

// TestHandlerSanitizesErrorURLs checks that a requested URL inside an error
// chain loses its query string and credentials before it is stored.
func TestHandlerSanitizesErrorURLs(t *testing.T) {
	t.Parallel()
	writer := &recordingWriter{}
	logger := slog.New(NewHandler(slog.DiscardHandler, writer, slog.LevelInfo, "node-a"))

	urlErr := &url.Error{Op: "Get", URL: "https://user:pass@cdn.example/v.mp4?token=s3cret&sig=abc", Err: errors.New("timeout")}
	logger.ErrorContext(context.Background(), "probe: error", "error", fmt.Errorf("fetch stream: %w", urlErr))

	writer.mu.Lock()
	entry := writer.entries[0]
	writer.mu.Unlock()
	got, _ := entry.Attrs["error"].(string)
	if strings.Contains(got, "s3cret") || strings.Contains(got, "pass") || strings.Contains(got, "sig=") {
		t.Fatalf("attrs[error] = %q leaks a secret", got)
	}
	if !strings.Contains(got, "fetch stream") || !strings.Contains(got, "timeout") {
		t.Fatalf("attrs[error] = %q, want the wrapper and cause kept", got)
	}
}

// TestHandlerRedactsSecretAssignmentsInErrorText checks that a secret
// key=value pair in plain error text, as a plugin's gRPC status description
// carries it, is masked before the entry is stored.
func TestHandlerRedactsSecretAssignmentsInErrorText(t *testing.T) {
	t.Parallel()
	writer := &recordingWriter{}
	logger := slog.New(NewHandler(slog.DiscardHandler, writer, slog.LevelInfo, "node-a"))

	err := errors.New("rpc error: code = Unauthenticated desc = api_key=FAKE_FIXTURE_SECRET rejected")
	logger.ErrorContext(context.Background(), "probe: error", "error", fmt.Errorf("metadata lookup: %w", err))

	writer.mu.Lock()
	entry := writer.entries[0]
	writer.mu.Unlock()
	got, _ := entry.Attrs["error"].(string)
	if want := "metadata lookup: rpc error: code = Unauthenticated desc = api_key=[REDACTED] rejected"; got != want {
		t.Fatalf("attrs[error] = %q, want %q", got, want)
	}
}
