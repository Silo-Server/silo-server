package collectionutil

import (
	"errors"
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
