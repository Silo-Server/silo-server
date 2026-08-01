package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunSettingValues runs the canonical settings-contract storage conformance
// tests. It is exposed separately from RunSuite so each backend can pin this
// behavior on its own, which is what keeps the PostgreSQL and per-user SQLite
// stores from drifting on the table the whole contract rests on.
func RunSettingValues(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	t.Run("ExplicitValuesPerScope", func(t *testing.T) {
		testSettingValueScopes(t, newStore)
	})
	t.Run("UnsetIsNotFalsy", func(t *testing.T) {
		testSettingValueUnsetIsNotFalsy(t, newStore)
	})
	t.Run("RevisionIncrements", func(t *testing.T) {
		testSettingValueRevisions(t, newStore)
	})
	t.Run("PartialUniqueness", func(t *testing.T) {
		testSettingValuePartialUniqueness(t, newStore)
	})
	t.Run("IdentityValidation", func(t *testing.T) {
		testSettingValueIdentityValidation(t, newStore)
	})
	t.Run("PreferenceTransactionRollback", func(t *testing.T) {
		testPreferenceSettingsTransactionRollback(t, newStore)
	})
	t.Run("ResolutionCandidates", func(t *testing.T) {
		testSettingValueResolution(t, newStore)
	})
	t.Run("ListAll", func(t *testing.T) {
		testSettingValueListAll(t, newStore)
	})
	t.Run("DeletePaths", func(t *testing.T) {
		testSettingValueDeletePaths(t, newStore)
	})
	t.Run("MutationIdempotency", func(t *testing.T) {
		testSettingMutationIdempotency(t, newStore)
	})
}

// testPreferenceSettingsTransactionRollback proves that a failure after both
// the legacy row and an earlier canonical row were written rolls the whole
// group back. This is the failure ordering used by the shipped legacy profile,
// audio, subtitle and library preference routes during the canonical cutover.
func testPreferenceSettingsTransactionRollback(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	seedSettingProfiles(t, ctx, store, "p1")

	legacy := userstore.AudioPreference{
		ProfileID: "p1", SeriesID: seriesOne, AudioTrackIndex: 1, AudioLanguage: "en",
	}
	if err := store.SetAudioPreference(ctx, legacy); err != nil {
		t.Fatalf("seed legacy preference: %v", err)
	}
	id := seriesID(audioKey, "p1", seriesOne)
	seeded := mustUpsert(t, ctx, store, id, `"en"`)

	transactioner, ok := store.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		t.Fatal("store does not implement PreferenceSettingsTransactioner")
	}
	err := transactioner.WithPreferenceSettingsTransaction(ctx, func(tx userstore.PreferenceSettingsWriter) error {
		language := "ja"
		if err := tx.UpdateProfile(ctx, "p1", userstore.UpdateProfileInput{Language: &language}); err != nil {
			return err
		}
		updated := legacy
		updated.AudioTrackIndex = 2
		updated.AudioLanguage = "ja"
		if err := tx.SetAudioPreference(ctx, updated); err != nil {
			return err
		}
		if _, err := tx.UpsertSettingValue(ctx, id, json.RawMessage(`"ja"`)); err != nil {
			return err
		}
		// Fail after both writes. An invalid identity is rejected consistently by
		// both backends and represents any later canonical-write failure.
		_, err := tx.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.Scope("invalid"),
		}, json.RawMessage(`"fr"`))
		return err
	})
	if err == nil {
		t.Fatal("transaction with invalid final write succeeded")
	}
	gotProfile, err := store.GetProfile(ctx, "p1")
	if err != nil {
		t.Fatalf("read profile after rollback: %v", err)
	}
	if gotProfile == nil || gotProfile.Language != "" {
		t.Fatalf("profile language after rollback = %+v, want empty seeded value", gotProfile)
	}

	gotLegacy, err := store.GetAudioPreference(ctx, "p1", seriesOne)
	if err != nil {
		t.Fatalf("read legacy preference after rollback: %v", err)
	}
	if gotLegacy == nil || gotLegacy.AudioTrackIndex != 1 || gotLegacy.AudioLanguage != "en" {
		t.Fatalf("legacy preference after rollback = %+v, want seeded value", gotLegacy)
	}
	gotCanonical, err := store.GetSettingValue(ctx, id)
	if err != nil {
		t.Fatalf("read canonical value after rollback: %v", err)
	}
	if gotCanonical == nil || !jsonEqual(gotCanonical.Value, json.RawMessage(`"en"`)) {
		t.Fatalf("canonical value after rollback = %+v, want seeded value", gotCanonical)
	}
	if gotCanonical.Revision != seeded.Revision {
		t.Fatalf("canonical revision after rollback = %d, want %d", gotCanonical.Revision, seeded.Revision)
	}
}

const (
	audioKey    = "playback.audio_language"
	subtitleKey = "playback.subtitle_mode"

	// Fixture identities. Named so a backend that mixes up two scope columns
	// fails on the assertion rather than on a typo in one of the literals.
	deviceApple  = "apple-tv"
	seriesOne    = "s-1"
	seriesTwo    = "s-2"
	mutationHash = "hash-a"
)

