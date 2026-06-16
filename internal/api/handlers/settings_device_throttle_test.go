package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// Device-setting reads register the device (last_seen_at upsert). A page that
// fetches many settings must not issue one upsert per read for the same device
// — they contend on a single row. The throttle collapses repeats within the
// window to one registration.
func TestShouldRegisterDevice_ThrottlesRepeatWithinWindow(t *testing.T) {
	h := &SettingsHandler{deviceSeen: cache.NewTTLCache[struct{}]()}

	if !h.shouldRegisterDevice("p1", "dev1") {
		t.Fatal("first sighting of a device should register")
	}
	if h.shouldRegisterDevice("p1", "dev1") {
		t.Fatal("repeat sighting within the window should be throttled (no upsert)")
	}
	if !h.shouldRegisterDevice("p1", "dev2") {
		t.Fatal("a different device on the same profile should register")
	}
	if !h.shouldRegisterDevice("p2", "dev1") {
		t.Fatal("the same device id under a different profile should register")
	}
}
