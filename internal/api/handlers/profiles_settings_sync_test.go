package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// These tests pin the seam the settings cutover opened: every server-side
// reader of the profile preferences resolves them from user_setting_values,
// while clients still write them through the profile create and update
// operations. A profile write that does not land in the canonical store never
// takes effect: the stale backfilled row (or the contract default) wins
// forever.

// updateOwnProfile updates profileID acting as itself (profile-1 is the
// household primary).
func updateOwnProfile(handler *ProfileHandler, profileID string, req ProfileUpdateRequest) (ProfileView, error) {
	return handler.UpdateProfile(profileServiceContext(profileID), ProfileUpdateCommand{
		UserID: 1, ProfileID: profileID, ActiveProfileID: profileID, Request: req,
		VerifyProfile: noProfileVerification,
	})
}

// createAsPrimary creates a profile acting as the primary profile-1.
func createAsPrimary(handler *ProfileHandler, req ProfileCreateRequest) (ProfileView, error) {
	return handler.CreateProfile(profileServiceContext("profile-1"), ProfileCreateCommand{
		UserID: 1, ActiveProfileID: "profile-1", Request: req, VerifyProfile: noProfileVerification,
	})
}

func storedProfileSetting(t *testing.T, store userstore.UserStore, key, profileID string) *userstore.SettingValue {
	t.Helper()
	value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       key,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: profileID,
	})
	if err != nil {
		t.Fatalf("reading canonical %s: %v", key, err)
	}
	return value
}

// TestUpdateProfileClearingLanguageClearsCanonicalRow: the legacy empty
// string means "no preference", spelled canonically as no row at all.
func TestUpdateProfileClearingLanguageClearsCanonicalRow(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	for _, language := range []string{"fr", ""} {
		if _, err := updateOwnProfile(handler, "profile-1", ProfileUpdateRequest{PreferredMetadataLanguage: new(language)}); err != nil {
			t.Fatalf("update to %q: %v", language, err)
		}
	}

	if value := storedProfileSetting(t, store, settingskeys.CatalogMetadataLanguage, "profile-1"); value != nil {
		t.Errorf("canonical row = %s after clearing, want none", value.Value)
	}
	if got := access.PreferredMetadataLanguage(context.Background(), store, "profile-1"); got != "" {
		t.Errorf("resolved metadata language = %q after clearing, want \"\"", got)
	}
}