// seedSettingProfiles creates the profiles every setting-value test addresses.
// The PostgreSQL table carries a composite profile FK, so a profile-anchored row
// cannot be written for a profile that does not exist.
func seedSettingProfiles(t *testing.T, ctx context.Context, store userstore.UserStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: "Profile " + id}); err != nil {
			t.Fatalf("CreateProfile(%s): %v", id, err)
		}
	}
}

func accountID(key string) userstore.SettingIdentity {
	return userstore.SettingIdentity{Key: key, Scope: settingscontract.ScopeAccount}
}

func profileID(key, profile string) userstore.SettingIdentity {
	return userstore.SettingIdentity{Key: key, Scope: settingscontract.ScopeProfile, ProfileID: profile}
}

func deviceID(key, profile, device string) userstore.SettingIdentity {
	return userstore.SettingIdentity{
		Key: key, Scope: settingscontract.ScopeProfileDevice, ProfileID: profile, DeviceID: device,
	}
}

func libraryID(key, profile string, library int) userstore.SettingIdentity {
	return userstore.SettingIdentity{
		Key: key, Scope: settingscontract.ScopeProfileLibrary, ProfileID: profile, LibraryID: library,
	}
}

func seriesID(key, profile, series string) userstore.SettingIdentity {
	return userstore.SettingIdentity{
		Key: key, Scope: settingscontract.ScopeProfileSeries, ProfileID: profile, SeriesID: series,
	}
}

func mustUpsert(
	t *testing.T,
	ctx context.Context,
	store userstore.UserStore,
	id userstore.SettingIdentity,
	value string,
) userstore.SettingValue {
	t.Helper()
	stored, err := store.UpsertSettingValue(ctx, id, json.RawMessage(value))
	if err != nil {
		t.Fatalf("UpsertSettingValue(%s at %s): %v", id.Key, id.Scope, err)
	}
	if stored == nil {
		t.Fatalf("UpsertSettingValue(%s at %s) returned nil", id.Key, id.Scope)
	}
	if stored.SettingIdentity != id {
		t.Fatalf("UpsertSettingValue echoed identity %+v, want %+v", stored.SettingIdentity, id)
	}
	if !jsonEqual(stored.Value, json.RawMessage(value)) {
		t.Fatalf("UpsertSettingValue stored %s, want %s", stored.Value, value)
	}
	return *stored
}

// testSettingValueScopes pins that every remote scope stores and reads back its
// own explicit value, that scopes do not read each other, and that an unset
// identity is nil rather than a zero value.
func testSettingValueScopes(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	seedSettingProfiles(t, ctx, store, "p1")

	cases := []struct {
		name  string
		id    userstore.SettingIdentity
		value string
	}{
		{"account", accountID(audioKey), `"en"`},
		{"profile", profileID(audioKey, "p1"), `"fr"`},
		{"profile_device", deviceID(audioKey, "p1", deviceApple), `"de"`},
		{"profile_library", libraryID(audioKey, "p1", 42), `"es"`},
		{"profile_series", seriesID(audioKey, "p1", "series-1"), `"ja"`},
	}

	for _, tc := range cases {
		missing, err := store.GetSettingValue(ctx, tc.id)
		if err != nil {
			t.Fatalf("GetSettingValue(%s, unset): %v", tc.name, err)
		}
		if missing != nil {
			t.Fatalf("GetSettingValue(%s, unset) = %+v, want nil", tc.name, missing)
		}
	}

	for _, tc := range cases {
		mustUpsert(t, ctx, store, tc.id, tc.value)
	}

	for _, tc := range cases {
		got, err := store.GetSettingValue(ctx, tc.id)
		if err != nil {
			t.Fatalf("GetSettingValue(%s): %v", tc.name, err)
		}
		if got == nil {
			t.Fatalf("GetSettingValue(%s) = nil, want a stored value", tc.name)
		}
		if !jsonEqual(got.Value, json.RawMessage(tc.value)) {
			t.Fatalf("GetSettingValue(%s) = %s, want %s", tc.name, got.Value, tc.value)
		}
		if got.Revision != 1 {
			t.Fatalf("GetSettingValue(%s) revision = %d, want 1", tc.name, got.Revision)
		}
		if got.CreatedAt == "" || got.UpdatedAt == "" {
			t.Fatalf("GetSettingValue(%s) timestamps = %q/%q, want both set", tc.name, got.CreatedAt, got.UpdatedAt)
		}
		if _, err := time.Parse(time.RFC3339, got.UpdatedAt); err != nil {
			t.Fatalf("GetSettingValue(%s) updated_at %q is not RFC3339: %v", tc.name, got.UpdatedAt, err)
		}
	}

	// A different profile, device, library or series is a different identity and
	// must not see the values above.
	seedSettingProfiles(t, ctx, store, "p2")
	for _, id := range []userstore.SettingIdentity{
		profileID(audioKey, "p2"),
		deviceID(audioKey, "p1", "iphone"),
		libraryID(audioKey, "p1", 43),
		seriesID(audioKey, "p1", "series-2"),
	} {
		got, err := store.GetSettingValue(ctx, id)
		if err != nil {
			t.Fatalf("GetSettingValue(neighbor %+v): %v", id, err)
		}
		if got != nil {
			t.Fatalf("GetSettingValue(neighbor %+v) = %+v, want nil", id, got)
		}
	}
}

