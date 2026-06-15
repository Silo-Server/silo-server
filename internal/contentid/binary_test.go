package contentid

import (
	"strings"
	"testing"
)

// TestPackUnpackRoundTrip is the load-bearing property for the reversible
// jellycompat codec: every provider-anchored and local content_id must survive
// Pack -> Unpack unchanged, and the packed form must fit the 15-byte budget a
// compat UUID leaves after its 1-byte kind prefix. This is what lets the compat
// layer decode an item id with no lookup table, so it stays correct across
// server restarts.
func TestPackUnpackRoundTrip(t *testing.T) {
	ids := []string{
		"movie-tmdb-228064",
		"movie-imdb-tt2413338",
		"movie-imdb-tt0944947", // leading zero must be preserved
		"movie-tvdb-12345",
		"series-tvdb-296762",
		"series-tmdb-1399",
		"series-imdb-tt0944947",
		"season-tvdb-296762-1",
		"season-tvdb-296762-0", // specials
		"episode-tvdb-296762-1-5",
		"episode-imdb-tt0944947-2-10",
		"episode-tmdb-1399-10-100",
		// 14-byte (28 hex) local hash — the post-change ForLocal width.
		"local-0123456789abcdef0123456789ab",
		ForLocal("/media/movies/Home Video.mkv"),
	}
	for _, id := range ids {
		data, ok := Pack(id)
		if !ok {
			t.Fatalf("Pack(%q) ok=false, want true", id)
		}
		if len(data) > 15 {
			t.Fatalf("Pack(%q) = %d bytes, exceeds 15-byte compat budget", id, len(data))
		}
		got, ok := Unpack(data)
		if !ok || got != id {
			t.Fatalf("round trip %q -> %x -> (%q,%v)", id, data, got, ok)
		}
	}
}

// TestPackRejectsNonContentID: Pack handles only the structured and local
// namespaces. Legacy numeric Sonyflake ids and anything malformed return
// ok=false so the caller falls back to its numeric/opaque path.
func TestPackRejectsNonContentID(t *testing.T) {
	for _, id := range []string{
		"",
		"1234567890123456", // legacy Sonyflake
		"garbage",
		"movie-bogus-x",       // invalid provider id
		"episode-tvdb-296762", // truncated (missing season/episode)
		"movie-",
		"local-",    // empty hash
		"local-xyz", // non-hex hash
	} {
		if data, ok := Pack(id); ok {
			t.Fatalf("Pack(%q) = (%x, true), want ok=false", id, data)
		}
	}
}

// TestUnpackRejectsGarbage: Unpack must fail closed on bytes that were not
// produced by Pack rather than returning a plausible-looking id.
func TestUnpackRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{},
		{0x00},
		{0xff, 0xff, 0xff},
		[]byte(strings.Repeat("\x01", 15)),
	} {
		if id, ok := Unpack(b); ok {
			t.Fatalf("Unpack(%x) = (%q, true), want ok=false", b, id)
		}
	}
}

// TestUnpackLocalRequiresExactLength: unlike the padded structured forms, the
// local form fills the payload exactly, so its body must be exactly
// localHashLen. Trailing or missing bytes are non-canonical and must fail closed.
func TestUnpackLocalRequiresExactLength(t *testing.T) {
	exact := append([]byte{tagLocal}, make([]byte, localHashLen)...)
	if _, ok := Unpack(exact); !ok {
		t.Fatalf("Unpack(exact-length local body) = false, want true")
	}
	for _, n := range []int{localHashLen - 1, localHashLen + 1} {
		b := append([]byte{tagLocal}, make([]byte, n)...)
		if id, ok := Unpack(b); ok {
			t.Fatalf("Unpack(local body len %d) = (%q, true), want fail-closed", n, id)
		}
	}
}
