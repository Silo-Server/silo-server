package contentid

import "testing"

func TestForMovie(t *testing.T) {
	tests := []struct {
		name string
		ids  ProviderIDs
		want string
		ok   bool
	}{
		{"tmdb wins", ProviderIDs{Tmdb: "228064", Imdb: "tt2413338", Tvdb: "5"}, "movie-tmdb-228064", true},
		{"imdb fallback", ProviderIDs{Imdb: "tt2413338"}, "movie-imdb-tt2413338", true},
		{"tvdb last", ProviderIDs{Tvdb: "12345"}, "movie-tvdb-12345", true},
		{"imdb uppercase normalized", ProviderIDs{Imdb: "TT2413338"}, "movie-imdb-tt2413338", true},
		{"whitespace trimmed", ProviderIDs{Tmdb: "  228064 "}, "movie-tmdb-228064", true},
		{"no ids", ProviderIDs{}, "", false},
		{"invalid imdb only", ProviderIDs{Imdb: "12345"}, "", false},
		{"invalid tmdb non-numeric", ProviderIDs{Tmdb: "abc"}, "", false},
		{"invalid tmdb falls through to imdb", ProviderIDs{Tmdb: "abc", Imdb: "tt99"}, "movie-imdb-tt99", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ForMovie(tt.ids)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ForMovie(%+v) = (%q,%v), want (%q,%v)", tt.ids, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestForSeries(t *testing.T) {
	tests := []struct {
		name string
		ids  ProviderIDs
		want string
		ok   bool
	}{
		{"tvdb wins for series", ProviderIDs{Tmdb: "1", Tvdb: "296762", Imdb: "tt1"}, "series-tvdb-296762", true},
		{"tmdb fallback", ProviderIDs{Tmdb: "1399"}, "series-tmdb-1399", true},
		{"imdb last", ProviderIDs{Imdb: "tt0944947"}, "series-imdb-tt0944947", true},
		{"none", ProviderIDs{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ForSeries(tt.ids)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ForSeries(%+v) = (%q,%v), want (%q,%v)", tt.ids, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestForSeasonAndEpisode(t *testing.T) {
	series := "series-tvdb-296762"

	gotSeason, ok := ForSeason(series, 1)
	if !ok || gotSeason != "season-tvdb-296762-1" {
		t.Fatalf("ForSeason = (%q,%v)", gotSeason, ok)
	}
	gotEp, ok := ForEpisode(series, 1, 5)
	if !ok || gotEp != "episode-tvdb-296762-1-5" {
		t.Fatalf("ForEpisode = (%q,%v)", gotEp, ok)
	}

	// Season 0 (Specials) and zero episode numbers compose fine.
	if got, ok := ForSeason(series, 0); !ok || got != "season-tvdb-296762-0" {
		t.Fatalf("ForSeason specials = (%q,%v)", got, ok)
	}

	// Non-anchored parents fall back (ok=false).
	for _, parent := range []string{"local-deadbeef", "1234567890", "series-", "series-bogus-xyz", ""} {
		if got, ok := ForSeason(parent, 1); ok {
			t.Fatalf("ForSeason(%q) unexpectedly ok with %q", parent, got)
		}
		if got, ok := ForEpisode(parent, 1, 1); ok {
			t.Fatalf("ForEpisode(%q) unexpectedly ok with %q", parent, got)
		}
	}
}

func TestForLocal(t *testing.T) {
	a := ForLocal("/media/movies/Home Video.mkv")
	b := ForLocal("/media/movies/Home Video.mkv")
	if a != b {
		t.Fatalf("ForLocal not deterministic: %q vs %q", a, b)
	}
	if !IsLocal(a) {
		t.Fatalf("ForLocal output %q not recognized as local", a)
	}
	if len(a) != len("local-")+2*localHashLen {
		t.Fatalf("ForLocal width unexpected: %q (len %d)", a, len(a))
	}
	// Whitespace differences normalize to the same id.
	if ForLocal("  /x ") != ForLocal("/x") {
		t.Fatalf("ForLocal whitespace not normalized")
	}
	// Distinct paths produce distinct ids.
	if ForLocal("/a") == ForLocal("/b") {
		t.Fatalf("ForLocal collision on distinct paths")
	}
}

func TestSeriesIDFromContentID(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"episode-tvdb-296762-1-5", "series-tvdb-296762", true},
		{"season-tvdb-296762-1", "series-tvdb-296762", true},
		{"episode-imdb-tt0944947-2-10", "series-imdb-tt0944947", true},
		{"movie-tmdb-228064", "movie-tmdb-228064", true}, // unchanged
		{"series-tvdb-296762", "series-tvdb-296762", true},
		{"local-deadbeefdeadbeefdeadbeefdeadbeef", "", false},
		{"1234567890123456", "", false}, // legacy sonyflake
		{"episode-bogus-xx-1-1", "", false},
		{"episode-tvdb", "", false},
		// Truncated/malformed anchors must fail closed, not masquerade as a
		// valid series anchor (regression: these previously slipped through).
		{"episode-tvdb-296762", "", false},     // missing season/episode
		{"episode-tvdb-296762-1", "", false},   // missing episode
		{"season-tvdb-296762", "", false},      // missing season number
		{"episode-tvdb-296762-1-x", "", false}, // non-numeric episode
		{"season-tvdb-296762-x", "", false},    // non-numeric season
		{"series-tvdb-296762-1", "", false},    // over-long series anchor
	}
	for _, tt := range tests {
		got, ok := SeriesIDFromContentID(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("SeriesIDFromContentID(%q) = (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// TestSeriesTransformMatchesComposition is the load-bearing invariant: the
// series id derived from a composed episode/season id must equal the series id
// the children were composed from. This is what lets the watch-history query
// drop the episodes join.
func TestSeriesTransformMatchesComposition(t *testing.T) {
	for _, series := range []string{"series-tvdb-296762", "series-tmdb-1399", "series-imdb-tt0944947"} {
		ep, ok := ForEpisode(series, 3, 7)
		if !ok {
			t.Fatalf("ForEpisode(%q) not ok", series)
		}
		back, ok := SeriesIDFromContentID(ep)
		if !ok || back != series {
			t.Fatalf("round trip failed: %q -> %q -> (%q,%v)", series, ep, back, ok)
		}
		se, ok := ForSeason(series, 3)
		if !ok {
			t.Fatalf("ForSeason(%q) not ok", series)
		}
		back, ok = SeriesIDFromContentID(se)
		if !ok || back != series {
			t.Fatalf("season round trip failed: %q -> %q -> (%q,%v)", series, se, back, ok)
		}
	}
}

func TestIsProviderAnchored(t *testing.T) {
	anchored := []string{"movie-tmdb-1", "series-tvdb-2", "season-tvdb-2-1", "episode-imdb-tt3-1-1"}
	for _, id := range anchored {
		if !IsProviderAnchored(id) {
			t.Fatalf("IsProviderAnchored(%q) = false, want true", id)
		}
	}
	notAnchored := []string{
		"local-abc", "1234567890", "movie-bogus-x", "movie-tmdb-", "", "movie-",
		// Truncated season/episode and non-numeric suffixes must fail closed.
		"season-tvdb-2", "episode-tvdb-2", "episode-tvdb-2-1", "episode-imdb-tt3-1-x",
		"season-tvdb-2-x", "series-tvdb-2-1",
	}
	for _, id := range notAnchored {
		if IsProviderAnchored(id) {
			t.Fatalf("IsProviderAnchored(%q) = true, want false", id)
		}
	}
}
