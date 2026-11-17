package backup

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// Planning resolves each listed collection with a query, so a backup's
// oversized list must list none.
func TestListedCollectionsIgnoresOversizedList(t *testing.T) {
	entries := make([]string, userdata.MaxCollectionListEntries+1)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"library_collection_id":"c%d"}`, i)
	}
	over := json.RawMessage(`{"collections":[` + strings.Join(entries, ",") + `]}`)
	if got := listedCollections(over); len(got) != 0 {
		t.Fatalf("oversized list listed %d collections, want 0", len(got))
	}
	full := json.RawMessage(`{"collections":[` + strings.Join(entries[1:], ",") + `]}`)
	if got := listedCollections(full); len(got) != userdata.MaxCollectionListEntries {
		t.Fatalf("full list listed %d collections, want %d", len(got), userdata.MaxCollectionListEntries)
	}
}
