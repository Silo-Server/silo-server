package settingscontract

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingskeys"
)

// TestMirroredKeysAgreeInTheContract keeps the pairing honest against the
// manifest. The write path lands the companion row at the identity the caller
// addressed without re-checking it, so the two definitions have to accept the
// same scopes — a pair that drifted apart would let a legal write produce a
// companion the contract forbids.
func TestMirroredKeysAgreeInTheContract(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	for _, key := range []string{
		settingskeys.PlaybackAutoSkipIntro,
		settingskeys.PlaybackIntroSkipMode,
	} {
		mirrorKey, ok := MirrorKey(key)
		if !ok {
			t.Fatalf("%s has no mirror", key)
		}
		if back, _ := MirrorKey(mirrorKey); back != key {
			t.Errorf("MirrorKey(%s) = %s, want the pairing to be symmetric", mirrorKey, back)
		}

		def, found := manifest.Lookup(key)
		if !found {
			t.Fatalf("%s is mirrored but has no definition", key)
		}
		mirrorDef, found := manifest.Lookup(mirrorKey)
		if !found {
			t.Fatalf("%s is mirrored but has no definition", mirrorKey)
		}
		if !def.IsRemote() || !mirrorDef.IsRemote() {
			t.Errorf("%s/%s are mirrored but not both server-stored", key, mirrorKey)
		}
		if !slices.Equal(def.AllowedScopes, mirrorDef.AllowedScopes) {
			t.Errorf("%s allows %v but %s allows %v; a write to one could not be mirrored onto the other",
				key, def.AllowedScopes, mirrorKey, mirrorDef.AllowedScopes)
		}
	}
}