// testSettingValueListAll pins the admin inspection read: every stored value
// comes back regardless of scope, with its identity intact, and nothing is
// invented for identities that were never written.
func testSettingValueListAll(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	empty, err := store.ListAllSettingValues(ctx)
	if err != nil {
		t.Fatalf("ListAllSettingValues(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListAllSettingValues(empty) = %+v, want none", empty)
	}

	seedSettingProfiles(t, ctx, store, "p1", "p2")
	seeded := map[userstore.SettingIdentity]string{
		accountID(audioKey):                   `"en"`,
		profileID(audioKey, "p1"):             `"fr"`,
		profileID(subtitleKey, "p2"):          `"always"`,
		deviceID(audioKey, "p1", deviceApple): `"de"`,
		libraryID(audioKey, "p1", 42):         `"es"`,
		seriesID(audioKey, "p1", seriesOne):   `"ja"`,
	}
	for id, value := range seeded {
		mustUpsert(t, ctx, store, id, value)
	}

	all, err := store.ListAllSettingValues(ctx)
	if err != nil {
		t.Fatalf("ListAllSettingValues: %v", err)
	}
	if len(all) != len(seeded) {
		t.Fatalf("ListAllSettingValues returned %d rows, want %d: %+v", len(all), len(seeded), all)
	}
	for _, got := range all {
		want, ok := seeded[got.SettingIdentity]
		if !ok {
			t.Fatalf("ListAllSettingValues invented identity %+v", got.SettingIdentity)
		}
		if !jsonEqual(got.Value, json.RawMessage(want)) {
			t.Fatalf("ListAllSettingValues(%+v) = %s, want %s", got.SettingIdentity, got.Value, want)
		}
		if got.Revision != 1 || got.UpdatedAt == "" {
			t.Fatalf("ListAllSettingValues(%+v) revision/updated_at = %d/%q, want 1/set",
				got.SettingIdentity, got.Revision, got.UpdatedAt)
		}
	}
}

// testSettingValueUnsetIsNotFalsy pins the distinction the whole contract rests
// on: false, 0, "" and JSON null are stored values, and only deleting the row
// makes a setting unset.
func testSettingValueUnsetIsNotFalsy(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	seedSettingProfiles(t, ctx, store, "p1")

	falsy := []struct {
		key   string
		value string
	}{
		{"playback.show_forced_subtitles", `false`},
		{"playback.next_up_prompt_seconds", `0`},
		{"catalog.metadata_language", `""`},
		{"playback.subtitle_language", `null`},
	}

	for _, tc := range falsy {
		id := profileID(tc.key, "p1")
		mustUpsert(t, ctx, store, id, tc.value)

		got, err := store.GetSettingValue(ctx, id)
		if err != nil {
			t.Fatalf("GetSettingValue(%s): %v", tc.key, err)
		}
		if got == nil {
			t.Fatalf("GetSettingValue(%s) = nil; %s must be a stored value, not unset", tc.key, tc.value)
		}
		if !jsonEqual(got.Value, json.RawMessage(tc.value)) {
			t.Fatalf("GetSettingValue(%s) = %s, want %s", tc.key, got.Value, tc.value)
		}

		removed, err := store.DeleteSettingValue(ctx, id)
		if err != nil {
			t.Fatalf("DeleteSettingValue(%s): %v", tc.key, err)
		}
		if !removed {
			t.Fatalf("DeleteSettingValue(%s) reported no row; %s was stored", tc.key, tc.value)
		}

		got, err = store.GetSettingValue(ctx, id)
		if err != nil {
			t.Fatalf("GetSettingValue(%s, after unset): %v", tc.key, err)
		}
		if got != nil {
			t.Fatalf("GetSettingValue(%s, after unset) = %+v, want nil", tc.key, got)
		}

		removed, err = store.DeleteSettingValue(ctx, id)
		if err != nil {
			t.Fatalf("DeleteSettingValue(%s, repeat): %v", tc.key, err)
		}
		if removed {
			t.Fatalf("DeleteSettingValue(%s, repeat) reported a row; the value was already unset", tc.key)
		}
	}
}

