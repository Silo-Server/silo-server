package userdata

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A stored configuration is unconstrained JSON: a list longer than a row of
// collections holds must not cost one lookup per entry.
func TestRemapCollectionListSkipsOversizedList(t *testing.T) {
	entries := make([]string, MaxCollectionListEntries+1)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"library_collection_id":"c%d"}`, i)
	}
	config := json.RawMessage(`{"collections":[` + strings.Join(entries, ",") + `]}`)
	_, total, kept, err := RemapCollectionList(config, func(_, id string) (string, bool, error) {
		t.Fatalf("looked up %s", id)
		return "", false, nil
	})
	if err != nil || total != len(entries) || kept != 0 {
		t.Fatalf("total=%d kept=%d err=%v, want %d, 0, nil", total, kept, err, len(entries))
	}
}
