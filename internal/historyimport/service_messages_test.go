package historyimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestUserFacingRunError(t *testing.T) {
	t.Parallel()

	t.Run("unauthorized upstream errors mention server settings", func(t *testing.T) {
		t.Parallel()

		message := userFacingRunError(ExecutionSummary{}, &jellyfinHTTPError{StatusCode: http.StatusUnauthorized})
		if message != "Couldn't connect to that server. Check the URL, username, and password and try again." {
			t.Fatalf("unexpected message: %q", message)
		}
	})

	t.Run("partial runs mention best effort behavior", func(t *testing.T) {
		t.Parallel()

		summary := ExecutionSummary{Fetched: 10, Matched: 8, ProgressUpdated: 2}
		message := userFacingRunError(summary, errors.New("timeout"))
		if message != "Import stopped early. Some history may already be imported." {
			t.Fatalf("unexpected message: %q", message)
		}
	})

	t.Run("empty failures stay concise", func(t *testing.T) {
		t.Parallel()

		message := userFacingRunError(ExecutionSummary{}, errors.New("timeout"))
		if message != "Import couldn't be completed. Please try again." {
			t.Fatalf("unexpected message: %q", message)
		}
	})
}

// The stored run error is fixed text, so the log line is the only record of
// the underlying cause. It must carry that cause without its credentials.
func TestLogRunFailureRecordsTheCauseWithoutCredentials(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	cause := errors.New(`fetching Jellyfin resumable items: Get "http://media.example/UserItems/Resume?api_key=secret-value": context deadline exceeded`)
	logRunFailure(context.Background(), logger, RunClaim{RunID: "run-1", DispatchKind: "admin"}, cause)

	line := out.String()
	for _, want := range []string{"history import: run failed", "run_id=run-1", "dispatch_kind=admin", "context deadline exceeded"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q does not contain %q", line, want)
		}
	}
	if strings.Contains(line, "secret-value") {
		t.Errorf("log line %q contains the credential", line)
	}

	// A request URL loses its whole query, including credentials under names
	// the text masking does not know.
	out.Reset()
	urlErr := &url.Error{Op: "Get", URL: "http://media.example/library?session=private-value", Err: errors.New("context deadline exceeded")}
	logRunFailure(context.Background(), logger, RunClaim{RunID: "run-3"}, fmt.Errorf("fetching items: %w", urlErr))
	line = out.String()
	if !strings.Contains(line, "media.example/library") || !strings.Contains(line, "context deadline exceeded") {
		t.Errorf("log line %q lost the request path or cause", line)
	}
	if strings.Contains(line, "private-value") {
		t.Errorf("log line %q contains the query value", line)
	}

	out.Reset()
	logRunFailure(context.Background(), logger, RunClaim{RunID: "run-2"}, nil)
	if out.Len() != 0 {
		t.Errorf("nil cause logged %q, want nothing", out.String())
	}
}
