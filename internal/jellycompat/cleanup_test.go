package jellycompat

import (
	"fmt"
	"testing"
	"time"
)

func TestCleanupExpiredDeviceProfilesDrainsBacklog(t *testing.T) {
	now := time.Now()
	store := NewDeviceProfileStore(time.Hour, func() time.Time { return now })
	for i := range 2501 {
		store.Put(fmt.Sprintf("expired-%d", i), DeviceProfile{})
	}
	now = now.Add(2 * time.Hour)
	store.Put("active", DeviceProfile{Name: "active"})
	deleted, err := cleanupExpiredDeviceProfiles(t.Context(), store)
	if err != nil || deleted != 2501 || len(store.profiles) != 1 {
		t.Fatalf("deleted %d remaining %d err %v", deleted, len(store.profiles), err)
	}
	if _, ok := store.Get("active"); !ok {
		t.Fatal("removed active registration")
	}
}