// testSettingValueRevisions pins last-write-wins with a per-row revision: each
// write replaces the value and increments revision, and created_at is not
// rewritten.
func testSettingValueRevisions(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	seedSettingProfiles(t, ctx, store, "p1")

	id := profileID(audioKey, "p1")
	first := mustUpsert(t, ctx, store, id, `"en"`)
	if first.Revision != 1 {
		t.Fatalf("first write revision = %d, want 1", first.Revision)
	}

	second := mustUpsert(t, ctx, store, id, `"ja"`)
	if second.Revision != 2 {
		t.Fatalf("second write revision = %d, want 2", second.Revision)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Fatalf("second write rewrote created_at %q -> %q", first.CreatedAt, second.CreatedAt)
	}

	third := mustUpsert(t, ctx, store, id, `"de"`)
	if third.Revision != 3 {
		t.Fatalf("third write revision = %d, want 3", third.Revision)
	}

	got, err := store.GetSettingValue(ctx, id)
	if err != nil {
		t.Fatalf("GetSettingValue: %v", err)
	}
	if got == nil || !jsonEqual(got.Value, json.RawMessage(`"de"`)) || got.Revision != 3 {
		t.Fatalf("GetSettingValue = %+v, want the newest write at revision 3", got)
	}

	// A re-set after an unset starts a fresh row rather than resurrecting the
	// old revision counter.
	if _, err := store.DeleteSettingValue(ctx, id); err != nil {
		t.Fatalf("DeleteSettingValue: %v", err)
	}
	reset := mustUpsert(t, ctx, store, id, `"it"`)
	if reset.Revision != 1 {
		t.Fatalf("revision after unset/re-set = %d, want 1", reset.Revision)
	}
}

// testSettingValuePartialUniqueness pins the five partial unique indexes: one
// explicit value per identity, and identities that differ in any one context
// column coexist.
func testSettingValuePartialUniqueness(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	seedSettingProfiles(t, ctx, store, "p1", "p2")

	identities := []userstore.SettingIdentity{
		accountID(audioKey),
		profileID(audioKey, "p1"),
		profileID(audioKey, "p2"),
		deviceID(audioKey, "p1", deviceApple),
		deviceID(audioKey, "p1", "iphone"),
		deviceID(audioKey, "p2", deviceApple),
		libraryID(audioKey, "p1", 1),
		libraryID(audioKey, "p1", 2),
		seriesID(audioKey, "p1", seriesOne),
		seriesID(audioKey, "p1", seriesTwo),
	}

	// Two writes each: the second must update its own row, never insert a
	// duplicate at the same identity.
	for _, id := range identities {
		mustUpsert(t, ctx, store, id, `"en"`)
		mustUpsert(t, ctx, store, id, `"ja"`)
	}

	rows, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       []string{audioKey},
		ProfileIDs: []string{"p1"},
		DeviceID:   deviceApple,
		LibraryIDs: []int{1, 2},
		SeriesIDs:  []string{seriesOne, seriesTwo},
	})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution: %v", err)
	}
	// p1's candidates: account, profile, one device, two libraries, two series.
	want := []userstore.SettingIdentity{
		accountID(audioKey),
		profileID(audioKey, "p1"),
		deviceID(audioKey, "p1", deviceApple),
		libraryID(audioKey, "p1", 1),
		libraryID(audioKey, "p1", 2),
		seriesID(audioKey, "p1", seriesOne),
		seriesID(audioKey, "p1", seriesTwo),
	}
	assertIdentitySet(t, rows, want)
	for _, row := range rows {
		if row.Revision != 2 {
			t.Fatalf("identity %+v has revision %d, want 2 — the second write inserted a duplicate row",
				row.SettingIdentity, row.Revision)
		}
	}
}

// testSettingValueIdentityValidation pins that both backends reject the same
// malformed identities and values, with the same sentinel errors, before any SQL
// runs. A scope CHECK violation surfacing as a driver error would read
// differently in each backend.
func testSettingValueIdentityValidation(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	seedSettingProfiles(t, ctx, store, "p1")

	invalid := []struct {
		name string
		id   userstore.SettingIdentity
	}{
		{"empty key", userstore.SettingIdentity{Scope: settingscontract.ScopeProfile, ProfileID: "p1"}},
		{"blank key", userstore.SettingIdentity{Key: "  ", Scope: settingscontract.ScopeProfile, ProfileID: "p1"}},
		{"unknown scope", userstore.SettingIdentity{Key: audioKey, Scope: "wishful"}},
		{"client_local is not remote", userstore.SettingIdentity{Key: audioKey, Scope: settingscontract.ScopeClientLocal}},
		{"default is not remote", userstore.SettingIdentity{Key: audioKey, Scope: settingscontract.ScopeDefault}},
		{"profile scope without profile", userstore.SettingIdentity{Key: audioKey, Scope: settingscontract.ScopeProfile}},
		{"account scope with profile", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeAccount, ProfileID: "p1",
		}},
		{"device scope without device", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeProfileDevice, ProfileID: "p1",
		}},
		{"profile scope with device", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeProfile, ProfileID: "p1", DeviceID: deviceApple,
		}},
		{"library scope without library", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeProfileLibrary, ProfileID: "p1",
		}},
		{"series scope with library", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeProfileSeries, ProfileID: "p1", SeriesID: seriesOne, LibraryID: 4,
		}},
		{"series scope without series", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeProfileSeries, ProfileID: "p1",
		}},
		// Padded ids must be rejected, not stored: the write path binds them
		// verbatim while resolution queries bind their trimmed forms, so a
		// padded id would persist as a row no resolution ever finds.
		{"padded profile id", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeProfile, ProfileID: " p1 ",
		}},
		{"padded key", userstore.SettingIdentity{
			Key: " " + audioKey, Scope: settingscontract.ScopeProfile, ProfileID: "p1",
		}},
		{"padded device id", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeProfileDevice, ProfileID: "p1", DeviceID: deviceApple + " ",
		}},
		{"padded series id", userstore.SettingIdentity{
			Key: audioKey, Scope: settingscontract.ScopeProfileSeries, ProfileID: "p1", SeriesID: " " + seriesOne,
		}},
	}

	for _, tc := range invalid {
		if _, err := store.UpsertSettingValue(ctx, tc.id, json.RawMessage(`"en"`)); !errors.Is(err, userstore.ErrInvalidSettingIdentity) {
			t.Fatalf("UpsertSettingValue(%s) error = %v, want ErrInvalidSettingIdentity", tc.name, err)
		}
		if _, err := store.GetSettingValue(ctx, tc.id); !errors.Is(err, userstore.ErrInvalidSettingIdentity) {
			t.Fatalf("GetSettingValue(%s) error = %v, want ErrInvalidSettingIdentity", tc.name, err)
		}
		if _, err := store.DeleteSettingValue(ctx, tc.id); !errors.Is(err, userstore.ErrInvalidSettingIdentity) {
			t.Fatalf("DeleteSettingValue(%s) error = %v, want ErrInvalidSettingIdentity", tc.name, err)
		}
	}

	valid := profileID(audioKey, "p1")
	for _, tc := range []struct {
		name  string
		value json.RawMessage
	}{
		{"empty", nil},
		{"truncated object", json.RawMessage(`{"fontScale":`)},
		{"bare word", json.RawMessage(`nope`)},
	} {
		if _, err := store.UpsertSettingValue(ctx, valid, tc.value); !errors.Is(err, userstore.ErrInvalidSettingValue) {
			t.Fatalf("UpsertSettingValue(%s value) error = %v, want ErrInvalidSettingValue", tc.name, err)
		}
	}
}

