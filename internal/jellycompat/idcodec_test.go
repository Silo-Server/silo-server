package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/contentid"
)

// TestContentIDDecodesWithoutSharedState is the core of the restart-safety fix:
// encoding and decoding run on DIFFERENT codec instances, modeling a server
// restart where the decoder's in-memory reverse map is empty. A structured
// content_id must still decode, because it is packed into the UUID reversibly
// rather than hashed into a side table.
func TestContentIDDecodesWithoutSharedState(t *testing.T) {
	cases := []struct {
		kind EncodedIDType
		id   string
	}{
		{EncodedIDItem, "movie-tmdb-228064"},
		{EncodedIDItem, "movie-imdb-tt2413338"},
		{EncodedIDItem, "series-tvdb-296762"},
		{EncodedIDItem, "episode-tvdb-296762-1-5"},
		{EncodedIDItem, contentid.ForLocal("/media/movies/Home Video.mkv")},
		{EncodedIDSeason, "season-tvdb-296762-1"},
	}
	for _, tc := range cases {
		enc := NewResourceIDCodec()
		dec := NewResourceIDCodec() // fresh instance: cold reverse map
		u := enc.EncodeStringID(tc.kind, tc.id)
		got, err := dec.DecodeStringID(tc.kind, u)
		if err != nil {
			t.Fatalf("DecodeStringID(%d, %q) on fresh codec: %v (id %q)", tc.kind, u, err, tc.id)
		}
		if got != tc.id {
			t.Fatalf("round trip across instances: %q -> %q -> %q", tc.id, u, got)
		}
	}
}

// TestLegacyNumericContentIDRoundTrips guards the unchanged path: non-anchored
// items (audiobooks, collisions, unmatched) keep their numeric Sonyflake
// content_id, which was already encoded statelessly and must stay that way.
func TestLegacyNumericContentIDRoundTrips(t *testing.T) {
	const legacy = "1234567890123456"
	u := NewResourceIDCodec().EncodeStringID(EncodedIDItem, legacy)
	got, err := NewResourceIDCodec().DecodeStringID(EncodedIDItem, u)
	if err != nil || got != legacy {
		t.Fatalf("legacy numeric round trip = (%q, %v), want (%q, nil)", got, err, legacy)
	}
}

// TestGenreNameStillUsesReverseMap guards that arbitrary string ids (genre
// names, etc.) are untouched by the content_id packing and still round-trip
// within a single codec instance via the reverse map.
func TestGenreNameStillUsesReverseMap(t *testing.T) {
	c := NewResourceIDCodec()
	const genre = "Science Fiction"
	u := c.EncodeStringID(EncodedIDGenre, genre)
	got, err := c.DecodeStringID(EncodedIDGenre, u)
	if err != nil || got != genre {
		t.Fatalf("genre round trip = (%q, %v), want (%q, nil)", got, err, genre)
	}
}
