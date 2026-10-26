package collectionutil

import (
	"errors"
	"testing"
)

func TestParseTMDBListURL(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"https://www.themoviedb.org/list/310-my-movie-list", 310},
		{"https://www.themoviedb.org/list/310", 310},
		{"http://themoviedb.org/list/310-my-movie-list/", 310},
		{"www.themoviedb.org/list/8649937-marvel?language=fr-FR#top", 8649937},
		{"  https://WWW.THEMOVIEDB.ORG/list/310  ", 310},
		{"310", 310},
	}
	for _, tc := range cases {
		got, err := ParseTMDBListURL(tc.in)
		if err != nil {
			t.Errorf("ParseTMDBListURL(%q) = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseTMDBListURL(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseTMDBListURLRejectsNonLists(t *testing.T) {
	for _, raw := range []string{
		"",
		"0",
		"-3",
		"abc",
		"99999999999999999999999",
		"https://www.themoviedb.org/collection/10-star-wars-collection",
		"https://www.themoviedb.org/movie/550-fight-club",
		"https://www.themoviedb.org/u/someone/lists",
		"https://www.themoviedb.org/list/",
		"https://www.themoviedb.org/list/my-movie-list",
		"https://www.themoviedb.org/list/310-my-movie-list/edit",
		"https://evil.example/list/310",
		"https://themoviedb.org.evil.example/list/310",
		"ftp://www.themoviedb.org/list/310",
		"https://mdblist.com/lists/user/slug",
	} {
		if id, err := ParseTMDBListURL(raw); !errors.Is(err, ErrTMDBListURL) {
			t.Errorf("ParseTMDBListURL(%q) = %d, %v; want ErrTMDBListURL", raw, id, err)
		}
	}
}

func TestCanonicalTMDBListURLDropsSlug(t *testing.T) {
	got, err := CanonicalTMDBListURL("https://www.themoviedb.org/list/310-my-movie-list?page=2")
	if err != nil {
		t.Fatalf("CanonicalTMDBListURL: %v", err)
	}
	if want := "https://www.themoviedb.org/list/310"; got != want {
		t.Fatalf("CanonicalTMDBListURL = %q, want %q", got, want)
	}
}
