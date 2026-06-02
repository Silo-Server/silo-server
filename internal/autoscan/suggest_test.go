package autoscan

import (
	"testing"
)

func TestSuggestRewrites(t *testing.T) {
	silo := []string{
		"/mnt/media/happy/storage2/tvshows1",
		"/mnt/media/happy4k/4ktv7",
		"/mnt/media/storage/Anime/Subs",
		"/mnt/media/storage/Anime2/Subs",
		"/library/Films",
		"/tank/television/Show",
	}

	t.Run("multi-segment unique match", func(t *testing.T) {
		got := suggestRewrites([]string{"/mnt/happy/storage2/tvshows1"}, silo, nil)
		// shares trailing happy/storage2/tvshows1 = 3 segments
		if len(got.Proposed) != 1 || got.Proposed[0].To != "/mnt/media/happy/storage2/tvshows1" || got.Proposed[0].MatchDepth != 3 {
			t.Fatalf("proposed=%+v", got.Proposed)
		}
	})

	t.Run("single-segment unique match (different parents)", func(t *testing.T) {
		got := suggestRewrites([]string{"/mnt/kodama/storage2/4ktv7"}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].To != "/mnt/media/happy4k/4ktv7" || got.Proposed[0].MatchDepth != 1 {
			t.Fatalf("proposed=%+v", got.Proposed)
		}
	})

	t.Run("longest-suffix disambiguation", func(t *testing.T) {
		got := suggestRewrites([]string{"/mnt/kodama/storage1/Anime/Subs"}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].To != "/mnt/media/storage/Anime/Subs" || got.Proposed[0].MatchDepth != 2 {
			t.Fatalf("expected Anime/Subs (depth 2), got %+v", got.Proposed)
		}
		if len(got.Ambiguous) != 0 {
			t.Fatalf("should not be ambiguous: %+v", got.Ambiguous)
		}
	})

	t.Run("unmatched when no shared segment", func(t *testing.T) {
		got := suggestRewrites([]string{"/data/Movies"}, silo, nil)
		if len(got.Unmatched) != 1 || got.Unmatched[0] != "/data/Movies" || len(got.Proposed) != 0 {
			t.Fatalf("got=%+v", got)
		}
	})

	t.Run("leaf match across unlike layouts", func(t *testing.T) {
		got := suggestRewrites([]string{"/srv/tv/Show"}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].To != "/tank/television/Show" {
			t.Fatalf("got=%+v", got)
		}
	})

	t.Run("ambiguous tie", func(t *testing.T) {
		got := suggestRewrites([]string{"/foo/bar/Subs"}, silo, nil)
		if len(got.Ambiguous) != 1 || len(got.Ambiguous[0].Candidates) != 2 {
			t.Fatalf("expected ambiguous with 2 candidates, got %+v", got.Ambiguous)
		}
	})

	t.Run("covered by existing rule", func(t *testing.T) {
		existing := []PathRewrite{{From: "/mnt/happy", To: "/mnt/media/happy"}}
		got := suggestRewrites([]string{"/mnt/happy/storage2/tvshows1"}, silo, existing)
		if len(got.Covered) != 1 || len(got.Proposed) != 0 {
			t.Fatalf("expected covered, got %+v", got)
		}
	})

	t.Run("normalization: trailing slash, backslashes, dup slashes", func(t *testing.T) {
		got := suggestRewrites([]string{`\mnt\happy\\storage2\tvshows1\`}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].From != "/mnt/happy/storage2/tvshows1" || got.Proposed[0].To != "/mnt/media/happy/storage2/tvshows1" {
			t.Fatalf("normalization failed: %+v", got.Proposed)
		}
	})

	t.Run("covered by a Windows-style existing rule (normalized)", func(t *testing.T) {
		existing := []PathRewrite{{From: `\mnt\happy`, To: "/mnt/media/happy"}}
		got := suggestRewrites([]string{"/mnt/happy/storage2/tvshows1"}, silo, existing)
		if len(got.Covered) != 1 || len(got.Proposed) != 0 {
			t.Fatalf("backslash From should cover the root: %+v", got)
		}
	})

	t.Run("duplicate arr roots collapse to one proposal", func(t *testing.T) {
		got := suggestRewrites([]string{"/mnt/happy/storage2/tvshows1", "/mnt/happy/storage2/tvshows1/"}, silo, nil)
		if len(got.Proposed) != 1 {
			t.Fatalf("dup roots should yield 1 proposal, got %+v", got.Proposed)
		}
	})

	t.Run("no-op (arr path equals Silo path) is not proposed", func(t *testing.T) {
		got := suggestRewrites([]string{"/library/Films"}, silo, nil)
		if len(got.Proposed) != 0 || len(got.Unmatched) != 0 {
			t.Fatalf("from==to should be skipped, got %+v", got)
		}
	})

	t.Run("duplicate Silo paths do not create duplicate candidates", func(t *testing.T) {
		dupSilo := []string{"/mnt/media/storage/Anime/Subs", "/mnt/media/storage/Anime/Subs"}
		got := suggestRewrites([]string{"/mnt/kodama/storage1/Anime/Subs"}, dupSilo, nil)
		if len(got.Proposed) != 1 || len(got.Ambiguous) != 0 {
			t.Fatalf("dup silo paths should still be a unique match, got %+v", got)
		}
	})
}

func TestCommonSuffixLen(t *testing.T) {
	cases := []struct {
		a, b []string
		want int
	}{
		{[]string{"a", "b", "c"}, []string{"x", "b", "c"}, 2},
		{[]string{"4ktv7"}, []string{"happy4k", "4ktv7"}, 1},
		{[]string{"4ktv7"}, []string{"4ktv70"}, 0},
		{[]string{"a"}, []string{"b"}, 0},
	}
	for _, tc := range cases {
		if got := commonSuffixLen(tc.a, tc.b); got != tc.want {
			t.Fatalf("commonSuffixLen(%v,%v)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