// testSettingValueResolution pins the read path's normative rule: one query
// returns every candidate row for a resolution request, unranked, and nothing
// belonging to another identity. Ranking is the resolver's job in Go.
func testSettingValueResolution(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	seedSettingProfiles(t, ctx, store, "p1", "p2")

	mustUpsert(t, ctx, store, accountID(audioKey), `"en"`)
	mustUpsert(t, ctx, store, profileID(audioKey, "p1"), `"fr"`)
	mustUpsert(t, ctx, store, deviceID(audioKey, "p1", deviceApple), `"de"`)
	mustUpsert(t, ctx, store, libraryID(audioKey, "p1", 42), `"es"`)
	mustUpsert(t, ctx, store, seriesID(audioKey, "p1", seriesOne), `"ja"`)
	mustUpsert(t, ctx, store, seriesID(audioKey, "p1", seriesTwo), `"ko"`)
	mustUpsert(t, ctx, store, profileID(subtitleKey, "p1"), `"forced"`)

	// Decoys: another profile, another device, another library, another series,
	// and a key nobody asked for.
	mustUpsert(t, ctx, store, profileID(audioKey, "p2"), `"pt"`)
	mustUpsert(t, ctx, store, deviceID(audioKey, "p1", "iphone"), `"nl"`)
	mustUpsert(t, ctx, store, libraryID(audioKey, "p1", 43), `"sv"`)
	mustUpsert(t, ctx, store, seriesID(audioKey, "p1", "s-3"), `"da"`)
	mustUpsert(t, ctx, store, profileID("ui.library_page_state", "p1"), `{"sort":"title"}`)

	// The batched shape: two content contexts resolved in one call.
	rows, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       []string{audioKey, subtitleKey},
		ProfileIDs: []string{"p1"},
		DeviceID:   deviceApple,
		LibraryIDs: []int{42},
		SeriesIDs:  []string{seriesOne, seriesTwo},
	})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution: %v", err)
	}
	assertIdentitySet(t, rows, []userstore.SettingIdentity{
		accountID(audioKey),
		profileID(audioKey, "p1"),
		deviceID(audioKey, "p1", deviceApple),
		libraryID(audioKey, "p1", 42),
		seriesID(audioKey, "p1", seriesOne),
		seriesID(audioKey, "p1", seriesTwo),
		profileID(subtitleKey, "p1"),
	})

	// A batch resolves the same rows n single-context calls would, which is what
	// lets a list view make one round trip instead of one per item.
	for _, series := range []string{seriesOne, seriesTwo} {
		single, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
			Keys:       []string{audioKey},
			ProfileIDs: []string{"p1"},
			DeviceID:   deviceApple,
			SeriesIDs:  []string{series},
		})
		if err != nil {
			t.Fatalf("ListSettingValuesForResolution(%s): %v", series, err)
		}
		assertIdentitySet(t, single, []userstore.SettingIdentity{
			accountID(audioKey),
			profileID(audioKey, "p1"),
			deviceID(audioKey, "p1", deviceApple),
			seriesID(audioKey, "p1", series),
		})
	}

	// No device identity — an incognito window, or jellycompat's seed — drops
	// profile_device candidates without touching the roaming ones.
	noDevice, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       []string{audioKey},
		ProfileIDs: []string{"p1"},
	})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution(no device): %v", err)
	}
	assertIdentitySet(t, noDevice, []userstore.SettingIdentity{
		accountID(audioKey),
		profileID(audioKey, "p1"),
	})

	// No profile at all leaves only account scope.
	accountOnly, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys: []string{audioKey},
	})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution(account only): %v", err)
	}
	assertIdentitySet(t, accountOnly, []userstore.SettingIdentity{accountID(audioKey)})

	// The household shape: several profiles in one read, which is what lets
	// GET /profiles serve a preference block per profile without a read each.
	// Every profile's own rows come back, and one profile's value can never
	// leak into another's because the identity travels on the row.
	household, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       []string{audioKey},
		ProfileIDs: []string{"p1", "p2"},
	})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution(household): %v", err)
	}
	assertIdentitySet(t, household, []userstore.SettingIdentity{
		accountID(audioKey),
		profileID(audioKey, "p1"),
		profileID(audioKey, "p2"),
	})

	// Blank and duplicate context ids are compacted rather than bound as
	// literals, so they neither match a '' row nor multiply the result set.
	dirty, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       []string{audioKey, "", audioKey, "   "},
		ProfileIDs: []string{"p1", "p1", "", "  "},
		DeviceID:   deviceApple,
		LibraryIDs: []int{42, 42, 0, -1},
		SeriesIDs:  []string{seriesOne, seriesOne, "", "  "},
	})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution(dirty): %v", err)
	}
	assertIdentitySet(t, dirty, []userstore.SettingIdentity{
		accountID(audioKey),
		profileID(audioKey, "p1"),
		deviceID(audioKey, "p1", deviceApple),
		libraryID(audioKey, "p1", 42),
		seriesID(audioKey, "p1", seriesOne),
	})

	// No keys is not an error and is not "everything".
	none, err := store.ListSettingValuesForResolution(ctx,
		userstore.SettingResolutionQuery{ProfileIDs: []string{"p1"}})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution(no keys): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListSettingValuesForResolution(no keys) = %d rows, want 0", len(none))
	}

	// An unknown key resolves to nothing rather than erroring; rejecting unknown
	// keys is the contract layer's job, not the store's.
	unknown, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       []string{"playback.not_a_setting"},
		ProfileIDs: []string{"p1"},
	})
	if err != nil {
		t.Fatalf("ListSettingValuesForResolution(unknown key): %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("ListSettingValuesForResolution(unknown key) = %d rows, want 0", len(unknown))
	}
}

