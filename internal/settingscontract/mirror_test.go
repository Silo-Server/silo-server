package settingscontract

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingskeys"
)

func TestMirrorWriteConvertsBothDirections(t *testing.T) {
	for name, tc := range map[string]struct {
		key       string
		value     string
		wantKey   string
		wantValue string
	}{
		"switch on means always": {
			settingskeys.PlaybackAutoSkipIntro, `true`,
			settingskeys.PlaybackIntroSkipMode, `"always"`,
		},
		"switch off means ask": {
			settingskeys.PlaybackAutoSkipIntro, `false`,
			settingskeys.PlaybackIntroSkipMode, `"ask"`,
		},
		"always means switch on": {
			settingskeys.PlaybackIntroSkipMode, `"always"`,
			settingskeys.PlaybackAutoSkipIntro, `true`,
		},
		"ask means switch off": {
			settingskeys.PlaybackIntroSkipMode, `"ask"`,
			settingskeys.PlaybackAutoSkipIntro, `false`,
		},
		// The lossy edge the design accepts: a client that only knows the
		// switch cannot express "leave intros alone", and false is the closer
		// of the two answers it can give.
		"never also means switch off": {
			settingskeys.PlaybackIntroSkipMode, `"never"`,
			settingskeys.PlaybackAutoSkipIntro, `false`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			mirror, ok, err := MirrorWrite(tc.key, json.RawMessage(tc.value))
			if err != nil {
				t.Fatalf("MirrorWrite(%s, %s): %v", tc.key, tc.value, err)
			}
			if !ok {
				t.Fatalf("MirrorWrite(%s) reported no mirror", tc.key)
			}
			if mirror.Key != tc.wantKey {
				t.Errorf("mirror key = %s, want %s", mirror.Key, tc.wantKey)
			}
			if string(mirror.Value) != tc.wantValue {
				t.Errorf("mirror value = %s, want %s", mirror.Value, tc.wantValue)
			}
		})
	}
}

// TestMirrorWriteRoundTripsThroughTheBoolean pins the one-way loss: every mode
// survives a round trip except "never", which the boolean cannot hold.
func TestMirrorWriteRoundTripsThroughTheBoolean(t *testing.T) {
	for _, mode := range []string{IntroSkipModeNever, IntroSkipModeAsk, IntroSkipModeAlways} {
		encoded, err := json.Marshal(mode)
		if err != nil {
			t.Fatalf("encoding %q: %v", mode, err)
		}
		boolean, _, err := MirrorWrite(settingskeys.PlaybackIntroSkipMode, encoded)
		if err != nil {
			t.Fatalf("MirrorWrite(%q): %v", mode, err)
		}
		back, _, err := MirrorWrite(settingskeys.PlaybackAutoSkipIntro, boolean.Value)
		if err != nil {
			t.Fatalf("MirrorWrite back from %s: %v", boolean.Value, err)
		}
		want := mode
		if mode == IntroSkipModeNever {
			want = IntroSkipModeAsk
		}
		if string(back.Value) != `"`+want+`"` {
			t.Errorf("%q round-tripped to %s, want %q", mode, back.Value, want)
		}
	}
}

func TestMirrorWriteIgnoresUnpairedKeys(t *testing.T) {
	for _, key := range []string{
		settingskeys.PlaybackAutoSkipCredits,
		settingskeys.PlaybackSubtitleMode,
		"totally.invented.key",
	} {
		if _, ok, err := MirrorWrite(key, json.RawMessage(`true`)); ok || err != nil {
			t.Errorf("MirrorWrite(%s) = ok %v err %v, want no mirror and no error", key, ok, err)
		}
		if _, ok := MirrorKey(key); ok {
			t.Errorf("MirrorKey(%s) reported a mirror", key)
		}
	}
}

// TestMirrorWriteRefusesValuesItCannotConvert: the callers write the companion
// row on the strength of this result, so a value the pairing does not
// understand has to be an error rather than a silently skipped write.
func TestMirrorWriteRefusesValuesItCannotConvert(t *testing.T) {
	for name, tc := range map[string]struct{ key, value string }{
		"boolean key given a string": {settingskeys.PlaybackAutoSkipIntro, `"yes"`},
		"enum key given a boolean":   {settingskeys.PlaybackIntroSkipMode, `true`},
		"enum key given a non-member": {
			settingskeys.PlaybackIntroSkipMode, `"sideways"`,
		},
		// encoding/json reads null as "not present" and leaves a bool or string
		// at its zero value, so an unguarded decode would mirror null onto
		// "ask" — a stored preference invented from a malformed request.
		"boolean key given null": {settingskeys.PlaybackAutoSkipIntro, `null`},
		"enum key given null":    {settingskeys.PlaybackIntroSkipMode, `null`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok, err := MirrorWrite(tc.key, json.RawMessage(tc.value)); err == nil || ok {
				t.Errorf("MirrorWrite(%s, %s) = ok %v err %v, want an error", tc.key, tc.value, ok, err)
			}
		})
	}
}

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
