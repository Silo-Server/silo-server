package collectionutil

import (
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
