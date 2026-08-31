package jellycompat

import (
	"net/http/httptest"
	"strings"
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

func TestUserCollectionIDDecodesWithoutSharedState(t *testing.T) {
	for _, id := range []string{
		"731d3da2-4f4b-4a71-8f2f-38e1d34775b0",
		"02c40000-0000-4000-8000-000000000000",
		"04c40000-0000-4000-8000-000000000000",
		"00000000-0000-4000-8000-000000000000",
		"ffffffff-ffff-4fff-bfff-ffffffffffff",
	} {
		t.Run(id, func(t *testing.T) {
			encoded := NewResourceIDCodec().EncodeStringID(EncodedIDUserCollection, id)
			if encoded == id {
				t.Fatal("personal route ID must carry a source marker")
			}
			if adminID := NewResourceIDCodec().EncodeStringID(EncodedIDCollection, id); adminID == encoded {
				t.Fatal("personal and admin collections must have distinct route IDs")
			}
			for _, route := range []string{encoded, strings.ReplaceAll(encoded, "-", "")} {
				codec := NewResourceIDCodec()
				got, err := codec.DecodeStringID(EncodedIDUserCollection, route)
				if err != nil || got != id {
					t.Fatalf("user collection round trip = (%q, %v), want (%q, nil)", got, err, id)
				}
				for kind := EncodedIDLibrary; kind < EncodedIDUserCollection; kind++ {
					if _, err := codec.DecodeStringID(kind, route); err == nil {
						t.Errorf("personal collection route decoded as kind %d", kind)
					}
				}
				query := parseItemsQuery(httptest.NewRequest("GET", "/Items?ParentId="+route+"&Ids="+route, nil), codec)
				if query.parentPersonalCollectionID != id || len(query.specificPersonalCollectionIDs) != 1 || query.specificPersonalCollectionIDs[0] != id {
					t.Errorf("personal route was misclassified: %+v", query)
				}
			}
		})
	}
	for _, id := range []string{"movie-tmdb-228064", "local-00000000b0000000000000000000"} {
		for _, kind := range []EncodedIDType{EncodedIDItem, EncodedIDSeason} {
			contentRoute := NewResourceIDCodec().EncodeStringID(kind, id)
			if _, ok := unpackUserCollectionUUID(contentRoute); ok {
				t.Errorf("content route %q decoded as a personal collection", contentRoute)
			}
		}
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
