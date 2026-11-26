package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// setItem is one profile set item as a device sends it.
type setItem map[string]any

func newSetItem(name string) setItem {
	return setItem{
		"sync_id": uuid.NewString(), "name": name, "picture": "", "pin": map[string]any{"keep": true},
		"is_child": false, "max_content_rating": "", "max_advisory_age": nil,
		"require_advisory_age": false, "requests_blocked": false, "settings": map[string]any{},
	}
}

func (i setItem) with(key string, value any) setItem {
	out := setItem{}
	for k, v := range i {
		out[k] = v
	}
	out[key] = value
	return out
}

func (i setItem) syncID() string { return i["sync_id"].(string) }

func setDoc(t *testing.T, version int, items ...setItem) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": version, "items": items})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type profileSetDBFixture struct {
	*profileLimitDBFixture
}

func newProfileSetDBFixture(t *testing.T, maxProfiles int) *profileSetDBFixture {
	t.Helper()
	f := &profileSetDBFixture{newProfileLimitDBFixture(t, maxProfiles)}
	f.handler.ProfileSetAvailable = true
	return f
}

// apply sends a signed set.
func (f *profileSetDBFixture) apply(t *testing.T, set json.RawMessage) (ProfileSetResult, error) {
	t.Helper()
	return f.handler.ApplyProfileSet(t.Context(), ProfileSetCommand{UserID: f.userID, Set: set, Signed: true})
}

func (f *profileSetDBFixture) mustApply(t *testing.T, set json.RawMessage) ProfileSetResult {
	t.Helper()
	res, err := f.apply(t, set)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return res
}

