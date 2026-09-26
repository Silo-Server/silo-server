package handlers

import (
	"strings"
	"testing"
)

// The bounded loaders must accept the API-wide maximum page size. A caller
// asking for 200 rows reaches the database (and fails here only because this
// loader has no pool); one asking for more is rejected by the guard.
func TestSessionLoadersAcceptTheMaximumPageSize(t *testing.T) {
	loader := new(PlaybackSessionsLoader)
	ctx := t.Context()
	query := PlaybackSessionsQuery{}

	for _, limit := range []int{1, 100, 150, MaxSessionPageLimit} {
		if _, err := loader.LoadPage(ctx, query, "", limit); err == nil || !strings.Contains(err.Error(), "database not configured") {
			t.Fatalf("LoadPage(%d) = %v, want the request to pass the bound", limit, err)
		}
		if _, _, err := loader.LoadSummary(ctx, query, limit); err == nil || !strings.Contains(err.Error(), "database not configured") {
			t.Fatalf("LoadSummary(%d) = %v, want the request to pass the bound", limit, err)
		}
	}

	for _, limit := range []int{0, -1, MaxSessionPageLimit + 1} {
		if _, err := loader.LoadPage(ctx, query, "", limit); err == nil || !strings.Contains(err.Error(), "between 1 and 200") {
			t.Fatalf("LoadPage(%d) = %v, want the bound to reject it", limit, err)
		}
		if _, _, err := loader.LoadSummary(ctx, query, limit); err == nil || !strings.Contains(err.Error(), "between 1 and 200") {
			t.Fatalf("LoadSummary(%d) = %v, want the bound to reject it", limit, err)
		}
	}
}
