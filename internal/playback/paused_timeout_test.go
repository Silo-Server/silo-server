package playback

import (
	"testing"
	"time"
)

// A row the administrator never saved ends paused sessions after the
// default; zero turns the timeout off.
func TestParsePausedSessionTimeout(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"":     DefaultPausedSessionTimeoutMinutes * time.Minute,
		"soon": DefaultPausedSessionTimeoutMinutes * time.Minute,
		"0":    0,
		"45":   45 * time.Minute,
	} {
		if got := ParsePausedSessionTimeout(raw); got != want {
			t.Errorf("ParsePausedSessionTimeout(%q) = %v, want %v", raw, got, want)
		}
	}
}
