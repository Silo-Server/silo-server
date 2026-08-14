package storetest

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunDeviceRegistry runs the device-registry conformance tests, centered on
// the custom-name rename semantics. It is exposed separately from RunSuite so
// each backend pins this behavior next to its own migration tests: the custom
// name is user data that registration's header-driven upserts must never
// clobber, and the two backends must agree on when a rename takes effect.
func RunDeviceRegistry(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()

	registryFor := func(t *testing.T) (userstore.UserStore, userstore.DeviceRegistry) {
		t.Helper()
		store := newStore(t)
		registry, ok := store.(userstore.DeviceRegistry)
		if !ok {
			t.Skip("store does not implement DeviceRegistry")
		}
		return store, registry
	}

	register := func(t *testing.T, registry userstore.DeviceRegistry, profileID, name string) {
		t.Helper()
		if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{
			ProfileID: profileID, DeviceID: deviceApple, DeviceName: name, DevicePlatform: "tvOS",
		}); err != nil {
			t.Fatalf("RegisterDevice: %v", err)
		}
	}

	// deviceRow finds one profile's registry row, or nil.
	deviceRow := func(t *testing.T, registry userstore.DeviceRegistry, profileID string) *userstore.DeviceEntry {
		t.Helper()
		devices, err := registry.ListDevices(ctx)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		for _, device := range devices {
			if device.ProfileID == profileID && device.DeviceID == deviceApple {
				return &device
			}
		}
		return nil
	}

	t.Run("RenameSetsCustomNameAndKeepsReportedName", func(t *testing.T) {
		store, registry := registryFor(t)
		seedSettingProfiles(t, ctx, store, "p1")
		register(t, registry, "p1", "Apple TV")

		if row := deviceRow(t, registry, "p1"); row == nil || row.CustomName != "" {
			t.Fatalf("fresh registration = %+v, want an empty custom name", row)
		}
		if err := registry.RenameDevice(ctx, "p1", deviceApple, "Bedroom TV"); err != nil {
			t.Fatalf("RenameDevice: %v", err)
		}
		row := deviceRow(t, registry, "p1")
		if row == nil || row.CustomName != "Bedroom TV" {
			t.Fatalf("after rename = %+v, want custom name %q", row, "Bedroom TV")
		}
		if row.DeviceName != "Apple TV" {
			t.Errorf("rename changed the reported name to %q; it must stay untouched", row.DeviceName)
		}
	})

	t.Run("EmptyNameClears", func(t *testing.T) {
		store, registry := registryFor(t)
		seedSettingProfiles(t, ctx, store, "p1")
		register(t, registry, "p1", "Apple TV")

		if err := registry.RenameDevice(ctx, "p1", deviceApple, "Bedroom TV"); err != nil {
			t.Fatalf("RenameDevice: %v", err)
		}
		if err := registry.RenameDevice(ctx, "p1", deviceApple, ""); err != nil {
			t.Fatalf("clearing rename: %v", err)
		}
		if row := deviceRow(t, registry, "p1"); row == nil || row.CustomName != "" {
			t.Fatalf("after clear = %+v, want an empty custom name", row)
		}
	})

	t.Run("RenameNeverInventsARow", func(t *testing.T) {
		store, registry := registryFor(t)
		seedSettingProfiles(t, ctx, store, "p1")

		if err := registry.RenameDevice(ctx, "p1", deviceApple, "Bedroom TV"); err != nil {
			t.Fatalf("RenameDevice on an unregistered device: %v", err)
		}
		if exists, err := registry.DeviceExists(ctx, "p1", deviceApple); err != nil || exists {
			t.Fatalf("DeviceExists = (%v, %v) after a no-op rename, want (false, nil)", exists, err)
		}
	})

	// Two profiles register the same TV separately, and one profile owns more
	// than one device; a rename lands on exactly the (profile, device) pair it
	// names. The second p1 device is what catches an UPDATE missing the
	// device_id predicate — every other subtest has one device per profile, so
	// "renamed all of p1's devices" would otherwise pass the suite.
	t.Run("RenameIsScopedToOneProfileAndDevice", func(t *testing.T) {
		store, registry := registryFor(t)
		seedSettingProfiles(t, ctx, store, "p1", "p2")
		register(t, registry, "p1", "Apple TV")
		register(t, registry, "p2", "Apple TV")
		const otherDevice = "web-browser"
		if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{
			ProfileID: "p1", DeviceID: otherDevice, DeviceName: "Chrome", DevicePlatform: "web",
		}); err != nil {
			t.Fatalf("RegisterDevice(%s): %v", otherDevice, err)
		}

		if err := registry.RenameDevice(ctx, "p1", deviceApple, "Bedroom TV"); err != nil {
			t.Fatalf("RenameDevice: %v", err)
		}
		if row := deviceRow(t, registry, "p2"); row == nil || row.CustomName != "" {
			t.Fatalf("p2's row = %+v; p1's rename must not leak across profiles", row)
		}
		devices, err := registry.ListDevices(ctx)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		for _, device := range devices {
			if device.ProfileID == "p1" && device.DeviceID == otherDevice && device.CustomName != "" {
				t.Fatalf("p1's other device = %+v; the rename must not touch it", device)
			}
		}
	})

	// The last-seen upsert runs on every request; if it reset the custom name,
	// a rename would survive only until the device was next used.
	t.Run("ReRegistrationPreservesCustomName", func(t *testing.T) {
		store, registry := registryFor(t)
		seedSettingProfiles(t, ctx, store, "p1")
		register(t, registry, "p1", "Apple TV")

		if err := registry.RenameDevice(ctx, "p1", deviceApple, "Bedroom TV"); err != nil {
			t.Fatalf("RenameDevice: %v", err)
		}
		register(t, registry, "p1", "Apple TV 4K")
		row := deviceRow(t, registry, "p1")
		if row == nil || row.CustomName != "Bedroom TV" {
			t.Fatalf("after re-registration = %+v, want custom name %q kept", row, "Bedroom TV")
		}
		if row.DeviceName != "Apple TV 4K" {
			t.Errorf("reported name = %q, want the re-registration's %q", row.DeviceName, "Apple TV 4K")
		}
	})

	t.Run("ForgetDropsCustomNameWithTheRow", func(t *testing.T) {
		store, registry := registryFor(t)
		seedSettingProfiles(t, ctx, store, "p1")
		register(t, registry, "p1", "Apple TV")

		if err := registry.RenameDevice(ctx, "p1", deviceApple, "Bedroom TV"); err != nil {
			t.Fatalf("RenameDevice: %v", err)
		}
		if err := registry.ForgetDevice(ctx, "p1", deviceApple); err != nil {
			t.Fatalf("ForgetDevice: %v", err)
		}
		register(t, registry, "p1", "Apple TV")
		if row := deviceRow(t, registry, "p1"); row == nil || row.CustomName != "" {
			t.Fatalf("re-registration after forget = %+v, want a fresh row with no custom name", row)
		}
	})
}
