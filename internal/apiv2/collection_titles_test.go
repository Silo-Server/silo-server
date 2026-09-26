package apiv2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCollectionMembershipTitlesAreAdditive(t *testing.T) {
	for _, member := range []any{
		PersonalCollectionItem{CollectionID: "collection", MediaItemID: "movie", Position: 4, Title: "Interstellar", AddedAt: NewInstant(fixedTime())},
		AdminCollectionMember{CollectionID: "collection", MediaItemID: "movie", Position: 4, Title: "Interstellar"},
	} {
		body, err := json.Marshal(member)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{`"collection_id":"collection"`, `"media_item_id":"movie"`, `"position":4`, `"title":"Interstellar"`} {
			if !strings.Contains(string(body), field) {
				t.Fatalf("missing %s: %s", field, body)
			}
		}
	}
}