// state dumps every row a set could write: the account's set state and
// access revision, each profile's row stamps and sync columns, and each
// setting row's revision.
func (f *profileSetDBFixture) state(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	ctx := t.Context()
	var account string
	if err := f.pool.QueryRow(ctx, `SELECT format('%s|%s|%s|%s', access_policy_revision, profile_set_version, encode(profile_set_sha256, 'hex'), profile_set_accepted_at)
		FROM users WHERE id = $1`, f.userID).Scan(&account); err != nil {
		t.Fatal(err)
	}
	b.WriteString(account + "\n")
	for _, q := range []string{
		`SELECT format('%s|%s|%s|%s|%s|%s|%s', id, name, updated_at, pin_revision, sync_id, sync_position, sync_applied) FROM user_profiles WHERE user_id = $1 ORDER BY id`,
		`SELECT format('%s|%s|%s|%s|%s', profile_id, key, value, revision, updated_at) FROM user_setting_values WHERE user_id = $1 ORDER BY profile_id, key`,
	} {
		rows, err := f.pool.Query(ctx, q, f.userID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			b.WriteString(line + "\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return b.String()
}

// profileStateLine is one profile's row in a state dump.
func profileStateLine(state, profileID string) string {
	for _, line := range strings.Split(state, "\n") {
		if strings.HasPrefix(line, profileID+"|") {
			return line
		}
	}
	return ""
}

func (f *profileSetDBFixture) profileBySyncID(t *testing.T, syncID string) *userstore.Profile {
	t.Helper()
	profiles, err := f.store.ListProfiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range profiles {
		if profiles[i].SyncID == syncID {
			p, err := f.store.GetProfile(t.Context(), profiles[i].ID)
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
	return nil
}

func pinHash(t *testing.T, pin string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

func isStale(err error) bool { return errors.Is(err, ErrProfileSetStale) }

// TestProfileSetIdenticalApplyWritesNothingDB: a set applied twice writes
// nothing the second time, PIN digits included, and a newer set that only
// renames one profile writes only that profile. It catches rows rewritten
// and revisions bumped on an unchanged set, which end PIN tokens and
// invalidate every client's cache.
func TestProfileSetIdenticalApplyWritesNothingDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	main := newSetItem("Parent").with("pin", map[string]any{"set": "1234"}).
		with("settings", map[string]any{settingskeys.PlaybackAutoSkipCredits: true})
	kid := newSetItem("Kid").with("is_child", true).with("max_content_rating", "PG").with("max_advisory_age", 10)
	f.mustApply(t, setDoc(t, 1, main, kid))
	before := f.state(t)
	f.mustApply(t, setDoc(t, 1, main, kid))
	if after := f.state(t); after != before {
		t.Fatalf("a repeat of the held set wrote:\nbefore %s\nafter  %s", before, after)
	}

	renamed := kid.with("name", "Kiddo")
	kidBefore, mainBefore := f.profileBySyncID(t, kid.syncID()), f.profileBySyncID(t, main.syncID())
	f.mustApply(t, setDoc(t, 2, main, renamed))
	kidAfter, mainAfter := f.profileBySyncID(t, kid.syncID()), f.profileBySyncID(t, main.syncID())
	if kidAfter.Name != "Kiddo" || kidAfter.PINRevision != kidBefore.PINRevision {
		t.Fatalf("renamed profile = %+v (before %+v)", kidAfter, kidBefore)
	}
	if line := profileStateLine(before, mainBefore.ID); line == "" || line != profileStateLine(f.state(t), mainAfter.ID) {
		t.Fatal("the unchanged main profile was rewritten")
	}
	if revisionBefore, revisionAfter := strings.SplitN(before, "|", 2)[0], strings.SplitN(f.state(t), "|", 2)[0]; revisionBefore != revisionAfter {
		t.Fatalf("a rename moved the access revision from %s to %s", revisionBefore, revisionAfter)
	}
}

// TestProfileSetVersionRuleDB: only a newer version changes anything. It
// catches a replayed or out-of-order set overwriting a newer one, and a PIN
// changing without a new version.
func TestProfileSetVersionRuleDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	main := newSetItem("Parent").with("pin", map[string]any{"set": "1234"})
	other := newSetItem("Other")
	f.mustApply(t, setDoc(t, 5, main, other))
	held := f.state(t)
	pinBefore := f.profileBySyncID(t, main.syncID()).PINHash

	for _, tc := range []struct {
		name string
		set  json.RawMessage
	}{
		{"older version", setDoc(t, 4, main, other)},
		{"same version, other content", setDoc(t, 5, main, other.with("name", "Renamed"))},
		{"same version, same content, other PIN digits", setDoc(t, 5, main.with("pin", map[string]any{"set": "9999"}), other)},
		{"same version, a clear that finds a PIN", setDoc(t, 5, main.with("pin", map[string]any{"clear": true}), other)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.apply(t, tc.set); !isStale(err) {
				t.Fatalf("err = %v, want profile_set_stale", err)
			}
			if got := f.state(t); got != held {
				t.Fatalf("a refused set wrote:\n%s\nwant\n%s", got, held)
			}
		})
	}
	t.Run("same version, same content, same PIN", func(t *testing.T) {
		withEdits, err := f.handler.ApplyProfileSet(t.Context(), ProfileSetCommand{
			UserID: f.userID, Set: setDoc(t, 5, main, other), Signed: true, EditsSeen: "anything",
		})
		if err != nil || withEdits.Version != 5 {
			t.Fatalf("repeat = %+v, %v", withEdits, err)
		}
		if got := f.state(t); got != held {
			t.Fatal("a repeat of the held set wrote")
		}
	})
	t.Run("newer version differing only by a PIN", func(t *testing.T) {
		f.mustApply(t, setDoc(t, 6, main.with("pin", map[string]any{"set": "9999"}), other))
		after := f.profileBySyncID(t, main.syncID()).PINHash
		if after == pinBefore || bcrypt.CompareHashAndPassword([]byte(after), []byte("9999")) != nil {
			t.Fatal("the newer set's PIN was not applied")
		}
	})
	t.Run("edits_seen with no edits on the server", func(t *testing.T) {
		if _, err := f.handler.ApplyProfileSet(t.Context(), ProfileSetCommand{
			UserID: f.userID, Set: setDoc(t, 7, main.with("pin", map[string]any{"keep": true}), other), Signed: true, EditsSeen: "stale-token",
		}); err != nil {
			t.Fatalf("apply with edits_seen: %v", err)
		}
	})
}

// TestProfileSetLimitInSetOrderDB: the owner's limit is applied in the
// set's order and nothing is deleted to fit. It catches a set creating past
// the limit, or deleting a profile to make room.
func TestProfileSetLimitInSetOrderDB(t *testing.T) {
	t.Run("limit 3, five items", func(t *testing.T) {
		f := newProfileSetDBFixture(t, 3)
		items := []setItem{newSetItem("Parent"), newSetItem("A"), newSetItem("B"), newSetItem("C"), newSetItem("D")}
		res := f.mustApply(t, setDoc(t, 1, items...))
		if len(res.Profiles) != 3 || res.Profiles[1].SyncID != items[1].syncID() || res.Profiles[2].SyncID != items[2].syncID() {
			t.Fatalf("profiles = %+v", res.Profiles)
		}
		if !slices.Equal(res.LeftOff, []string{items[3].syncID(), items[4].syncID()}) {
			t.Fatalf("left_off = %v", res.LeftOff)
		}
	})
	t.Run("household already over the limit", func(t *testing.T) {
		f := newProfileSetDBFixture(t, 5)
		for _, name := range []string{"L1", "L2"} {
			if err := f.create(name); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE users SET max_profiles = 2 WHERE id = $1`, f.userID); err != nil {
			t.Fatal(err)
		}
		extra := newSetItem("New")
		res := f.mustApply(t, setDoc(t, 1, newSetItem("Parent"), extra))
		profiles, _ := f.store.ListProfiles(t.Context())
		if len(profiles) != 3 || !slices.Equal(res.LeftOff, []string{extra.syncID()}) {
			t.Fatalf("household = %d profiles, left_off = %v", len(profiles), res.LeftOff)
		}
	})
}

// TestProfileSetChildCreatedWithNoLibrariesDB: a child profile a set
// creates sees no library until someone picks them here. It catches a child
// from another server seeing every library on this one.
func TestProfileSetChildCreatedWithNoLibrariesDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	kid := newSetItem("Kid").with("is_child", true)
	res := f.mustApply(t, setDoc(t, 1, newSetItem("Parent"), kid))
	p := f.profileBySyncID(t, kid.syncID())
	if p == nil || !p.LibraryRestrictionsEnabled || len(p.AllowedLibraryIDs) != 0 {
		t.Fatalf("created child = %+v", p)
	}
	if allowed, limited := access.ProfileLibraryLimit(p); !limited || len(allowed) != 0 {
		t.Fatalf("library limit = %v, %v; want limited to none", allowed, limited)
	}
	if !slices.Equal(res.AwaitingLibraries, []string{kid.syncID()}) {
		t.Fatalf("awaiting_libraries = %v", res.AwaitingLibraries)
	}

	// The first set on an account with no profiles: its first profile
	// becomes the main one and has no library limit, and a child after it
	// is limited like any other.
	empty := newProfileSetDBFixture(t, 5)
	if _, err := empty.pool.Exec(t.Context(), `DELETE FROM user_profiles WHERE user_id = $1`, empty.userID); err != nil {
		t.Fatal(err)
	}
	parent, child := newSetItem("Parent"), newSetItem("Kid").with("is_child", true)
	res = empty.mustApply(t, setDoc(t, 1, parent, child))
	main, limited := empty.profileBySyncID(t, parent.syncID()), empty.profileBySyncID(t, child.syncID())
	if main == nil || !main.IsPrimary || main.LibraryRestrictionsEnabled {
		t.Fatalf("first profile of an empty household = %+v", main)
	}
	if limited == nil || limited.IsPrimary || !limited.LibraryRestrictionsEnabled || len(limited.AllowedLibraryIDs) != 0 {
		t.Fatalf("child created after it = %+v", limited)
	}
	if !slices.Equal(res.AwaitingLibraries, []string{child.syncID()}) {
		t.Fatalf("awaiting_libraries on an empty household = %v", res.AwaitingLibraries)
	}
}

// TestProfileSetNamesDB: a set never leaves two profiles with one name, and
// a set never names two items alike, even an item it does not apply. It
// catches duplicates, which also make Jellyfin sign-in by name ambiguous,
// and a set held with two items of one name, which a device that left the
// second off would send everywhere else.
func TestProfileSetNamesDB(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		items []setItem
	}{
		{"two items with one name", 5, []setItem{newSetItem("Parent"), newSetItem("Sam"), newSetItem("Sam")}},
		{"two items with one name once trimmed and folded", 5, []setItem{newSetItem("Parent"), newSetItem("Sam"), newSetItem(" sam ")}},
		// The limit leaves item 2 off, so the household would hold one Sam.
		{"two items with one name, the second left off for the limit", 2, []setItem{newSetItem("Parent"), newSetItem("Sam"), newSetItem("sam")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProfileSetDBFixture(t, tc.limit)
			_, err := f.apply(t, setDoc(t, 1, tc.items...))
			assertSetNameConflict(t, err, "set.items[2].name")
		})
	}
	t.Run("a rename chain", func(t *testing.T) {
		f := newProfileSetDBFixture(t, 5)
		a, b := newSetItem("A"), newSetItem("B")
		main := newSetItem("Parent")
		f.mustApply(t, setDoc(t, 1, main, a, b))
		f.mustApply(t, setDoc(t, 2, main, a.with("name", "B"), b.with("name", "A")))
		if f.profileBySyncID(t, a.syncID()).Name != "B" || f.profileBySyncID(t, b.syncID()).Name != "A" {
			t.Fatal("the rename chain did not apply")
		}
	})
	t.Run("a new item named like a profile outside the set", func(t *testing.T) {
		f := newProfileSetDBFixture(t, 5)
		if err := f.create("Guest"); err != nil {
			t.Fatal(err)
		}
		_, err := f.apply(t, setDoc(t, 1, newSetItem("Parent"), newSetItem("guest")))
		assertSetField(t, err, "set.items[1].name")
	})
}

func assertSetNameConflict(t *testing.T, err error, location string) {
	t.Helper()
	var fieldErr *ProfileSetFieldError
	if !errors.As(err, &fieldErr) || fieldErr.Location != location || fieldErr.Code != "name_conflict" {
		t.Fatalf("err = %v, want name_conflict at %s", err, location)
	}
}

func assertSetField(t *testing.T, err error, location string) {
	t.Helper()
	var fieldErr *ProfileSetFieldError
	if !errors.As(err, &fieldErr) || fieldErr.Location != location {
		t.Fatalf("err = %v, want a field error at %s", err, location)
	}
}

// TestProfileSetPairsKeepHistoryDB: pairing an existing profile keeps it,
// history included, and a profile no pair names is left alone, row and
// history. It catches pairing that creates a new profile and drops the old
// one's history, and a set that touches a profile it doesn't hold.
func TestProfileSetPairsKeepHistoryDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	ctx := t.Context()
	for _, name := range []string{"Sam", "Guest"} {
		if err := f.create(name); err != nil {
			t.Fatal(err)
		}
	}
	profiles, _ := f.store.ListProfiles(ctx)
	ids := map[string]string{}
	for _, p := range profiles {
		ids[p.Name] = p.ID
	}
	for _, name := range []string{"Sam", "Guest"} {
		if err := f.store.SetProgress(ctx, ids[name], "movie:history", 600, 6000, userstore.ProgressThresholds{}); err != nil {
			t.Fatal(err)
		}
	}
	guestBefore := profileStateLine(f.state(t), ids["Guest"])
	sam := newSetItem("Sam")
	pairs, _ := json.Marshal([]map[string]string{{"sync_id": sam.syncID(), "local_id": ids["Sam"]}})
	if _, err := f.handler.ApplyProfileSet(ctx, ProfileSetCommand{UserID: f.userID, Set: setDoc(t, 1, newSetItem("Parent"), sam), Pairs: pairs, Signed: true}); err != nil {
		t.Fatal(err)
	}
	paired := f.profileBySyncID(t, sam.syncID())
	if paired == nil || paired.ID != ids["Sam"] {
		t.Fatalf("paired profile = %+v, want the existing %s", paired, ids["Sam"])
	}
	for _, name := range []string{"Sam", "Guest"} {
		if progress, err := f.store.GetProgress(ctx, ids[name], "movie:history"); err != nil || progress == nil || progress.PositionSeconds != 600 {
			t.Fatalf("%s's progress after the set = %+v, %v", name, progress, err)
		}
	}
	// The profile no pair names is not touched at all: same row, no sync ID.
	if guestBefore == "" || profileStateLine(f.state(t), ids["Guest"]) != guestBefore {
		t.Fatalf("the unpaired profile's row changed:\n%s\n%s", guestBefore, profileStateLine(f.state(t), ids["Guest"]))
	}
	f.handler.JellyfinCompatEnabled = true
	held, err := f.handler.HeldProfileSet(ctx, f.userID)
	if err != nil || len(held.LocalOnly) != 1 || held.LocalOnly[0].ID != ids["Guest"] {
		t.Fatalf("local_only = %+v, %v", held.LocalOnly, err)
	}
	// Three profiles with no PIN and none named after the account: the bare
	// account name no longer picks one on the Jellyfin-compatible surface.
	if held.JellyfinBareNameSignIn == nil || *held.JellyfinBareNameSignIn {
		t.Fatalf("jellyfin_bare_name_sign_in = %v, want false", held.JellyfinBareNameSignIn)
	}
}

// TestProfileSetPINsDB: keep leaves a PIN alone, set writes only a PIN that
// differs, clear removes it, a profile made from keep waits for its PIN.
// It catches keep touching a PIN, a re-hash ending every token, and a
// malformed PIN slipping in.
func TestProfileSetPINsDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	ctx := t.Context()
	main, kid := newSetItem("Parent"), newSetItem("Kid")
	f.mustApply(t, setDoc(t, 1, main, kid))
	kidID := f.profileBySyncID(t, kid.syncID()).ID
	sixDigits := pinHash(t, "123456")
	if err := f.store.UpdateProfile(ctx, kidID, userstore.UpdateProfileInput{PINHash: &sixDigits}); err != nil {
		t.Fatal(err)
	}
	version := 1
	step := func(pin map[string]any) *userstore.Profile {
		t.Helper()
		version++
		f.mustApply(t, setDoc(t, version, main, kid.with("pin", pin)))
		return f.profileBySyncID(t, kid.syncID())
	}
	before := f.profileBySyncID(t, kid.syncID())
	if got := step(map[string]any{"keep": true}); got.PINHash != sixDigits || got.PINRevision != before.PINRevision {
		t.Fatal("keep touched a six-digit PIN")
	}
	set := step(map[string]any{"set": "4321"})
	if bcrypt.CompareHashAndPassword([]byte(set.PINHash), []byte("4321")) != nil || set.PINRevision == before.PINRevision {
		t.Fatal("a differing set PIN was not written")
	}
	if again := step(map[string]any{"set": "4321"}); again.PINHash != set.PINHash || again.PINRevision != set.PINRevision {
		t.Fatal("an equal set PIN was written again")
	}
	if cleared := step(map[string]any{"clear": true}); cleared.PINHash != "" {
		t.Fatal("clear left the PIN")
	}

	fresh := newSetItem("Fresh")
	version++
	res := f.mustApply(t, setDoc(t, version, main, kid.with("pin", map[string]any{"clear": true}), fresh))
	if !slices.Equal(res.AwaitingPins, []string{fresh.syncID()}) {
		t.Fatalf("awaiting_pins = %v", res.AwaitingPins)
	}
	res = f.mustApply(t, setDoc(t, version, main, kid.with("pin", map[string]any{"clear": true}), fresh))
	if !slices.Equal(res.AwaitingPins, []string{fresh.syncID()}) {
		t.Fatalf("awaiting_pins on a repeat = %v", res.AwaitingPins)
	}
	_, err := f.apply(t, setDoc(t, version+1, main.with("pin", map[string]any{"set": "12345"})))
	assertSetField(t, err, "set.items[0].pin.set")
}

// recordingPurger records the profiles a post-delete purge ran for.
type recordingPurger struct{ calls []string }

func (p *recordingPurger) PurgeProfile(_ context.Context, _ int, profileID string) error {
	p.calls = append(p.calls, profileID)
	return nil
}

func (p *recordingPurger) PurgeProfileDevices(_ context.Context, _ int, profileID string) error {
	p.calls = append(p.calls, profileID)
	return nil
}

// TestProfileSetRemovalDeletesLikeProfileDeleteDB: a profile the set no
// longer holds is deleted as a profile delete deletes it, clean-ups
// included, and a profile that was never in the set is not. It catches
// removal that leaves data behind or removes local profiles.
func TestProfileSetRemovalDeletesLikeProfileDeleteDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	ctx := t.Context()
	jellyfin, devices, dropped := &recordingPurger{}, &recordingPurger{}, &recordingPurger{}
	f.handler.JellyfinSessionPurger, f.handler.DeviceLibraryPurger, f.handler.DroppedSeriesPurger = jellyfin, devices, dropped
	if err := f.create("Local"); err != nil {
		t.Fatal(err)
	}
	main, gone := newSetItem("Parent"), newSetItem("Gone")
	f.mustApply(t, setDoc(t, 1, main, gone))
	goneID := f.profileBySyncID(t, gone.syncID()).ID
	if err := f.store.SetProgress(ctx, goneID, "movie:x", 60, 600, userstore.ProgressThresholds{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AddFavorite(ctx, goneID, "movie:x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: goneID, Name: "Mine"}); err != nil {
		t.Fatal(err)
	}

	f.mustApply(t, setDoc(t, 2, main))
	for _, table := range []string{"user_profiles", "user_watch_progress", "user_favorites", "user_personal_collections", "user_setting_values"} {
		column := "profile_id"
		if table == "user_profiles" {
			column = "id"
		}
		var n int
		if err := f.pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE user_id = $1 AND %s = $2`, table, column), f.userID, goneID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s holds %d rows of the removed profile (%v)", table, n, err)
		}
	}
	for name, p := range map[string]*recordingPurger{"jellyfin": jellyfin, "devices": devices, "dropped": dropped} {
		if !slices.Equal(p.calls, []string{goneID}) {
			t.Fatalf("%s purge calls = %v, want once for %s", name, p.calls, goneID)
		}
	}
	held, _ := f.handler.HeldProfileSet(ctx, f.userID)
	if len(held.LocalOnly) != 1 || held.LocalOnly[0].Name != "Local" {
		t.Fatalf("local_only after removal = %+v", held.LocalOnly)
	}
}

// TestProfileSetSessionPINProofDB: the session form needs the main profile
// verified with its current PIN, and a set that changes that PIN ends the
// old proof. It catches a stale or stolen main-profile token pushing a set.
func TestProfileSetSessionPINProofDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	ctx := t.Context()
	pin := pinHash(t, "1111")
	if err := f.store.UpdateProfile(ctx, f.primary, userstore.UpdateProfileInput{PINHash: &pin}); err != nil {
		t.Fatal(err)
	}
	primary, _ := f.store.GetProfile(ctx, f.primary)
	scope := access.Scope{ProfileID: f.primary, ProfileVerified: true, ProfilePINRevision: primary.PINRevision}
	session := func(set json.RawMessage, s access.Scope) error {
		_, err := f.handler.ApplyProfileSet(ctx, ProfileSetCommand{UserID: f.userID, Set: set, ActiveProfileID: f.primary, VerifyProfile: s.CheckProfileVerification})
		return err
	}
	stale := scope
	stale.ProfilePINRevision--
	// Another profile, and no profile, are not the main one.
	second := uuid.NewString()
	if err := f.store.CreateProfile(ctx, userstore.Profile{ID: second, Name: "Second"}); err != nil {
		t.Fatal(err)
	}
	before := f.state(t)
	for _, active := range []string{second, ""} {
		_, err := f.handler.ApplyProfileSet(ctx, ProfileSetCommand{UserID: f.userID, Set: setDoc(t, 1, newSetItem("Parent")), ActiveProfileID: active, VerifyProfile: scope.CheckProfileVerification})
		if !errors.Is(err, ErrProfileSetNotMainProfile) {
			t.Fatalf("session form as profile %q: err = %v, want ErrProfileSetNotMainProfile", active, err)
		}
	}
	if err := session(setDoc(t, 1, newSetItem("Parent")), stale); !errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("stale token: err = %v", err)
	}
	if f.state(t) != before {
		t.Fatal("a refused session set wrote")
	}
	main := newSetItem("Parent").with("pin", map[string]any{"set": "2222"})
	if err := session(setDoc(t, 1, main), scope); err != nil {
		t.Fatalf("verified main profile: %v", err)
	}
	after, _ := f.store.GetProfile(ctx, f.primary)
	if scope.CheckProfileVerification(after) == nil {
		t.Fatal("the token for the old PIN still verifies after the set changed it")
	}
	if err := session(setDoc(t, 2, main), access.Scope{}); err == nil {
		t.Fatal("an unverified session sent a set")
	}
}

// TestProfileSetSettingsDB: the set writes the profile settings it carries,
// ignores those that stay on one server, and keeps the column-backed
// preferences in step. It catches server-local values crossing servers,
// reset confused with null, and profile columns going stale.
func TestProfileSetSettingsDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	ctx := t.Context()
	main := newSetItem("Parent")
	f.mustApply(t, setDoc(t, 1, main.with("settings", map[string]any{settingskeys.PlaybackAudioLanguage: "fr"})))
	if _, err := f.store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.UiDisabledLibraryIds, Scope: "profile", ProfileID: f.primary,
	}, json.RawMessage(`["1"]`)); err != nil {
		t.Fatal(err)
	}
	stored := func(key string) (string, bool) {
		t.Helper()
		var value string
		err := f.pool.QueryRow(ctx, `SELECT value::text FROM user_setting_values WHERE user_id = $1 AND profile_id = $2 AND key = $3 AND scope = 'profile'`,
			f.userID, f.primary, key).Scan(&value)
		return value, err == nil
	}
	settings := map[string]any{
		"unknown.key":                               true,
		settingskeys.UiDisabledLibraryIds:           []string{"9"},
		settingskeys.PlaybackCellularMaxBitrateKbps: nil,
		settingskeys.PlaybackAutoSkipCredits:        true,
		settingskeys.PlaybackIntroSkipMode:          "always",
		settingskeys.PlaybackSubtitleMode:           "not-a-mode",
	}
	if _, ok := stored(settingskeys.PlaybackSubtitleMode); ok {
		t.Fatal("fixture: subtitle mode already stored")
	}
	res := f.mustApply(t, setDoc(t, 2, main.with("settings", settings).with("settings_reset", []string{settingskeys.PlaybackAudioLanguage})))

	if _, ok := stored("unknown.key"); ok {
		t.Error("an unknown key was stored")
	}
	if v, _ := stored(settingskeys.UiDisabledLibraryIds); v != `["1"]` {
		t.Errorf("server_local key = %s, want the server's own value kept", v)
	}
	if _, ok := stored(settingskeys.PlaybackAudioLanguage); ok {
		t.Error("settings_reset left the row")
	}
	if v, ok := stored(settingskeys.PlaybackCellularMaxBitrateKbps); !ok || v != "null" {
		t.Errorf("null on a nullable key = %q, %v; want stored null", v, ok)
	}
	if v, _ := stored(settingskeys.PlaybackAutoSkipIntro); v != "true" {
		t.Errorf("intro_skip_mode's mirror = %s", v)
	}
	if _, ok := stored(settingskeys.PlaybackSubtitleMode); ok {
		t.Error("an invalid value was stored")
	}
	if !slices.Equal(res.SkippedSettings, []ProfileSetSkippedSetting{{SyncID: main.syncID(), Key: settingskeys.PlaybackSubtitleMode}}) {
		t.Errorf("skipped_settings = %+v", res.SkippedSettings)
	}
	p, _ := f.store.GetProfile(ctx, f.primary)
	if !p.AutoSkipCredits || !p.AutoSkipIntro {
		t.Errorf("column preferences = credits %v, intro %v; want both true", p.AutoSkipCredits, p.AutoSkipIntro)
	}
	held, err := f.handler.HeldProfileSet(ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := held.Items[0].Settings[settingskeys.UiDisabledLibraryIds]; ok {
		t.Error("GET reports a server_local key")
	}

	_, err = f.apply(t, setDoc(t, 3, main.with("settings", map[string]any{settingskeys.PlaybackAudioLanguage: "en"}).
		with("settings_reset", []string{settingskeys.PlaybackAudioLanguage})))
	assertSetField(t, err, "set.items[0].settings_reset[0]")
}

