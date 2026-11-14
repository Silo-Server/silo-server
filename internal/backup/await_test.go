package backup

import (
	"testing"
	"time"
)

// awaitTimeout bounds every wait a test makes on a channel it made, so a
// goroutine that fails or stalls fails the test, naming the wait, instead
// of hanging the package until the test binary times out.
const awaitTimeout = 2 * time.Minute

// await receives from ch, failing the test when nothing arrives within
// awaitTimeout.
func await[T any](t testing.TB, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(awaitTimeout):
		t.Fatalf("waited %s for %s", awaitTimeout, what)
		var zero T
		return zero
	}
}

// awaitBefore receives from ch while a goroutine runs, failing the test
// with what the goroutine returned when it returns first (it failed before
// the point ch marks), and after awaitTimeout.
func awaitBefore[T, R any](t testing.TB, ch <-chan T, returned <-chan R, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case r := <-returned:
		t.Fatalf("returned %v before %s", r, what)
	case <-time.After(awaitTimeout):
		t.Fatalf("waited %s for %s", awaitTimeout, what)
	}
	var zero T
	return zero
}
