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

// CastReceiverSinkV3 is output.current_sink for a play a Cast receiver
// renders. The sender keeps it going without the viewer, so the paused
// timeout exempts it.
const CastReceiverSinkV3 = "cast_receiver"

// WatchTogetherMembersSQL lists the members of Watch Together room r, one
// row per member with the member as value. A room whose runtime holds no
// members object (JSON null for a nil map, or any other type) has none,
// instead of failing the whole statement. The paused timeout's sweep and its
// claim both use it to exempt members.
const WatchTogetherMembersSQL = `jsonb_each(CASE WHEN jsonb_typeof(r.runtime->'members') = 'object'
	THEN r.runtime->'members' ELSE '{}'::jsonb END)`

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
