package recipes

import "time"

// Clock returns the "current" time. Resolvers must use a Clock from the registry
// or ResolverContext, never time.Now() directly.
type Clock interface {
	Now() time.Time
}

// RealClock returns time.Now(). Used in production wiring.
type RealClock struct{}

// Now returns the system time.
func (RealClock) Now() time.Time { return time.Now() }
