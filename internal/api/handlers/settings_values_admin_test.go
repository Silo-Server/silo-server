package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	adminValuesAdminID  = 1
	adminValuesTargetID = 7
)

func TestAdminSettingServiceTargetIdentity(t *testing.T) {
	env := newAdminSeamEnv(t)
	ctx := t.Context()
	req := SettingIdentityRequest{Key: "playback.subtitle_language", Scope: "profile_device", ProfileID: "profile-1", DeviceID: "tv-1"}
	row, err := env.handler.SetAdminAccountSetting(ctx, adminValuesTargetID, req, json.RawMessage(`"de"`))
	if err != nil || string(row.Value) != `"de"` || row.ProfileID != "profile-1" {
		t.Fatalf("%+v %v", row, err)
	}
	id := userstore.SettingIdentity{Key: req.Key, Scope: settingscontract.ScopeProfileDevice, ProfileID: req.ProfileID, DeviceID: req.DeviceID}
	if got, err := env.adminStore.GetSettingValue(ctx, id); err != nil || got != nil {
		t.Fatalf("administrator store changed: %+v %v", got, err)
	}
	if err := env.handler.DeleteAdminAccountSetting(ctx, adminValuesTargetID, req); err != nil {
		t.Fatal(err)
	}
	if got, err := env.targetStore.GetSettingValue(ctx, id); err != nil || got != nil {
		t.Fatalf("target setting retained: %+v %v", got, err)
	}
	for _, profile := range []string{"", "ghost"} {
		req.ProfileID = profile
		if _, err := env.handler.SetAdminAccountSetting(ctx, adminValuesTargetID, req, json.RawMessage(`"en"`)); err == nil {
			t.Fatal("accepted nonexistent target profile")
		}
	}
}

// TestAdminNavigationShortcutRepairPreservesRevisionHistory: an admin clears a
// user's shortcuts with an explicit empty document, never by deleting the row.
// Deleting would restart the revision at one and let a stale compare-and-set
// from before the repair land.
func TestAdminNavigationShortcutRepairPreservesRevisionHistory(t *testing.T) {
	env := newAdminSeamEnv(t)
	ctx := context.Background()
	identity := navShortcutIdentity()
	seeded, err := env.targetStore.UpsertSettingValue(ctx, identity,
		json.RawMessage(`{"items":[{"type":"library","library_id":42,"label":"Movies"}]}`))
	if err != nil || seeded.Revision != 1 {
		t.Fatalf("seed shortcuts = %+v, %v; want revision 1", seeded, err)
	}

	req := SettingIdentityRequest{Key: "nav.shortcuts", Scope: "profile", ProfileID: "profile-1"}
	if err := env.handler.DeleteAdminAccountSetting(ctx, adminValuesTargetID, req); err == nil {
		t.Fatal("admin DELETE of nav.shortcuts succeeded; want refusal")
	}
	if got, err := env.targetStore.GetSettingValue(ctx, identity); err != nil || got == nil || got.Revision != 1 {
		t.Fatalf("shortcut after refused DELETE = %+v err=%v, want revision 1 intact", got, err)
	}

	repaired, err := env.handler.SetAdminAccountSetting(ctx, adminValuesTargetID, req, json.RawMessage(`{"items":[]}`))
	if err != nil {
		t.Fatalf("admin repair with an empty document: %v", err)
	}
	if repaired.Revision != 2 || string(repaired.Value) != `{"items":[]}` {
		t.Fatalf("repair = revision %d value %s, want empty revision 2", repaired.Revision, repaired.Value)
	}

	cas := env.targetStore.(userstore.SettingValueCompareAndSetter)
	_, err = cas.CompareAndSetSettingValue(ctx, identity,
		json.RawMessage(`{"items":[{"type":"library","library_id":99,"label":"Shows"}]}`), seeded.Revision)
	if !errors.Is(err, userstore.ErrSettingValueRevisionConflict) {
		t.Fatalf("stale CAS after admin repair = %v, want revision conflict", err)
	}
}

// TestAdminMutationsAttributeEventsToTheTargetUser: the change event is
// addressed to the account whose settings moved, never to the acting admin,
// or the target's devices stay stale on exactly the change they need.
func TestAdminMutationsAttributeEventsToTheTargetUser(t *testing.T) {
	env := newAdminSeamEnv(t)
	env.handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := env.handler.EventsHub.Subscribe()
	defer unsubscribe()

	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: adminValuesAdminID, Role: "admin"})
	req := SettingIdentityRequest{Key: "playback.subtitle_mode", Scope: "profile", ProfileID: "profile-1"}

	assertTargetEnvelope := func(operation string) {
		t.Helper()
		var envelope evt.Envelope
		select {
		case envelope = <-events:
		default:
			t.Fatalf("admin %s published no event", operation)
		}
		if envelope.UserID != adminValuesTargetID || envelope.ProfileID != "profile-1" {
			t.Errorf("admin %s event addressed to user %d profile %q, want target %d profile-1",
				operation, envelope.UserID, envelope.ProfileID, adminValuesTargetID)
		}
		if envelope.Channel != evt.ChannelUserSettings || envelope.Event != userSettingsChangedEvent {
			t.Errorf("admin %s published %s on %s, want %s on %s", operation,
				envelope.Event, envelope.Channel, userSettingsChangedEvent, evt.ChannelUserSettings)
		}
	}

	if _, err := env.handler.SetAdminAccountSetting(ctx, adminValuesTargetID, req, json.RawMessage(`"always"`)); err != nil {
		t.Fatalf("set: %v", err)
	}
	assertTargetEnvelope("set")

	if err := env.handler.DeleteAdminAccountSetting(ctx, adminValuesTargetID, req); err != nil {
		t.Fatalf("delete: %v", err)
	}
	assertTargetEnvelope("delete")
}
