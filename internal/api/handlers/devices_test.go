package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func seedDevice(t *testing.T, store userstore.UserStore, profileID, deviceID, name string) {
	t.Helper()
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		t.Fatal("store does not implement DeviceRegistry")
	}
	if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID: profileID, DeviceID: deviceID, DeviceName: name, DevicePlatform: "web",
	}); err != nil {
		t.Fatalf("registering %s: %v", deviceID, err)
	}
}
