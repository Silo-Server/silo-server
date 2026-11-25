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
// must end the path or mdblist.com answers with the HTML list page.
func TestCanonicalMDBListURLDropsQueryAndFragment(t *testing.T) {
	t.Parallel()

	const want = "https://mdblist.com/lists/example-user/watchlist/json"
	for _, raw := range []string{
		"https://mdblist.com/lists/example-user/watchlist?sort=rank&sortorder=asc",
		"https://mdblist.com/lists/example-user/watchlist/?ref=share",
		"https://mdblist.com/lists/example-user/watchlist#top",
		// What earlier builds stored for the two URLs above; a sync
		// normalizes the stored URL again, so these collections recover.
		"https://mdblist.com/lists/example-user/watchlist?sort=rank&sortorder=asc/json",
		"https://mdblist.com/lists/example-user/watchlist#top/json",
		"https://mdblist.com/lists/example-user/watchlist/json?limit=50",
		"https://mdblist.com/lists/example-user/watchlist/JSON",
		"https://mdblist.com/lists/example-user/watchlist/json/json/",
	} {
		got, err := CanonicalMDBListURL(raw)
		if err != nil {
			t.Fatalf("CanonicalMDBListURL(%q) = %v", raw, err)
		}
		if got != want {
			t.Errorf("CanonicalMDBListURL(%q) = %q, want %q", raw, got, want)
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
