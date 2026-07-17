package playback

import "testing"

func TestParseTrackIDV3StrictCanonicalRoundTrip(t *testing.T) {
	for _, id := range []string{"file:1:audio:0", "file:42:subtitle:7", "file:10:audio:10", "file:987:subtitle:100"} {
		fileID, kind, ordinal, ok := ParseTrackIDV3(id)
		if !ok {
			t.Fatalf("canonical id rejected: %s", id)
		}
		if rebuilt := TrackIDV3(fileID, kind, ordinal); rebuilt != id {
			t.Fatalf("round trip: parsed %s rebuilt as %s", id, rebuilt)
		}
	}
}

func TestParseTrackIDV3RejectsNonCanonicalNumerics(t *testing.T) {
	invalid := []string{
		"file:007:audio:1",                  // leading zeros in file id
		"file:7:audio:01",                   // leading zeros in ordinal
		"file:7:audio:+1",                   // sign
		"file:+7:audio:1",                   // sign
		"file:7:audio:-1",                   // sign
		"file:0:audio:1",                    // file ids are positive
		"file:7:video:1",                    // unknown kind
		"file:7:audio:",                     // empty ordinal
		"file::audio:1",                     // empty file id
		"file:7:audio:1:2",                  // extra segment
		"file:7:audio: 1",                   // whitespace
		"file:7:audio:1e2",                  // exponent form
		"file:99999999999999999999:audio:1", // overflow
	}
	for _, id := range invalid {
		if _, _, _, ok := ParseTrackIDV3(id); ok {
			t.Errorf("non-canonical id accepted: %q", id)
		}
	}
}
