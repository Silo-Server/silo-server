package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// TestProfileSetEditsRaceSetDB: writes that record edits, racing a set,
// finish without a deadlock. It catches a lock-order mistake in the edit
// rows, which every such transaction locks last.
func TestProfileSetEditsRaceSetDB(t *testing.T) {
	f := newEditsFixture(t)
	main, kid, extra := newSetItem("Parent"), newSetItem("Kid"), newSetItem("Extra")
	f.mustApply(t, setDoc(t, 1, main, kid, extra))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	extraID := f.profileBySyncID(t, extra.syncID()).ID
	ref := testAvatarRef(f.userID, kidID)

	const rounds = 15
	var wg sync.WaitGroup
	errs := make(chan error, 5*rounds)
	run := func(fn func(i int) error) {
		wg.Go(func() {
			for i := range rounds {
				errs <- fn(i)
			}
		})
	}
	run(func(i int) error { // the set, with the token it read
		held, err := f.handler.HeldProfileSet(t.Context(), f.userID)
		if err != nil {
			return err
		}
		_, err = f.applySeen(t, setDoc(t, 2+i, main, kid), held.EditsToken)
		if isStale(err) {
			return nil
		}
		return err
	})
	run(func(i int) error { // the person's web edit
		_, err := f.handler.UpdateProfile(f.account(), ProfileUpdateCommand{UserID: f.userID, ProfileID: kidID,
			ActiveProfileID: f.primary, VerifyProfile: noProfileVerification, Request: ProfileUpdateRequest{Name: new(fmt.Sprintf("Kid %d", i))}})
		return err
	})
	run(func(i int) error { // an admin edit
		_, err := f.handler.UpdateProfile(f.admin(), ProfileUpdateCommand{UserID: f.userID, ProfileID: kidID, AccountAdmin: true,
			Request: ProfileUpdateRequest{IsChild: new(i%2 == 0)}})
		return err
	})
	run(func(i int) error { // an avatar upload
		return f.store.UpdateProfile(f.account(), kidID, userstore.UpdateProfileInput{Avatar: &ref, AvatarSourceSHA256: new(fmt.Sprintf("%064d", i))})
	})
	run(func(i int) error { // a profile delete, once
		if i > 0 {
			return nil
		}
		return f.handler.DeleteProfile(f.account(), ProfileDeleteCommand{UserID: f.userID, ProfileID: extraID,
			ActiveProfileID: f.primary, VerifyProfile: noProfileVerification})
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == sqlstateDeadlockDetected {
			t.Fatalf("deadlock: %v", err)
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("non-deadlock error: %v", err)
		}
	}
}
