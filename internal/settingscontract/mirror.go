package settingscontract

import (
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/settingskeys"
)

// Key relationships: settings that are two spellings of one preference.
//
// A deprecated key is not free to leave behind. Every shipped client reads
// playback.auto_skip_intro, and revision 7 replaced it with the three-way
// playback.intro_skip_mode, so for one release the server keeps the pair in
// step at write time: a preference set on an old client shows up on a new one
// and the other way round. See docs/design/2026-08-16-intro-skip-mode.md.
//
// The pairing lives here, next to the contract that declares both keys, rather
// than in the handlers. Four write paths need it — the canonical mutation
// route, its delete, the legacy profile route, and the one-time migration
// planner — and a mapping that disagreed between any two of them would be a
// preference that changes meaning depending on which client last touched it.

// Intro-skip modes. These are the enum members playback.intro_skip_mode
// declares; the manifest is the source of truth and Validate proves a default
// or stored value is one of them, but the mirror has to name them to convert.
const (
	IntroSkipModeNever  = "never"
	IntroSkipModeAsk    = "ask"
	IntroSkipModeAlways = "always"
)

// MirroredWrite is the companion row implied by writing another key.
type MirroredWrite struct {
	Key   string
	Value json.RawMessage
}

// MirrorKey names the key whose row travels with this one, and reports whether
// there is one. Used by the delete path, which has no value to convert:
// clearing either half of a mirrored pair clears both, or the surviving row
// would resolve as an explicit choice nobody made.
func MirrorKey(key string) (string, bool) {
	switch key {
	case settingskeys.PlaybackAutoSkipIntro:
		return settingskeys.PlaybackIntroSkipMode, true
	case settingskeys.PlaybackIntroSkipMode:
		return settingskeys.PlaybackAutoSkipIntro, true
	default:
		return "", false
	}
}

// LogicalKey collapses a mirrored pair onto one name, so surfaces that count or
// summarize stored rows describe preferences rather than storage.
//
// A mirrored pair is one preference the server keeps in two spellings for the
// duration of the overlap window. Anything that answers "how many settings does
// this device override" has to say one, or every fleet total, count filter and
// changed-settings badge doubles the moment a household touches the intro
// prompt — and un-doubles again when the mirror is retired, which looks like a
// fleet-wide change nobody made.
//
// Which of the two names survives is arbitrary and never reaches a user; it
// only has to be stable and to agree for both halves, so the pair lands in one
// bucket. Keys with no mirror are returned unchanged.
func LogicalKey(key string) string {
	mirror, ok := MirrorKey(key)
	if !ok || key < mirror {
		return key
	}
	return mirror
}

// MirrorWrite converts a value written at key into the companion row that must
// be written alongside it.
//
// The second result is false when key has no mirror at all, which is the
// common case and not an error. An error means the key does have a mirror but
// the value is not one the pairing can express — impossible for a value that
// came through NormalizeValue, and therefore a defect rather than bad input,
// so callers must surface it rather than skip the companion write.
//
// The boolean direction is lossy on purpose: "never" and "ask" both mean
// "don't skip it for me" to a client that only understands the switch, so both
// map to false. An old client that then flips that switch overwrites "never" —
// accepted for the overlap window, and the reason the mirror is temporary.
func MirrorWrite(key string, value json.RawMessage) (MirroredWrite, bool, error) {
	mirror, ok := MirrorKey(key)
	if !ok {
		return MirroredWrite{}, false, nil
	}

	// Both directions decode into a pointer so JSON null is rejected rather than
	// silently mirrored. encoding/json treats null as "not present" and leaves a
	// bool or string variable at its zero value, which would turn a null body
	// into a stored "ask" — a preference nobody expressed, written on behalf of
	// a request that was malformed.
	switch key {
	case settingskeys.PlaybackAutoSkipIntro:
		var enabled *bool
		if err := json.Unmarshal(value, &enabled); err != nil || enabled == nil {
			return MirroredWrite{}, false, fmt.Errorf(
				"%s: mirroring to %s needs a boolean, got %s", key, mirror, value)
		}
		mode := IntroSkipModeAsk
		if *enabled {
			mode = IntroSkipModeAlways
		}
		encoded, err := json.Marshal(mode)
		if err != nil {
			return MirroredWrite{}, false, fmt.Errorf("%s: encoding %s: %w", key, mirror, err)
		}
		return MirroredWrite{Key: mirror, Value: encoded}, true, nil

	case settingskeys.PlaybackIntroSkipMode:
		var mode *string
		if err := json.Unmarshal(value, &mode); err != nil || mode == nil {
			return MirroredWrite{}, false, fmt.Errorf(
				"%s: mirroring to %s needs a string, got %s", key, mirror, value)
		}
		switch *mode {
		case IntroSkipModeNever, IntroSkipModeAsk, IntroSkipModeAlways:
		default:
			return MirroredWrite{}, false, fmt.Errorf(
				"%s: %q is not an intro skip mode", key, *mode)
		}
		encoded := json.RawMessage("false")
		if *mode == IntroSkipModeAlways {
			encoded = json.RawMessage("true")
		}
		return MirroredWrite{Key: mirror, Value: encoded}, true, nil

	default:
		return MirroredWrite{}, false, nil
	}
}
