package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// requireNotFound fails unless err is the 404 a missing record answers.
func requireNotFound(t *testing.T, what string, err error) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("%s: err = %v; want 404", what, err)
	}
}

// overriddenFields lists the fields of syncID's profile GET reports in
// overrides.
func overriddenFields(held ProfileSetHeld, syncID string) []string {
	var out []string
	for _, o := range held.Overrides {
		if o.SyncID == syncID {
			out = append(out, o.Field)
		}
	}
	return out
}

// TestClearProfileSetOverrideDB: the owner's Clear puts the set's value back
// at once and hands the field back to the set; a hidden profile comes back
// only once cleared, and only with the next set. It catches a clear that
// does nothing, applies the wrong value, records itself as an edit, or
// forgets to tell the person's apps; and a PIN the server can't know
// written from nothing.
func TestClearProfileSetOverrideDB(t *testing.T) {
	f := newEditsFixture(t)
	woken := 0
	f.handler.ProfileSetWaker = func(context.Context, int) bool { woken++; return true }
	main := newSetItem("Parent")
	kid := newSetItem("Kid").with("max_content_rating", "PG").with("pin", map[string]any{"set": "1111"}).
		with("settings", map[string]any{settingskeys.PlaybackAudioLanguage: "en"})
	guest := newSetItem("Guest")
	f.mustApply(t, setDoc(t, 1, main, kid, guest))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	f.update(t, f.admin(), kidID, ProfileUpdateRequest{MaxContentRating: new("G"), PIN: new("2222")}, true)
	audio := userstore.SettingIdentity{Key: settingskeys.PlaybackAudioLanguage, Scope: "profile", ProfileID: kidID}
	f.mustPutSetting(t, f.admin(), audio, `"fr"`)
	guestID := f.profileBySyncID(t, guest.syncID()).ID
	if err := f.handler.DeleteProfile(f.admin(), ProfileDeleteCommand{UserID: f.userID, ProfileID: guestID, AccountAdmin: true}); err != nil {
		t.Fatal(err)
	}
	clear := func(field string) (ProfileSetClearResult, error) {
		return f.handler.ClearProfileSetOverride(f.admin(), ProfileSetClearCommand{UserID: f.userID, ProfileID: kidID, Field: field})
	}
	version := 1
	nextSet := func(items ...setItem) {
		t.Helper()
		version++
		f.mustApply(t, setDoc(t, version, items...))
	}

	t.Run("clearing a field puts the set's value back now, and the next set applies to it", func(t *testing.T) {
		res, err := clear("max_content_rating")
		if err != nil || !res.Applied || !res.WakeQueued || woken != 1 {
			t.Fatalf("clear = %+v, %v; woken %d", res, err, woken)
		}
		held := f.held(t)
		if p := f.profileBySyncID(t, kid.syncID()); p.MaxContentRating != "PG" || slices.Contains(overriddenFields(held, kid.syncID()), "max_content_rating") ||
			slices.Contains(changedFields(held, kid.syncID()), "owner:max_content_rating") || held.EditsToken != "" {
			t.Fatalf("after the clear: rating %q, held %+v", p.MaxContentRating, held)
		}
		nextSet(main, kid.with("max_content_rating", "R"), guest)
		if p := f.profileBySyncID(t, kid.syncID()); p.MaxContentRating != "R" {
			t.Fatalf("the next set left the cleared field at %q", p.MaxContentRating)
		}
		kid = kid.with("max_content_rating", "R")
	})
	t.Run("clearing a setting puts the set's value back now", func(t *testing.T) {
		if res, err := clear("settings." + settingskeys.PlaybackAudioLanguage); err != nil || !res.Applied {
			t.Fatalf("clear = %+v, %v", res, err)
		}
		value, err := f.store.GetSettingValue(t.Context(), audio)
		if err != nil || value == nil || string(value.Value) != `"en"` || len(f.held(t).Overrides) != 1 {
			t.Fatalf("setting after the clear = %+v, %v; overrides %+v", value, err, f.held(t).Overrides)
		}
	})
	t.Run("clearing a PIN keeps the owner's PIN until a newer set sends one", func(t *testing.T) {
		res, err := clear("pin")
		if err != nil || res.Applied {
			t.Fatalf("clear = %+v, %v", res, err)
		}
		if p := f.profileBySyncID(t, kid.syncID()); !pinMatches(p.PINHash, "2222") || len(f.held(t).Overrides) != 0 {
			t.Fatalf("after the clear: PIN kept %v, overrides %+v", pinMatches(p.PINHash, "2222"), f.held(t).Overrides)
		}
		nextSet(main, kid.with("pin", map[string]any{"set": "3333"}), guest)
		if p := f.profileBySyncID(t, kid.syncID()); !pinMatches(p.PINHash, "3333") {
			t.Fatal("the newer set's PIN was not applied")
		}
	})
	t.Run("a hidden profile comes back only with a set after the owner clears it", func(t *testing.T) {
		nextSet(main, kid, guest)
		if f.profileBySyncID(t, guest.syncID()) != nil {
			t.Fatal("a set brought back a profile the owner removed")
		}
		hidden := ProfileSetClearCommand{UserID: f.userID, SyncID: guest.syncID()}
		if res, err := f.handler.ClearProfileSetHidden(f.admin(), hidden); err != nil || res.Applied || !res.WakeQueued {
			t.Fatalf("clear hidden = %+v, %v", res, err)
		}
		if f.profileBySyncID(t, guest.syncID()) != nil {
			t.Fatal("clearing recreated the profile")
		}
		nextSet(main, kid, guest)
		if f.profileBySyncID(t, guest.syncID()) == nil {
			t.Fatal("the next set did not bring the profile back")
		}
		_, err := f.handler.ClearProfileSetHidden(f.admin(), hidden)
		requireNotFound(t, "clear hidden again", err)
	})
	t.Run("a clear of something not there is not found", func(t *testing.T) {
		_, err := clear("max_content_rating")
		requireNotFound(t, "clear again", err)
		_, err = f.handler.ClearProfileSetOverride(f.admin(), ProfileSetClearCommand{UserID: f.userID, ProfileID: f.primary + "x", Field: "name"})
		requireNotFound(t, "clear on no profile", err)
	})
	t.Run("the list names who changed a field before and after the app merges it", func(t *testing.T) {
		f.update(t, f.admin(), kidID, ProfileUpdateRequest{MaxContentRating: new("G")}, true)
		requireEdit(t, f, ProfileSetEditOverride, f.adminID)
		f.update(t, f.account(), kidID, ProfileUpdateRequest{MaxContentRating: new("PG-13")}, false)
		if e := requireEdit(t, f, ProfileSetEditUnmerged, f.userID); !e.ReplacedOwner {
			t.Fatalf("unmerged edit %+v does not say it replaced the owner's", e)
		}
		version++
		if _, err := f.applySeen(t, setDoc(t, version, main, kid.with("max_content_rating", "PG-13"), guest), f.held(t).EditsToken); err != nil {
			t.Fatal(err)
		}
		requireEdit(t, f, ProfileSetEditSettled, f.userID)
		dismiss := ProfileSetClearCommand{UserID: f.userID, ProfileID: kidID, Field: "max_content_rating"}
		if err := f.handler.DismissProfileSetEdit(f.admin(), dismiss); err != nil {
			t.Fatal(err)
		}
		if views, err := f.handler.ListProfileSetEdits(t.Context(), f.userID); err != nil || len(views) != 0 {
			t.Fatalf("after dismissing: %+v, %v", views, err)
		}
		requireNotFound(t, "dismiss again", f.handler.DismissProfileSetEdit(f.admin(), dismiss))
	})
	t.Run("no row is recorded by a clear", func(t *testing.T) {
		for _, e := range f.edits(t) {
			if e.SyncID == kid.syncID() {
				t.Fatalf("an edit row is left: %+v", e)
			}
		}
	})
}

// requireEdit lists the account's edit records and requires exactly one, of
// the kind, on max_content_rating, by actor, with actor's username.
func requireEdit(t *testing.T, f *editsFixture, kind string, actor int) ProfileSetEditView {
	t.Helper()
	views, err := f.handler.ListProfileSetEdits(t.Context(), f.userID)
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.handler.UserRepo.GetByID(t.Context(), actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Kind != kind || views[0].Field != profileedits.FieldMaxContentRating ||
		views[0].Actor.UserID != actor || views[0].Actor.Username != user.Username || views[0].At.IsZero() {
		t.Fatalf("records = %+v; want one %s by %s", views, kind, user.Username)
	}
	var value string
	if err := json.Unmarshal(views[0].Value, &value); err != nil {
		t.Fatalf("value in force %s: %v", views[0].Value, err)
	}
	return views[0]
}
