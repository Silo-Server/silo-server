package apiv2

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// TestListCollectionsContainsItem pins contains_item on listCollections: each
// of the acting profile's own manual collections carries contains, and no
// other collection does, so a client can tick Add to collection without
// reading every collection's items.
func TestListCollectionsContainsItem(t *testing.T) {
	deps, pc, _ := collectionDeps(t)
	view := func(id, creator, kind string) handlers.PersonalCollectionView {
		v := fixtureCollectionView()
		v.ID, v.CreatorProfileID, v.CollectionType = id, creator, kind
		return v
	}
	pc.list.Collections = []handlers.PersonalCollectionView{
		view("holds", "p-owner", "manual"),
		view("lacks", "p-owner", "manual"),
		view("smart", "p-owner", "smart"),
		view("synced", "p-owner", "mdblist"),
		view("shared-with-me", "p-other", "manual"),
	}
	// The service answers only the profile's own manual collections; the
	// mapping must not trust a stray id either.
	pc.holding = map[string]bool{"holds": true, "shared-with-me": true}
	h := newTestHandler(t, deps)

	containsOf := func(t *testing.T, body []byte) map[string]json.RawMessage {
		t.Helper()
		var list struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		out := map[string]json.RawMessage{}
		for _, item := range list.Items {
			var id string
			if err := json.Unmarshal(item["id"], &id); err != nil {
				t.Fatal(err)
			}
			if v, ok := item["contains"]; ok {
				out[id] = v
			}
		}
		return out
	}

	rec := do(t, h, http.MethodGet, "/api/v2/collections?contains_item=title-1", "", viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	got := containsOf(t, rec.Body.Bytes())
	want := map[string]json.RawMessage{"holds": json.RawMessage("true"), "lacks": json.RawMessage("false")}
	if len(got) != len(want) || string(got["holds"]) != "true" || string(got["lacks"]) != "false" {
		t.Fatalf("contains = %v, want %v", got, want)
	}
	if !slices.Equal(pc.holdingCalls, []string{"p-owner/title-1"}) {
		t.Fatalf("holding calls = %v", pc.holdingCalls)
	}

	// Without the parameter no collection carries the member and the
	// membership read does not run.
	pc.holdingCalls = nil
	rec = do(t, h, http.MethodGet, "/api/v2/collections", "", viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if got := containsOf(t, rec.Body.Bytes()); len(got) != 0 {
		t.Fatalf("contains without the parameter = %v", got)
	}
	if len(pc.holdingCalls) != 0 {
		t.Fatalf("holding read without the parameter: %v", pc.holdingCalls)
	}

	// A failed membership read fails the list rather than reporting false.
	pc.holdingErr = errors.New("boom")
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/collections?contains_item=title-1", "", viewerHeaders()), TypeInternalError)
}
