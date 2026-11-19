package animeids

import (
	"net/http"
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