// TestUpdateProfileSyncsSkipPreferences. The player resolves these keys
// canonically, so an update that only moved the columns would succeed and
// change nothing about playback.
func TestUpdateProfileSyncsSkipPreferences(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	if _, err := updateOwnProfile(handler, "profile-1", ProfileUpdateRequest{
		AutoSkipIntro: new(true), AutoSkipCredits: new(true), AutoSkipRecap: new(true), AutoPlayNextPreview: new(false),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	for key, want := range map[string]string{
		settingskeys.PlaybackAutoSkipIntro:       `true`,
		settingskeys.PlaybackAutoSkipCredits:     `true`,
		settingskeys.PlaybackAutoSkipRecap:       `true`,
		settingskeys.PlaybackAutoPlayNextPreview: `false`,
		// auto_skip_intro carries its revision-7 replacement with it, so a
		// client still sending the old field does not leave a current client
		// resolving the contract default.
		settingskeys.PlaybackIntroSkipMode: `"always"`,
	} {
		value := storedProfileSetting(t, store, key, "profile-1")
		if value == nil {
			t.Errorf("no canonical %s row after the profile update", key)
			continue
		}
		if string(value.Value) != want {
			t.Errorf("canonical %s = %s, want %s", key, value.Value, want)
		}
	}

	// A field the request omitted must not be written: clients send
	// single-field deltas, and an absent field is not a choice. Its own
	// store, since the test store's DSN is derived from the test name.
	t.Run("omitted fields are not written", func(t *testing.T) {
		store := newProfileTestStore(t)
		handler := NewProfileHandler(testUserStoreProvider{store: store})
		if _, err := updateOwnProfile(handler, "profile-1", ProfileUpdateRequest{AutoSkipIntro: new(true)}); err != nil {
			t.Fatalf("single-field update: %v", err)
		}
		if value := storedProfileSetting(t, store, settingskeys.PlaybackAutoSkipCredits, "profile-1"); value != nil {
			t.Errorf("an omitted field wrote %s", value.Value)
		}
	})
}

// TestUpdateProfileRejectsInvalidLanguageBeforeWriting: a value the canonical
// store would refuse must fail the request as a no-op instead of leaving the
// column and the canonical store disagreeing.
func TestUpdateProfileRejectsInvalidLanguageBeforeWriting(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	_, err := updateOwnProfile(handler, "profile-1", ProfileUpdateRequest{Language: new("!!!")})
	requireAPIStatus(t, err, http.StatusBadRequest)

	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil || profile == nil {
		t.Fatalf("reading profile: %v", err)
	}
	if profile.Language != "" {
		t.Errorf("column = %q after a rejected write, want untouched", profile.Language)
	}
	if value := storedProfileSetting(t, store, settingskeys.PlaybackAudioLanguage, "profile-1"); value != nil {
		t.Errorf("canonical row = %s after a rejected write, want none", value.Value)
	}
}

// TestCreateProfileSyncsCanonicalSettings: a profile born with preferences is
// resolvable canonically from its first request, and it inherits the
// account-level settings that survive from before profiles had their own.
func TestCreateProfileSyncsCanonicalSettings(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.SetSetting(context.Background(), searchMediaScopeSettingKey, "audiobook"); err != nil {
		t.Fatalf("seeding legacy account setting: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	created, err := createAsPrimary(handler, ProfileCreateRequest{Name: "Kids", Language: "de", PreferredMetadataLanguage: "fr"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	audio := storedProfileSetting(t, store, settingskeys.PlaybackAudioLanguage, created.ID)
	if audio == nil || string(audio.Value) != `"de"` {
		t.Errorf("canonical audio language after create = %v, want \"de\"", audio)
	}
	if got := access.PreferredMetadataLanguage(context.Background(), store, created.ID); got != "fr" {
		t.Errorf("canonical metadata language after create = %q, want %q", got, "fr")
	}
	if inherited := storedProfileSetting(t, store, searchMediaScopeSettingKey, created.ID); inherited == nil || string(inherited.Value) != `"audiobook"` {
		t.Errorf("inherited canonical value = %+v", inherited)
	}
}

// failingSettingsWriteStore fails every canonical setting write, simulating a
// store whose user_setting_values table is unavailable while profile CRUD
// still works.
type failingSettingsWriteStore struct {
	userstore.UserStore
}

type failingPreferenceSettingsWriter struct {
	userstore.PreferenceSettingsWriter
}

func (s failingSettingsWriteStore) UpsertSettingValue(
	context.Context, userstore.SettingIdentity, json.RawMessage,
) (*userstore.SettingValue, error) {
	return nil, errors.New("settings storage unavailable")
}

func (s failingSettingsWriteStore) WithPreferenceSettingsTransaction(
	ctx context.Context,
	fn func(userstore.PreferenceSettingsWriter) error,
) error {
	transactioner, ok := s.UserStore.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		return errors.New("wrapped store does not support preference settings transactions")
	}
	return transactioner.WithPreferenceSettingsTransaction(ctx, func(tx userstore.PreferenceSettingsWriter) error {
		return fn(failingPreferenceSettingsWriter{PreferenceSettingsWriter: tx})
	})
}

func (w failingPreferenceSettingsWriter) UpsertSettingValue(
	context.Context, userstore.SettingIdentity, json.RawMessage,
) (*userstore.SettingValue, error) {
	return nil, errors.New("settings storage unavailable")
}

// TestProfileWritesRollBackWhenSettingsSyncFails pins the atomic profile and
// canonical-settings transaction: a failed canonical write leaves no
// half-configured profile, so the client's retry does not hit a name
// conflict, and an update leaves both the column and the canonical row as
// they were.
func TestProfileWritesRollBackWhenSettingsSyncFails(t *testing.T) {
	base := newProfileTestStore(t)
	failing := NewProfileHandler(testUserStoreProvider{store: failingSettingsWriteStore{UserStore: base}})

	_, err := createAsPrimary(failing, ProfileCreateRequest{Name: "Kids", Language: "de"})
	requireAPIStatus(t, err, http.StatusInternalServerError)
	profiles, err := base.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("listing profiles: %v", err)
	}
	for _, p := range profiles {
		if p.Name == "Kids" {
			t.Fatalf("profile %q survived a failed settings sync", p.Name)
		}
	}
	// The rollback lets the retry succeed once the store recovers.
	if _, err := createAsPrimary(NewProfileHandler(testUserStoreProvider{store: base}), ProfileCreateRequest{Name: "Kids", Language: "de"}); err != nil {
		t.Fatalf("retry create: %v", err)
	}

	before, err := base.GetProfile(context.Background(), "profile-1")
	if err != nil || before == nil {
		t.Fatalf("reading profile before update: profile=%+v err=%v", before, err)
	}
	_, err = updateOwnProfile(failing, "profile-1", ProfileUpdateRequest{Language: new("de")})
	requireAPIStatus(t, err, http.StatusInternalServerError)
	after, err := base.GetProfile(context.Background(), "profile-1")
	if err != nil || after == nil {
		t.Fatalf("reading profile after rollback: profile=%+v err=%v", after, err)
	}
	if after.Language != before.Language {
		t.Fatalf("legacy language after rollback = %q, want %q", after.Language, before.Language)
	}
	if value := storedProfileSetting(t, base, settingskeys.PlaybackAudioLanguage, "profile-1"); value != nil {
		t.Fatalf("canonical language survived rollback: %+v", value)
	}
}

// TestUpdateProfilePublishesUserSettingsEvents: the synced rows change what
// other clients resolve, so they get the same refresh signal a settings
// write publishes.
func TestUpdateProfilePublishesUserSettingsEvents(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := handler.EventsHub.Subscribe()
	defer unsubscribe()

	if _, err := updateOwnProfile(handler, "profile-1", ProfileUpdateRequest{PreferredMetadataLanguage: new("de")}); err != nil {
		t.Fatalf("update: %v", err)
	}

	env := receiveUserSettingsEvent(t, events)
	assertUserSettingsEnvelope(t, env, settingskeys.CatalogMetadataLanguage, "profile")

	// A field the request did not carry publishes nothing.
	select {
	case extra := <-events:
		t.Errorf("unexpected extra event for %s", extra.Data)
	default:
	}
}

// --- Read side ---
//
// The mirror of the tests above: the view's preference fields are served from
// the canonical rows, so a write that never touched a legacy column is still
// visible to every profile reader on every platform.

func listProfilesOf(t *testing.T, handler *ProfileHandler) ProfileListView {
	t.Helper()
	resp, err := handler.ListProfiles(profileServiceContext("profile-1"), 1)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	return resp
}

func profileFromList(t *testing.T, resp ProfileListView, profileID string) ProfileView {
	t.Helper()
	for _, p := range resp.Profiles {
		if p.ID == profileID {
			return p
		}
	}
	t.Fatalf("profile %s missing from the list response", profileID)
	return ProfileView{}
}

// TestListProfilesServesCanonicalWrite is the cross-client coherence gap this
// read path exists to close: a preference saved as a profile-scope setting
// writes only user_setting_values, and the profile view must reflect it on
// the next read without the legacy column having moved at all.
func TestListProfilesServesCanonicalWrite(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	before, err := store.GetProfile(ctx, "profile-1")
	if err != nil || before == nil {
		t.Fatalf("reading the profile before the canonical write: %v", err)
	}

	for key, value := range map[string]string{
		settingskeys.PlaybackAudioLanguage:       `"de"`,
		settingskeys.CatalogMetadataLanguage:     `"fr"`,
		settingskeys.PlaybackSubtitleLanguage:    `"ja"`,
		settingskeys.PlaybackSubtitleMode:        `"always"`,
		settingskeys.PlaybackShowForcedSubtitles: `false`,
	} {
		if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key:       key,
			Scope:     settingscontract.ScopeProfile,
			ProfileID: "profile-1",
		}, json.RawMessage(value)); err != nil {
			t.Fatalf("canonical write of %s: %v", key, err)
		}
	}

	got := profileFromList(t, listProfilesOf(t, handler), "profile-1")
	if got.Language != "de" || got.PreferredMetadataLanguage != "fr" || got.SubtitleLanguage != "ja" ||
		got.SubtitleMode != "always" || got.ShowForcedSubtitles {
		t.Errorf("listed preferences = %+v, want the canonical values", got)
	}

	// The legacy columns never moved: the canonical write is the only storage
	// involved, which is precisely why reading the columns hid it.
	after, err := store.GetProfile(ctx, "profile-1")
	if err != nil || after == nil {
		t.Fatalf("reading the profile after the canonical write: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("a canonical write moved the legacy columns:\n before = %+v\n after  = %+v", before, after)
	}
}

// TestListProfilesRoundTripsProfileWrite: a profile update reaches every
// reader. The columns are no longer read, so this only passes because the
// write mirrors into the canonical rows, replacing a stale one.
func TestListProfilesRoundTripsProfileWrite(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       settingskeys.CatalogMetadataLanguage,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: "profile-1",
	}, json.RawMessage(`"fr"`)); err != nil {
		t.Fatalf("seed stale canonical language: %v", err)
	}

	updated, err := updateOwnProfile(handler, "profile-1", ProfileUpdateRequest{
		Language: new("es"), PreferredMetadataLanguage: new("it"), SubtitleLanguage: new("ko"),
		SubtitleMode: new("off"), ShowForcedSubtitles: new(false),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// The update's view and the next list must agree; both serve resolution.
	listed := profileFromList(t, listProfilesOf(t, handler), "profile-1")
	if !reflect.DeepEqual(updated, listed) {
		t.Errorf("update view and list disagree:\n update = %+v\n list   = %+v", updated, listed)
	}
	if listed.Language != "es" || listed.PreferredMetadataLanguage != "it" || listed.SubtitleLanguage != "ko" ||
		listed.SubtitleMode != "off" || listed.ShowForcedSubtitles {
		t.Errorf("listed preferences = %+v, want the updated values", listed)
	}
	for key, want := range map[string]string{
		settingskeys.PlaybackAudioLanguage:       `"es"`,
		settingskeys.CatalogMetadataLanguage:     `"it"`,
		settingskeys.PlaybackSubtitleLanguage:    `"ko"`,
		settingskeys.PlaybackSubtitleMode:        `"off"`,
		settingskeys.PlaybackShowForcedSubtitles: `false`,
	} {
		value := storedProfileSetting(t, store, key, "profile-1")
		if value == nil || string(value.Value) != want {
			t.Errorf("canonical %s = %v, want %s", key, value, want)
		}
	}
	if got := access.PreferredMetadataLanguage(context.Background(), store, "profile-1"); got != "it" {
		t.Errorf("resolved metadata language = %q, want it instead of stale fr", got)
	}
}

// TestListProfilesResolvesHouseholdInOneRead: the list serves several
// profiles, so it must not cost a store read each. It also pins that one
// profile's preference never leaks into another's.
func TestListProfilesResolvesHouseholdInOneRead(t *testing.T) {
	ctx := context.Background()
	base := newProfileTestStore(t)
	if err := base.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("creating the second profile: %v", err)
	}
	for profileID, language := range map[string]string{
		"profile-1": `"de"`,
		"profile-2": `"ja"`,
	} {
		if _, err := base.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key:       settingskeys.PlaybackSubtitleLanguage,
			Scope:     settingscontract.ScopeProfile,
			ProfileID: profileID,
		}, json.RawMessage(language)); err != nil {
			t.Fatalf("canonical write for %s: %v", profileID, err)
		}
	}

	store := &countingResolutionStore{UserStore: base}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	resp := listProfilesOf(t, handler)
	if got := profileFromList(t, resp, "profile-1").SubtitleLanguage; got != "de" {
		t.Errorf("profile-1 subtitle_language = %q, want %q", got, "de")
	}
	if got := profileFromList(t, resp, "profile-2").SubtitleLanguage; got != "ja" {
		t.Errorf("profile-2 subtitle_language = %q, want %q", got, "ja")
	}
	if store.reads != 1 {
		t.Errorf("listing %d profiles issued %d resolution reads, want 1",
			len(resp.Profiles), store.reads)
	}
}

// countingResolutionStore counts the batched resolution reads a request makes,
// so a regression to one read per profile fails rather than merely slowing
// the list down.
type countingResolutionStore struct {
	userstore.UserStore
	reads int
}

func (s *countingResolutionStore) ListSettingValuesForResolution(
	ctx context.Context, query userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	s.reads++
	return s.UserStore.ListSettingValuesForResolution(ctx, query)
}
