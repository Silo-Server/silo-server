package historyimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

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
