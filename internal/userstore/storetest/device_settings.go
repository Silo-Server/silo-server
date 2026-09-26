package storetest

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func RunDeviceSettings(t *testing.T, newStore func(*testing.T) userstore.UserStore) {
	store := newStore(t)
	devices, ok := store.(userstore.DeviceSettingsStore)
	if !ok {
		t.Fatal("missing device settings implementation")
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		t.Fatal("missing device registry implementation")
	}
	ctx := t.Context()
	for _, profile := range []string{"a", "b"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: profile}); err != nil {
			t.Fatal(err)
		}
		for _, device := range []string{"one", "two"} {
			if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{ProfileID: profile, DeviceID: device, LastSeenAt: "2026-01-02T03:04:05.123456Z"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{Key: "theme", Scope: settingscontract.ScopeProfileDevice, ProfileID: profile, DeviceID: device}, json.RawMessage(`"dark"`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	opts := userstore.DevicePageOptions{Limit: 1}
	var got []string
	for {
		page, err := devices.ListDeviceSettingsPage(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		item := page[0]
		if item.ChangedCount != 1 || item.ProfileName != item.ProfileID {
			t.Fatalf("bad projection: %+v", item)
		}
		got = append(got, item.ProfileID+"/"+item.DeviceID)
		opts.After = &userstore.DevicePosition{LastSeenAt: item.LastSeenAt, ProfileID: item.ProfileID, DeviceID: item.DeviceID}
	}
	slices.Sort(got)
	if !reflect.DeepEqual(got, []string{"a/one", "a/two", "b/one", "b/two"}) {
		t.Fatalf("recency: %v", got)
	}
	page, err := devices.ListDeviceSettingsPage(ctx, userstore.DevicePageOptions{ProfileID: "b", Limit: 10})
	if err != nil || len(page) != 2 {
		t.Fatalf("scoped page: %+v %v", page, err)
	}
	keys, err := devices.RemoveDeviceSettings(ctx, "a", "one", false)
	if err != nil || !reflect.DeepEqual(keys, []string{"theme"}) {
		t.Fatalf("clear: %v %v", keys, err)
	}
	exists, err := registry.DeviceExists(ctx, "a", "one")
	if err != nil || !exists {
		t.Fatalf("clear forgot device: %v %v", exists, err)
	}
	if _, err := devices.RemoveDeviceSettings(ctx, "a", "one", true); err != nil {
		t.Fatal(err)
	}
	if _, err := devices.RemoveDeviceSettings(ctx, "a", "one", true); !errors.Is(err, userstore.ErrDeviceNotFound) {
		t.Fatalf("repeat forget: %v", err)
	}
	page, err = devices.ListDeviceSettingsPage(ctx, userstore.DevicePageOptions{ProfileID: "b", Limit: 10})
	if err != nil || len(page) != 2 || page[0].ChangedCount != 1 {
		t.Fatalf("sibling changed: %+v %v", page, err)
	}
}
