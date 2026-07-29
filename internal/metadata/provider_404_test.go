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

func TestProvider404StateKeepsAllRejectedValues(t *testing.T) {
	t.Parallel()

	state := newProvider404State()
	state.record("tmdb", "111")
	state.record("tmdb", "222")
	if _, ok := state.stale["tmdb"]["111"]; !ok {
		t.Fatal("first rejected tmdb value was lost")
	}
	if _, ok := state.stale["tmdb"]["222"]; !ok {
		t.Fatal("second rejected tmdb value was not recorded")
	}
}

func TestApplyProvider404sDropsNonDurableIDsWithoutPersistingThem(t *testing.T) {
	t.Parallel()

	state := newProvider404State()
	state.record(testMetaDBProvider, "ephemeral-1")
	accumulator := &MetadataResult{ProviderIDs: map[string]string{
		testMetaDBProvider: "ephemeral-1",
		"tmdb":             "603",
	}}
	applyProvider404sToAccumulator(accumulator, state)
	if accumulator.ProviderIDs[testMetaDBProvider] != "" {
		t.Fatalf("rejected metadb id was resurrected: %#v", accumulator.ProviderIDs)
	}
	if accumulator.ProviderIDs["tmdb"] != "603" {
		t.Fatalf("unrelated tmdb id was removed: %#v", accumulator.ProviderIDs)
	}
	if len(accumulator.sameRunStaleProviderIDs) != 0 {
		t.Fatalf("ephemeral ID was marked durable stale: %#v", accumulator.sameRunStaleProviderIDs)
	}
}
