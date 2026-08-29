package collectionutil

import (
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func TestNormalizeMDBListURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"https://mdblist.com/lists/example-user/watchlist", "https://mdblist.com/lists/example-user/watchlist/json"},
		{"https://mdblist.com/lists/example-user/watchlist/", "https://mdblist.com/lists/example-user/watchlist/json"},
		{"https://mdblist.com/lists/example-user/watchlist/json", "https://mdblist.com/lists/example-user/watchlist/json"},
		{"https://mdblist.com/lists/example-user/watchlist/json/", "https://mdblist.com/lists/example-user/watchlist/json"},
		{"https://mdblist.com/lists/example-user/external/1234/json", "https://mdblist.com/lists/example-user/external/1234/json"},
		{"https://mdblist.com/lists/example-user/external/1234/json/json", "https://mdblist.com/lists/example-user/external/1234/json"},
		{"  https://mdblist.com/lists/example-user/watchlist  ", "https://mdblist.com/lists/example-user/watchlist/json"},
	}
	for _, tc := range cases {
		if got := NormalizeMDBListURL(tc.in); got != tc.want {
			t.Errorf("NormalizeMDBListURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMDBListURLCandidatesNormalizesAndDeduplicates(t *testing.T) {
	got := MDBListURLCandidates(
		"https://mdblist.com/lists/example-user/external/1234/json/json",
		"https://mdblist.com/lists/example-user/external/1234/json",
		"https://mdblist.com/lists/example-user/other",
	)
	want := []string{
		"https://mdblist.com/lists/example-user/external/1234/json",
		"https://mdblist.com/lists/example-user/other/json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MDBListURLCandidates = %#v, want %#v", got, want)
	}
}

func TestFetchMDBListWithFallback(t *testing.T) {
	errFetch := errors.New("fetch failed")

	t.Run("returns first success without trying later candidates", func(t *testing.T) {
		var tried []string
		got, err := FetchMDBListWithFallback([]string{"a", "b"}, func(url string) ([]string, error) {
			tried = append(tried, url)
			return []string{url + "-entry"}, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(got, []string{"a-entry"}) {
			t.Fatalf("entries = %#v, want first candidate's result", got)
		}
		if !reflect.DeepEqual(tried, []string{"a"}) {
			t.Fatalf("tried = %#v, want to stop after first success", tried)
		}
	})

	t.Run("falls back past a failing candidate", func(t *testing.T) {
		var tried []string
		got, err := FetchMDBListWithFallback([]string{"a", "b"}, func(url string) ([]string, error) {
			tried = append(tried, url)
			if url == "a" {
				return nil, errFetch
			}
			return []string{url + "-entry"}, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(got, []string{"b-entry"}) {
			t.Fatalf("entries = %#v, want fallback candidate's result", got)
		}
		if !reflect.DeepEqual(tried, []string{"a", "b"}) {
			t.Fatalf("tried = %#v, want both candidates attempted", tried)
		}
	})

	t.Run("returns the last error when every candidate fails", func(t *testing.T) {
		_, err := FetchMDBListWithFallback([]string{"a", "b"}, func(string) ([]string, error) {
			return nil, errFetch
		})
		if !errors.Is(err, errFetch) {
			t.Fatalf("err = %v, want %v", err, errFetch)
		}
	})

	t.Run("empty candidate list yields nil result and nil error", func(t *testing.T) {
		got, err := FetchMDBListWithFallback(nil, func(string) ([]string, error) {
			t.Fatal("fetch should not be called for an empty list")
			return nil, nil
		})
		if err != nil || got != nil {
			t.Fatalf("got %#v, %v; want nil, nil", got, err)
		}
	})
}

func TestValidateMDBListURL(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"https://mdblist.com/lists/example-user/watchlist",
		"https://mdblist.com/lists/example-user/watchlist/json",
		"https://www.mdblist.com/lists/example-user/watchlist/json",
		"http://mdblist.com/lists/example-user/watchlist/json",
		"https://mdblist.com./lists/example-user/watchlist/json",
		"https://mdblist.com:443/lists/example-user/watchlist/json",
	}
	for _, raw := range allowed {
		if err := ValidateMDBListURL(raw); err != nil {
			t.Errorf("ValidateMDBListURL(%q) = %v, want nil", raw, err)
		}
	}

	blocked := []string{
		"",
		"http://127.0.0.1:8096/",
		"http://127.0.0.1:8096/json",
		"http://127.0.0.1/lists/x/y/json",
		"http://localhost/lists/x/y/json",
		"http://[::1]/lists/x/y/json",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/lists/x/y/json",
		"http://192.168.1.1/lists/x/y/json",
		"http://172.16.0.1/lists/x/y/json",
		"https://mdblist.com.evil.example/lists/x/y/json",
		"https://evil.example/lists/x/y/json",
		"https://mdblist.com/",
		"https://mdblist.com/json",
		"https://mdblist.com/admin/json",
		"https://api.mdblist.com/lists/x/y/json",
		"https://mdblist.com:8080/lists/x/y/json",
		"https://mdblist.com@127.0.0.1/lists/x/y/json",
		"file:///etc/passwd",
		"ftp://mdblist.com/lists/x/y/json",
	}
	for _, raw := range blocked {
		if err := ValidateMDBListURL(raw); !errors.Is(err, ErrMDBListURL) {
			t.Errorf("ValidateMDBListURL(%q) = %v, want ErrMDBListURL", raw, err)
		}
	}
}

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
