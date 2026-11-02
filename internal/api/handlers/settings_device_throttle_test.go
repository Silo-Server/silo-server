package handlers

import (
	"testing"
)

// Device-setting reads register the device (last_seen_at upsert). A page that
// fetches many settings must not issue one upsert per read for the same device
// — they contend on a single row. The throttle collapses repeats within the
// window to one registration.
func TestDeviceSightingsDue_ThrottlesRepeatWithinWindow(t *testing.T) {
	s := NewDeviceSightings()

	if !s.due("p1", "dev1") {
		t.Fatal("first sighting of a device should register")
	}
	if s.due("p1", "dev1") {
		t.Fatal("repeat sighting within the window should be throttled (no upsert)")
	}
	if !s.due("p1", "dev2") {
		t.Fatal("a different device on the same profile should register")
	}
	if !s.due("p2", "dev1") {
		t.Fatal("the same device id under a different profile should register")
	}
}
