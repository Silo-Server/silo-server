package collectionutil

import (
	"errors"
	"net/http"
	"testing"
)

func TestCanonicalMDBListURLRejectsPrivateHosts(t *testing.T) {
	t.Parallel()

	if _, err := CanonicalMDBListURL("http://127.0.0.1:8096/"); !errors.Is(err, ErrMDBListURL) {
		t.Fatalf("CanonicalMDBListURL(loopback) = %v, want ErrMDBListURL", err)
	}

	got, err := CanonicalMDBListURL("https://mdblist.com/lists/example-user/watchlist")
	if err != nil {
		t.Fatalf("CanonicalMDBListURL(valid) = %v", err)
	}
	if got != "https://mdblist.com/lists/example-user/watchlist/json" {
		t.Fatalf("CanonicalMDBListURL = %q", got)
	}
}

// A list URL copied from the browser can carry a query or fragment, and /json
// must end the path or mdblist.com answers with the HTML list page. The query
// is the feed's too (sort=rank orders it), so it stays.
func TestCanonicalMDBListURLPutsJSONOnThePath(t *testing.T) {
	t.Parallel()

	const list = "https://mdblist.com/lists/example-user/watchlist"
	for raw, want := range map[string]string{
		list + "?sort=rank&sortorder=asc": list + "/json?sort=rank&sortorder=asc",
		list + "/?ref=share":              list + "/json?ref=share",
		list + "#top":                     list + "/json",
		list + "/json?sort=imdbpopular":   list + "/json?sort=imdbpopular",
		list + "/JSON":                    list + "/json",
		list + "/json/json/":              list + "/json",
		list + "/json?sort=rank#top":      list + "/json?sort=rank",
		// What earlier builds stored for the first and third URLs; a sync
		// normalizes the stored URL again, so those collections recover.
		list + "?sort=rank&sortorder=asc/json": list + "/json?sort=rank&sortorder=asc",
		list + "#top/json":                     list + "/json",
		list + "/json?sort=rank/json/json":     list + "/json?sort=rank",
		list + "/json?sort=rank #top":          list + "/json?sort=rank",
		list + "/json?sort=rank /json":         list + "/json?sort=rank",
	} {
		got, err := CanonicalMDBListURL(raw)
		if err != nil {
			t.Fatalf("CanonicalMDBListURL(%q) = %v", raw, err)
		}
		if got != want {
			t.Errorf("CanonicalMDBListURL(%q) = %q, want %q", raw, got, want)
		}
		// Sync normalizes the stored URL again, so a second pass must not
		// change it.
		if again, _ := CanonicalMDBListURL(got); again != got {
			t.Errorf("CanonicalMDBListURL(%q) = %q, want it unchanged", got, again)
		}
	}

	for _, raw := range []string{"https://evil.example?x=https://mdblist.com/lists/a/b", "https://mdblist.com#/lists/a/b", "?#"} {
		if got, err := CanonicalMDBListURL(raw); err == nil {
			t.Errorf("CanonicalMDBListURL(%q) = %q, want an error", raw, got)
		}
	}
}

func TestMDBListHTTPClientRejectsPrivateRedirect(t *testing.T) {
	t.Parallel()

	client := MDBListHTTPClient(nil)
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8096/json", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := client.CheckRedirect(req, []*http.Request{req}); !errors.Is(err, ErrMDBListURL) {
		t.Fatalf("CheckRedirect = %v, want ErrMDBListURL", err)
	}
}
