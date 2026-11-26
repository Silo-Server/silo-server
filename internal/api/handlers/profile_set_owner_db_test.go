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

// clearFixture is an account whose set has a main profile, a kid and a
// guest, with the owner's rating, PIN and audio language on the kid and the
// guest removed by the owner.
type clearFixture struct {
	*editsFixture
	main, kid, guest setItem
	kidID            string
	audio            userstore.SettingIdentity
	version          int
	woken            int
}

func newClearFixture(t *testing.T) *clearFixture {
	t.Helper()
	c := &clearFixture{editsFixture: newEditsFixture(t), version: 1}
	f := c.editsFixture
	f.handler.ProfileSetWaker = func(context.Context, int) bool { c.woken++; return true }
	c.main = newSetItem("Parent")
	c.kid = newSetItem("Kid").with("max_content_rating", "PG").with("pin", map[string]any{"set": "1111"}).
		with("settings", map[string]any{settingskeys.PlaybackAudioLanguage: "en"})
	c.guest = newSetItem("Guest")
	f.mustApply(t, setDoc(t, 1, c.main, c.kid, c.guest))
	c.kidID = f.profileBySyncID(t, c.kid.syncID()).ID
	f.update(t, f.admin(), c.kidID, ProfileUpdateRequest{MaxContentRating: new("G"), PIN: new("2222")}, true)
	c.audio = userstore.SettingIdentity{Key: settingskeys.PlaybackAudioLanguage, Scope: "profile", ProfileID: c.kidID}
	f.mustPutSetting(t, f.admin(), c.audio, `"fr"`)
	guestID := f.profileBySyncID(t, c.guest.syncID()).ID
	if err := f.handler.DeleteProfile(f.admin(), ProfileDeleteCommand{UserID: f.userID, ProfileID: guestID, AccountAdmin: true}); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *clearFixture) clearField(field string) (ProfileSetClearResult, error) {
	return c.handler.ClearProfileSetOverride(c.admin(), ProfileSetClearCommand{UserID: c.userID, ProfileID: c.kidID, Field: field})
}

func (c *clearFixture) nextSet(t *testing.T, items ...setItem) {
	t.Helper()
	c.version++
	c.mustApply(t, setDoc(t, c.version, items...))
}

// TestClearProfileSetOverrideDB: the owner's Clear puts the set's value back
// at once and hands the field back to the set; a hidden profile comes back
// only once cleared, and only with the next set. It catches a clear that
// does nothing, applies the wrong value, records itself as an edit, or
// forgets to tell the person's apps; and a PIN the server can't know
// written from nothing.
func TestClearProfileSetOverrideDB(t *testing.T) {
	c := newClearFixture(t)
	t.Run("clearing a field puts the set's value back now, and the next set applies to it", c.clearsAField)
	t.Run("clearing a setting puts the set's value back now", c.clearsASetting)
	t.Run("clearing a PIN keeps the owner's PIN until a newer set sends one", c.clearsAPIN)
	t.Run("a hidden profile comes back only with a set after the owner clears it", c.clearsHidden)
	t.Run("a clear of something not there is not found", func(t *testing.T) {
		_, err := c.clearField("max_content_rating")
		requireNotFound(t, "clear again", err)
		_, err = c.handler.ClearProfileSetOverride(c.admin(), ProfileSetClearCommand{UserID: c.userID, ProfileID: c.primary + "x", Field: "name"})
		requireNotFound(t, "clear on no profile", err)
	})
	t.Run("the list names who changed a field before and after the app merges it", c.listsWhoChanged)
	t.Run("no row is recorded by a clear", func(t *testing.T) {
		for _, e := range c.edits(t) {
			if e.SyncID == c.kid.syncID() {
				t.Fatalf("an edit row is left: %+v", e)
			}
		}
	})
}

func (c *clearFixture) clearsAField(t *testing.T) {
	res, err := c.clearField("max_content_rating")
	if err != nil || !res.Applied || !res.WakeQueued || c.woken != 1 {
		t.Fatalf("clear = %+v, %v; woken %d", res, err, c.woken)
	}
	held := c.held(t)
	if p := c.profileBySyncID(t, c.kid.syncID()); p.MaxContentRating != "PG" || slices.Contains(overriddenFields(held, c.kid.syncID()), "max_content_rating") ||
		slices.Contains(changedFields(held, c.kid.syncID()), "owner:max_content_rating") || held.EditsToken != "" {
		t.Fatalf("after the clear: rating %q, held %+v", p.MaxContentRating, held)
	}
	c.kid = c.kid.with("max_content_rating", "R")
	c.nextSet(t, c.main, c.kid, c.guest)
	if p := c.profileBySyncID(t, c.kid.syncID()); p.MaxContentRating != "R" {
		t.Fatalf("the next set left the cleared field at %q", p.MaxContentRating)
	}
}