// testSettingValueDeletePaths pins the application-enforced delete behavior.
// Neither backend can inherit this from constraints: the per-user SQLite store
// declares no foreign keys, and library, series and device columns reference
// nothing in PostgreSQL either.
func testSettingValueDeletePaths(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()

	// seed writes one value at every scope for two profiles, two devices, two
	// libraries and two series, so each delete can be checked for over-reach.
	seed := func(t *testing.T) (userstore.UserStore, []userstore.SettingIdentity) {
		t.Helper()
		store := newStore(t)
		seedSettingProfiles(t, ctx, store, "p1", "p2")
		identities := []userstore.SettingIdentity{
			accountID(audioKey),
			profileID(audioKey, "p1"),
			profileID(audioKey, "p2"),
			deviceID(audioKey, "p1", deviceApple),
			deviceID(audioKey, "p1", "iphone"),
			deviceID(audioKey, "p2", deviceApple),
			libraryID(audioKey, "p1", 1),
			libraryID(audioKey, "p1", 2),
			libraryID(audioKey, "p2", 1),
			seriesID(audioKey, "p1", seriesOne),
			seriesID(audioKey, "p1", seriesTwo),
			seriesID(audioKey, "p2", seriesOne),
		}
		for _, id := range identities {
			mustUpsert(t, ctx, store, id, `"en"`)
		}
		return store, identities
	}

	assertRemaining := func(t *testing.T, store userstore.UserStore, all, removed []userstore.SettingIdentity) {
		t.Helper()
		gone := make(map[userstore.SettingIdentity]struct{}, len(removed))
		for _, id := range removed {
			gone[id] = struct{}{}
		}
		for _, id := range all {
			got, err := store.GetSettingValue(ctx, id)
			if err != nil {
				t.Fatalf("GetSettingValue(%+v): %v", id, err)
			}
			_, shouldBeGone := gone[id]
			if shouldBeGone && got != nil {
				t.Fatalf("identity %+v survived a delete that owns it", id)
			}
			if !shouldBeGone && got == nil {
				t.Fatalf("identity %+v was removed by a delete that does not own it", id)
			}
		}
	}

	t.Run("Device", func(t *testing.T) {
		store, all := seed(t)
		removed, err := store.DeleteSettingValuesForDevice(ctx, "p1", deviceApple)
		if err != nil {
			t.Fatalf("DeleteSettingValuesForDevice: %v", err)
		}
		if removed != 1 {
			t.Fatalf("DeleteSettingValuesForDevice removed %d rows, want 1", removed)
		}
		assertRemaining(t, store, all, []userstore.SettingIdentity{deviceID(audioKey, "p1", deviceApple)})
	})

	t.Run("ForgetDeviceThroughDeviceSettings", func(t *testing.T) {
		store, all := seed(t)
		// DeleteAllDeviceSettings is the forget-device path: it must clear the
		// canonical profile_device values alongside the legacy string overrides.
		if err := store.SetDeviceSetting(ctx, userstore.DeviceSettingEntry{
			ProfileID: "p1", DeviceID: deviceApple, Key: "player.playback_speed", Value: "1.25",
		}); err != nil {
			t.Fatalf("SetDeviceSetting: %v", err)
		}
		if err := store.DeleteAllDeviceSettings(ctx, "p1", deviceApple); err != nil {
			t.Fatalf("DeleteAllDeviceSettings: %v", err)
		}
		legacy, err := store.GetDeviceSetting(ctx, "p1", deviceApple, "player.playback_speed")
		if err != nil {
			t.Fatalf("GetDeviceSetting after forget: %v", err)
		}
		if legacy != nil {
			t.Fatalf("GetDeviceSetting after forget = %+v, want nil", legacy)
		}
		assertRemaining(t, store, all, []userstore.SettingIdentity{deviceID(audioKey, "p1", deviceApple)})
	})

	t.Run("Library", func(t *testing.T) {
		store, all := seed(t)
		removed, err := store.DeleteSettingValuesForLibrary(ctx, 1)
		if err != nil {
			t.Fatalf("DeleteSettingValuesForLibrary: %v", err)
		}
		if removed != 2 {
			t.Fatalf("DeleteSettingValuesForLibrary removed %d rows, want 2 (one per profile)", removed)
		}
		assertRemaining(t, store, all, []userstore.SettingIdentity{
			libraryID(audioKey, "p1", 1),
			libraryID(audioKey, "p2", 1),
		})
	})

	t.Run("Series", func(t *testing.T) {
		store, all := seed(t)
		removed, err := store.DeleteSettingValuesForSeries(ctx, seriesOne)
		if err != nil {
			t.Fatalf("DeleteSettingValuesForSeries: %v", err)
		}
		if removed != 2 {
			t.Fatalf("DeleteSettingValuesForSeries removed %d rows, want 2 (one per profile)", removed)
		}
		assertRemaining(t, store, all, []userstore.SettingIdentity{
			seriesID(audioKey, "p1", seriesOne),
			seriesID(audioKey, "p2", seriesOne),
		})
	})

	t.Run("Profile", func(t *testing.T) {
		store, all := seed(t)
		removed, err := store.DeleteSettingValuesForProfile(ctx, "p1")
		if err != nil {
			t.Fatalf("DeleteSettingValuesForProfile: %v", err)
		}
		if removed != 7 {
			t.Fatalf("DeleteSettingValuesForProfile removed %d rows, want 7", removed)
		}
		assertRemaining(t, store, all, []userstore.SettingIdentity{
			profileID(audioKey, "p1"),
			deviceID(audioKey, "p1", deviceApple),
			deviceID(audioKey, "p1", "iphone"),
			libraryID(audioKey, "p1", 1),
			libraryID(audioKey, "p1", 2),
			seriesID(audioKey, "p1", seriesOne),
			seriesID(audioKey, "p1", seriesTwo),
		})
	})

	t.Run("DeleteProfileCascades", func(t *testing.T) {
		store, all := seed(t)
		if err := store.DeleteProfile(ctx, "p1"); err != nil {
			t.Fatalf("DeleteProfile: %v", err)
		}
		// Account scope belongs to the account, not to any one household member.
		assertRemaining(t, store, all, []userstore.SettingIdentity{
			profileID(audioKey, "p1"),
			deviceID(audioKey, "p1", deviceApple),
			deviceID(audioKey, "p1", "iphone"),
			libraryID(audioKey, "p1", 1),
			libraryID(audioKey, "p1", 2),
			seriesID(audioKey, "p1", seriesOne),
			seriesID(audioKey, "p1", seriesTwo),
		})
	})
}

