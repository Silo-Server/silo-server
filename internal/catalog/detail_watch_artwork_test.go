package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGetWatchDetailArtwork covers #803 through GetWatchDetail itself: a
// movie carries its own poster and backdrop, an episode its still as the
// poster and its series' backdrop, and a local extra its parent's artwork.
// An item without artwork leaves the fields empty.
func TestGetWatchDetailArtwork(t *testing.T) {
	f := newVersionsFixture(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"movie", "series"} {
		exec(`UPDATE media_items SET poster_path=$2, poster_thumbhash=$3, backdrop_path=$4, backdrop_thumbhash=$5 WHERE content_id=$1`,
			f.ids[kind], "tmdb/"+kind+"/poster.jpg", kind+"-poster-hash", "tmdb/"+kind+"/backdrop.jpg", kind+"-backdrop-hash")
	}
	exec(`UPDATE episodes SET still_path=$2, still_thumbhash=$3 WHERE content_id=$1`, f.ids["episode"], "tmdb/episode/still.jpg", "episode-still-hash")

	filter := AccessFilter{UserID: 1, ProfileID: "profile-1", ImageSize: "medium"}
	presign := func(path, kind string) string {
		return f.svc.PresignImageURL(t.Context(), path, kind, string(filter.ImageSize))
	}
	cases := []struct {
		name, id                                 string
		posterURL, posterHash, backURL, backHash string
	}{
		{"movie", f.ids["movie"], presign("tmdb/movie/poster.jpg", "poster"), "movie-poster-hash", presign("tmdb/movie/backdrop.jpg", "backdrop"), "movie-backdrop-hash"},
		{"episode", f.ids["episode"], presign("tmdb/episode/still.jpg", "still"), "episode-still-hash", presign("tmdb/series/backdrop.jpg", "backdrop"), "series-backdrop-hash"},
		{"extra", f.ids["extra"], presign("tmdb/movie/poster.jpg", "poster"), "movie-poster-hash", presign("tmdb/movie/backdrop.jpg", "backdrop"), "movie-backdrop-hash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail, err := f.svc.GetWatchDetail(t.Context(), tc.id, filter)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(detail.PosterURL, "https://images.invalid/") || !strings.HasPrefix(detail.BackdropURL, "https://images.invalid/") {
				t.Fatalf("artwork URLs not resolved through the image resolver: poster=%q backdrop=%q", detail.PosterURL, detail.BackdropURL)
			}
			if detail.PosterURL != tc.posterURL || detail.PosterThumbhash != tc.posterHash {
				t.Fatalf("poster = %q/%q, want %q/%q", detail.PosterURL, detail.PosterThumbhash, tc.posterURL, tc.posterHash)
			}
			if detail.BackdropURL != tc.backURL || detail.BackdropThumbhash != tc.backHash {
				t.Fatalf("backdrop = %q/%q, want %q/%q", detail.BackdropURL, detail.BackdropThumbhash, tc.backURL, tc.backHash)
			}
		})
	}

	t.Run("no artwork", func(t *testing.T) {
		exec(`UPDATE media_items SET poster_path='', poster_thumbhash='', backdrop_path='', backdrop_thumbhash='' WHERE content_id=$1`, f.ids["movie"])
		detail, err := f.svc.GetWatchDetail(t.Context(), f.ids["movie"], filter)
		if err != nil {
			t.Fatal(err)
		}
		if detail.PosterURL != "" || detail.BackdropURL != "" || detail.PosterThumbhash != "" || detail.BackdropThumbhash != "" {
			t.Fatalf("artwork for an item without any: poster=%q backdrop=%q", detail.PosterURL, detail.BackdropURL)
		}
	})
}

func TestWatchDetailLegacyJSONOmitsArtwork(t *testing.T) {
	detail := WatchDetail{
		ContentID: "movie:test", Title: "Test",
		PosterURL: "https://images.invalid/poster.jpg", PosterThumbhash: "poster-hash",
		BackdropURL: "https://images.invalid/backdrop.jpg", BackdropThumbhash: "backdrop-hash",
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"poster_url", "poster_thumbhash", "backdrop_url", "backdrop_thumbhash"} {
		if _, exists := body[key]; exists {
			t.Errorf("frozen v1 watch JSON includes %s", key)
		}
	}
	if _, exists := body["content_id"]; !exists {
		t.Fatal("watch JSON lost content_id")
	}
}

func TestGetWatchDetailOmitsArtworkResolution(t *testing.T) {
	f := newVersionsFixture(t)
	for _, kind := range []string{"movie", "series"} {
		if _, err := f.pool.Exec(t.Context(), `UPDATE media_items SET poster_path=$2, backdrop_path=$3 WHERE content_id=$1`,
			f.ids[kind], "provider://poster", "provider://backdrop"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE episodes SET still_path=$2 WHERE content_id=$1`, f.ids["episode"], "provider://still"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"movie", "episode", "extra"} {
		t.Run(kind, func(t *testing.T) {
			f.images.calls.Store(0)
			detail, err := f.svc.GetWatchDetail(t.Context(), f.ids[kind], AccessFilter{
				UserID: 1, ProfileID: "profile-1", OmitWatchArtwork: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls := f.images.calls.Load(); calls != 0 {
				t.Fatalf("source lookup made %d artwork resolver calls", calls)
			}
			if len(detail.Versions) == 0 {
				t.Fatal("source lookup lost playable versions")
			}
			if detail.PosterURL != "" || detail.BackdropURL != "" {
				t.Fatal("source lookup included artwork")
			}
		})
	}
}
