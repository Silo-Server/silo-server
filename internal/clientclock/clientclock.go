// Package clientclock bounds event times that client devices report against
// the server clock. Device clocks drift, so a time slightly ahead of the
// server's is normal; one far ahead must not win a last-write-wins comparison.
package clientclock

import "time"

// MaxSkew is how far ahead of server time a client event time may sit.
const MaxSkew = 2 * time.Minute

// TooFarAhead reports whether a client event time is more than MaxSkew ahead
// of now.
func TooFarAhead(client, now time.Time) bool {
	return client.After(now.Add(MaxSkew))
}

// Clamp bounds a client event time to at most now+MaxSkew: a zero value or one
// past the window becomes now, so a skewed or malicious clock can at most
// claim "now" and never lock in a far-future last-write-wins win.
func Clamp(client, now time.Time) time.Time {
	if client.IsZero() || TooFarAhead(client, now) {
		return now
	}
	return client
}
