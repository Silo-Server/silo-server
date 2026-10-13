package sections

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
)

// A row that failed only because the caller went away logs at debug from every
// caller of LogFetchError. The same cancellation while the caller is still there
// is a real failure and still logs an ERROR.
func TestLogFetchErrorLogsAbandonedFetchAtDebug(t *testing.T) {
	logs := recordSectionLogs(t)
	row := ResolvedSection{ID: "row", SectionType: SectionCollection}
	canceled := fmt.Errorf("loading row: %w", context.Canceled)

	gone, cancel := context.WithCancel(t.Context())
	cancel()
	LogFetchError(gone, "api", row, canceled)
	if errs := logs.messages(slog.LevelError); len(errs) != 0 {
		t.Fatalf("ERROR records after the caller left = %q, want none", errs)
	}
	if debug := logs.messages(slog.LevelDebug); len(debug) != 1 || debug[0] != "fetching section items" {
		t.Fatalf("DEBUG records after the caller left = %q, want one fetching section items record", debug)
	}

	logs.reset()
	LogFetchError(t.Context(), "api", row, canceled)
	if errs := logs.messages(slog.LevelError); len(errs) != 1 || errs[0] != "fetching section items" {
		t.Fatalf("ERROR records with the caller still there = %q, want one fetching section items record", errs)
	}
}
