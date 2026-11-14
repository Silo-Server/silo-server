package collectionutil

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestCanonicalCollectionSource: a collection a restore or import writes
// keeps only the list URLs its editor would store, in canonical form; a URL
// the sync would fetch that is not that service's list is refused.
func TestCanonicalCollectionSource(t *testing.T) {
	for _, tc := range []struct {
		name, collectionType, sourceURL, config string
		wantURL, wantConfig                     string
		refused                                 bool
	}{
		{name: "mdblist", collectionType: "mdblist", sourceURL: "https://mdblist.com/lists/a/b", config: `{"mode":"mdblist_json","url":"https://mdblist.com/lists/a/b/"}`,
			wantURL: "https://mdblist.com/lists/a/b/json", wantConfig: `{"mode":"mdblist_json","url":"https://mdblist.com/lists/a/b/json"}`},
		{name: "mdblist on loopback", collectionType: "mdblist", config: `{"mode":"mdblist_json","url":"http://127.0.0.1:8080/lists/a"}`, refused: true},
		{name: "mdblist source url on link-local", collectionType: "mdblist", sourceURL: "http://169.254.169.254/latest/meta-data/", config: `{}`, refused: true},
		{name: "mdblist list_url elsewhere", config: `{"mode":"mdblist_json","list_url":"https://evil.example/lists/a"}`, refused: true},
		{name: "tmdb list", collectionType: "tmdb", config: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/42"}`,
			wantConfig: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/42"}`},
		{name: "tmdb list elsewhere", collectionType: "tmdb", config: `{"mode":"tmdb_list","url":"http://10.0.0.1/list/42"}`, refused: true},
		{name: "a url in a mode that fetches none", collectionType: "tmdb", config: `{"mode":"tmdb_preset","url":"http://127.0.0.1/"}`, refused: true},
		{name: "a preset", collectionType: "tmdb", config: `{"mode":"tmdb_preset","preset":"popular"}`, wantConfig: `{"mode":"tmdb_preset","preset":"popular"}`},
		{name: "none", collectionType: "manual"},
		{name: "not an object", collectionType: "mdblist", config: `["http://127.0.0.1/"]`, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, config, err := CanonicalCollectionSource(tc.collectionType, tc.sourceURL, json.RawMessage(tc.config))
			if tc.refused {
				if !errors.Is(err, ErrCollectionSource) {
					t.Fatalf("= %q %s, %v; want refused", url, config, err)
				}
				return
			}
			if err != nil || url != tc.wantURL || (tc.wantConfig != "" && string(config) != tc.wantConfig) {
				t.Fatalf("= %q %s, %v; want %q %s", url, config, err, tc.wantURL, tc.wantConfig)
			}
		})
	}
}