// testSettingMutationIdempotency pins the receipt storage behind
// mutation_id idempotency: a receipt is written once and never overwritten, a
// replay reads back the original result, and expired receipts are sweepable.
func testSettingMutationIdempotency(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	expires := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
	record := userstore.SettingMutationRecord{
		MutationID:  "8cc515ad-88c5-48f0-a6cc-44d0a870e32c",
		RequestHash: mutationHash,
		Result:      json.RawMessage(`{"status":"applied"}`),
		ExpiresAt:   expires,
	}

	missing, err := store.GetSettingMutation(ctx, record.MutationID)
	if err != nil {
		t.Fatalf("GetSettingMutation(unrecorded): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetSettingMutation(unrecorded) = %+v, want nil", missing)
	}

	stored, inserted, err := store.PutSettingMutation(ctx, record)
	if err != nil {
		t.Fatalf("PutSettingMutation: %v", err)
	}
	if !inserted {
		t.Fatal("PutSettingMutation reported no insertion for a new mutation id")
	}
	if stored.RequestHash != mutationHash || !jsonEqual(stored.Result, record.Result) {
		t.Fatalf("PutSettingMutation stored %+v, want the submitted receipt", stored)
	}
	if !stored.ExpiresAt.Equal(expires) {
		t.Fatalf("PutSettingMutation expires_at = %s, want %s", stored.ExpiresAt, expires)
	}
	if stored.CreatedAt.IsZero() {
		t.Fatal("PutSettingMutation left created_at zero")
	}

	// A replay of the same id must read back the first result, whatever the
	// second attempt carries: that is what makes a retry idempotent instead of a
	// silent re-run, and what lets the caller answer mutation_id_conflict.
	replay := record
	replay.RequestHash = "hash-b"
	replay.Result = json.RawMessage(`{"status":"invalid_value"}`)
	existing, inserted, err := store.PutSettingMutation(ctx, replay)
	if err != nil {
		t.Fatalf("PutSettingMutation(replay): %v", err)
	}
	if inserted {
		t.Fatal("PutSettingMutation(replay) reported an insertion; the receipt already existed")
	}
	if existing.RequestHash != mutationHash {
		t.Fatalf("PutSettingMutation(replay) request hash = %q, want the original hash-a", existing.RequestHash)
	}
	if !jsonEqual(existing.Result, record.Result) {
		t.Fatalf("PutSettingMutation(replay) result = %s, want the original result", existing.Result)
	}

	got, err := store.GetSettingMutation(ctx, record.MutationID)
	if err != nil {
		t.Fatalf("GetSettingMutation: %v", err)
	}
	if got == nil || got.RequestHash != mutationHash {
		t.Fatalf("GetSettingMutation = %+v, want the original receipt", got)
	}

	// Expiry is not self-enforcing; the sweeper removes only what has expired.
	expired := userstore.SettingMutationRecord{
		MutationID:  "5ae96ffc-1077-4da8-8f64-a1ca9c3c72b8",
		RequestHash: "hash-c",
		Result:      json.RawMessage(`{"status":"applied"}`),
		ExpiresAt:   time.Now().UTC().Add(-time.Hour),
	}
	if _, _, err := store.PutSettingMutation(ctx, expired); err != nil {
		t.Fatalf("PutSettingMutation(expired): %v", err)
	}

	swept, err := store.DeleteExpiredSettingMutations(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("DeleteExpiredSettingMutations: %v", err)
	}
	if swept != 1 {
		t.Fatalf("DeleteExpiredSettingMutations swept %d rows, want 1", swept)
	}
	if got, err := store.GetSettingMutation(ctx, expired.MutationID); err != nil || got != nil {
		t.Fatalf("GetSettingMutation(expired) = %+v (%v), want nil", got, err)
	}
	if got, err := store.GetSettingMutation(ctx, record.MutationID); err != nil || got == nil {
		t.Fatalf("GetSettingMutation(live) = %+v (%v), want the unexpired receipt", got, err)
	}

	invalid := []userstore.SettingMutationRecord{
		{RequestHash: "h", Result: json.RawMessage(`{}`), ExpiresAt: expires},
		{MutationID: "m", Result: json.RawMessage(`{}`), ExpiresAt: expires},
		{MutationID: "m", RequestHash: "h", Result: json.RawMessage(`{}`)},
	}
	for i, rec := range invalid {
		if _, _, err := store.PutSettingMutation(ctx, rec); !errors.Is(err, userstore.ErrInvalidSettingIdentity) {
			t.Fatalf("PutSettingMutation(invalid %d) error = %v, want ErrInvalidSettingIdentity", i, err)
		}
	}
	if _, _, err := store.PutSettingMutation(ctx, userstore.SettingMutationRecord{
		MutationID: "m", RequestHash: "h", ExpiresAt: expires,
	}); !errors.Is(err, userstore.ErrInvalidSettingValue) {
		t.Fatalf("PutSettingMutation(no result) error = %v, want ErrInvalidSettingValue", err)
	}
}

// assertIdentitySet compares the returned rows to the expected identities as a
// set. Row order is deliberately not asserted: the two backends sort text under
// different collations, and ranking is the resolver's job anyway.
func assertIdentitySet(t *testing.T, rows []userstore.SettingValue, want []userstore.SettingIdentity) {
	t.Helper()
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, identityToken(row.SettingIdentity))
	}
	expected := make([]string, 0, len(want))
	for _, id := range want {
		expected = append(expected, identityToken(id))
	}
	sort.Strings(got)
	sort.Strings(expected)
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("candidate identities =\n  %v\nwant\n  %v", got, expected)
	}
}

func identityToken(id userstore.SettingIdentity) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%s",
		id.Key, id.Scope, id.ProfileID, id.DeviceID, id.LibraryID, id.SeriesID)
}

// jsonEqual compares two JSON documents by value. PostgreSQL stores jsonb, which
// re-serializes objects in its own key order, so a byte comparison would report
// a difference between the backends that no client can observe.
func jsonEqual(a, b json.RawMessage) bool {
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
