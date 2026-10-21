package apiv2

import (
	"encoding/json"
	"strings"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// V2 extras carry the image; the shared catalog shape the frozen V1 detail
// serializes does not.
func TestItemExtrasOfCarriesStillOnlyInV2(t *testing.T) {
	extra := catalogpkg.ItemExtraInfo{
		ContentID: "local-a", Kind: "featurette", Title: "Making Of", DurationSeconds: 90, FileID: 7,
		StillPath: "local/extras/local-a/still/original.r.webp", StillURL: "https://img/still", StillThumbhash: "hash",
	}

	got := itemExtrasOf([]catalogpkg.ItemExtraInfo{extra})
	want := ItemExtraInfo{
		ContentID: "local-a", Kind: "featurette", Title: "Making Of", DurationSeconds: 90, FileID: 7,
		StillURL: "https://img/still", StillThumbhash: "hash",
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("itemExtrasOf = %+v, want [%+v]", got, want)
	}
	if itemExtrasOf(nil) != nil {
		t.Fatal("itemExtrasOf(nil) must stay nil so extras is omitted")
	}

	v1, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v1), "still") {
		t.Fatalf("V1 extra JSON gained a still field: %s", v1)
	}
}
