//go:build !linux && !darwin

package jellycompat

// processToken has no portable implementation. Returning the empty
// "unknown" token keeps lock recovery on the age plus liveness fallback, which
// is the behaviour every platform had before the Linux and Darwin readers
// existed.
func processToken(int) string {
	return ""
}
