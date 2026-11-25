package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// editsFixture is a profile set fixture with an administrator and contexts
// that act as the account, as the administrator on an admin route, and as
// the administrator signed in as the account.
type editsFixture struct {
	*profileSetDBFixture
	adminID int
}

func newEditsFixture(t *testing.T) *editsFixture {
	t.Helper()
	f := &editsFixture{profileSetDBFixture: newProfileSetDBFixture(t, 6)}
	name := fmt.Sprintf("edits-admin-%d", time.Now().UnixNano())
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id`,
		name, name+"@example.invalid").Scan(&f.adminID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = f.pool.Exec(bg, `DELETE FROM profile_set_edits WHERE user_id = $1`, f.userID)
		_, _ = f.pool.Exec(bg, `DELETE FROM users WHERE id = $1`, f.adminID)
	})
	return f
}

// actingAs is a request context whose auth named userID, acting as an
// administrator or not, with impersonator the administrator signed in as it
// (nil for none).
func actingAs(userID int, admin bool, impersonator *int) context.Context {
	role := models.RoleUser
	if admin {
		role = models.RoleAdmin
	}
	ctx, req := profileedits.WithRequest(apimw.SetClaims(context.Background(), &auth.Claims{
		UserID: userID, Role: role, TokenType: auth.TokenTypeAccess, ImpersonatorUserID: impersonator,
	}))
	req.SetActor(userID, admin, impersonator)
	return ctx
}

func (f *editsFixture) account() context.Context { return actingAs(f.userID, false, nil) }
func (f *editsFixture) admin() context.Context   { return actingAs(f.adminID, true, nil) }

// update saves a profile through the profile service: as the account's main
// profile, or through the admin route.
func (f *editsFixture) update(t *testing.T, ctx context.Context, profileID string, req ProfileUpdateRequest, admin bool) {
	t.Helper()
	cmd := ProfileUpdateCommand{UserID: f.userID, ProfileID: profileID, Request: req, AccountAdmin: admin}
	if !admin {
		cmd.ActiveProfileID, cmd.VerifyProfile = f.primary, noProfileVerification
	}
	if _, err := f.handler.UpdateProfile(ctx, cmd); err != nil {
		t.Fatalf("update profile: %v", err)
	}
}

func (f *editsFixture) held(t *testing.T) ProfileSetHeld {
	t.Helper()
	held, err := f.handler.HeldProfileSet(t.Context(), f.userID)
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func (f *editsFixture) edits(t *testing.T) []profileedits.Edit {
	t.Helper()
	rows, err := profileedits.List(t.Context(), f.pool, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// applySeen sends a signed set carrying edits_seen.
func (f *editsFixture) applySeen(t *testing.T, set json.RawMessage, seen string) (ProfileSetResult, error) {
	t.Helper()
	return f.handler.ApplyProfileSet(t.Context(), ProfileSetCommand{UserID: f.userID, Set: set, Signed: true, EditsSeen: seen})
}

func changedFields(held ProfileSetHeld, syncID string) []string {
	for _, item := range held.Items {
		if item.SyncID == syncID {
			var out []string
			for _, c := range item.ChangedHere {
				out = append(out, c.By+":"+c.Field)
			}
			return out
		}
	}
	return nil
}

// TestProfileSetEditsTokenDB: a set must carry the token of the person's
// edits on this server. It catches a set overwriting an edit its sender
// never saw, marks that never clear, and an edit undone by another edit on
// the server going unnoticed.
func TestProfileSetEditsTokenDB(t *testing.T) {
	f := newEditsFixture(t)
	main, kid := newSetItem("Parent"), newSetItem("Kid")
	f.mustApply(t, setDoc(t, 1, main, kid))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	f.update(t, f.account(), kidID, ProfileUpdateRequest{Name: new("Kiddo")}, false)

	held := f.held(t)
	if got := changedFields(held, kid.syncID()); !slices.Equal(got, []string{"account:name"}) || held.EditsToken == "" {
		t.Fatalf("after an edit: changed_here = %v, token %q", got, held.EditsToken)
	}
	before := f.state(t)
	t.Run("a newer set with the old token is stale and the edit survives", func(t *testing.T) {
		if _, err := f.applySeen(t, setDoc(t, 2, main, kid), ""); !isStale(err) {
			t.Fatalf("err = %v, want profile_set_stale", err)
		}
		if f.state(t) != before || f.profileBySyncID(t, kid.syncID()).Name != "Kiddo" || len(f.edits(t)) != 1 {
			t.Fatal("a stale set wrote, or the edit is gone")
		}
	})
	t.Run("the same set with the token GET answered is accepted and settles the mark", func(t *testing.T) {
		if _, err := f.applySeen(t, setDoc(t, 2, main, kid), held.EditsToken); err != nil {
			t.Fatal(err)
		}
		after := f.held(t)
		if len(changedFields(after, kid.syncID())) != 0 || after.EditsToken != "" || len(f.edits(t)) != 0 {
			t.Fatalf("after the set: %+v", after)
		}
	})
	t.Run("an edit and its undo each move the token", func(t *testing.T) {
		mainID := f.profileBySyncID(t, main.syncID()).ID
		if _, err := f.store.UpsertSettingValue(f.account(), userstore.SettingIdentity{
			Key: settingskeys.PlaybackAudioLanguage, Scope: "profile", ProfileID: mainID,
		}, json.RawMessage(`"fr"`)); err != nil {
			t.Fatal(err)
		}
		t0 := f.held(t).EditsToken
		f.update(t, f.account(), kidID, ProfileUpdateRequest{Name: new("Kiddo 2")}, false)
		t1 := f.held(t).EditsToken
		f.update(t, f.account(), kidID, ProfileUpdateRequest{Name: new("Kid")}, false)
		t2 := f.held(t).EditsToken
		if t0 == "" || t0 == t1 || t1 == t2 || t0 == t2 {
			t.Fatalf("tokens: before %q, after the edit %q, after the undo %q", t0, t1, t2)
		}
	})
}

// TestProfileEditRecorderDB: which writes record which edits. It catches a
// web save marking every field it sends, an owner's edit recorded as the
// person's, a set apply or an unattributed write recording an edit, and a
// new upload behind the same avatar reference going unrecorded.
func TestProfileEditRecorderDB(t *testing.T) {
	f := newEditsFixture(t)
	main, kid := newSetItem("Parent"), newSetItem("Kid").with("max_content_rating", "PG")
	f.mustApply(t, setDoc(t, 1, main, kid))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	if err := f.create("Local"); err != nil {
		t.Fatal(err)
	}
	var localID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM user_profiles WHERE user_id = $1 AND name = 'Local'`, f.userID).Scan(&localID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range recorderCases(f, main, kid, kidID, localID) {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.pool.Exec(t.Context(), `DELETE FROM profile_set_edits WHERE user_id = $1`, f.userID); err != nil {
				t.Fatal(err)
			}
			tc.write(t)
			var got []string
			for _, e := range f.edits(t) {
				got = append(got, fmt.Sprintf("%s:%s:%d", e.By, e.Field, e.ActorUserID))
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("edit rows = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("deleting the account leaves no edit row", func(t *testing.T) {
		if err := auth.NewUserRepository(f.pool).Delete(t.Context(), f.userID); err != nil {
			t.Fatal(err)
		}
		if got := f.edits(t); len(got) != 0 {
			t.Fatalf("rows after the account delete = %+v", got)
		}
	})
}

type recorderCase struct {
	name  string
	write func(t *testing.T)
	want  []string
}

// recorderCases are TestProfileEditRecorderDB's writes, each with the edit
// rows it leaves (by:field:actor).
func recorderCases(f *editsFixture, main, kid setItem, kidID, localID string) []recorderCase {
	setting := func(key string) userstore.SettingIdentity {
		return userstore.SettingIdentity{Key: key, Scope: "profile", ProfileID: kidID}
	}
	impersonated := f.adminID
	return []recorderCase{
		{"the web editor's full save with one field changed marks that field", func(t *testing.T) {
			f.update(t, f.account(), kidID, ProfileUpdateRequest{
				Name: new("Kid 2"), IsChild: new(false), MaxContentRating: new("PG"), MaxAdvisoryAge: new(0),
				RequireAdvisoryAge: new(false), RequestsBlocked: new(false), LibraryRestrictionsEnabled: new(false),
				AllowedLibraryIDs: &[]int{},
			}, false)
		}, []string{fmt.Sprintf("account:name:%d", f.userID)}},
		{"an admin route's edit is an override", func(t *testing.T) {
			f.update(t, f.admin(), kidID, ProfileUpdateRequest{MaxContentRating: new("G")}, true)
		}, []string{fmt.Sprintf("owner:max_content_rating:%d", f.adminID)}},
		{"an administrator signed in as the account is the owner", func(t *testing.T) {
			f.update(t, actingAs(f.userID, false, &impersonated), kidID, ProfileUpdateRequest{Name: new("Kid 3")}, false)
		}, []string{fmt.Sprintf("owner:name:%d", f.adminID)}},
		{"another account that is not an administrator records nothing", func(t *testing.T) {
			if err := f.store.UpdateProfile(actingAs(f.adminID, false, nil), kidID, userstore.UpdateProfileInput{Name: new("Kid 3b")}); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"a write no request attributes records nothing", func(t *testing.T) {
			if err := f.store.UpdateProfile(t.Context(), kidID, userstore.UpdateProfileInput{Name: new("Kid 4")}); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"a write without recording records nothing", func(t *testing.T) {
			if err := f.store.UpdateProfile(profileedits.WithoutRecording(f.account()), kidID, userstore.UpdateProfileInput{Name: new("Kid 5")}); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"a profile outside the set records nothing", func(t *testing.T) {
			f.update(t, f.admin(), localID, ProfileUpdateRequest{Name: new("Local 2")}, true)
		}, nil},
		{"a server_local setting records nothing", func(t *testing.T) {
			if _, err := f.store.UpsertSettingValue(f.account(), setting(settingskeys.UiDisabledLibraryIds), json.RawMessage(`["1"]`)); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"a device-scope setting records nothing", func(t *testing.T) {
			if _, err := f.store.UpsertSettingValue(f.account(), userstore.SettingIdentity{
				Key: settingskeys.PlaybackAudioLanguage, Scope: "profile_device", ProfileID: kidID, DeviceID: "tv",
			}, json.RawMessage(`"fr"`)); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"resetting a setting with no stored value records nothing", func(t *testing.T) {
			if _, err := f.store.DeleteSettingValue(f.account(), setting(settingskeys.PlaybackAudioLanguage)); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"writing a setting's stored value again records nothing", func(t *testing.T) {
			if _, err := f.store.UpsertSettingValue(profileedits.WithoutRecording(f.account()), setting(settingskeys.PlaybackAudioLanguage), json.RawMessage(`"fr"`)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.UpsertSettingValue(f.account(), setting(settingskeys.PlaybackAudioLanguage), json.RawMessage(`"fr"`)); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"resetting a stored setting is an edit", func(t *testing.T) {
			if _, err := f.store.DeleteSettingValue(f.account(), setting(settingskeys.PlaybackAudioLanguage)); err != nil {
				t.Fatal(err)
			}
		}, []string{fmt.Sprintf("account:settings.%s:%d", settingskeys.PlaybackAudioLanguage, f.userID)}},
		{"the account's change of one half of a mirrored pair takes both from the owner", func(t *testing.T) {
			mode, skip := setting(settingskeys.PlaybackIntroSkipMode), setting(settingskeys.PlaybackAutoSkipIntro)
			for _, w := range []struct {
				ctx   context.Context
				id    userstore.SettingIdentity
				value string
			}{
				{profileedits.WithoutRecording(f.account()), mode, `"always"`},
				{profileedits.WithoutRecording(f.account()), skip, `true`},
				{f.admin(), mode, `"never"`},
				{f.admin(), skip, `false`},
				{f.account(), mode, `"ask"`},
				{f.account(), skip, `false`},
			} {
				if _, err := f.store.UpsertSettingValue(w.ctx, w.id, json.RawMessage(w.value)); err != nil {
					t.Fatal(err)
				}
			}
		}, []string{
			fmt.Sprintf("account:settings.%s:%d", settingskeys.PlaybackAutoSkipIntro, f.userID),
			fmt.Sprintf("account:settings.%s:%d", settingskeys.PlaybackIntroSkipMode, f.userID),
		}},
		{"a save carrying unchanged preference members records nothing, as the account", func(t *testing.T) {
			f.update(t, f.account(), kidID, ProfileUpdateRequest{AutoSkipIntro: new(false), Language: new("")}, false)
		}, nil},
		{"a save carrying unchanged preference members records nothing, as an administrator", func(t *testing.T) {
			f.update(t, f.admin(), kidID, ProfileUpdateRequest{AutoSkipIntro: new(false), Language: new("")}, true)
		}, nil},
		{"a new upload behind the same avatar reference is a picture edit", func(t *testing.T) {
			ref := testAvatarRef(f.userID, kidID)
			if err := f.store.UpdateProfile(profileedits.WithoutRecording(f.account()), kidID, userstore.UpdateProfileInput{Avatar: &ref, AvatarSourceSHA256: new(strings.Repeat("a", 64))}); err != nil {
				t.Fatal(err)
			}
			if err := f.store.UpdateProfile(f.account(), kidID, userstore.UpdateProfileInput{Avatar: &ref, AvatarSourceSHA256: new(strings.Repeat("c", 64))}); err != nil {
				t.Fatal(err)
			}
		}, []string{fmt.Sprintf("account:picture:%d", f.userID)}},
		{"a set's own apply records nothing", func(t *testing.T) {
			if _, err := f.handler.ApplyProfileSet(f.account(), ProfileSetCommand{UserID: f.userID, Signed: true,
				Set: setDoc(t, 2, main, kid.with("name", "Set name").with("settings", map[string]any{settingskeys.PlaybackAudioLanguage: "de"}))}); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"deleting a profile from the set leaves one removal row", func(t *testing.T) {
			f.update(t, f.account(), kidID, ProfileUpdateRequest{Name: new("Before removal")}, false)
			if err := f.handler.DeleteProfile(f.account(), ProfileDeleteCommand{UserID: f.userID, ProfileID: kidID,
				ActiveProfileID: f.primary, VerifyProfile: noProfileVerification}); err != nil {
				t.Fatal(err)
			}
			if e := f.edits(t); len(e) != 1 || e[0].ProfileName != "Before removal" {
				t.Fatalf("removal rows = %+v", e)
			}
		}, []string{fmt.Sprintf("account::%d", f.userID)}},
	}
}

// TestProfileSetOwnerOverridesDB: the owner's edits on this server outlast
// the person's sets until the person edits the field here. It catches owner
// choices undone by the person's other server, retries stuck stale on a PIN
// the owner set, a profile the owner removed coming back, and an ended
// override leaving no record of who ended it.
func TestProfileSetOwnerOverridesDB(t *testing.T) {
	f := newEditsFixture(t)
	main := newSetItem("Parent")
	kid := newSetItem("Kid").with("max_content_rating", "PG").with("pin", map[string]any{"set": "1111"}).
		with("settings", map[string]any{settingskeys.PlaybackAudioLanguage: "en"})
	guest := newSetItem("Guest")
	f.mustApply(t, setDoc(t, 1, main, kid, guest))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	f.update(t, f.admin(), kidID, ProfileUpdateRequest{MaxContentRating: new("G"), PIN: new("2222")}, true)
	if _, err := f.store.UpsertSettingValue(f.admin(), userstore.SettingIdentity{
		Key: settingskeys.PlaybackAudioLanguage, Scope: "profile", ProfileID: kidID,
	}, json.RawMessage(`"fr"`)); err != nil {
		t.Fatal(err)
	}

	t.Run("a repeat of the held version is accepted while the owner set the PIN", func(t *testing.T) {
		if _, err := f.apply(t, setDoc(t, 1, main, kid, guest)); err != nil {
			t.Fatalf("repeat: %v", err)
		}
	})
	t.Run("a newer set leaves the owner's values and reports them", func(t *testing.T) {
		res := f.mustApply(t, setDoc(t, 2, main, kid.with("max_content_rating", "R").
			with("settings", map[string]any{settingskeys.PlaybackAudioLanguage: "de"}), guest))
		p := f.profileBySyncID(t, kid.syncID())
		if p.MaxContentRating != "G" || !pinMatches(p.PINHash, "2222") {
			t.Fatalf("profile after the set = %+v", p)
		}
		want := []ProfileSetOverride{
			{SyncID: kid.syncID(), Field: "max_content_rating", Value: json.RawMessage(`"G"`)},
			{SyncID: kid.syncID(), Field: "pin", Value: json.RawMessage(`"set"`)},
			{SyncID: kid.syncID(), Field: "settings." + settingskeys.PlaybackAudioLanguage, Value: json.RawMessage(`"fr"`)},
		}
		if !sameOverrides(res.Overrides, want) || !sameOverrides(f.held(t).Overrides, want) {
			t.Fatalf("overrides: PUT %+v, GET %+v", res.Overrides, f.held(t).Overrides)
		}
	})
	t.Run("a profile the owner removed stays removed and is reported", func(t *testing.T) {
		guestID := f.profileBySyncID(t, guest.syncID()).ID
		if err := f.handler.DeleteProfile(f.admin(), ProfileDeleteCommand{UserID: f.userID, ProfileID: guestID, AccountAdmin: true}); err != nil {
			t.Fatal(err)
		}
		res := f.mustApply(t, setDoc(t, 3, main, kid, guest))
		if f.profileBySyncID(t, guest.syncID()) != nil || !slices.Equal(res.HiddenByOwner, []string{guest.syncID()}) ||
			len(res.LeftOff) != 0 || !slices.Equal(f.held(t).HiddenByOwner, []string{guest.syncID()}) {
			t.Fatalf("hidden: %+v, GET %v", res, f.held(t).HiddenByOwner)
		}
	})
	t.Run("the person's edit of an overridden field ends the override and is kept as history", func(t *testing.T) {
		f.update(t, f.account(), kidID, ProfileUpdateRequest{MaxContentRating: new("PG-13")}, false)
		if got := changedFields(f.held(t), kid.syncID()); !slices.Contains(got, "account:max_content_rating") {
			t.Fatalf("changed_here = %v", got)
		}
		if _, err := f.applySeen(t, setDoc(t, 4, main, kid, guest), f.held(t).EditsToken); err != nil {
			t.Fatal(err)
		}
		var kept *profileedits.Edit
		for _, e := range f.edits(t) {
			if e.Field == "max_content_rating" {
				kept = &e
			}
		}
		held := f.held(t)
		if kept == nil || !kept.ReplacedOwner || kept.SettledAt == nil || kept.ActorUserID != f.userID || kept.At.IsZero() ||
			slices.Contains(changedFields(held, kid.syncID()), "account:max_content_rating") || held.EditsToken != "" {
			t.Fatalf("settled row %+v, held %+v", kept, held)
		}
		if p := f.profileBySyncID(t, kid.syncID()); p.MaxContentRating != "PG" {
			t.Fatalf("the set did not apply the field the person took back: %q", p.MaxContentRating)
		}
	})
}

// TestProfileSetOwnerEditsFollowTheProfileDB: an owner's edits hold however
// a set names the profile. It catches overrides shed by giving the main
// profile a new sync_id, a hidden profile un-hidden by the main profile
// taking its sync_id, and two profiles left with one name because the owner
// renamed one.
func TestProfileSetOwnerEditsFollowTheProfileDB(t *testing.T) {
	f := newEditsFixture(t)
	main, kid, guest := newSetItem("Parent"), newSetItem("Kid"), newSetItem("Guest")
	f.mustApply(t, setDoc(t, 1, main, kid, guest))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	if err := f.handler.DeleteProfile(f.admin(), ProfileDeleteCommand{UserID: f.userID,
		ProfileID: f.profileBySyncID(t, guest.syncID()).ID, AccountAdmin: true}); err != nil {
		t.Fatal(err)
	}

	t.Run("a name the owner gave one profile refuses another item with that name", func(t *testing.T) {
		f.update(t, f.admin(), kidID, ProfileUpdateRequest{Name: new("Buddy")}, true)
		before := f.state(t)
		_, err := f.applySeen(t, setDoc(t, 5, main, kid, guest, newSetItem("Buddy")), f.held(t).EditsToken)
		var field *ProfileSetFieldError
		if !errors.As(err, &field) || field.Location != "set.items[3].name" || field.Code != "name_conflict" {
			t.Fatalf("err = %v, want name_conflict at set.items[3].name", err)
		}
		if f.state(t) != before {
			t.Fatal("a refused set wrote")
		}
	})
	t.Run("a new sync_id on the main profile keeps the owner's values", func(t *testing.T) {
		f.update(t, f.admin(), f.primary, ProfileUpdateRequest{PIN: new("4321"), MaxContentRating: new("PG")}, true)
		renamed := main.with("sync_id", uuid.NewString()).with("pin", map[string]any{"clear": true})
		res, err := f.applySeen(t, setDoc(t, 6, renamed, kid, guest), f.held(t).EditsToken)
		if err != nil {
			t.Fatal(err)
		}
		p, err := f.store.GetProfile(t.Context(), f.primary)
		if err != nil || p.SyncID != renamed.syncID() || !pinMatches(p.PINHash, "4321") || p.MaxContentRating != "PG" {
			t.Fatalf("main profile after the set = %+v, %v", p, err)
		}
		want := []ProfileSetOverride{
			{SyncID: renamed.syncID(), Field: "max_content_rating", Value: json.RawMessage(`"PG"`)},
			{SyncID: renamed.syncID(), Field: "pin", Value: json.RawMessage(`"set"`)},
		}
		mine := func(all []ProfileSetOverride) []ProfileSetOverride {
			return slices.DeleteFunc(slices.Clone(all), func(o ProfileSetOverride) bool { return o.SyncID != renamed.syncID() })
		}
		if !sameOverrides(mine(res.Overrides), want) || !sameOverrides(mine(f.held(t).Overrides), want) {
			t.Fatalf("overrides: PUT %+v, GET %+v", res.Overrides, f.held(t).Overrides)
		}
		for _, e := range f.edits(t) {
			if e.SyncID == main.syncID() {
				t.Fatalf("an edit row stayed under the old sync_id: %+v", e)
			}
		}
	})
	t.Run("the main profile can't take a sync_id the owner removed", func(t *testing.T) {
		before := f.state(t)
		_, err := f.applySeen(t, setDoc(t, 7, main.with("sync_id", guest.syncID()), kid), f.held(t).EditsToken)
		assertSetField(t, err, "set.items[0].sync_id")
		if f.state(t) != before || !slices.Equal(f.held(t).HiddenByOwner, []string{guest.syncID()}) {
			t.Fatal("the refused set wrote, or the profile is no longer hidden")
		}
	})
}

func pinMatches(hash, pin string) bool {
	return hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)) == nil
}

// testAvatarRef is the avatar reference an upload stores for a profile.
func testAvatarRef(userID int, profileID string) string {
	return fmt.Sprintf("%sprofile-avatars/%d/%s/original.webp", userstore.AvatarUploadPrefix, userID, profileID)
}

func sameOverrides(got, want []ProfileSetOverride) bool {
	return slices.EqualFunc(got, want, func(a, b ProfileSetOverride) bool {
		return a.SyncID == b.SyncID && a.Field == b.Field && string(a.Value) == string(b.Value)
	})
}

// TestProfileSetEditsRaceSetDB: writes that record edits, racing sets and
// then an account delete, finish with only the outcomes such a race allows,
// lose no edit, and leave no row of a deleted account. It catches a
// lock-order mistake in the edit rows (a deadlock, which the set retries but
// an edit does not), a mark a set settled without seeing it, and an edit row
// that outlives its account.
func TestProfileSetEditsRaceSetDB(t *testing.T) {
	f := newEditsFixture(t)
	language := settingskeys.PlaybackAudioLanguage
	main, extra := newSetItem("Parent"), newSetItem("Extra")
	kid := newSetItem("Kid").with("settings", map[string]any{language: "en"})
	f.mustApply(t, setDoc(t, 1, main, kid, extra))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	extraID := f.profileBySyncID(t, extra.syncID()).ID
	racers := raceRacers(t, f, main, kid, kidID, extraID)
	race := func(t *testing.T, rounds int, also func() error, allowed func(racer string, err error) bool) {
		t.Helper()
		var wg sync.WaitGroup
		var mu sync.Mutex
		var unexpected []string
		check := func(name string, err error) {
			if err != nil && !allowed(name, err) {
				mu.Lock()
				unexpected = append(unexpected, name+": "+err.Error())
				mu.Unlock()
			}
		}
		for name, fn := range racers {
			wg.Go(func() {
				for i := range rounds {
					check(name, fn(i))
				}
			})
		}
		if also != nil {
			wg.Go(func() { check("account delete", also()) })
		}
		wg.Wait()
		if len(unexpected) > 0 {
			t.Fatalf("unexpected outcomes: %v", unexpected)
		}
	}

	t.Run("no edit is lost to a set racing it", func(t *testing.T) {
		race(t, 15, nil, func(racer string, err error) bool {
			// A set built before an edit is stale; one whose household kept
			// changing gives up after its retries. The set removes Extra, so
			// deleting it can find it gone.
			var apiErr *APIError
			return isStale(err) || (errors.As(err, &apiErr) && (apiErr.Code == "conflict" ||
				(racer == "profile delete" && apiErr.Status == http.StatusNotFound)))
		})
		// The set gives the kid profile its item's values. A value it does
		// not hold now was edited after the last set accepted, so its edit
		// is recorded: a mark the set never saw, or an override.
		p := f.profileBySyncID(t, kid.syncID())
		var stored string
		if err := f.pool.QueryRow(t.Context(), `SELECT value::text FROM user_setting_values WHERE user_id = $1 AND profile_id = $2 AND key = $3 AND scope = 'profile'`,
			f.userID, kidID, language).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		recorded := map[string]bool{}
		for _, e := range f.edits(t) {
			if e.SyncID == kid.syncID() && e.Open() {
				recorded[e.Field] = true
			}
		}
		for field, differs := range map[string]bool{
			profileedits.FieldName:            p.Name != "Kid",
			profileedits.FieldPicture:         p.Avatar != "",
			profileedits.FieldRequestsBlocked: p.RequestsBlocked,
			"settings." + language:            stored != `"en"`,
		} {
			if differs && !recorded[field] {
				t.Errorf("%s differs from the set's value with no edit recorded", field)
			}
		}
	})
	t.Run("an account deleted during the race leaves no edit row", func(t *testing.T) {
		race(t, 10, func() error { return auth.NewUserRepository(f.pool).Delete(t.Context(), f.userID) }, func(racer string, err error) bool {
			// Once the account is gone: the set finds no account; the
			// profile service finds no account or profile (404), no main
			// profile to manage the household as (403 profile_management),
			// or a profile gone between its read and its write (500); the
			// store finds no profile; a setting insert has no account.
			var apiErr *APIError
			var pgErr *pgconn.PgError
			if errors.As(err, &apiErr) {
				switch {
				case apiErr.Status == http.StatusNotFound, apiErr.Code == "conflict",
					racer == "web edit" && apiErr.Code == "profile_management",
					racer == "admin edit" && apiErr.Code == "internal_error":
					return true
				}
				return false
			}
			return isStale(err) || errors.Is(err, userstore.ErrProfileSetAccountGone) ||
				(errors.As(err, &pgErr) && pgErr.Code == "23503") || strings.Contains(err.Error(), "not found")
		})
		var left int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM profile_set_edits WHERE user_id = $1`, f.userID).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Fatalf("%d edit rows outlived the deleted account", left)
		}
	})
}

// raceRacers are TestProfileSetEditsRaceSetDB's writers, by name, each run
// once per round with the round's number.
func raceRacers(t *testing.T, f *editsFixture, main, kid setItem, kidID, extraID string) map[string]func(i int) error {

	language := settingskeys.PlaybackAudioLanguage
	ref := testAvatarRef(f.userID, kidID)
	version := 1
	var versionMu sync.Mutex
	nextVersion := func() int {
		versionMu.Lock()
		defer versionMu.Unlock()
		version++
		return version
	}
	setting := userstore.SettingIdentity{Key: language, Scope: "profile", ProfileID: kidID}

	return map[string]func(i int) error{
		"set": func(int) error {
			held, err := f.handler.HeldProfileSet(t.Context(), f.userID)
			if err != nil {
				return err
			}
			_, err = f.applySeen(t, setDoc(t, nextVersion(), main, kid), held.EditsToken)
			return err
		},
		"web edit": func(i int) error {
			_, err := f.handler.UpdateProfile(f.account(), ProfileUpdateCommand{UserID: f.userID, ProfileID: kidID,
				ActiveProfileID: f.primary, VerifyProfile: noProfileVerification, Request: ProfileUpdateRequest{Name: new(fmt.Sprintf("Kid %d", i))}})
			return err
		},
		"admin edit": func(i int) error {
			_, err := f.handler.UpdateProfile(f.admin(), ProfileUpdateCommand{UserID: f.userID, ProfileID: kidID, AccountAdmin: true,
				Request: ProfileUpdateRequest{RequestsBlocked: new(i%2 == 0)}})
			return err
		},
		"avatar upload": func(i int) error {
			return f.store.UpdateProfile(f.account(), kidID, userstore.UpdateProfileInput{Avatar: &ref, AvatarSourceSHA256: new(fmt.Sprintf("%064d", i))})
		},
		"pool setting write": func(i int) error {
			_, err := f.store.UpsertSettingValue(f.account(), setting, json.RawMessage([]string{`"fr"`, `"de"`}[i%2]))
			return err
		},
		"profile delete": func(i int) error {
			if i > 0 {
				return nil
			}
			return f.handler.DeleteProfile(f.account(), ProfileDeleteCommand{UserID: f.userID, ProfileID: extraID,
				ActiveProfileID: f.primary, VerifyProfile: noProfileVerification})
		},
	}
}

// TestProfileEditAccountDeleteRaceDB: an edit recorded while the account is
// being deleted leaves no row once both commit. The account delete runs
// first and stays open; the recorder, with no lock of its own taken yet,
// must wait for it and then find no profile. It catches an edit row that
// outlives its account for good, as the table has no foreign key.
func TestProfileEditAccountDeleteRaceDB(t *testing.T) {
	f := newEditsFixture(t)
	main, kid := newSetItem("Parent"), newSetItem("Kid")
	f.mustApply(t, setDoc(t, 1, main, kid))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	ctx := t.Context()

	deleting, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = deleting.Rollback(context.Background()) }()
	if _, err := deleting.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID); err != nil {
		t.Fatal(err)
	}
	if err := profileedits.DeleteAccount(ctx, deleting, f.userID); err != nil {
		t.Fatal(err)
	}

	recorded := make(chan error, 1)
	go func() {
		recorded <- profileedits.RecordFields(f.account(), f.pool, f.userID, kidID, []string{profileedits.FieldName})
	}()
	// The recorder must be waiting on the delete's lock; one that is not
	// has already inserted from its snapshot.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	waiting := false
	for !waiting {
		select {
		case err := <-recorded:
			recorded <- err
			waiting = true // finished without waiting; the check below fails
		case <-deadline:
			t.Fatal("the recorder neither finished nor waited")
		case <-ticker.C:
			var n int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%INSERT INTO profile_set_edits%'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			waiting = n > 0
		}
	}
	if err := deleting.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-recorded; err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM profile_set_edits WHERE user_id = $1`, f.userID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d edit rows outlived the deleted account", left)
	}
}

// TestProfileSetNamesWithOwnerRenamesDB: names are judged as the profiles
// will end up, the owner's renames kept, against profiles outside the set
// too. It catches a set refused for a clash that applying it would not
// leave, and one accepted though it would leave two profiles with one name.
func TestProfileSetNamesWithOwnerRenamesDB(t *testing.T) {
	f := newEditsFixture(t)
	main, kid := newSetItem("Parent"), newSetItem("Kid")
	f.mustApply(t, setDoc(t, 1, main, kid))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	f.update(t, f.admin(), kidID, ProfileUpdateRequest{Name: new("Buddy")}, true)
	if err := f.create("Kid"); err != nil {
		t.Fatal(err)
	}
	var localID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM user_profiles WHERE user_id = $1 AND name = 'Kid'`, f.userID).Scan(&localID); err != nil {
		t.Fatal(err)
	}

	t.Run("an item named like a local profile whose own profile keeps the owner's name", func(t *testing.T) {
		if _, err := f.apply(t, setDoc(t, 2, main, kid)); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if p := f.profileBySyncID(t, kid.syncID()); p.Name != "Buddy" {
			t.Fatalf("name = %q, want the owner's Buddy", p.Name)
		}
	})
	t.Run("the owner's name equal to a local profile's name", func(t *testing.T) {
		buddy := "Buddy"
		if err := f.store.UpdateProfile(t.Context(), localID, userstore.UpdateProfileInput{Name: &buddy}); err != nil {
			t.Fatal(err)
		}
		before := f.state(t)
		_, err := f.apply(t, setDoc(t, 3, main, kid))
		var field *ProfileSetFieldError
		if !errors.As(err, &field) || field.Location != "set.items[1].name" || field.Code != "name_conflict" {
			t.Fatalf("err = %v, want name_conflict at set.items[1].name", err)
		}
		if f.state(t) != before {
			t.Fatal("a refused set wrote")
		}
	})
	t.Run("the main profile's owner name equal to a local profile's name", func(t *testing.T) {
		f.update(t, f.admin(), f.primary, ProfileUpdateRequest{Name: new("Owner")}, true)
		owner := "Owner"
		if err := f.store.UpdateProfile(t.Context(), localID, userstore.UpdateProfileInput{Name: &owner}); err != nil {
			t.Fatal(err)
		}
		before := f.state(t)
		_, err := f.apply(t, setDoc(t, 4, main.with("name", "Sam"), kid.with("name", "Other")))
		var field *ProfileSetFieldError
		if !errors.As(err, &field) || field.Location != "set.items[0].name" || field.Code != "name_conflict" {
			t.Fatalf("err = %v, want name_conflict at set.items[0].name", err)
		}
		if f.state(t) != before {
			t.Fatal("a refused set wrote")
		}
	})
}
