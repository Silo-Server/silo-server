package queryattr

// callerThrough runs plant, then caller, depth frames below its first call. Its
// own frames are database frames, so caller skips them.
//
//go:noinline
func callerThrough(depth int, plant func()) *site {
	if depth == 0 {
		plant()
		return caller()
	}
	return callerThrough(depth-1, plant)
}
