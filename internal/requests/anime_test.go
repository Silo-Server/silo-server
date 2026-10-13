package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func TestDetectAnime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detail *tmdb.MediaDetail
		listed bool
		want   bool
	}{
		{"TMDB's anime keyword", &tmdb.MediaDetail{KeywordIDs: []int{99, animeKeywordID}}, false, true},
		{"Japanese-language animation", &tmdb.MediaDetail{GenreIDs: []int{animationGenreID}, OriginalLanguage: "ja"}, false, true},
		{"animation from Japan in another language", &tmdb.MediaDetail{GenreIDs: []int{animationGenreID}, OriginalLanguage: "en", OriginCountries: []string{"US", "JP"}}, false, true},
		{"Western animation", &tmdb.MediaDetail{GenreIDs: []int{animationGenreID}, OriginalLanguage: "en", OriginCountries: []string{"US"}}, false, false},
		{"Japanese live action", &tmdb.MediaDetail{GenreIDs: []int{18}, OriginalLanguage: "ja", OriginCountries: []string{"JP"}}, false, false},
		{"a film on the anime list", &tmdb.MediaDetail{MediaType: "movie", GenreIDs: []int{16}, OriginalLanguage: "en", OriginCountries: []string{"US"}}, true, true},
		{"a Western series on the anime list", &tmdb.MediaDetail{MediaType: "series", GenreIDs: []int{16}, OriginalLanguage: "en", OriginCountries: []string{"US"}}, true, false},
		{"a Japanese series on the list TMDB files under no genre", &tmdb.MediaDetail{MediaType: "series", OriginalLanguage: "ja", OriginCountries: []string{"JP"}}, true, true},
		{"Chinese animation on the anime list", &tmdb.MediaDetail{GenreIDs: []int{animationGenreID}, OriginalLanguage: "zh", OriginCountries: []string{"CN"}}, true, false},
		{"Korean animation on the anime list", &tmdb.MediaDetail{GenreIDs: []int{animationGenreID}, OriginalLanguage: "ko"}, true, false},
		{"a Chinese film in English on the anime list", &tmdb.MediaDetail{MediaType: "movie", GenreIDs: []int{animationGenreID}, OriginalLanguage: "en", OriginCountries: []string{"CN", "US"}}, true, false},
		{"a Korean film in English on the anime list", &tmdb.MediaDetail{MediaType: "movie", GenreIDs: []int{animationGenreID}, OriginalLanguage: "en", OriginCountries: []string{"KR"}}, true, false},
		{"a Japanese-Chinese series in Chinese on the anime list", &tmdb.MediaDetail{MediaType: "series", OriginalLanguage: "zh", OriginCountries: []string{"CN", "JP"}}, true, true},
		{"a Japanese-Korean film on the anime list", &tmdb.MediaDetail{MediaType: "movie", OriginalLanguage: "ko", OriginCountries: []string{"JP", "KR"}}, true, true},
		{"Chinese animation TMDB tags anime", &tmdb.MediaDetail{KeywordIDs: []int{animeKeywordID}, GenreIDs: []int{animationGenreID}, OriginalLanguage: "zh"}, false, true},
		{"no detail", nil, true, false},
	} {
		if got := detectAnime(tc.detail, tc.listed); got != tc.want {
			t.Errorf("%s: detectAnime = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type fakeAnimeIndex struct {
	listed bool
	err    error
	asked  []any
}

func (f *fakeAnimeIndex) Listed(_ context.Context, movie bool, tvdbID int, imdbID string) (bool, error) {
	f.asked = append(f.asked, movie, tvdbID, imdbID)
	return f.listed, f.err
}

func TestRoutingFactsAskTheAnimeList(t *testing.T) {
	svc := newTestService(newFakeStore())
	detail := &tmdb.MediaDetail{MediaType: "movie", ID: 5, IMDbID: "tt1", TVDBID: 9, OriginalLanguage: "en"}
	if svc.routingFacts(context.Background(), detail).Anime {
		t.Fatal("anime without a list or a TMDB signal")
	}
	index := &fakeAnimeIndex{listed: true}
	svc.SetAnimeIndex(index)
	if !svc.routingFacts(context.Background(), detail).Anime {
		t.Fatal("a listed title is not anime")
	}
	if index.asked[0] != true || index.asked[1] != 9 || index.asked[2] != "tt1" {
		t.Fatalf("asked the list %v", index.asked)
	}
	// A failed lookup never holds the request up; it is just not listed.
	svc.SetAnimeIndex(&fakeAnimeIndex{err: errors.New("db down")})
	if facts := svc.routingFacts(context.Background(), detail); facts.Anime || !facts.Captured() {
		t.Fatalf("facts = %+v, want captured and not anime", facts)
	}
}

func TestRoutingRating(t *testing.T) {
	certs := map[string][]string{"JP": {"G", "PG12"}, "KR": {"15"}, "FR": {"U"}, "XX": {"weird"}}
	for _, tc := range []struct {
		name    string
		us      string
		origins []string
		want    string
	}{
		{"the US rating wins", "PG-13", []string{"JP"}, "PG-13"},
		{"the own country's, strictest", "", []string{"JP"}, "JP:PG12"},
		{"the first origin country with a rating", "", []string{"CN", "KR"}, "KR:15"},
		{"a rating no scale reads", "", []string{"XX"}, ""},
		{"no rating anywhere", "", []string{"DE"}, ""},
		{"only NR in the US", "NR", []string{"JP"}, "JP:PG12"},
		{"only NR anywhere", "NR", []string{"DE"}, "NR"},
	} {
		if got := routingRating(tc.us, certs, tc.origins); got != tc.want {
			t.Errorf("%s: routingRating = %q, want %q", tc.name, got, tc.want)
		}
	}
	jp := "JP:PG12"
	if !ratingWithin(&jp, "PG-13") || ratingWithin(&jp, "PG") {
		t.Fatal("a Japanese PG12 should be within PG-13 and above PG")
	}
}
