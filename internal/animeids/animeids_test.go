package animeids

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// realEntries are verbatim entries from Kometa's published anime_ids.json,
// keyed by AniDB ID: a series by TVDB ID only, one with an IMDb ID too, one
// whose IMDb IDs name the parts of a multi-part film, and one mapped to
// neither.
const realEntries = `{
	"1": {"tvdb_id": 72025, "tvdb_season": 1, "tvdb_epoffset": 0, "mal_id": 290, "anilist_id": 290},
	"11": {"tvdb_id": 70900, "tvdb_season": 0, "tvdb_epoffset": 2, "imdb_id": "tt7941838", "mal_id": 821, "anilist_id": 821},
	"4772": {"tvdb_id": 75411, "tvdb_season": 0, "tvdb_epoffset": 1, "imdb_id": "tt0936323,tt0936320", "mal_id": 1719, "anilist_id": 1719},
	"159": {"tvdb_epoffset": 0}
}`

// realIDs is how many distinct IDs realEntries holds.
const realIDs = 6

// list builds a published list: the real entries plus n made-up series.
func list(n int) []byte {
	raw := map[string]map[string]any{}
	if err := json.Unmarshal([]byte(realEntries), &raw); err != nil {
		panic(err)
	}
	for i := 1; i <= n; i++ {
		raw[fmt.Sprint("filler-", i)] = map[string]any{"tvdb_id": 1000 + i, "mal_id": i}
	}
	raw["null"] = map[string]any{"tvdb_id": nil}
	body, _ := json.Marshal(raw)
	return body
}

func TestParse(t *testing.T) {
	ids, err := parse(list(minEntries))
	if err != nil {
		t.Fatal(err)
	}
	got := map[listedID]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if len(ids) != minEntries+realIDs {
		t.Fatalf("parsed %d IDs, want %d", len(ids), minEntries+realIDs)
	}
	for _, want := range []listedID{
		{"tvdb", "72025"}, {"tvdb", "70900"}, {"tvdb", "75411"}, {"tvdb", "1001"},
		{"imdb", "tt7941838"}, {"imdb", "tt0936323"}, {"imdb", "tt0936320"},
	} {
		if !got[want] {
			t.Errorf("missing %v: want every series by TVDB ID and each part of a film by IMDb ID", want)
		}
	}
	if _, err := parse(list(10)); err == nil || !strings.Contains(err.Error(), "only") {
		t.Fatalf("a short list: %v, want it refused", err)
	}
	if _, err := parse([]byte("<html>rate limited</html>")); err == nil {
		t.Fatal("parsed a page that is not the list")
	}
}

func TestRedirectsStayOnTheListHost(t *testing.T) {
	client := newClient()
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	if err := client.CheckRedirect(req, []*http.Request{{}}); err == nil {
		t.Fatal("followed a redirect off the list host")
	}
	req, _ = http.NewRequest(http.MethodGet, "https://"+sourceHost+"/x", nil)
	if err := client.CheckRedirect(req, []*http.Request{{}}); err != nil {
		t.Fatal(err)
	}
}
