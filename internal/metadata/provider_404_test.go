package metadata

import (
	"errors"
	"testing"
)

func TestHandleScopedProvider404KeepsProviderID(t *testing.T) {
	t.Parallel()

	ids := map[string]string{
		"tmdb": "32843",
		"tvdb": "164951",
	}

	if !handleScopedProvider404("tmdb", ids, errors.New("tmdb: HTTP 404: not found"), "season", 0) {
		t.Fatal("handleScopedProvider404() = false, want true")
	}
	if ids["tmdb"] != "32843" {
		t.Fatalf("tmdb id = %q, want preserved", ids["tmdb"])
	}
	if ids["tvdb"] != "164951" {
		t.Fatalf("tvdb id = %q, want preserved", ids["tvdb"])
	}
}
