package models

// VideoDurationImplausible reports signs of a malformed video timestamp.
// The absolute check catches tiny reported durations on large files. The
// size-to-duration ratio also catches longer values that imply impossible
// bitrates, such as a 100 GB file claiming to run for 61 seconds.
func VideoDurationImplausible(seconds float64, sizeBytes int64, hasVideo bool) bool {
	if !hasVideo || seconds <= 0 || sizeBytes <= 0 {
		return false
	}
	return seconds <= 10 && sizeBytes >= 100*1024*1024 ||
		float64(sizeBytes)*8/seconds > 1_000_000_000
}
