package catalog

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestLibraryCollectionCountStructuralAvailability(t *testing.T) {
	groups := make([]QueryGroup, 17)
	legacy, err := json.Marshal(QueryDefinition{Groups: groups})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind string
		definition json.RawMessage
		libraries  []int
		count      int
		wantStatus string
		wantCount  int
	}{
		{"legacy cached count", "smart", legacy, []int{1}, 99, "unavailable", 0},
		{"legacy cached zero", "smart", legacy, []int{1}, 0, "unavailable", 0},
		{"legacy bindings", "smart", json.RawMessage(`{}`), make([]int, 257), 99, "unavailable", 0},
		{"ordinary cached count", "smart", json.RawMessage(`{"groups":[]}`), []int{1}, 2, "available", 2},
		{"available real zero", "smart", json.RawMessage(`{"groups":[]}`), []int{1}, 0, "available", 0},
		{"manual dormant definition", "manual", legacy, []int{1}, 2, "available", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &models.LibraryCollection{CollectionType: tc.kind, QueryDefinition: tc.definition, LibraryIDs: tc.libraries, ItemCount: tc.count}
			setLibraryCollectionCountAvailability(c)
			if c.ItemCountStatus != tc.wantStatus || c.ItemCount != tc.wantCount {
				t.Fatalf("count = %d/%s, want %d/%s", c.ItemCount, c.ItemCountStatus, tc.wantCount, tc.wantStatus)
			}
			if string(c.QueryDefinition) != string(tc.definition) {
				t.Fatal("availability changed the saved definition")
			}
		})
	}
}