func (c *clearFixture) clearsASetting(t *testing.T) {
	if res, err := c.clearField("settings." + settingskeys.PlaybackAudioLanguage); err != nil || !res.Applied {
		t.Fatalf("clear = %+v, %v", res, err)
	}
	value, err := c.store.GetSettingValue(t.Context(), c.audio)
	if err != nil || value == nil || string(value.Value) != `"en"` || len(c.held(t).Overrides) != 1 {
		t.Fatalf("setting after the clear = %+v, %v; overrides %+v", value, err, c.held(t).Overrides)
	}
}

func (c *clearFixture) clearsAPIN(t *testing.T) {
	res, err := c.clearField("pin")
	if err != nil || res.Applied {
		t.Fatalf("clear = %+v, %v", res, err)
	}
	if p := c.profileBySyncID(t, c.kid.syncID()); !pinMatches(p.PINHash, "2222") || len(c.held(t).Overrides) != 0 {
		t.Fatalf("after the clear: PIN kept %v, overrides %+v", pinMatches(p.PINHash, "2222"), c.held(t).Overrides)
	}
	c.nextSet(t, c.main, c.kid.with("pin", map[string]any{"set": "3333"}), c.guest)
	if p := c.profileBySyncID(t, c.kid.syncID()); !pinMatches(p.PINHash, "3333") {
		t.Fatal("the newer set's PIN was not applied")
	}
}

func (c *clearFixture) clearsHidden(t *testing.T) {
	c.nextSet(t, c.main, c.kid, c.guest)
	if c.profileBySyncID(t, c.guest.syncID()) != nil {
		t.Fatal("a set brought back a profile the owner removed")
	}
	hidden := ProfileSetClearCommand{UserID: c.userID, SyncID: c.guest.syncID()}
	if res, err := c.handler.ClearProfileSetHidden(c.admin(), hidden); err != nil || res.Applied || !res.WakeQueued {
		t.Fatalf("clear hidden = %+v, %v", res, err)
	}
	if c.profileBySyncID(t, c.guest.syncID()) != nil {
		t.Fatal("clearing recreated the profile")
	}
	c.nextSet(t, c.main, c.kid, c.guest)
	if c.profileBySyncID(t, c.guest.syncID()) == nil {
		t.Fatal("the next set did not bring the profile back")
	}
	_, err := c.handler.ClearProfileSetHidden(c.admin(), hidden)
	requireNotFound(t, "clear hidden again", err)
}

func (c *clearFixture) listsWhoChanged(t *testing.T) {
	c.update(t, c.admin(), c.kidID, ProfileUpdateRequest{MaxContentRating: new("G")}, true)
	requireEdit(t, c.editsFixture, ProfileSetEditOverride, c.adminID)
	c.update(t, c.account(), c.kidID, ProfileUpdateRequest{MaxContentRating: new("PG-13")}, false)
	if e := requireEdit(t, c.editsFixture, ProfileSetEditUnmerged, c.userID); !e.ReplacedOwner {
		t.Fatalf("unmerged edit %+v does not say it replaced the owner's", e)
	}
	c.version++
	if _, err := c.applySeen(t, setDoc(t, c.version, c.main, c.kid.with("max_content_rating", "PG-13"), c.guest), c.held(t).EditsToken); err != nil {
		t.Fatal(err)
	}
	requireEdit(t, c.editsFixture, ProfileSetEditSettled, c.userID)
	dismiss := ProfileSetClearCommand{UserID: c.userID, ProfileID: c.kidID, Field: "max_content_rating"}
	if err := c.handler.DismissProfileSetEdit(c.admin(), dismiss); err != nil {
		t.Fatal(err)
	}
	if views, err := c.handler.ListProfileSetEdits(t.Context(), c.userID); err != nil || len(views) != 0 {
		t.Fatalf("after dismissing: %+v, %v", views, err)
	}
	requireNotFound(t, "dismiss again", c.handler.DismissProfileSetEdit(c.admin(), dismiss))
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
