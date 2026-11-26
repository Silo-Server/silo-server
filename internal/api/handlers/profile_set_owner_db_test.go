package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
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
	// woken counts calls of the waker; wake, when set, answers them in
	// place of a waker that queues one.
	woken int
	wake  func(context.Context, int) bool
}

func newClearFixture(t *testing.T) *clearFixture {
	t.Helper()
	c := &clearFixture{editsFixture: newEditsFixture(t), version: 1}
	f := c.editsFixture
	f.handler.ProfileSetWaker = func(ctx context.Context, userID int) bool {
		c.woken++
		if c.wake != nil {
			return c.wake(ctx, userID)
		}
		return true
	}
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
	t.Run("clearing a setting the set never sent writes nothing and keeps the owner's value", c.clearsASettingTheSetNeverSent)
	t.Run("clearing a PIN keeps the owner's PIN until a newer set sends one", c.clearsAPIN)
	t.Run("clearing a PIN the app has none for removes the owner's PIN", c.clearsAPINTheAppDropped)
	t.Run("the wake comes once, after the clear commits", c.wakesAfterCommit)
	t.Run("a waker that queues nothing leaves the clear standing", func(t *testing.T) {
		c.wakeFailing(t, func(context.Context, int) bool { return false })
	})
	t.Run("a waker that panics leaves the clear standing", func(t *testing.T) {
		c.wakeFailing(t, func(context.Context, int) bool { panic("relay down") })
	})
	t.Run("a hidden profile comes back only with a set after the owner clears it", c.clearsHidden)
	t.Run("a clear of something not there is not found", func(t *testing.T) {
		_, err := c.clearField("max_content_rating")
		requireNotFound(t, "clear again", err)
		_, err = c.handler.ClearProfileSetOverride(c.admin(), ProfileSetClearCommand{UserID: c.userID, ProfileID: c.primary + "x", Field: "name"})
		requireNotFound(t, "clear on no profile", err)
	})
	t.Run("a name another profile now has is refused, and writes nothing", c.refusesATakenName)
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

// clearsASettingTheSetNeverSent: the last set neither names nor resets the
// key, so it has no value to put back. The clear ends the override, writes
// nothing, says so (clear_applies and applied false), and the profile keeps
// the owner's value until a set names or resets the key.
func (c *clearFixture) clearsASettingTheSetNeverSent(t *testing.T) {
	subtitles := userstore.SettingIdentity{Key: settingskeys.PlaybackSubtitleLanguage, Scope: "profile", ProfileID: c.kidID}
	c.mustPutSetting(t, c.admin(), subtitles, `"fr"`)
	field := "settings." + settingskeys.PlaybackSubtitleLanguage
	if record := c.record(t, field); record.ClearApplies || record.SetValue != nil || record.SetResets {
		t.Fatalf("record = %+v; want nothing from the set to put back", record)
	}
	res, err := c.clearField(field)
	if err != nil || res.Applied {
		t.Fatalf("clear = %+v, %v; want done, not applied", res, err)
	}
	value, err := c.store.GetSettingValue(t.Context(), subtitles)
	if err != nil || value == nil || string(value.Value) != `"fr"` || slices.Contains(overriddenFields(c.held(t), c.kid.syncID()), field) {
		t.Fatalf("after the clear: setting %+v, %v; overrides %+v", value, err, c.held(t).Overrides)
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

// clearsAPINTheAppDropped: the person's app sends the profile with no PIN,
// then the owner sets one here. The list says clearing puts the app's value
// back now, and the clear does: the profile has no PIN afterwards.
func (c *clearFixture) clearsAPINTheAppDropped(t *testing.T) {
	c.nextSet(t, c.main, c.kid.with("pin", map[string]any{"clear": true}), c.guest)
	c.update(t, c.admin(), c.kidID, ProfileUpdateRequest{PIN: new("4444")}, true)
	pin := c.record(t, "pin")
	if string(pin.SetValue) != `"clear"` || !pin.ClearApplies {
		t.Fatalf("PIN record = %+v; want the set's clear, applied on clearing", pin)
	}
	if res, err := c.clearField("pin"); err != nil || !res.Applied {
		t.Fatalf("clear = %+v, %v", res, err)
	}
	if p := c.profileBySyncID(t, c.kid.syncID()); p.PINHash != "" {
		t.Fatal("the owner's PIN is still set after clearing to the app's no PIN")
	}
}

// record is the account's one record of field on the kid.
func (c *clearFixture) record(t *testing.T, field string) ProfileSetEditView {
	t.Helper()
	views, err := c.handler.ListProfileSetEdits(t.Context(), c.userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.SyncID == c.kid.syncID() && v.Field == field {
			return v
		}
	}
	t.Fatalf("no record of %s in %+v", field, views)
	return ProfileSetEditView{}
}

// ownerRating gives the kid an owner's rating again, an override to clear.
func (c *clearFixture) ownerRating(t *testing.T) {
	t.Helper()
	c.update(t, c.admin(), c.kidID, ProfileUpdateRequest{MaxContentRating: new("G")}, true)
	if c.record(t, "max_content_rating").Kind != ProfileSetEditOverride {
		t.Fatal("the owner's rating is not an override")
	}
}

// wakesAfterCommit: the person's app is told once, and only once the clear
// is visible to everyone, so an app that syncs at once finds it.
func (c *clearFixture) wakesAfterCommit(t *testing.T) {
	c.ownerRating(t)
	calls, sawCleared := 0, false
	c.wake = func(ctx context.Context, _ int) bool {
		calls++
		rows, err := profileedits.List(ctx, c.pool, c.userID)
		if err != nil {
			t.Error(err)
			return false
		}
		sawCleared = !slices.ContainsFunc(rows, func(e profileedits.Edit) bool {
			return e.SyncID == c.kid.syncID() && e.Field == "max_content_rating"
		})
		return true
	}
	defer func() { c.wake = nil }()
	if res, err := c.clearField("max_content_rating"); err != nil || !res.WakeQueued {
		t.Fatalf("clear = %+v, %v", res, err)
	}
	if calls != 1 || !sawCleared {
		t.Fatalf("waker called %d times; saw the clear committed: %v", calls, sawCleared)
	}
}

// wakeFailing: whatever the waker does, a committed clear answers success,
// with wake_queued false when no wake was queued.
func (c *clearFixture) wakeFailing(t *testing.T, wake func(context.Context, int) bool) {
	c.ownerRating(t)
	c.wake = wake
	defer func() { c.wake = nil }()
	res, err := c.clearField("max_content_rating")
	if err != nil || !res.Applied || res.WakeQueued {
		t.Fatalf("clear = %+v, %v; want applied, no wake queued", res, err)
	}
	if p := c.profileBySyncID(t, c.kid.syncID()); p.MaxContentRating != c.kid["max_content_rating"] {
		t.Fatalf("rating after the clear = %q", p.MaxContentRating)
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

// refusesATakenName: the owner renames the kid, then gives the set's name to
// a new profile on this server. Putting the set's name back would leave two
// profiles with one name, and the person's next set refused at that item, so
// the clear is refused as the owner's own rename to that name would be.
func (c *clearFixture) refusesATakenName(t *testing.T) {
	c.update(t, c.admin(), c.kidID, ProfileUpdateRequest{Name: new("Kiddo")}, true)
	local, err := c.handler.CreateProfile(c.admin(), ProfileCreateCommand{UserID: c.userID, Request: ProfileCreateRequest{Name: "kid "}, AccountAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	before := c.state(t)
	_, err = c.clearField("name")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "name_conflict" {
		t.Fatalf("clear of a taken name: err = %v; want 409 name_conflict", err)
	}
	if after := c.state(t); after != before || !slices.Contains(overriddenFields(c.held(t), c.kid.syncID()), "name") {
		t.Fatalf("a refused clear wrote something or dropped the override:\nbefore %s\nafter  %s", before, after)
	}
	c.update(t, c.admin(), local.ID, ProfileUpdateRequest{Name: new("Visitor")}, true)
	if res, err := c.clearField("name"); err != nil || !res.Applied {
		t.Fatalf("clear once the name is free = %+v, %v", res, err)
	}
	if p := c.profileBySyncID(t, c.kid.syncID()); p.Name != "Kid" {
		t.Fatalf("name after the clear = %q, want the set's", p.Name)
	}
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

// TestClearMirroredSettingDB: two settings the contract keeps as one
// preference under two keys are cleared together and restored together from
// the half the set sent. The list's clear_applies, the clear's applied and
// the write must agree whichever half the set carried and whichever half
// the owner clears. It catches a clear that restores the pair while the list
// and the answer say the value stays until the app syncs.
func TestClearMirroredSettingDB(t *testing.T) {
	for _, tc := range []struct {
		name            string
		sent            map[string]any
		ownerKey, owner string
		cleared         string
		want            map[string]string
	}{
		{name: "the set sent the mode, the owner clears the boolean",
			sent:     map[string]any{settingskeys.PlaybackIntroSkipMode: "always"},
			ownerKey: settingskeys.PlaybackIntroSkipMode, owner: `"never"`, cleared: settingskeys.PlaybackAutoSkipIntro,
			want: map[string]string{settingskeys.PlaybackIntroSkipMode: `"always"`, settingskeys.PlaybackAutoSkipIntro: `true`}},
		{name: "the set sent the mode, the owner clears the mode",
			sent:     map[string]any{settingskeys.PlaybackIntroSkipMode: "always"},
			ownerKey: settingskeys.PlaybackIntroSkipMode, owner: `"never"`, cleared: settingskeys.PlaybackIntroSkipMode,
			want: map[string]string{settingskeys.PlaybackIntroSkipMode: `"always"`, settingskeys.PlaybackAutoSkipIntro: `true`}},
		{name: "the set sent the boolean, the owner clears the mode",
			sent:     map[string]any{settingskeys.PlaybackAutoSkipIntro: true},
			ownerKey: settingskeys.PlaybackAutoSkipIntro, owner: `false`, cleared: settingskeys.PlaybackIntroSkipMode,
			want: map[string]string{settingskeys.PlaybackIntroSkipMode: `"always"`, settingskeys.PlaybackAutoSkipIntro: `true`}},
		{name: "the set sent the boolean, the owner clears the boolean",
			sent:     map[string]any{settingskeys.PlaybackAutoSkipIntro: true},
			ownerKey: settingskeys.PlaybackAutoSkipIntro, owner: `false`, cleared: settingskeys.PlaybackAutoSkipIntro,
			want: map[string]string{settingskeys.PlaybackIntroSkipMode: `"always"`, settingskeys.PlaybackAutoSkipIntro: `true`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEditsFixture(t)
			kid := newSetItem("Kid").with("settings", tc.sent)
			f.mustApply(t, setDoc(t, 1, newSetItem("Parent"), kid))
			kidID := f.profileBySyncID(t, kid.syncID()).ID
			f.mustPutSetting(t, f.admin(), userstore.SettingIdentity{Key: tc.ownerKey, Scope: "profile", ProfileID: kidID}, tc.owner)
			field := profileedits.FieldSettingsPrefix + tc.cleared
			views, err := f.handler.ListProfileSetEdits(t.Context(), f.userID)
			if err != nil {
				t.Fatal(err)
			}
			listed, setValue := false, ""
			for _, v := range views {
				if v.Field == field {
					listed, setValue = v.ClearApplies, string(v.SetValue)
				}
			}
			if setValue != tc.want[tc.cleared] {
				t.Fatalf("the list says the set's value of %s is %s; the clear writes %s", tc.cleared, setValue, tc.want[tc.cleared])
			}
			res, err := f.handler.ClearProfileSetOverride(f.admin(), ProfileSetClearCommand{UserID: f.userID, ProfileID: kidID, Field: field})
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range tc.want {
				got, err := f.store.GetSettingValue(t.Context(), userstore.SettingIdentity{Key: key, Scope: "profile", ProfileID: kidID})
				if err != nil || got == nil || string(got.Value) != want {
					t.Fatalf("%s after the clear = %+v, %v; want %s", key, got, err, want)
				}
			}
			if !listed || !res.Applied {
				t.Fatalf("the clear restored the pair, but the list said clear_applies %v and the clear answered applied %v", listed, res.Applied)
			}
		})
	}
}

// mutableAdminDefaults is an administrator's defaults a test changes.
type mutableAdminDefaults map[string]string

func (d mutableAdminDefaults) Get(_ context.Context, key string) (string, error) { return d[key], nil }

// TestOverrideTokenDB: an override's token moves when what a clear of it
// would do moves, or when the override is edited again, and on nothing
// else. Each step moves one of its ingredients alone. It catches a clear
// confirmed on one state applying another (an ingredient missing from the
// token), and a pending clear refused for a change the confirmation never
// showed (a value read outside the clear's locks in the token).
func TestOverrideTokenDB(t *testing.T) {
	f := newEditsFixture(t)
	defaults := mutableAdminDefaults{}
	f.handler.AdminDefaults = defaults
	mode, audio := settingskeys.UiNextUpMode, settingskeys.PlaybackAudioLanguage
	missing := "sha256:" + strings.Repeat("ab", 32)
	main := newSetItem("Parent")
	kid := newSetItem("Kid").with("max_content_rating", "PG").with("picture", missing).
		with("settings", map[string]any{mode: "separate", audio: nil})
	f.mustApply(t, setDoc(t, 1, main, kid))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	preset := "avatar-2"
	f.update(t, f.admin(), kidID, ProfileUpdateRequest{MaxContentRating: new("G"), Avatar: &preset}, true)
	f.mustPutSetting(t, f.admin(), userstore.SettingIdentity{Key: audio, Scope: "profile", ProfileID: kidID}, `"fr"`)
	// The owner resets the kid's mode, so the kid inherits it.
	if _, err := f.store.DeleteSettingValue(f.admin(), userstore.SettingIdentity{Key: mode, Scope: "profile", ProfileID: kidID}); err != nil {
		t.Fatal(err)
	}
	rating, modeField, audioField, picture := "max_content_rating", profileedits.FieldSettingsPrefix+mode,
		profileedits.FieldSettingsPrefix+audio, profileedits.FieldPicture
	all := []string{rating, modeField, audioField, picture}
	tokens := func() map[string]string {
		t.Helper()
		views, err := f.handler.ListProfileSetEdits(t.Context(), f.userID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, v := range views {
			if v.Kind == ProfileSetEditOverride {
				if v.Token == "" {
					t.Fatalf("override %s has no token", v.Field)
				}
				out[v.Field] = v.Token
			}
		}
		return out
	}
	before := tokens()
	if len(before) != len(all) {
		t.Fatalf("tokens = %v; want one per override", before)
	}
	version := 1
	nextSet := func(item setItem) {
		version++
		f.mustApply(t, setDoc(t, version, main, item))
	}
	steps := []struct {
		name    string
		change  func()
		changed []string
	}{
		{"nothing changes", func() {}, nil},
		{"the set's value of the field changes (SetValue)", func() {
			kid = kid.with("max_content_rating", "R")
			nextSet(kid)
		}, []string{rating}},
		{"the set resets a key it sent as null, so a clear resets it instead (SetResets)", func() {
			kid = kid.with("settings", map[string]any{mode: "separate"}).with("settings_reset", []string{audio})
			nextSet(kid)
		}, []string{audioField}},
		{"the profile comes to hold the set's picture outside any edit, so a clear can apply it (Applies)", func() {
			if _, err := f.pool.Exec(t.Context(), `UPDATE user_profiles SET avatar = $3, avatar_source_sha256 = $4 WHERE user_id = $1 AND id = $2`,
				f.userID, kidID, testAvatarRef(f.userID, kidID), strings.TrimPrefix(missing, "sha256:")); err != nil {
				t.Fatal(err)
			}
		}, []string{picture}},
		{"the owner changes the field and back, so the override is another edit (Revision)", func() {
			f.update(t, f.admin(), kidID, ProfileUpdateRequest{MaxContentRating: new("PG-13")}, true)
			f.update(t, f.admin(), kidID, ProfileUpdateRequest{MaxContentRating: new("G")}, true)
		}, []string{rating}},
		{"an inherited value changes through an administrator default, outside the clear's locks", func() {
			defaults[settingscontract.AdminDefaultSettingKey(mode)] = `"separate"`
		}, nil},
	}
	for _, step := range steps {
		step.change()
		after := tokens()
		for _, field := range all {
			if moved := after[field] != before[field]; moved != slices.Contains(step.changed, field) {
				t.Fatalf("%s: token of %s moved %v", step.name, field, moved)
			}
		}
		before = after
	}
}
