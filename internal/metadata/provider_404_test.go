package metadata

import (
	"errors"
	"testing"
)

func TestHandleChildProvider404KeepsProviderID(t *testing.T) {
	t.Parallel()

	ids := map[string]string{
		"tmdb": "32843",
		"tvdb": "164951",
	}

	if !handleChildProvider404("tmdb", ids, errors.New("tmdb: HTTP 404: not found"), "season", 0) {
		t.Fatal("handleChildProvider404() = false, want true")
	}
	if ids["tmdb"] != "32843" {
		t.Fatalf("tmdb id = %q, want preserved", ids["tmdb"])
	}
	if ids["tvdb"] != "164951" {
		t.Fatalf("tvdb id = %q, want preserved", ids["tvdb"])
	}
}

func TestHandleProvider404DropsProviderID(t *testing.T) {
	t.Parallel()

	ids := map[string]string{
		"tmdb": "32843",
		"tvdb": "164951",
	}

	if !handleProvider404(nil, ids, "tmdb", errors.New("tmdb: HTTP 404: not found")) {
		t.Fatal("handleProvider404() = false, want true")
	}
	if _, ok := ids["tmdb"]; ok {
		t.Fatalf("tmdb id was not dropped: %v", ids)
	}
	if ids["tvdb"] != "164951" {
		t.Fatalf("tvdb id = %q, want preserved", ids["tvdb"])
	}
}