// TestProfileSetRepeatCountsPINGuessesDB: a repeat of the held set answers
// whether its set PIN matches the stored one, so each comparison spends the
// profile's PIN attempts as verifyProfilePIN does. It catches the repeat
// becoming an unthrottled way to read another profile's PIN.
func TestProfileSetRepeatCountsPINGuessesDB(t *testing.T) {
	f := newProfileSetDBFixture(t, 5)
	f.handler.PINAttempts = ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy)
	main, kid := newSetItem("Parent"), newSetItem("Kid")
	f.mustApply(t, setDoc(t, 1, main, kid.with("pin", map[string]any{"set": "1234"})))
	guess := func(pin string) error {
		_, err := f.apply(t, setDoc(t, 1, main, kid.with("pin", map[string]any{"set": pin})))
		return err
	}
	for i := range ratelimit.ProfilePINPolicy.MaxAttempts {
		if err := guess("0000"); !isStale(err) {
			t.Fatalf("wrong guess %d: err = %v, want profile_set_stale", i+1, err)
		}
	}
	for _, pin := range []string{"1234", "0000"} {
		var apiErr *APIError
		if err := guess(pin); !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || apiErr.RetryAfter <= 0 {
			t.Fatalf("guess %s while locked: err = %v, want 429 with Retry-After either way", pin, err)
		}
	}
}
