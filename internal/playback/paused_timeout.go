package playback

import (
	"strconv"
	"strings"
	"time"
)

// The server setting that ends a native session left paused too long, and
// the value that applies while it has no stored value. The admin settings
// defaults and validation reference these so the settings form and the
// runtime agree. Zero turns the timeout off.
const (
	PausedSessionTimeoutSettingKey     = "playback.paused_session_timeout_minutes"
	DefaultPausedSessionTimeoutMinutes = 15
	MaxPausedSessionTimeoutMinutes     = 1440
)

// ParsePausedSessionTimeout resolves a stored paused-session timeout. An
// empty or unreadable value takes the default, so a row the admin never saved
// still ends sessions; zero (or less) disables the timeout, and a value past
// the maximum is held to it.
func ParsePausedSessionTimeout(raw string) time.Duration {
	minutes := DefaultPausedSessionTimeoutMinutes
	if configured, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		minutes = min(max(configured, 0), MaxPausedSessionTimeoutMinutes)
	}
	return time.Duration(minutes) * time.Minute
}
