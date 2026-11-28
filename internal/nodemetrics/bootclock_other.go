//go:build !linux

package nodemetrics

// bootClockTicks has no boot clock to read off Linux; the sampler publishes no
// attribution there anyway.
func bootClockTicks() (uint64, bool) { return 0, false }
