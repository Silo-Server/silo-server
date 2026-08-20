package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
// while the shipped clients still write them through POST/PUT /profiles. A
// profile write that does not land in the canonical store never takes effect
// — the stale backfilled row (or the contract default) wins forever.

// updateProfileVia sends PUT /profiles/{id} as profile-1's own session.
func updateProfileVia(t *testing.T, handler *ProfileHandler, profileID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newAuthorizedProfileRequestWithRole(
		http.MethodPut, "/profiles/"+profileID, body, "user", profileID)
	req = withProfileRouteParam(req, "id", profileID)
	rr := httptest.NewRecorder()
	handler.HandleUpdateProfile(rr, req)
	return rr
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

// TestUpdateProfileSyncsCanonicalMetadataLanguage replays the cutover bug: a
// backfilled canonical row said "fr", the user changes the metadata language
// to "de" through the legacy profile endpoint, and access-scope resolution
// must see "de" — not the stale "fr" the one-time backfill left behind.
func TestUpdateProfileSyncsCanonicalMetadataLanguage(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	// The one-time backfill stored the pre-cutover column value.
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       settingskeys.CatalogMetadataLanguage,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: "profile-1",
	}, json.RawMessage(`"fr"`)); err != nil {
		t.Fatalf("seeding backfilled row: %v", err)
	}

	rr := updateProfileVia(t, handler, "profile-1", `{"preferred_metadata_language":"de"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	// The SQLite per-user schema never grew a preferred_metadata_language
	// column, so the canonical row is the only storage this write has — which
	// is exactly why the sync must exist.
	if got := access.PreferredMetadataLanguage(context.Background(), store, "profile-1"); got != "de" {
		t.Errorf("canonical metadata language = %q after profile update, want %q", got, "de")
	}
}

// TestUpdateProfileSyncsCanonicalAudioLanguage is the playback-start half: a
// profile that never had a backfilled row chooses a spoken language, and the
// canonical store — which preferredAudioTrackIndexV3 resolves when a start omits
// the audio track — must carry it.
func TestUpdateProfileSyncsCanonicalAudioLanguage(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	rr := updateProfileVia(t, handler, "profile-1", `{"language":"de"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	value := storedProfileSetting(t, store, settingskeys.PlaybackAudioLanguage, "profile-1")
	if value == nil {
		t.Fatal("no canonical playback.audio_language row after the profile update")
	}
	if string(value.Value) != `"de"` {
		t.Errorf("canonical audio language = %s, want \"de\"", value.Value)
	}
}

// TestUpdateProfileClearingLanguageClearsCanonicalRow: the legacy empty
// string means "no preference", spelled canonically as no row at all.
func TestUpdateProfileClearingLanguageClearsCanonicalRow(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	if rr := updateProfileVia(t, handler, "profile-1",
		`{"preferred_metadata_language":"fr"}`); rr.Code != http.StatusOK {
		t.Fatalf("seeding PUT = %d: %s", rr.Code, rr.Body.String())
	}
	if rr := updateProfileVia(t, handler, "profile-1",
		`{"preferred_metadata_language":""}`); rr.Code != http.StatusOK {
		t.Fatalf("clearing PUT = %d: %s", rr.Code, rr.Body.String())
	}

	if value := storedProfileSetting(t, store, settingskeys.CatalogMetadataLanguage, "profile-1"); value != nil {
		t.Errorf("canonical row = %s after clearing, want none", value.Value)
	}
	if got := access.PreferredMetadataLanguage(context.Background(), store, "profile-1"); got != "" {
		t.Errorf("resolved metadata language = %q after clearing, want \"\"", got)
	}
}

// TestUpdateProfileSyncsSubtitlePreferences covers the triple the player's
// subtitle picker still saves through PUT /profiles, resolved canonically by
// catalog detail since the earlier cutover.
func TestUpdateProfileSyncsSubtitlePreferences(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	rr := updateProfileVia(t, handler, "profile-1",
		`{"subtitle_language":"ja","subtitle_mode":"always","show_forced_subtitles":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	for key, want := range map[string]string{
		settingskeys.PlaybackSubtitleLanguage:    `"ja"`,
		settingskeys.PlaybackSubtitleMode:        `"always"`,
		settingskeys.PlaybackShowForcedSubtitles: `false`,
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
}

// TestUpdateProfileSyncsSkipPreferences. The player resolves these four keys
// canonically, so a legacy PUT that only moved the columns would return 200
// and change nothing about playback.
func TestUpdateProfileSyncsSkipPreferences(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	rr := updateProfileVia(t, handler, "profile-1",
		`{"auto_skip_intro":true,"auto_skip_credits":true,"auto_skip_recap":true,`+
			`"auto_play_next_preview":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	for key, want := range map[string]string{
		settingskeys.PlaybackAutoSkipIntro:       `true`,
		settingskeys.PlaybackAutoSkipCredits:     `true`,
		settingskeys.PlaybackAutoSkipRecap:       `true`,
		settingskeys.PlaybackAutoPlayNextPreview: `false`,
		// auto_skip_intro carries its revision-7 replacement with it, so a
		// client still using the legacy route does not leave a current client
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

	// A field the request omitted must not be written: the shipped clients
	// send single-field deltas, and an absent field is not a choice. Its own
	// store, since the test store's DSN is derived from the test name.
	t.Run("omitted fields are not written", func(t *testing.T) {
		store := newProfileTestStore(t)
		handler := NewProfileHandler(testUserStoreProvider{store: store})
		if rr := updateProfileVia(t, handler, "profile-1", `{"auto_skip_intro":true}`); rr.Code != http.StatusOK {
			t.Fatalf("single-field PUT = %d: %s", rr.Code, rr.Body.String())
		}
		if value := storedProfileSetting(t, store, settingskeys.PlaybackAutoSkipCredits, "profile-1"); value != nil {
			t.Errorf("an omitted field wrote %s", value.Value)
		}
	})
}

// TestUpdateProfileSyncsIntroSkipMode covers both directions of the legacy
// switch, including the one an untouched-looking false has to produce: "ask" is
// the mode that reproduces what auto_skip_intro=false always did, and a profile
// route that wrote only the boolean would leave the enum saying something else.
func TestUpdateProfileSyncsIntroSkipMode(t *testing.T) {
	for _, tc := range []struct{ body, wantMode, wantBool string }{
		{`{"auto_skip_intro":true}`, `"always"`, `true`},
		{`{"auto_skip_intro":false}`, `"ask"`, `false`},
	} {
		t.Run(tc.body, func(t *testing.T) {
			store := newProfileTestStore(t)
			handler := NewProfileHandler(testUserStoreProvider{store: store})

			if rr := updateProfileVia(t, handler, "profile-1", tc.body); rr.Code != http.StatusOK {
				t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
			}

			for key, want := range map[string]string{
				settingskeys.PlaybackIntroSkipMode: tc.wantMode,
				settingskeys.PlaybackAutoSkipIntro: tc.wantBool,
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
		})
	}
}

// TestUpdateProfileRejectsInvalidLanguageBeforeWriting: a value the canonical
// endpoint would refuse must fail the request as a no-op instead of leaving
// the column and the canonical store disagreeing.
func TestUpdateProfileRejectsInvalidLanguageBeforeWriting(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	rr := updateProfileVia(t, handler, "profile-1", `{"language":"!!!"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT of an invalid tag = %d, want 400: %s", rr.Code, rr.Body.String())
	}

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

// TestCreateProfileSyncsCanonicalLanguages: a profile born with preferences
// must be resolvable canonically from its first request.
func TestCreateProfileSyncsCanonicalLanguages(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(http.MethodPost, "/profiles",
		`{"name":"Kids","language":"de","preferred_metadata_language":"fr"}`,
		"user", "profile-1")
	rr := httptest.NewRecorder()
	handler.HandleCreateProfile(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rr.Code, rr.Body.String())
	}
	var created profileResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}

	audio := storedProfileSetting(t, store, settingskeys.PlaybackAudioLanguage, created.ID)
	if audio == nil || string(audio.Value) != `"de"` {
		t.Errorf("canonical audio language after create = %v, want \"de\"", audio)
	}
	if got := access.PreferredMetadataLanguage(context.Background(), store, created.ID); got != "fr" {
		t.Errorf("canonical metadata language after create = %q, want %q", got, "fr")
	}
}

func TestCreateProfileInheritsSurvivingLegacyAccountSettings(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.SetSetting(context.Background(), searchMediaScopeSettingKey, "audiobook"); err != nil {
		t.Fatalf("seeding legacy account setting: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	req := newAuthorizedProfileRequestWithRole(http.MethodPost, "/profiles",
		`{"name":"Guest"}`, "user", "profile-1")
	rec := httptest.NewRecorder()
	handler.HandleCreateProfile(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	var created profileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	value := storedProfileSetting(t, store, searchMediaScopeSettingKey, created.ID)
	if value == nil || string(value.Value) != `"audiobook"` {
		t.Fatalf("inherited canonical value = %+v", value)
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

// TestCreateProfileRollsBackWhenSettingsSyncFails pins the atomic profile and
// canonical-settings transaction. A failed canonical write must leave no
// half-configured profile and the client's retry must not hit a name conflict.
func TestCreateProfileRollsBackWhenSettingsSyncFails(t *testing.T) {
	base := newProfileTestStore(t)
	store := failingSettingsWriteStore{UserStore: base}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(http.MethodPost, "/profiles",
		`{"name":"Kids","language":"de"}`, "user", "profile-1")
	rr := httptest.NewRecorder()
	handler.HandleCreateProfile(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("POST = %d, want 500: %s", rr.Code, rr.Body.String())
	}

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
	retry := newAuthorizedProfileRequestWithRole(http.MethodPost, "/profiles",
		`{"name":"Kids","language":"de"}`, "user", "profile-1")
	retryRec := httptest.NewRecorder()
	NewProfileHandler(testUserStoreProvider{store: base}).HandleCreateProfile(retryRec, retry)
	if retryRec.Code != http.StatusCreated {
		t.Fatalf("retry POST = %d, want 201: %s", retryRec.Code, retryRec.Body.String())
	}
}

func TestUpdateProfileRollsBackWhenSettingsSyncFails(t *testing.T) {
	base := newProfileTestStore(t)
	store := failingSettingsWriteStore{UserStore: base}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	before, err := base.GetProfile(context.Background(), "profile-1")
	if err != nil || before == nil {
		t.Fatalf("reading profile before update: profile=%+v err=%v", before, err)
	}
	rr := updateProfileVia(t, handler, "profile-1", `{"language":"de"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("PUT = %d, want 500: %s", rr.Code, rr.Body.String())
	}

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
// other clients resolve, so they get the same refresh signal a
// /settings/values write publishes.
func TestUpdateProfilePublishesUserSettingsEvents(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := handler.EventsHub.Subscribe()
	defer unsubscribe()

	rr := updateProfileVia(t, handler, "profile-1", `{"preferred_metadata_language":"de"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
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
// The mirror of the tests above: the DTO's preference fields are served from
// the canonical rows, so a write that never touched a legacy column is still
// visible to every profile-DTO reader on every platform.

// listProfilesVia sends GET /profiles as profile-1's own session.
func listProfilesVia(t *testing.T, handler *ProfileHandler) profileListResponse {
	t.Helper()
	req := newAuthorizedProfileRequestWithRole(http.MethodGet, "/profiles", "", "user", "profile-1")
	rr := httptest.NewRecorder()
	handler.HandleListProfiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /profiles = %d: %s", rr.Code, rr.Body.String())
	}
	var resp profileListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding profile list: %v", err)
	}
	return resp
}

func profileFromList(t *testing.T, resp profileListResponse, profileID string) profileResponse {
	t.Helper()
	for _, p := range resp.Profiles {
		if p.ID == profileID {
			return p
		}
	}
	t.Fatalf("profile %s missing from the list response", profileID)
	return profileResponse{}
}

// TestListProfilesServesCanonicalWrite is the cross-client coherence gap this
// read path exists to close: a preference saved through PUT
// /settings/values?scope=profile writes only user_setting_values, and the
// profile DTO — which the Apple clients read — must reflect it on the next GET
// without the legacy column having moved at all.
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

	got := profileFromList(t, listProfilesVia(t, handler), "profile-1")
	if got.Language != "de" {
		t.Errorf("language = %q, want %q", got.Language, "de")
	}
	if got.PreferredMetadataLanguage != "fr" {
		t.Errorf("preferred_metadata_language = %q, want %q", got.PreferredMetadataLanguage, "fr")
	}
	if got.SubtitleLanguage != "ja" {
		t.Errorf("subtitle_language = %q, want %q", got.SubtitleLanguage, "ja")
	}
	if got.SubtitleMode != "always" {
		t.Errorf("subtitle_mode = %q, want %q", got.SubtitleMode, "always")
	}
	if got.ShowForcedSubtitles {
		t.Error("show_forced_subtitles = true, want false")
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

// TestListProfilesFallsBackToContractDefaults: a profile with neither a
// canonical row nor column data serves the contract's defaults, not the
// columns' schema defaults. subtitle_mode is the one that shows the
// difference is real — the column defaults to 'auto' and so does the
// contract, so show_forced_subtitles and the languages carry the assertion.
func TestListProfilesFallsBackToContractDefaults(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	got := profileFromList(t, listProfilesVia(t, handler), "profile-1")
	if got.Language != "" {
		t.Errorf("language = %q, want the contract default \"\"", got.Language)
	}
	if got.PreferredMetadataLanguage != "" {
		t.Errorf("preferred_metadata_language = %q, want the contract default \"\"",
			got.PreferredMetadataLanguage)
	}
	if got.SubtitleLanguage != "" {
		t.Errorf("subtitle_language = %q, want the contract default \"\"", got.SubtitleLanguage)
	}
	if got.SubtitleMode != "auto" {
		t.Errorf("subtitle_mode = %q, want the contract default %q", got.SubtitleMode, "auto")
	}
	if !got.ShowForcedSubtitles {
		t.Error("show_forced_subtitles = false, want the contract default true")
	}
}

// TestListProfilesRoundTripsLegacyWrite: the legacy write path still works
// end to end. The columns are no longer read, so this only passes because the
// write mirrors into the canonical rows — which is the whole cutover shape,
// and the regression that would break every shipped client if the sync broke.
func TestListProfilesRoundTripsLegacyWrite(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	rr := updateProfileVia(t, handler, "profile-1",
		`{"language":"es","preferred_metadata_language":"it","subtitle_language":"ko",`+
			`"subtitle_mode":"off","show_forced_subtitles":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}

	// The update response and the next list must agree; both serve resolution.
	var updated profileResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding update response: %v", err)
	}
	listed := profileFromList(t, listProfilesVia(t, handler), "profile-1")
	if !reflect.DeepEqual(updated, listed) {
		t.Errorf("update response and list disagree:\n update = %+v\n list   = %+v", updated, listed)
	}

	if listed.Language != "es" {
		t.Errorf("language = %q, want %q", listed.Language, "es")
	}
	if listed.PreferredMetadataLanguage != "it" {
		t.Errorf("preferred_metadata_language = %q, want %q", listed.PreferredMetadataLanguage, "it")
	}
	if listed.SubtitleLanguage != "ko" {
		t.Errorf("subtitle_language = %q, want %q", listed.SubtitleLanguage, "ko")
	}
	if listed.SubtitleMode != "off" {
		t.Errorf("subtitle_mode = %q, want %q", listed.SubtitleMode, "off")
	}
	if listed.ShowForcedSubtitles {
		t.Error("show_forced_subtitles = true, want false")
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

	resp := listProfilesVia(t, handler)
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
