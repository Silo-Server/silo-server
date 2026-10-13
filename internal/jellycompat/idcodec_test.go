package jellycompat

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

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

// hashedItemIDStartingWith returns a content_id that does not pack, so the
// codec hashes it, whose compat id starts with first, as about one hash in 256
// does.
func hashedItemIDStartingWith(t *testing.T, c *ResourceIDCodec, first EncodedIDType) (id, raw string) {
	t.Helper()
	for i := 0; i < 100000; i++ {
		id = fmt.Sprintf("jf12-multi-%d", i)
		if _, packed := packContentIDUUID(EncodedIDItem, id); packed {
			t.Fatalf("%q packs; the test needs an id that is hashed", id)
		}
		raw = c.EncodeStringID(EncodedIDItem, id)
		if uuid.MustParse(raw)[0] == byte(first) {
			return id, raw
		}
	}
	t.Fatalf("no hashed item id starts with byte %d", first)
	return "", ""
}

// TestHashedIDStartingWithKindByteDecodes: the numeric decoding must not claim
// a hashed id that merely starts with a kind's byte. An item id starting with
// the item byte decodes through the reverse map, and one starting with another
// numeric kind's byte is not read as that kind, so /Items/{id}, which tries
// numeric kinds first, reaches the item.
func TestHashedIDStartingWithKindByteDecodes(t *testing.T) {
	c := NewResourceIDCodec()
	id, raw := hashedItemIDStartingWith(t, c, EncodedIDItem)
	if got, err := c.DecodeStringID(EncodedIDItem, raw); err != nil || got != id {
		t.Fatalf("DecodeStringID(%q) = (%q, %v), want (%q, nil)", raw, got, err, id)
	}
	_, raw = hashedItemIDStartingWith(t, c, EncodedIDLibrary)
	if got, err := c.DecodeIntID(EncodedIDLibrary, raw); err == nil {
		t.Fatalf("DecodeIntID(library, %q) = %d, want an error for a hashed item id", raw, got)
	}
	if got, err := c.DecodeIntID(EncodedIDLibrary, c.EncodeIntID(EncodedIDLibrary, 42)); err != nil || got != 42 {
		t.Fatalf("library id round trip = (%d, %v), want (42, nil)", got, err)
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

// TestPlaySessionIDsAreNotRetained guards the reverse map against unbounded
// growth: play-session ids are minted from a fresh random UUID per playback
// and never decoded back, so encoding them must not leave an entry behind.
// Other hashed kinds (genres, studios) dedupe by value and plateau at catalog
// size; a retained play-session entry would live until restart.
func TestPlaySessionIDsAreNotRetained(t *testing.T) {
	c := NewResourceIDCodec()
	for i := 0; i < 3; i++ {
		if u := c.EncodeStringID(EncodedIDPlaySession, uuidNewString()); u == "" {
			t.Fatal("EncodeStringID returned empty play session id")
		}
	}
	c.mu.RLock()
	n := len(c.reverse)
	c.mu.RUnlock()
	if n != 0 {
		t.Fatalf("reverse map retained %d play session entries, want 0", n)
	}
}
